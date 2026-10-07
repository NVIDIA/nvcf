/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/
/*
 * nvsnap gpushare — release/remap cross-process cuMem mappings around a CUDA
 * checkpoint. See gpushare.h.
 *
 * Built as libnvsnap_gpushare.so and loaded with LD_PRELOAD. CUDA libraries
 * (cudart, NCCL, PyTorch) fetch driver entry points with dlsym() on a
 * libcuda handle and with cuGetProcAddress(), which LD_PRELOAD alone does
 * not intercept, so both are hooked here.
 *
 * "release" must not race with the app: while it is pending, kernel and
 * graph launches wait in a gate (in our wrapper, outside the driver, so
 * cuCheckpointProcessLock still succeeds), in-flight GPU work is drained,
 * then the imported mappings are dropped. "remap" opens the gate again.
 */
#define _GNU_SOURCE
#include <cuda.h>
#include <dirent.h>
#include <dlfcn.h>
#include <errno.h>
#include <execinfo.h>
#include <fcntl.h>
#include <linux/kcmp.h>
#include <pthread.h>
#include <signal.h>
#include <stdarg.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/prctl.h>
#include <sys/random.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/time.h>
#include <sys/un.h>
#include <time.h>
#include <unistd.h>

#include "gpushare.h"

#define MAX_ACC 8

static void logf_(const char *fmt, ...)
{
    char buf[512];
    int off = snprintf(buf, sizeof(buf), "[nvsnap-gpushare %d] ", getpid());
    va_list ap;
    va_start(ap, fmt);
    off += vsnprintf(buf + off, sizeof(buf) - off - 1, fmt, ap);
    va_end(ap);
    if (off > (int)sizeof(buf) - 2) off = sizeof(buf) - 2;
    buf[off++] = '\n';
    ssize_t __attribute__((unused)) w = write(2, buf, off);
}

/* ── Real symbols ─────────────────────────────────────────────────────── */

static void *(*real_dlsym)(void *, const char *);

static void init_real_dlsym(void)
{
    real_dlsym = dlvsym(RTLD_NEXT, "dlsym", "GLIBC_2.34");
    if (!real_dlsym) real_dlsym = dlvsym(RTLD_NEXT, "dlsym", "GLIBC_2.2.5");  /* x86_64 */
    if (!real_dlsym) real_dlsym = dlvsym(RTLD_NEXT, "dlsym", "GLIBC_2.17");   /* aarch64 */
}

static void *cuda_sym(const char *name)
{
    static pthread_once_t once = PTHREAD_ONCE_INIT;
    static void *lib;
    pthread_once(&once, init_real_dlsym);
    if (!lib) lib = dlopen("libcuda.so.1", RTLD_LAZY | RTLD_NOLOAD);
    if (!lib) lib = dlopen("libcuda.so.1", RTLD_LAZY);
    return lib ? real_dlsym(lib, name) : NULL;
}

#define STR_(x) #x
#define STR(x) STR_(x)
/* Call a driver function we do not wrap (macro-expanded name, e.g. _v2). */
#define CU(fn, ...) ({ static __typeof__(&fn) p_; \
    if (!p_) p_ = (__typeof__(&fn))cuda_sym(STR(fn)); \
    p_ ? p_(__VA_ARGS__) : CUDA_ERROR_NOT_FOUND; })

/* ── IMEX channel ─────────────────────────────────────────────────────── */

/* Fabric handles need the driver client to be subscribed to an IMEX
 * channel. libcuda subscribes once, with the RM control
 * NV0000_CTRL_CMD_CLIENT_SUBSCRIBE_TO_IMEX_CHANNEL on its root client,
 * passing the fd of /dev/nvidia-caps-imex-channels/channelN. A driver
 * restore (610) closes that fd and re-creates the root client without the
 * subscription, and libcuda never subscribes again: fabric create, import
 * and multicast fail with NOT_PERMITTED. So the shim watches libcuda's
 * ioctls for root clients and the subscription, and after a restore
 * subscribes the new root client again (imex_resubscribe). The RM ABI is
 * from open-gpu-kernel-modules (nvos.h, nv_escape.h, ctrl0000client.h). */
struct rm_control { uint32_t hClient, hObject, cmd, flags; uint64_t params; uint32_t paramsSize, status; };
struct rm_alloc { uint32_t hRoot, hObjectParent, hObjectNew, hClass; uint64_t pAllocParms, pRights;
                  uint32_t paramsSize, flags, status; };
struct rm_subscribe { uint64_t devDescriptor; uint32_t channel, pad; };
#define NV_ESC_RM_CONTROL 0x2A
#define NV_ESC_RM_ALLOC 0x2B
#define NV01_ROOT_CLIENT 0x41
#define CMD_SUBSCRIBE_IMEX 0xd08
#define NV_IOC(nr, T) ((3UL << 30) | ((unsigned long)sizeof(T) << 16) | ('F' << 8) | (nr))

#define N_ROOTS 64
static struct { int fd; uint32_t h; } roots[N_ROOTS];  /* newest root clients, a ring */
static unsigned n_roots;
static char imex_path[64];                      /* channel libcuda subscribed to */
static pthread_mutex_t imex_mu = PTHREAD_MUTEX_INITIALIZER;
static int (*real_ioctl)(int, unsigned long, ...);

int ioctl(int fd, unsigned long req, ...)
{
    va_list ap;
    va_start(ap, req);
    void *arg = va_arg(ap, void *);
    va_end(ap);
    if (!real_ioctl) {
        static pthread_once_t once = PTHREAD_ONCE_INIT;
        pthread_once(&once, init_real_dlsym);
        real_ioctl = (int (*)(int, unsigned long, ...))real_dlsym(RTLD_NEXT, "ioctl");
    }
    int r = real_ioctl(fd, req, arg);
    if (r || !arg) return r;
    if (req == NV_IOC(NV_ESC_RM_ALLOC, struct rm_alloc)) {
        struct rm_alloc *a = arg;
        if (!a->status && a->hClass == NV01_ROOT_CLIENT) {
            pthread_mutex_lock(&imex_mu);
            roots[n_roots % N_ROOTS].fd = fd;
            roots[n_roots++ % N_ROOTS].h = a->hObjectNew;
            pthread_mutex_unlock(&imex_mu);
        }
    } else if (req == NV_IOC(NV_ESC_RM_CONTROL, struct rm_control)) {
        struct rm_control *c = arg;
        if (!c->status && c->cmd == CMD_SUBSCRIBE_IMEX && !imex_path[0]) {
            struct rm_subscribe *s = (void *)(uintptr_t)c->params;
            char p[64];
            snprintf(p, sizeof(p), "/proc/self/fd/%d", (int)s->devDescriptor);
            pthread_mutex_lock(&imex_mu);
            ssize_t k = readlink(p, imex_path, sizeof(imex_path) - 1);
            imex_path[k > 0 ? k : 0] = 0;
            pthread_mutex_unlock(&imex_mu);
        }
    }
    return r;
}

/* After a restore: subscribe the process's root clients to the channel
 * again. libcuda's is among them, next to others (NVML's); stale ones are
 * refused (invalid client). Returns 0 if one was subscribed, or if libcuda
 * never was. */
static int imex_resubscribe(void)
{
    pthread_mutex_lock(&imex_mu);
    int ok = !imex_path[0], ch = ok ? -1 : open(imex_path, O_RDONLY | O_CLOEXEC), ns = 0;
    uint32_t st = 0;
    for (unsigned i = 0; ch >= 0 && i < N_ROOTS && i < n_roots; i++) {
        unsigned k = (n_roots - 1 - i) % N_ROOTS;
        struct rm_subscribe s = { (uint64_t)ch, (uint32_t)-1, 0 };
        struct rm_control c = { roots[k].h, roots[k].h, CMD_SUBSCRIBE_IMEX, 0, (uint64_t)(uintptr_t)&s, sizeof(s), 0 };
        if (real_ioctl(roots[k].fd, NV_IOC(NV_ESC_RM_CONTROL, struct rm_control), &c) == 0 && !(st = c.status))
            ns++;
    }
    ok |= ns > 0;
    if (ch >= 0) close(ch);
    if (!ok) logf_("IMEX channel %s: subscribing again failed (status %#x)", imex_path, st);
    else if (getenv("NVSNAP_GPUSHARE_DEBUG"))
        logf_("IMEX channel '%s': subscribed %d of %u root client(s) again", imex_path, ns, n_roots);
    pthread_mutex_unlock(&imex_mu);
    return ok ? 0 : -1;
}

/* Wrapped functions: real pointer, set from whichever lookup path the
 * application used first (dlsym, cuGetProcAddress or direct linking). */
static __typeof__(&cuMemMap) r_map;
static __typeof__(&cuMemUnmap) r_unmap;
static __typeof__(&cuMemRelease) r_release;
static __typeof__(&cuMemSetAccess) r_set_access;
static __typeof__(&cuMemImportFromShareableHandle) r_import;
static __typeof__(&cuMemExportToShareableHandle) r_export;
static CUresult (*r_gpa2)(const char *, void **, int, cuuint64_t, CUdriverProcAddressQueryResult *);
static CUresult (*r_gpa1)(const char *, void **, int, cuuint64_t);
static __typeof__(&cuMemHostRegister) r_host_reg;
static __typeof__(&cuMemHostUnregister) r_host_unreg;
static __typeof__(&cuMulticastCreate) r_mc_create;
static __typeof__(&cuMulticastAddDevice) r_mc_add;
static __typeof__(&cuMulticastBindMem) r_mc_bind_mem;
static __typeof__(&cuMulticastBindAddr) r_mc_bind_addr;
static __typeof__(&cuMulticastUnbind) r_mc_unbind;
static __typeof__(&cuMemCreate) r_mem_create;

static void *real_of(void **p, const char *name)
{
    if (!*p) *p = cuda_sym(name);
    return *p;
}
#define REAL(p, name) ((__typeof__(p))real_of((void **)&p, name))

/* ── State ───────────────────────────────────────────────────────────── */

/* An allocation imported from another process. */
struct imp {
    CUmemGenericAllocationHandle app_h;  /* handle the app knows */
    CUmemGenericAllocationHandle cur_h;  /* live handle (differs after remap) */
    int peer;                            /* exporter pid, 0 = unknown */
    unsigned long long key;              /* exporter's registry id (struct xreg) */
    int app_released;                    /* app called cuMemRelease */
    int released;                        /* dropped by "release" */
    int fab;                             /* shared as a fabric handle (IMEX) */
    /* Exporter not found at import (it exported before mapping): looked up
     * again at "quiesce" by the fabric handle or a dup of the fd. */
    CUmemFabricHandle fh;
    int ufd;                             /* 0 = none */
};

/* A mapping of an imported allocation. */
struct map {
    CUdeviceptr va;
    size_t size, offset;
    int imp;
    int ipc;  /* opened by w_ipc_open: we own the VA reservation */
    int gone; /* exporter freed it before checkpoint: left unmapped */
    int down; /* unmapped by "release" */
    CUmemAccessDesc acc[MAX_ACC];
    int nacc;
};

/* A mapping of a local allocation (to re-export by VA if needed). */
struct lmap { CUdeviceptr va; size_t size; CUmemGenericAllocationHandle h; size_t off; };
/* A local allocation the shim replaced (see replace_alloc): the app's
 * handle stands for the new one. */
struct swap { CUmemGenericAllocationHandle app_h, cur_h; };

/* An fd this process exported (dup kept until "release", to identify it).
 * id 0: the handle h was not mapped yet; w_map assigns the id (NCCL exports
 * symmetric buffers before mapping them). */
struct exp { int fd; unsigned long long id; int mc; /* a multicast object */ CUmemGenericAllocationHandle h; };
/* A fabric handle this process exported (IMEX): importers identify us by
 * its bytes. Stale after a checkpoint, so dropped by "release". */
struct fexp { CUmemFabricHandle fh; unsigned long long id; int mc; CUmemGenericAllocationHandle h; };

/* An allocation this process shares, by id. VAs and handle values are
 * reused once freed, so peers re-request exports by id, and an id dies with
 * its allocation: a re-export never hands out a different allocation. */
struct xreg {
    unsigned long long id;
    CUdeviceptr va;                      /* where it is mapped here */
    CUmemGenericAllocationHandle h;      /* held handle (valloc), else 0 */
};

#define VEC(T, name) static T *name; static int n_##name, cap_##name
/* Out of memory: losing track of an allocation would corrupt a restore. */
#define PUSH(name, v) do { if (n_##name == cap_##name) { \
    void *p_ = realloc(name, (cap_##name ? 2 * cap_##name : 16) * sizeof(*name)); \
    if (!p_) { logf_("out of memory"); abort(); } \
    name = p_; cap_##name = cap_##name ? 2 * cap_##name : 16; } \
    name[n_##name++] = (v); } while (0)
#define DEL(name, i) (name[i] = name[--n_##name])

VEC(struct imp, imps);
VEC(struct map, maps);
VEC(struct lmap, lmaps);
VEC(struct lmap, rets);  /* handles from cuMemRetainAllocationHandle */
VEC(struct swap, swaps);
/* cuMemAlloc backed by cuMem (see "CUDA IPC on cuMem"). */
struct valloc {
    CUdeviceptr va; size_t size; CUmemGenericAllocationHandle h; CUdevice dev;
    int dropped;     /* freed by "release" (see valloc_drop); contents in: */
    void *save;      /*   host memory, or */
    struct chunk *chunks;  /* the chunk store, one per STAGE_SIZE */
};
struct chunk { uint64_t h[2]; };  /* content hash; 0 = all zero, not stored */
VEC(struct valloc, vallocs);
VEC(struct exp, exps);
VEC(struct fexp, fexps);
/* Fabric handles change across a restore. At "load" the exporter exports
 * again what it shared as fabric handles and lists old -> new ("fabmap");
 * importers of memory exported on other nodes look theirs up in the list
 * merged from all nodes ("remap <file>"). */
struct fabmap { CUmemFabricHandle old, cur; };
VEC(struct fabmap, pubs);   /* ours, since the last "release" */
VEC(struct fabmap, fmap);   /* all nodes', from "remap <file>" */
VEC(struct xreg, xregs);
/* Page-locked host memory (see "Host memory"). */
struct hreg { void *p; size_t size; unsigned flags; int owned; CUdeviceptr dptr; int down; /* unregistered by "release" */ };
VEC(struct hreg, hregs);

/* A multicast object (NCCL NVLS) this process created or imported. The
 * driver restores none: "release" unmaps, unbinds and drops it; "remap"
 * re-creates it (creator) or re-imports it from the creator, and rejoins
 * this process's devices; "resume" binds the memory again and maps it. */
#define MAX_DEV 16
struct mcobj {
    CUmemGenericAllocationHandle app_h, cur_h;
    CUmulticastObjectProp prop;          /* creator: to re-create it */
    int creator, peer;                   /* peer: creator pid (importer) */
    unsigned long long key;              /* id at the creator */
    CUdevice devs[MAX_DEV];              /* added by this process */
    int ndev;
    int app_released, released, dead;    /* dead: app done with it */
    int fab;                             /* imported as a fabric handle */
    CUmemFabricHandle fh;                /* from another node: its handle */
};
/* Memory this process bound to a multicast object. */
struct mcbind {
    int mc;
    CUdevice dev;
    size_t mcoff, size;
    unsigned long long flags;
    CUmemGenericAllocationHandle memh;   /* cuMulticastBindMem, else 0 */
    size_t memoff;
    CUdeviceptr addr;                    /* cuMulticastBindAddr */
    int memh_released;                   /* app released memh: retain from ucva */
    CUdeviceptr ucva;
    int unbound;
};
VEC(struct mcobj, mcs);
VEC(struct mcbind, mcbinds);
VEC(struct map, mcmaps);  /* .imp: index into mcs */
static unsigned long long next_id = 1;
static pthread_mutex_t mu = PTHREAD_MUTEX_INITIALIZER;

/* Registry id for the allocation mapped at va (mu held). */
static unsigned long long xreg_id(CUdeviceptr va, CUmemGenericAllocationHandle h)
{
    for (int i = 0; i < n_xregs; i++)
        if (xregs[i].va == va) return xregs[i].id;
    PUSH(xregs, ((struct xreg){ next_id, va, h }));
    return next_id++;
}

/* The allocation at [va, va+size) is going away (mu held). */
static void xreg_kill(CUdeviceptr va, size_t size)
{
    for (int i = n_xregs - 1; i >= 0; i--)
        if (xregs[i].va >= va && xregs[i].va < va + size) DEL(xregs, i);
}

/* Control thread: app threads may be frozen while holding mu. */
static int lock_ctl(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    ts.tv_sec += 2;
    return pthread_mutex_timedlock(&mu, &ts);
}

static int fabric_hidden(void);

static int find_imp(CUmemGenericAllocationHandle h)
{
    for (int i = 0; i < n_imps; i++)
        if (!imps[i].app_released && (imps[i].cur_h == h || imps[i].app_h == h)) return i;
    return -1;
}

/* The live handle for a local allocation handle of the app (mu held). */
static CUmemGenericAllocationHandle own_h(CUmemGenericAllocationHandle h)
{
    for (int i = 0; i < n_swaps; i++)
        if (swaps[i].app_h == h) return swaps[i].cur_h;
    return h;
}

static int find_mc(CUmemGenericAllocationHandle h)
{
    for (int i = 0; i < n_mcs; i++)
        if (!mcs[i].dead && !mcs[i].app_released && (mcs[i].cur_h == h || mcs[i].app_h == h)) return i;
    return -1;
}

static const char *errstr(CUresult r);

/* mu held, context current. */
static CUresult mc_recreate(struct mcobj *m)
{
    CUmemGenericAllocationHandle h;
    /* Without an IMEX channel subscription (see "IMEX channel"), fabric is
     * refused: importers on this node re-import as POSIX fds. */
    CUmulticastObjectProp p = m->prop;
    CUresult r = REAL(r_mc_create, "cuMulticastCreate")(&h, &p);
    if (r == CUDA_ERROR_NOT_PERMITTED && (p.handleTypes & CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR)) {
        p.handleTypes &= ~CU_MEM_HANDLE_TYPE_FABRIC;
        r = REAL(r_mc_create, "cuMulticastCreate")(&h, &p);
    }
    if (r != CUDA_SUCCESS) { logf_("cuMulticastCreate: %s", errstr(r)); return r; }
    for (int d = 0; r == CUDA_SUCCESS && d < m->ndev; d++)
        if ((r = REAL(r_mc_add, "cuMulticastAddDevice")(h, m->devs[d])) != CUDA_SUCCESS)
            logf_("cuMulticastAddDevice(dev %d): %s", m->devs[d], errstr(r));
    if (r != CUDA_SUCCESS) return r;
    m->cur_h = h;
    m->released = 0;
    return CUDA_SUCCESS;
}

/* Fabric handles travel as hex text. */
static void hex_encode(const unsigned char *b, size_t n, char *out)
{
    for (size_t i = 0; i < n; i++) sprintf(out + 2 * i, "%02x", b[i]);
}

static int hex_decode(const char *in, unsigned char *b, size_t n)
{
    for (size_t i = 0; i < n; i++) {
        unsigned v;
        if (sscanf(in + 2 * i, "%2x", &v) != 1) return -1;
        b[i] = v;
    }
    return 0;
}

/* ── Messages ─────────────────────────────────────────────────────────── */

static int msg_send(int s, const char *txt, int fd)
{
    char cbuf[CMSG_SPACE(sizeof(int))] = {0};
    struct iovec iov = { (void *)txt, strlen(txt) + 1 };
    struct msghdr m = { .msg_iov = &iov, .msg_iovlen = 1 };
    if (fd >= 0) {
        m.msg_control = cbuf;
        m.msg_controllen = sizeof(cbuf);
        struct cmsghdr *c = CMSG_FIRSTHDR(&m);
        c->cmsg_level = SOL_SOCKET;
        c->cmsg_type = SCM_RIGHTS;
        c->cmsg_len = CMSG_LEN(sizeof(int));
        memcpy(CMSG_DATA(c), &fd, sizeof(int));
    }
    return sendmsg(s, &m, MSG_NOSIGNAL) > 0 ? 0 : -1;
}

static int msg_recv(int s, char *buf, size_t n, int *fd)
{
    char cbuf[CMSG_SPACE(sizeof(int))];
    struct iovec iov = { buf, n - 1 };
    struct msghdr m = { .msg_iov = &iov, .msg_iovlen = 1, .msg_control = cbuf, .msg_controllen = sizeof(cbuf) };
    ssize_t r = recvmsg(s, &m, MSG_CMSG_CLOEXEC);
    if (r <= 0) return -1;
    buf[r] = 0;
    if (m.msg_flags & MSG_CTRUNC) logf_("recvmsg: control data truncated (fd dropped)");
    if (fd) {
        *fd = -1;
        struct cmsghdr *c = CMSG_FIRSTHDR(&m);
        if (c && c->cmsg_type == SCM_RIGHTS) memcpy(fd, CMSG_DATA(c), sizeof(int));
    }
    return 0;
}

static int sock_addr(int pid, struct sockaddr_un *a)
{
    memset(a, 0, sizeof(*a));
    a->sun_family = AF_UNIX;
    int n = snprintf(a->sun_path + 1, sizeof(a->sun_path) - 1, GPUSHARE_SOCK_FMT, pid);
    return offsetof(struct sockaddr_un, sun_path) + 1 + n;
}

/* Abstract sockets have no permissions: anything in the network namespace
 * (a sidecar; with hostNetwork, the whole node) can connect or bind a name.
 * As a server (pid 0), accept a peer that runs as root or as us and is in
 * our pid namespace (pid 0 here: a sidecar or a host process). As a
 * client, talk only to process pid itself, never to whoever bound its name. */
static int peer_ok(int s, int pid)
{
    struct ucred c;
    socklen_t l = sizeof(c);
    if (getsockopt(s, SOL_SOCKET, SO_PEERCRED, &c, &l) != 0) return 0;
    if (pid ? c.pid == pid : c.pid != 0 && (c.uid == 0 || c.uid == geteuid())) return 1;
    logf_("control socket: refused peer pid %d uid %u%s", c.pid, c.uid, pid ? " (expected the socket's owner)" : "");
    return 0;
}

/* One request/reply to another process's control thread. */
static int request(int pid, const char *txt, int fd_in, char *reply, size_t n, int *fd_out)
{
    struct sockaddr_un a;
    socklen_t len = sock_addr(pid, &a);
    int s = socket(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0);
    struct timeval tv = { 30, 0 };
    setsockopt(s, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    int rc = (s >= 0 && connect(s, (struct sockaddr *)&a, len) == 0 && peer_ok(s, pid) &&
              msg_send(s, txt, fd_in) == 0 && msg_recv(s, reply, n, fd_out) == 0) ? 0 : -1;
    if (s >= 0) close(s);
    return rc;
}

static CUresult export_id(unsigned long long id, int fab, int *fd, CUmemFabricHandle *fh);
static CUresult valloc_drop(struct valloc *v);
static CUresult valloc_restore(struct valloc *v);
static void stage_put(void);
#define STAGE_SIZE (64UL << 20)
static void *stage;           /* pinned staging buffer for the chunk store */
static char store[400];       /* chunk store directory (see valloc_drop) */
static char cache[400];       /* optional node-local cache of the store (same layout) */
static FILE *chunk_list;      /* this checkpoint's chunks, for accounting/GC */
static size_t st_written, st_dedup, st_zero, st_hit, st_miss;
static __typeof__(&cuMemAllocHost) r_alloc_host;
static __typeof__(&cuMemFreeHost) r_free_host;

/* Fetch a fresh export of `key` from its exporter and import it, as an fd
 * or (fab) a fabric handle. On failure rep holds the peer's reply. Our own
 * exports (an app importing its own handle) are taken directly: mu is held. */
static CUresult fetch_import(int peer, unsigned long long key, int fab, CUmemGenericAllocationHandle *h,
                             char *rep, size_t n)
{
    char req[64];
    int fd = -1;
    CUmemFabricHandle fh;
    if (peer == getpid()) {
        CUresult e = export_id(key, fab, &fd, &fh);
        if (e == CUDA_SUCCESS) {
            snprintf(rep, n, "ok ");  /* the fabric handle is already in fh */
        } else {
            snprintf(rep, n, e == CUDA_ERROR_NOT_FOUND ? "err gone" : "err export: %s", errstr(e));
        }
    } else {
        snprintf(req, sizeof(req), "%s %llu", fab ? "exportfab" : "export", key);
        if (request(peer, req, -1, rep, n, &fd) < 0) {
            snprintf(rep, n, "err no reply");
            return CUDA_ERROR_INVALID_HANDLE;
        }
    }
    CUresult r = CUDA_ERROR_INVALID_HANDLE;
    if (strncmp(rep, "ok", 2)) {
        /* rep explains */
    } else if (fab) {
        if (peer == getpid() || hex_decode(rep + 3, fh.data, sizeof(fh.data)) == 0)
            r = REAL(r_import, "cuMemImportFromShareableHandle")(h, &fh, CU_MEM_HANDLE_TYPE_FABRIC);
        else snprintf(rep, n, "err bad fabric handle");
    } else if (fd >= 0) {
        r = REAL(r_import, "cuMemImportFromShareableHandle")(h, (void *)(uintptr_t)fd, CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR);
    } else {
        snprintf(rep, n, "err no fd");
    }
    if (fd >= 0) close(fd);
    return r;
}

/* Re-import what was shared as a fabric handle as a fabric handle again
 * (see "IMEX channel"), and as a POSIX fd if that fails: memory created
 * with fabric handles also allows fds (see w_mem_create). */
static CUresult refetch(int peer, unsigned long long key, int fab, CUmemGenericAllocationHandle *h,
                        char *rep, size_t n)
{
    CUresult r = fetch_import(peer, key, fab, h, rep, n);
    if (r != CUDA_SUCCESS && fab && strncmp(rep, "err gone", 8)) {
        logf_("re-import id %llu from pid %d as fabric: %s (%s); trying fd", key, peer, errstr(r), rep);
        r = fetch_import(peer, key, 0, h, rep, n);
    }
    return r;
}

/* ── CUDA helpers for the control thread ─────────────────────────────── */

/* Push an active primary context (VMM calls need one); returns its device. */
static int push_any_ctx(void)
{
    int n = 0;
    if (CU(cuDeviceGetCount, &n) != CUDA_SUCCESS) return -1;
    for (int d = 0; d < n; d++) {
        unsigned flags; int active = 0; CUcontext c;
        if (CU(cuDevicePrimaryCtxGetState, d, &flags, &active) == CUDA_SUCCESS && active &&
            CU(cuDevicePrimaryCtxRetain, &c, d) == CUDA_SUCCESS) {
            CU(cuCtxPushCurrent, c);
            return d;
        }
    }
    return -1;
}

static void pop_ctx(int dev)
{
    CUcontext c;
    if (dev < 0) return;
    CU(cuCtxPopCurrent, &c);
    CU(cuDevicePrimaryCtxRelease, dev);
}

/* Push dev's primary context (pop_ctx undoes it): a process may drive
 * several GPUs, and an allocation is copied on its own device. */
static int push_dev_ctx(CUdevice dev)
{
    CUcontext c;
    if (CU(cuDevicePrimaryCtxRetain, &c, dev) != CUDA_SUCCESS) return -1;
    if (CU(cuCtxPushCurrent, c) == CUDA_SUCCESS) return dev;
    CU(cuDevicePrimaryCtxRelease, dev);
    return -1;
}

/* Wait for all work on every active primary context. */
static CUresult sync_all(void)
{
    int n = 0;
    CUresult r = CU(cuDeviceGetCount, &n);
    for (int d = 0; r == CUDA_SUCCESS && d < n; d++) {
        unsigned flags; int active = 0; CUcontext c;
        if (CU(cuDevicePrimaryCtxGetState, d, &flags, &active) != CUDA_SUCCESS || !active) continue;
        if (CU(cuDevicePrimaryCtxRetain, &c, d) != CUDA_SUCCESS) continue;
        CU(cuCtxPushCurrent, c);
        r = CU(cuCtxSynchronize);
        CU(cuCtxPopCurrent, &c);
        CU(cuDevicePrimaryCtxRelease, d);
    }
    return r;
}

/* sync_all() with a timeout. With the app frozen, work on the GPU can wait
 * forever for work a frozen thread has not launched yet (e.g. the matching
 * NCCL kernel of a peer). Then "release" reports busy; the tool thaws the
 * app and retries. The sync keeps running in its own thread meanwhile. */
static pthread_mutex_t sync_mu = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t sync_cv = PTHREAD_COND_INITIALIZER;
static int sync_running, sync_done;
static CUresult sync_res;

static void wc_thread_init(void);

static void *sync_main(void *arg)
{
    wc_thread_init();  /* named GPUSHARE_THREAD: the tool must not freeze it */
    CUresult r = sync_all();
    pthread_mutex_lock(&sync_mu);
    sync_res = r;
    sync_running = 0;
    sync_done = 1;
    pthread_cond_broadcast(&sync_cv);
    pthread_mutex_unlock(&sync_mu);
    return arg;
}

static int timed_sync(int ms, CUresult *r)
{
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    ts.tv_sec += ms / 1000;
    ts.tv_nsec += (ms % 1000) * 1000000L;
    if (ts.tv_nsec >= 1000000000L) { ts.tv_sec++; ts.tv_nsec -= 1000000000L; }
    pthread_mutex_lock(&sync_mu);
    if (!sync_running) {
        pthread_t t;
        sync_done = 0;
        sync_running = pthread_create(&t, NULL, sync_main, NULL) == 0;
        if (sync_running) pthread_detach(t);
        else { sync_done = 1; sync_res = CUDA_ERROR_OPERATING_SYSTEM; }
    }
    while (!sync_done && pthread_cond_timedwait(&sync_cv, &sync_mu, &ts) == 0) {}
    int done = sync_done;
    if (done) { *r = sync_res; sync_done = 0; }
    pthread_mutex_unlock(&sync_mu);
    return done ? 0 : -1;
}

static const char *errstr(CUresult r)
{
    const char *s = "?";
    CU(cuGetErrorName, r, &s);
    return s;
}

static int n_legacy;  /* imports through the driver's own CUDA IPC */

/* ── Gates ─────────────────────────────────────────────────────────────── */

/* Two gates hold the app's CUDA calls during a checkpoint. "quiesce" closes
 * the launch gate (kernels, graphs) and drains the GPU, then closes the
 * memory gate (copies, memsets, stream memory operations) and drains again.
 * Copies wait for the drain to finish before they are held: NCCL's proxy
 * thread issues copies that its kernels in flight need to complete. */
struct gate { int closed, inflight; };
static struct gate lgate, mgate;
static pthread_mutex_t gate_mu = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t gate_cv = PTHREAD_COND_INITIALIZER;
#define gated (lgate.closed)

static void gate_enter(struct gate *g)
{
    for (;;) {
        __atomic_add_fetch(&g->inflight, 1, __ATOMIC_SEQ_CST);
        if (!__atomic_load_n(&g->closed, __ATOMIC_SEQ_CST)) return;
        __atomic_sub_fetch(&g->inflight, 1, __ATOMIC_SEQ_CST);
        pthread_mutex_lock(&gate_mu);
        while (g->closed) pthread_cond_wait(&gate_cv, &gate_mu);
        pthread_mutex_unlock(&gate_mu);
    }
}

static void gate_exit(struct gate *g) { __atomic_sub_fetch(&g->inflight, 1, __ATOMIC_SEQ_CST); }

/* Close the gate and wait for calls already past it. */
static int gate_close(struct gate *g, int ms)
{
    __atomic_store_n(&g->closed, 1, __ATOMIC_SEQ_CST);
    for (int i = 0; i < ms && __atomic_load_n(&g->inflight, __ATOMIC_SEQ_CST); i++) usleep(1000);
    return __atomic_load_n(&g->inflight, __ATOMIC_SEQ_CST) ? -1 : 0;
}

/* Open both gates. */
static void gate_open(void)
{
    pthread_mutex_lock(&gate_mu);
    lgate.closed = mgate.closed = 0;
    pthread_cond_broadcast(&gate_cv);
    pthread_mutex_unlock(&gate_mu);
}

/* ── Control commands ─────────────────────────────────────────────────── */

/* Is the import still in use by the app (mu held)? NCCL imports its own
 * fabric export to probe IMEX, then drops it. */
static int imp_live(int i)
{
    if (!imps[i].app_released) return 1;
    for (int j = 0; j < n_maps; j++) if (maps[j].imp == i) return 1;
    return 0;
}

static int find_exporter(const char *req, int fd, unsigned long long *key, int *mc);

/* Look up the exporter of an import made before the exporter registered it
 * (mu held: the peers' replies do not need it, and the tool asks one
 * process at a time). */
static void resolve_import(struct imp *im)
{
    unsigned long long key = 0;
    int mc = 0;
    if (im->fab) {
        char req[16 + 2 * sizeof(im->fh.data)] = "whosefab ";
        hex_encode(im->fh.data, sizeof(im->fh.data), req + 9);
        im->peer = find_exporter(req, -1, &key, &mc);
    } else if (im->ufd) {
        im->peer = find_exporter("whose", im->ufd, &key, &mc);
    }
    if (!im->peer) return;
    im->key = key;
    if (im->ufd) close(im->ufd);
    im->ufd = 0;
}

/* Is the multicast object still in use by the app (mu held)? */
static int mc_live(int i)
{
    if (!mcs[i].app_released) return 1;
    for (int j = 0; j < n_mcmaps; j++) if (mcmaps[j].imp == i) return 1;
    for (int j = 0; j < n_mcbinds; j++) if (mcbinds[j].mc == i) return 1;
    return 0;
}

/* Where memh is mapped here, 0 if nowhere (mu held). */
static CUdeviceptr lmap_va(CUmemGenericAllocationHandle h)
{
    for (int i = 0; i < n_lmaps; i++) if (lmaps[i].h == h) return lmaps[i].va;
    return 0;
}

/* Hold launches, drain GPU work, and check that "release" can drop every
 * shared mapping. Separate from "release" so that a busy or refusing
 * process leaves every process's mappings untouched. */
static void do_quiesce(char *reply, size_t n)
{
    if (lock_ctl()) { snprintf(reply, n, "err busy (state lock)"); return; }
    /* Busy: GPU work waits on work a gated thread has not launched yet
     * (e.g. a peer's NCCL kernel). Reopen the gate; the tool retries. */
    CUresult r;
    if (gate_close(&lgate, 2000) < 0 || timed_sync(2000, &r) < 0 ||
        gate_close(&mgate, 2000) < 0 || timed_sync(2000, &r) < 0) {
        gate_open();
        snprintf(reply, n, "err busy (GPU work did not drain)");
        pthread_mutex_unlock(&mu);
        return;
    }
    reply[0] = 0;
    for (int i = 0; i < n_imps; i++)
        if (!imps[i].peer && !imps[i].released && imp_live(i)) resolve_import(&imps[i]);
    if (r != CUDA_SUCCESS)
        snprintf(reply, n, "err synchronize: %s", errstr(r));
    else if (n_legacy)
        snprintf(reply, n, "err %d CUDA IPC import(s) of memory not allocated through "
                 "libnvsnap_gpushare (cuMemAlloc >= 2 MiB) cannot be restored", n_legacy);
    /* Fabric imports with no exporter on this node: from another node (see
     * struct fabmap), unless fabric support is hidden. */
    int remote_ok = !fabric_hidden();
    for (int i = 0; !reply[0] && i < n_imps; i++)
        if (!imps[i].peer && !(imps[i].fab && remote_ok) && !imps[i].released && imp_live(i))
            snprintf(reply, n, "err import of handle %llu has no known exporter",
                     (unsigned long long)imps[i].app_h);
    for (int i = 0; !reply[0] && i < n_mcs; i++)
        if (!mcs[i].dead && !mcs[i].creator && !mcs[i].peer && !(mcs[i].fab && remote_ok) && mc_live(i))
            snprintf(reply, n, "err multicast import of handle %llu has no known creator",
                     (unsigned long long)mcs[i].app_h);
    for (int i = 0; !reply[0] && i < n_mcbinds; i++) {
        struct mcbind *b = &mcbinds[i];
        if (b->memh && b->memh_released && !(b->ucva = lmap_va(b->memh)))
            snprintf(reply, n, "err memory bound to a multicast object is neither held nor mapped");
    }
    if (reply[0]) gate_open();
    else snprintf(reply, n, "ok quiesced");
    pthread_mutex_unlock(&mu);
}

static double secs(void)
{
    struct timespec t;
    clock_gettime(CLOCK_MONOTONIC, &t);
    return t.tv_sec + t.tv_nsec / 1e9;
}

/* Point the app's own copies of the fds it exported at /dev/null (mu
 * held, before our dups in exps are closed). Apps can keep them: FlashInfer
 * sends its export to itself and closes neither copy. They pin the
 * pre-checkpoint memory and CRIU cannot dump them. The fd numbers stay
 * valid, for the app to close. Returns how many, or -1. */
static int void_export_copies(void)
{
    DIR *d = opendir("/proc/self/fd");
    int nul = open("/dev/null", O_RDWR | O_CLOEXEC), nv = 0;
    pid_t me = getpid();
    struct dirent *e;
    if (!d || nul < 0) nv = -1;
    while (nv >= 0 && (e = readdir(d))) {
        int fd = atoi(e->d_name), fl;
        if (e->d_name[0] == '.' || fd == dirfd(d) || fd == nul) continue;
        for (int i = 0; i < n_exps; i++) {
            if (fd == exps[i].fd || syscall(SYS_kcmp, me, me, KCMP_FILE, exps[i].fd, fd) != 0) continue;
            if ((fl = fcntl(fd, F_GETFD)) < 0 || dup3(nul, fd, fl & FD_CLOEXEC ? O_CLOEXEC : 0) < 0) nv = -1;
            else nv++;
            break;
        }
    }
    if (d) closedir(d);
    if (nul >= 0) close(nul);
    return nv;
}

/* args "<store> <ckpt-dir> [cache]": save the cuMemAlloc allocations to a
 * chunk store (see valloc_drop), with a copy in the node cache; NULL: to
 * host memory. */
static void do_release(char *reply, size_t n, const char *args)
{
    if (lock_ctl()) { snprintf(reply, n, "err busy (state lock)"); return; }
    if (!gated) {
        snprintf(reply, n, "err not quiesced");
        pthread_mutex_unlock(&mu);
        return;
    }
    store[0] = cache[0] = 0;
    st_written = st_dedup = st_zero = 0;
    if (args) {
        char ckdir[400], path[512];
        if (sscanf(args, "%399s %399s %399s", store, ckdir, cache) < 2) {
            snprintf(reply, n, "err usage: release <store> <ckpt-dir> [cache]");
            store[0] = 0;
            pthread_mutex_unlock(&mu);
            return;
        }
        mkdir(store, 0755);
        snprintf(path, sizeof(path), "%s/chunks", store);
        mkdir(path, 0755);
        mkdir(ckdir, 0755);
        snprintf(path, sizeof(path), "%s/gpu-%d.chunks", ckdir, getpid());
        if (!(chunk_list = fopen(path, "we"))) {
            snprintf(reply, n, "err open %.200s: %s", path, strerror(errno));
            store[0] = 0;
            pthread_mutex_unlock(&mu);
            return;
        }
    }
    /* Each object is marked dropped only once it is: if a step fails, the
     * tool rolls back with "load", "remap" and "resume", which restore
     * exactly what was dropped. */
    CUresult r = CUDA_SUCCESS;
    const char *what = "";
    int dev = push_any_ctx();
    int nm = 0, nmc = 0;
    for (int i = 0; r == CUDA_SUCCESS && i < n_maps; i++)
        if (!maps[i].gone && !maps[i].down) {
            what = "unmap import";
            if ((r = REAL(r_unmap, "cuMemUnmap")(maps[i].va, maps[i].size)) == CUDA_SUCCESS) maps[i].down = 1;
            nm++;
        }
    for (int i = 0; r == CUDA_SUCCESS && i < n_imps; i++) {
        if (imps[i].ufd) close(imps[i].ufd);  /* would pin the exporter's memory */
        imps[i].ufd = 0;
        if (imps[i].released) continue;
        what = "release import";
        if (!imps[i].app_released) r = REAL(r_release, "cuMemRelease")(imps[i].cur_h);
        if (r == CUDA_SUCCESS) imps[i].released = 1;
    }
    /* Multicast: unmap, unbind, then drop the object. */
    for (int i = 0; r == CUDA_SUCCESS && i < n_mcmaps; i++) {
        if (mcmaps[i].down) continue;
        what = "unmap multicast";
        if ((r = REAL(r_unmap, "cuMemUnmap")(mcmaps[i].va, mcmaps[i].size)) == CUDA_SUCCESS) mcmaps[i].down = 1;
    }
    for (int i = 0; r == CUDA_SUCCESS && i < n_mcbinds; i++) {
        struct mcbind *b = &mcbinds[i];
        if (b->unbound) continue;
        what = "unbind multicast";
        r = REAL(r_mc_unbind, "cuMulticastUnbind")(mcs[b->mc].cur_h, b->dev, b->mcoff, b->size);
        if (r == CUDA_SUCCESS) b->unbound = 1;
    }
    for (int i = 0; r == CUDA_SUCCESS && i < n_mcs; i++) {
        if (mcs[i].dead || mcs[i].released) continue;
        if (!mc_live(i)) { mcs[i].dead = 1; continue; }
        what = "release multicast";
        if (!mcs[i].app_released) r = REAL(r_release, "cuMemRelease")(mcs[i].cur_h);
        if (r == CUDA_SUCCESS) mcs[i].released = 1;
        nmc++;
    }
    size_t nb = 0;
    double t0 = secs();
    for (int i = 0; r == CUDA_SUCCESS && i < n_vallocs; i++) {
        what = "save allocation";
        r = valloc_drop(&vallocs[i]);
        nb += vallocs[i].size;
    }
    if (r == CUDA_SUCCESS) what = "write chunk list";
    if (chunk_list) {  /* durable before the checkpoint is reported done */
        if (fclose(chunk_list) != 0 && r == CUDA_SUCCESS) r = CUDA_ERROR_FILE_NOT_FOUND;
        chunk_list = NULL;
        int sfd = open(store, O_RDONLY | O_DIRECTORY | O_CLOEXEC);
        if (sfd < 0 || syncfs(sfd) != 0) r = r == CUDA_SUCCESS ? CUDA_ERROR_FILE_NOT_FOUND : r;
        if (sfd >= 0) close(sfd);
    }
    double ts = secs() - t0;
    stage_put();
    for (int i = 0; r == CUDA_SUCCESS && i < n_hregs; i++) {
        if (hregs[i].down) continue;
        what = "unregister host memory";
        r = CU(cuMemHostGetDevicePointer_v2, &hregs[i].dptr, hregs[i].p, 0);
        if (r == CUDA_SUCCESS) r = REAL(r_host_unreg, "cuMemHostUnregister")(hregs[i].p);
        if (r == CUDA_SUCCESS) hregs[i].down = 1;
    }
    /* Exported fds pin the pre-checkpoint memory: drop them, once nothing
     * is left to roll back. */
    int nv = 0;
    if (r == CUDA_SUCCESS && (nv = void_export_copies()) < 0) {
        what = "point exported fds at /dev/null";
        r = CUDA_ERROR_OPERATING_SYSTEM;
    }
    if (nv > 0) logf_("pointed %d fd(s) the app kept of its exports at /dev/null", nv);
    for (int i = 0; r == CUDA_SUCCESS && i < n_exps; i++) close(exps[i].fd);
    if (r == CUDA_SUCCESS) n_exps = n_pubs = 0;
    pop_ctx(dev);
    if (r == CUDA_SUCCESS)
        snprintf(reply, n, "ok released %d mapping(s), %d multicast object(s), %d host buffer(s), "
                 "saved %d allocation(s) (%zu MiB) to %s in %.1fs: %zu MiB written, %zu MiB already stored, "
                 "%zu MiB zero", nm, nmc, n_hregs, n_vallocs, nb >> 20, args ? "store" : "memory", ts,
                 (args ? st_written : nb) >> 20, st_dedup >> 20, st_zero >> 20);
    else snprintf(reply, n, "err release: %s: %s", what, errstr(r));
    pthread_mutex_unlock(&mu);
}

/* Re-create the saved allocations (each process at once; "remap" needs
 * them, and peers' re-imports of them, after this). */
/* cache: this node's cache of the store, NULL for none. */
static void do_load(char *reply, size_t n, const char *cache_dir)
{
    if (lock_ctl()) { snprintf(reply, n, "err busy (state lock)"); return; }
    snprintf(cache, sizeof(cache), "%s", cache_dir ? cache_dir : "");
    imex_resubscribe();  /* before any fabric use (see "IMEX channel") */
    st_hit = st_miss = 0;
    int dev = push_any_ctx(), nl = 0;
    size_t nb = 0;
    double t0 = secs();
    CUresult r = CUDA_SUCCESS;
    for (int i = 0; r == CUDA_SUCCESS && i < n_vallocs; i++) {
        if (!vallocs[i].dropped) continue;
        if ((r = valloc_restore(&vallocs[i])) != CUDA_SUCCESS)
            snprintf(reply, n, "err load allocation %#llx+%zu: %s", (unsigned long long)vallocs[i].va,
                     vallocs[i].size, errstr(r));
        nl++;
        nb += vallocs[i].size;
    }
    stage_put();
    for (int i = 0; r == CUDA_SUCCESS && i < n_fexps; i++) {  /* see struct fabmap */
        CUmemFabricHandle old = fexps[i].fh, nf;
        int done = !fexps[i].id;
        for (int k = 0; !done && k < n_pubs; k++) done = !memcmp(pubs[k].cur.data, old.data, sizeof(old.data));
        if (done) continue;
        CUresult e = export_id(fexps[i].id, 1, NULL, &nf);
        if (e == CUDA_ERROR_NOT_FOUND) continue;  /* freed */
        if ((r = e) != CUDA_SUCCESS) {
            snprintf(reply, n, "err export fabric id %llu again: %s", fexps[i].id, errstr(r));
            break;
        }
        PUSH(pubs, ((struct fabmap){ old, nf }));
        for (int j = i; j < n_fexps; j++)
            if (!memcmp(fexps[j].fh.data, old.data, sizeof(old.data))) fexps[j].fh = nf;
    }
    if (r == CUDA_SUCCESS)
        snprintf(reply, n, "ok loaded %d allocation(s) (%zu MiB) in %.1fs: %zu MiB from cache, %zu MiB from store",
                 nl, nb >> 20, secs() - t0, st_hit >> 20, st_miss >> 20);
    pop_ctx(dev);
    pthread_mutex_unlock(&mu);
}

/* Write our fabric handles' old -> new list ("load") to path, as hex. */
static void do_fabmap(char *reply, size_t n, const char *path)
{
    if (lock_ctl()) { snprintf(reply, n, "err busy (state lock)"); return; }
    FILE *fp = fopen(path, "we");
    char a[2 * sizeof(((CUmemFabricHandle *)0)->data) + 1], b[sizeof(a)];
    for (int i = 0; fp && i < n_pubs; i++) {
        hex_encode(pubs[i].old.data, sizeof(pubs[i].old.data), a);
        hex_encode(pubs[i].cur.data, sizeof(pubs[i].cur.data), b);
        fprintf(fp, "%s %s\n", a, b);
    }
    if (!fp || fclose(fp)) snprintf(reply, n, "err write %.200s: %s", path, strerror(errno));
    else snprintf(reply, n, "ok %d fabric handle(s)", n_pubs);
    pthread_mutex_unlock(&mu);
}

/* Read the fabric handles of all nodes (mu held). */
static int read_fabmap(const char *path, char *reply, size_t n)
{
    FILE *fp = fopen(path, "re");
    char a[2 * sizeof(((CUmemFabricHandle *)0)->data) + 1], b[sizeof(a)];
    if (!fp) { snprintf(reply, n, "err read %.200s: %s", path, strerror(errno)); return -1; }
    n_fmap = 0;
    while (fscanf(fp, "%128s %128s", a, b) == 2) {
        struct fabmap m;
        if (hex_decode(a, m.old.data, sizeof(m.old.data)) == 0 && hex_decode(b, m.cur.data, sizeof(m.cur.data)) == 0)
            PUSH(fmap, m);
    }
    fclose(fp);
    return 0;
}

/* Import again what was exported on another node, by its new fabric
 * handle; fh becomes the new handle. */
static CUresult remote_fetch(CUmemFabricHandle *fh, CUmemGenericAllocationHandle *h, char *rep, size_t n)
{
    for (int i = 0; i < n_fmap; i++)
        if (!memcmp(fmap[i].old.data, fh->data, sizeof(fh->data))) {
            snprintf(rep, n, "ok");
            CUresult r = REAL(r_import, "cuMemImportFromShareableHandle")(h, &fmap[i].cur, CU_MEM_HANDLE_TYPE_FABRIC);
            if (r == CUDA_SUCCESS) *fh = fmap[i].cur;
            return r;
        }
    snprintf(rep, n, "err not in the fabric map of the other nodes");
    return CUDA_ERROR_NOT_FOUND;
}

static void do_remap(char *reply, size_t n, const char *fabmap_path)
{
    char rep[256] = "no reply";
    int nm = 0, ngone = 0, nh = 0;
    CUresult r = CUDA_SUCCESS;
    if (lock_ctl()) { snprintf(reply, n, "err busy (state lock)"); return; }
    int dev = push_any_ctx();
    if (fabmap_path && read_fabmap(fabmap_path, reply, n) < 0) goto out;
    for (int i = 0; i < n_vallocs; i++)
        if ((r = valloc_restore(&vallocs[i])) != CUDA_SUCCESS) {
            snprintf(reply, n, "err restore allocation %#llx+%zu: %s", (unsigned long long)vallocs[i].va,
                     vallocs[i].size, errstr(r));
            goto out;
        }
    for (int i = 0; i < n_hregs; i++) {
        CUdeviceptr d = 0;
        if (!hregs[i].down) continue;
        r = REAL(r_host_reg, "cuMemHostRegister_v2")(hregs[i].p, hregs[i].size, hregs[i].flags);
        if (r == CUDA_SUCCESS) r = CU(cuMemHostGetDevicePointer_v2, &d, hregs[i].p, 0);
        if (r == CUDA_SUCCESS && d != hregs[i].dptr) r = CUDA_ERROR_INVALID_VALUE;
        if (r != CUDA_SUCCESS) {
            snprintf(reply, n, "err re-register host %p+%zu (device pointer %#llx -> %#llx): %s", hregs[i].p,
                     hregs[i].size, (unsigned long long)hregs[i].dptr, (unsigned long long)d, errstr(r));
            goto out;
        }
        hregs[i].down = 0;
        nh++;
    }
    for (int i = 0; r == CUDA_SUCCESS && i < n_imps; i++) {
        struct imp *im = &imps[i];
        if (!im->released) continue;
        int mapped = 0;
        for (int j = 0; j < n_maps; j++) mapped |= maps[j].imp == i;
        if (im->app_released && !mapped) { im->released = 0; continue; }  /* nothing to restore */
        CUmemGenericAllocationHandle h;
        const char *what = "import";
        r = im->peer > 0 ? refetch(im->peer, im->key, im->fab, &h, rep, sizeof(rep))
                         : remote_fetch(&im->fh, &h, rep, sizeof(rep));
        if (r != CUDA_SUCCESS && !strncmp(rep, "err gone", 8)) {
            /* The exporter freed it: like CUDA IPC after the exporter's
             * cuMemFree, the memory is undefined. Keep the VA reserved
             * and unmapped. */
            for (int j = 0; j < n_maps; j++)
                if (maps[j].imp == i) { maps[j].gone = 1; maps[j].down = 0; ngone++; }
            im->released = 0;
            im->app_released = 1;
            r = CUDA_SUCCESS;
            continue;
        }
        if (r != CUDA_SUCCESS && strncmp(rep, "ok", 2)) {
            snprintf(reply, n, "err export of id %llu from pid %d: %s", im->key, im->peer, rep);
            goto out;
        }
        for (int j = 0; r == CUDA_SUCCESS && j < n_maps; j++) {
            if (maps[j].imp != i || !maps[j].down) continue;
            what = "map";
            r = REAL(r_map, "cuMemMap")(maps[j].va, maps[j].size, maps[j].offset, h, 0);
            if (r == CUDA_SUCCESS && maps[j].nacc) {
                what = "set access";
                r = REAL(r_set_access, "cuMemSetAccess")(maps[j].va, maps[j].size, maps[j].acc, maps[j].nacc);
            }
            if (r != CUDA_SUCCESS)
                snprintf(reply, n, "err remap id %llu from pid %d: %s %#llx+%zu: %s", im->key, im->peer,
                         what, (unsigned long long)maps[j].va, maps[j].size, errstr(r));
            else maps[j].down = 0;
            nm++;
        }
        if (r != CUDA_SUCCESS) {
            if (!strcmp(what, "import"))
                snprintf(reply, n, "err remap id %llu from pid %d: import: %s", im->key, im->peer, errstr(r));
            goto out;
        }
        im->cur_h = h;
        im->released = 0;
        if (im->app_released) REAL(r_release, "cuMemRelease")(h);  /* mappings hold the memory */
    }
    /* A release that failed after unmapping an import but before releasing
     * it: the handle is still held, map it again. */
    for (int j = 0; r == CUDA_SUCCESS && j < n_maps; j++) {
        struct map *m = &maps[j];
        if (!m->down || m->gone || imps[m->imp].released) continue;
        r = REAL(r_map, "cuMemMap")(m->va, m->size, m->offset, imps[m->imp].cur_h, 0);
        if (r == CUDA_SUCCESS && m->nacc) r = REAL(r_set_access, "cuMemSetAccess")(m->va, m->size, m->acc, m->nacc);
        if (r != CUDA_SUCCESS) {
            snprintf(reply, n, "err map again %#llx+%zu: %s", (unsigned long long)m->va, m->size, errstr(r));
            goto out;
        }
        m->down = 0;
        nm++;
    }
    /* Multicast objects: rejoin now, bind in "resume" (binding blocks until
     * every device of the group has been added, in every process). */
    int nmc = 0;
    for (int i = 0; i < n_mcs; i++) {
        struct mcobj *m = &mcs[i];
        if (m->dead || !m->released) continue;  /* a creator may be re-created by a peer's request */
        nmc++;
        if (m->creator) {
            if ((r = mc_recreate(m)) != CUDA_SUCCESS) {
                snprintf(reply, n, "err re-create multicast id %llu (%d device(s), first %d, %zu bytes, %u in group): %s",
                         m->key, m->ndev, m->ndev ? m->devs[0] : -1, m->prop.size, m->prop.numDevices, errstr(r));
                goto out;
            }
            continue;
        }
        CUmemGenericAllocationHandle h;
        r = m->peer > 0 ? refetch(m->peer, m->key, m->fab, &h, rep, sizeof(rep))
                        : remote_fetch(&m->fh, &h, rep, sizeof(rep));
        if (r != CUDA_SUCCESS && strncmp(rep, "ok", 2)) {
            snprintf(reply, n, "err export of multicast id %llu from pid %d: %s", m->key, m->peer, rep);
            goto out;
        }
        for (int d = 0; r == CUDA_SUCCESS && d < m->ndev; d++)
            r = REAL(r_mc_add, "cuMulticastAddDevice")(h, m->devs[d]);
        if (r != CUDA_SUCCESS) {
            snprintf(reply, n, "err rejoin multicast id %llu from pid %d: %s", m->key, m->peer, errstr(r));
            goto out;
        }
        m->cur_h = h;
        m->released = 0;
    }
    snprintf(reply, n, "ok remapped %d mapping(s), %d freed by their exporter, %d multicast object(s), "
             "%d host buffer(s)", nm, ngone, nmc, nh);
out:
    stage_put();
    pop_ctx(dev);
    pthread_mutex_unlock(&mu);
}

/* Bind multicast memory and map multicast objects again, then let the
 * app's launches through. */
static void do_resume(char *reply, size_t n)
{
    CUresult r = CUDA_SUCCESS;
    int nb = 0, nm = 0;
    if (lock_ctl()) { snprintf(reply, n, "err busy (state lock)"); return; }
    int dev = push_any_ctx();
    for (int i = 0; r == CUDA_SUCCESS && i < n_mcbinds; i++) {
        struct mcbind *b = &mcbinds[i];
        if (!b->unbound) continue;
        CUmemGenericAllocationHandle mc = mcs[b->mc].cur_h, h = b->memh;
        if (b->memh && b->memh_released)
            r = CU(cuMemRetainAllocationHandle, &h, (void *)b->ucva);
        if (r == CUDA_SUCCESS)
            r = b->memh ? REAL(r_mc_bind_mem, "cuMulticastBindMem")(mc, b->mcoff, h, b->memoff, b->size, b->flags)
                        : REAL(r_mc_bind_addr, "cuMulticastBindAddr")(mc, b->mcoff, b->addr, b->size, b->flags);
        if (b->memh && b->memh_released && h) REAL(r_release, "cuMemRelease")(h);
        if (r != CUDA_SUCCESS)
            snprintf(reply, n, "err rebind multicast id %llu: %s", mcs[b->mc].key, errstr(r));
        else b->unbound = 0;
        nb++;
    }
    for (int i = 0; r == CUDA_SUCCESS && i < n_mcmaps; i++) {
        struct map *m = &mcmaps[i];
        if (!m->down) continue;
        r = REAL(r_map, "cuMemMap")(m->va, m->size, m->offset, mcs[m->imp].cur_h, 0);
        if (r == CUDA_SUCCESS && m->nacc) r = REAL(r_set_access, "cuMemSetAccess")(m->va, m->size, m->acc, m->nacc);
        if (r != CUDA_SUCCESS)
            snprintf(reply, n, "err map multicast %#llx+%zu: %s", (unsigned long long)m->va, m->size, errstr(r));
        else m->down = 0;
        nm++;
    }
    if (r == CUDA_SUCCESS) {
        snprintf(reply, n, "ok resumed (%d multicast bind(s), %d multicast mapping(s))", nb, nm);
        gate_open();
    }
    pop_ctx(dev);
    pthread_mutex_unlock(&mu);
}

/* Reply to an export request: an fd, or (fab) the fabric handle as hex. */
static void send_export(int s, CUresult r, int fab, int fd, const CUmemFabricHandle *fh, const char *what)
{
    char rep[200];
    if (r != CUDA_SUCCESS) {
        snprintf(rep, sizeof(rep), "err export%s: %s", what, errstr(r));
        msg_send(s, rep, -1);
    } else if (fab) {
        strcpy(rep, "ok ");
        hex_encode(fh->data, sizeof(fh->data), rep + 3);
        msg_send(s, rep, -1);
    } else {
        msg_send(s, "ok", fd);
    }
    if (fd >= 0) close(fd);
}

/* Driver 610 will not export an allocation of 512 MiB or more that a
 * restored process created before its checkpoint (INVALID_VALUE), while
 * one created afterwards exports. FlashInfer's all-reduce workspace is
 * such an allocation. So the app's allocation mapped by lmaps[k] is
 * replaced (mu held): a new one with the same properties gets its
 * contents and takes its place, and the app's reference to the old one is
 * dropped. Only an allocation mapped once, from offset 0, is replaced. */
static CUresult replace_alloc(int k)
{
    struct lmap *l = &lmaps[k];
    CUmemGenericAllocationHandle old = l->h, nh;
    CUmemAllocationProp p;
    for (int j = 0; j < n_lmaps; j++)
        if (j != k && lmaps[j].h == old) return CUDA_ERROR_NOT_SUPPORTED;
    CUresult r = CU(cuMemGetAllocationPropertiesFromHandle, &p, old);
    if (r != CUDA_SUCCESS) return r;
    if (l->off || p.location.type != CU_MEM_LOCATION_TYPE_DEVICE) return CUDA_ERROR_NOT_SUPPORTED;
    CUmemAccessDesc a[MAX_DEV], own = { { CU_MEM_LOCATION_TYPE_DEVICE, p.location.id }, CU_MEM_ACCESS_FLAGS_PROT_READWRITE };
    int na = 0, ndev = 0;
    CU(cuDeviceGetCount, &ndev);
    for (int d = 0; d < ndev && d < MAX_DEV; d++) {
        CUmemLocation loc = { CU_MEM_LOCATION_TYPE_DEVICE, d };
        unsigned long long f = CU_MEM_ACCESS_FLAGS_PROT_NONE;
        if (CU(cuMemGetAccess, &f, &loc, l->va) == CUDA_SUCCESS && f != CU_MEM_ACCESS_FLAGS_PROT_NONE)
            a[na++] = (CUmemAccessDesc){ loc, (CUmemAccess_flags)f };
    }
    int dev = push_dev_ctx(p.location.id);
    CUdeviceptr tmp = 0;
    r = REAL(r_mem_create, "cuMemCreate")(&nh, l->size, &p, 0);
    if (r == CUDA_ERROR_NOT_PERMITTED && (p.requestedHandleTypes & CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR)) {
        p.requestedHandleTypes &= ~CU_MEM_HANDLE_TYPE_FABRIC;  /* see "IMEX channel" */
        r = REAL(r_mem_create, "cuMemCreate")(&nh, l->size, &p, 0);
    }
    if (r != CUDA_SUCCESS) goto out;
    if ((r = CU(cuMemAddressReserve, &tmp, l->size, 0, 0, 0)) == CUDA_SUCCESS &&
        (r = REAL(r_map, "cuMemMap")(tmp, l->size, 0, nh, 0)) == CUDA_SUCCESS) {
        if ((r = REAL(r_set_access, "cuMemSetAccess")(tmp, l->size, &own, 1)) == CUDA_SUCCESS &&
            (r = CU(cuMemcpyDtoD_v2, tmp, l->va, l->size)) == CUDA_SUCCESS)
            r = CU(cuCtxSynchronize);
        REAL(r_unmap, "cuMemUnmap")(tmp, l->size);
    }
    if (tmp) CU(cuMemAddressFree, tmp, l->size);
    if (r == CUDA_SUCCESS && (r = REAL(r_unmap, "cuMemUnmap")(l->va, l->size)) == CUDA_SUCCESS) {
        if ((r = REAL(r_map, "cuMemMap")(l->va, l->size, 0, nh, 0)) == CUDA_SUCCESS && na)
            r = REAL(r_set_access, "cuMemSetAccess")(l->va, l->size, a, na);
        if (r != CUDA_SUCCESS) {  /* put the old one back */
            REAL(r_unmap, "cuMemUnmap")(l->va, l->size);
            if (REAL(r_map, "cuMemMap")(l->va, l->size, 0, old, 0) != CUDA_SUCCESS ||
                (na && REAL(r_set_access, "cuMemSetAccess")(l->va, l->size, a, na) != CUDA_SUCCESS))
                logf_("replace %#llx: could not map the old allocation back", (unsigned long long)l->va);
        }
    }
    if (r != CUDA_SUCCESS) { REAL(r_release, "cuMemRelease")(nh); goto out; }
    REAL(r_release, "cuMemRelease")(old);
    l->h = nh;
    for (int j = 0; j < n_mcbinds; j++)
        if (mcbinds[j].memh == old) mcbinds[j].memh = nh;
    int j = 0;
    while (j < n_swaps && swaps[j].cur_h != old) j++;
    if (j < n_swaps) swaps[j].cur_h = nh;
    else PUSH(swaps, ((struct swap){ old, nh }));
out:
    pop_ctx(dev);
    return r;
}

/* Export our allocation or multicast object `id` afresh (mu held, a
 * context current): an fd, or (fab) a fabric handle. NOT_FOUND: freed. */
static CUresult export_id(unsigned long long id, int fab, int *fd, CUmemFabricHandle *fh)
{
    CUmemAllocationHandleType type = fab ? CU_MEM_HANDLE_TYPE_FABRIC : CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR;
    void *out = fab ? (void *)fh : (void *)fd;
    for (int m = 0; m < n_mcs; m++) {
        if (!mcs[m].creator || mcs[m].key != id || mcs[m].dead) continue;  /* ids are ours */
        CUresult r = CUDA_SUCCESS;
        if (mcs[m].released) r = mc_recreate(&mcs[m]);  /* a peer rejoins before our own "remap" */
        return r == CUDA_SUCCESS ? REAL(r_export, "cuMemExportToShareableHandle")(out, mcs[m].cur_h, type, 0) : r;
    }
    int i = 0;
    while (i < n_xregs && xregs[i].id != id) i++;
    if (i == n_xregs) return CUDA_ERROR_NOT_FOUND;  /* freed: never export a handle we do not hold (the driver crashes) */
    for (int v = 0; v < n_vallocs; v++)  /* a peer re-imports before our own "remap" */
        if (xregs[i].va >= vallocs[v].va && xregs[i].va < vallocs[v].va + vallocs[v].size) {
            CUresult r = valloc_restore(&vallocs[v]);
            if (r != CUDA_SUCCESS) return r;
        }
    /* NCCL exports a retained handle and releases it: take our own. */
    CUmemGenericAllocationHandle h = xregs[i].h;
    int retained = !h;
    CUresult r = retained ? CU(cuMemRetainAllocationHandle, &h, (void *)xregs[i].va) : CUDA_SUCCESS;
    if (r == CUDA_SUCCESS)
        r = REAL(r_export, "cuMemExportToShareableHandle")(out, h, type, 0);
    if (retained && h) REAL(r_release, "cuMemRelease")(h);
    if (r == CUDA_ERROR_INVALID_VALUE && retained) {  /* see replace_alloc */
        int k = 0;
        while (k < n_lmaps && lmaps[k].va != xregs[i].va) k++;
        CUresult r2 = k < n_lmaps ? replace_alloc(k) : CUDA_ERROR_NOT_FOUND;
        logf_("export id %llu va %#llx: %s; replaced the allocation: %s", id, (unsigned long long)xregs[i].va,
              errstr(r), errstr(r2));
        if (r2 == CUDA_SUCCESS)
            r = REAL(r_export, "cuMemExportToShareableHandle")(out, lmaps[k].h, type, 0);
    }
    if (getenv("NVSNAP_GPUSHARE_DEBUG")) {
        CUmemAllocationProp p = {0};
        CUmemGenericAllocationHandle ph;
        if (CU(cuMemRetainAllocationHandle, &ph, (void *)xregs[i].va) == CUDA_SUCCESS) {
            CU(cuMemGetAllocationPropertiesFromHandle, &p, ph);
            REAL(r_release, "cuMemRelease")(ph);
        }
        logf_("export id %llu va %#llx (loc %d/%d handles %#x): %s", id, (unsigned long long)xregs[i].va,
              p.location.type, p.location.id, p.requestedHandleTypes, errstr(r));
    }
    return r;
}

/* A peer asks for a fresh export of our shared allocation `id`. */
static void do_export(unsigned long long id, int fab, int s)
{
    int fd = -1;
    CUmemFabricHandle fh;
    if (lock_ctl()) { msg_send(s, "err busy", -1); return; }
    int dev = push_any_ctx();
    CUresult r = export_id(id, fab, &fd, &fh);
    pop_ctx(dev);
    pthread_mutex_unlock(&mu);
    if (r == CUDA_ERROR_NOT_FOUND) msg_send(s, "err gone", -1);
    else send_export(s, r, fab, fd, &fh, "");
}

/* A peer that imported fd asks whether we exported it, and as what. */
static void do_whose(int fd, int s)
{
    char rep[64] = "err not ours";
    pid_t me = getpid();
    if (lock_ctl()) { msg_send(s, "err busy", -1); return; }
    for (int i = 0; i < n_exps; i++)
        if (exps[i].id && syscall(SYS_kcmp, me, me, KCMP_FILE, exps[i].fd, fd) == 0) {
            snprintf(rep, sizeof(rep), "ok %llu%s", exps[i].id, exps[i].mc ? " mc" : "");
            break;
        }
    pthread_mutex_unlock(&mu);
    msg_send(s, rep, -1);
}

/* ... or, for a fabric handle (hex), whether we exported it. */
static void do_whosefab(const char *hex, int s)
{
    char rep[64] = "err not ours";
    CUmemFabricHandle fh;
    if (hex_decode(hex, fh.data, sizeof(fh.data)) < 0) { msg_send(s, "err bad fabric handle", -1); return; }
    if (lock_ctl()) { msg_send(s, "err busy", -1); return; }
    for (int i = 0; i < n_fexps; i++)
        if (fexps[i].id && !memcmp(fexps[i].fh.data, fh.data, sizeof(fh.data))) {
            snprintf(rep, sizeof(rep), "ok %llu%s", fexps[i].id, fexps[i].mc ? " mc" : "");
            break;
        }
    pthread_mutex_unlock(&mu);
    msg_send(s, rep, -1);
}

static void serve(int s)
{
    char buf[256], rep[256];
    int fd = -1;
    if (msg_recv(s, buf, sizeof(buf), &fd) < 0) return;
    if (!strcmp(buf, "quiesce")) { do_quiesce(rep, sizeof(rep)); msg_send(s, rep, -1); }
    else if (!strcmp(buf, "release")) { do_release(rep, sizeof(rep), NULL); msg_send(s, rep, -1); }
    else if (!strncmp(buf, "release ", 8)) { do_release(rep, sizeof(rep), buf + 8); msg_send(s, rep, -1); }
    else if (!strcmp(buf, "load")) { do_load(rep, sizeof(rep), NULL); msg_send(s, rep, -1); }
    else if (!strncmp(buf, "load ", 5)) { do_load(rep, sizeof(rep), buf + 5); msg_send(s, rep, -1); }
    else if (!strcmp(buf, "remap")) { do_remap(rep, sizeof(rep), NULL); msg_send(s, rep, -1); }
    else if (!strncmp(buf, "remap ", 6)) { do_remap(rep, sizeof(rep), buf + 6); msg_send(s, rep, -1); }
    else if (!strncmp(buf, "fabmap ", 7)) { do_fabmap(rep, sizeof(rep), buf + 7); msg_send(s, rep, -1); }
    else if (!strcmp(buf, "resume")) { do_resume(rep, sizeof(rep)); msg_send(s, rep, -1); }
    else if (!strncmp(buf, "export ", 7)) do_export(strtoull(buf + 7, NULL, 10), 0, s);
    else if (!strncmp(buf, "exportfab ", 10)) do_export(strtoull(buf + 10, NULL, 10), 1, s);
    else if (!strcmp(buf, "whose") && fd >= 0) do_whose(fd, s);
    else if (!strncmp(buf, "whosefab ", 9)) do_whosefab(buf + 9, s);
    else msg_send(s, "err unknown command", -1);
    if (fd >= 0) close(fd);
    if (strncmp(buf, "export", 6) && strncmp(buf, "whose", 5))
        logf_("%s: %s", buf, rep);
}

/* Our threads block every signal but synchronous faults, which would
 * otherwise kill the process without a trace. */
static __thread int wc_thread;
static struct sigaction old_sa[NSIG];
static const int fault_sigs[] = { SIGSEGV, SIGBUS, SIGILL, SIGFPE, SIGABRT };

static void on_fault(int sig, siginfo_t *si, void *uc)
{
    if (wc_thread) {
        char b[160];
        int n = snprintf(b, sizeof(b), "[nvsnap-gpushare %d] signal %d in a gpushare thread, addr %p\n",
                         getpid(), sig, si->si_addr);
        ssize_t __attribute__((unused)) w = write(2, b, n);
        void *bt[48];
        backtrace_symbols_fd(bt, backtrace(bt, 48), 2);
    }
    /* Re-run the fault under the previous handler (e.g. Python faulthandler). */
    sigaction(sig, &old_sa[sig], NULL);
    (void)uc;
}

static void wc_thread_init(void)
{
    sigset_t m;
    sigfillset(&m);
    for (size_t i = 0; i < sizeof(fault_sigs) / sizeof(fault_sigs[0]); i++) sigdelset(&m, fault_sigs[i]);
    pthread_sigmask(SIG_SETMASK, &m, NULL);
    prctl(PR_SET_NAME, GPUSHARE_THREAD);
    wc_thread = 1;
}

static void install_fault_handler(void)
{
    void *bt[1];
    backtrace(bt, 1);  /* load libgcc now, not in the handler */
    struct sigaction sa = { .sa_sigaction = on_fault, .sa_flags = SA_SIGINFO | SA_NODEFER };
    sigemptyset(&sa.sa_mask);
    for (size_t i = 0; i < sizeof(fault_sigs) / sizeof(fault_sigs[0]); i++)
        sigaction(fault_sigs[i], &sa, &old_sa[fault_sigs[i]]);
}

static int listen_fd = -1;
static pid_t ctl_pid;  /* pid the control thread was started for */

static void *ctl_main(void *arg)
{
    wc_thread_init();
    for (;;) {
        int s = accept4(listen_fd, NULL, NULL, SOCK_CLOEXEC);
        if (s < 0) { if (errno == EINTR || errno == ECONNABORTED) continue; break; }
        if (peer_ok(s, 0)) serve(s);
        close(s);
    }
    return arg;
}

static void start_ctl(void)
{
    static pthread_mutex_t smu = PTHREAD_MUTEX_INITIALIZER;
    pthread_mutex_lock(&smu);
    if (ctl_pid != getpid()) {
        struct sockaddr_un a;
        socklen_t len = sock_addr(getpid(), &a);
        if (listen_fd >= 0) close(listen_fd);  /* inherited across fork */
        listen_fd = socket(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0);
        pthread_t t;
        if (listen_fd >= 0 && bind(listen_fd, (struct sockaddr *)&a, len) == 0 &&
            listen(listen_fd, 16) == 0 && pthread_create(&t, NULL, ctl_main, NULL) == 0) {
            pthread_detach(t);
            if (!ctl_pid) install_fault_handler();
            ctl_pid = getpid();
        } else {
            logf_("control socket: %s", strerror(errno));
        }
    }
    pthread_mutex_unlock(&smu);
}

/* Ask every process with a control socket whether it exported fd, or
 * (req "whosefab <hex>") a fabric handle. Only processes on this node are
 * asked: memory shared across nodes (multi-node NVLink) is not tracked, so
 * its checkpoint is refused. */
static int find_exporter(const char *req, int fd, unsigned long long *key, int *mc)
{
    char line[512], rep[64], name[64];
    int me = getpid(), found = 0;
    FILE *fp = fopen("/proc/net/unix", "r");
    if (!fp) return 0;
    while (!found && fgets(line, sizeof(line), fp)) {
        char *p = strstr(line, "@nvsnap-gpushare.");
        int pid;
        if (!p || sscanf(p + 1, GPUSHARE_SOCK_FMT, &pid) != 1 || pid == me) continue;
        snprintf(name, sizeof(name), GPUSHARE_SOCK_FMT, pid);
        if (strncmp(p + 1, name, strlen(name)) || (p[1 + strlen(name)] != '\n' && p[1 + strlen(name)] != 0))
            continue;
        if (request(pid, req, fd, rep, sizeof(rep), NULL) == 0 && !strncmp(rep, "ok ", 3)) {
            char *end;
            *key = strtoull(rep + 3, &end, 10);
            *mc = !strcmp(end, " mc");
            found = pid;
        }
    }
    fclose(fp);
    return found;
}

/* ── Wrappers ─────────────────────────────────────────────────────────── */

static CUresult w_export(void *sh, CUmemGenericAllocationHandle h, CUmemAllocationHandleType type,
                         unsigned long long flags)
{
    pthread_mutex_lock(&mu);
    int mc = find_mc(h);
    h = mc >= 0 ? mcs[mc].cur_h : own_h(h);
    pthread_mutex_unlock(&mu);
    CUresult r = REAL(r_export, "cuMemExportToShareableHandle")(sh, h, type, flags);
    if (r == CUDA_SUCCESS && type == CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR) {
        start_ctl();
        int fd = fcntl(*(int *)sh, F_DUPFD_CLOEXEC, 0);
        CUdeviceptr va = 0;
        pthread_mutex_lock(&mu);
        if (mc >= 0) {
            /* Ids are per creator: a re-export by an importer stays
             * unregistered, so its importers' checkpoint is refused. */
            if (fd >= 0 && mcs[mc].creator) PUSH(exps, ((struct exp){ fd, mcs[mc].key, 1, 0 }));
            else if (fd >= 0) close(fd);
            pthread_mutex_unlock(&mu);
            return r;
        }
        for (int i = 0; !va && i < n_lmaps; i++) if (lmaps[i].h == h) va = lmaps[i].va;
        for (int i = 0; !va && i < n_rets; i++) if (rets[i].h == h) va = rets[i].va;
        /* Not mapped yet: registered when it is (w_map). Until then importers
         * find no exporter, and checkpoint is refused. */
        if (fd >= 0) PUSH(exps, ((struct exp){ fd, va ? xreg_id(va, 0) : 0, 0, va ? 0 : h }));
        pthread_mutex_unlock(&mu);
    } else if (r == CUDA_SUCCESS && type == CU_MEM_HANDLE_TYPE_FABRIC) {
        start_ctl();
        struct fexp e = { .fh = *(CUmemFabricHandle *)sh };
        if (getenv("NVSNAP_GPUSHARE_DEBUG")) {
            char hx[129];
            hex_encode(e.fh.data, sizeof(e.fh.data), hx);
            logf_("fabric export h=%llu mc=%d %.32s", (unsigned long long)h, mc, hx);
        }
        pthread_mutex_lock(&mu);
        if (mc >= 0) {
            e.id = mcs[mc].key;
            e.mc = 1;
            if (mcs[mc].creator) PUSH(fexps, e);  /* ids are per creator, as above */
        } else {
            CUdeviceptr va = 0;
            for (int i = 0; !va && i < n_lmaps; i++) if (lmaps[i].h == h) va = lmaps[i].va;
            for (int i = 0; !va && i < n_rets; i++) if (rets[i].h == h) va = rets[i].va;
            if (va) e.id = xreg_id(va, 0);
            else e.h = h;  /* registered when mapped, as above */
            PUSH(fexps, e);
        }
        pthread_mutex_unlock(&mu);
    }
    return r;
}

/* Did this process export os (an fd, or a fabric handle)? Returns our pid. */
static int self_export(void *os, int fab, unsigned long long *key, int *mc)
{
    pid_t me = getpid();
    int found = 0;
    pthread_mutex_lock(&mu);
    for (int i = 0; fab && !found && i < n_fexps; i++)
        if (fexps[i].id && !memcmp(fexps[i].fh.data, ((CUmemFabricHandle *)os)->data, sizeof(fexps[i].fh.data))) {
            *key = fexps[i].id; *mc = fexps[i].mc; found = me;
        }
    for (int i = 0; !fab && !found && i < n_exps; i++)
        if (exps[i].id && syscall(SYS_kcmp, me, me, KCMP_FILE, exps[i].fd, (int)(uintptr_t)os) == 0) {
            *key = exps[i].id; *mc = exps[i].mc; found = me;
        }
    pthread_mutex_unlock(&mu);
    return found;
}

static CUresult w_import(CUmemGenericAllocationHandle *h, void *os, CUmemAllocationHandleType type)
{
    CUresult r = REAL(r_import, "cuMemImportFromShareableHandle")(h, os, type);
    int fab = type == CU_MEM_HANDLE_TYPE_FABRIC;
    if (r != CUDA_SUCCESS || (type != CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR && !fab)) return r;
    start_ctl();
    unsigned long long key = 0;
    int mc = 0, peer = self_export(os, fab, &key, &mc);
    if (peer) {
        /* our own export (NCCL probes IMEX this way) */
    } else if (fab) {
        char req[16 + 2 * sizeof(((CUmemFabricHandle *)0)->data)] = "whosefab ";
        hex_encode(((CUmemFabricHandle *)os)->data, sizeof(((CUmemFabricHandle *)os)->data), req + 9);
        peer = find_exporter(req, -1, &key, &mc);
        if (getenv("NVSNAP_GPUSHARE_DEBUG")) logf_("fabric import h=%llu peer=%d %.32s", (unsigned long long)*h, peer, req + 9);
    } else {
        peer = find_exporter("whose", (int)(uintptr_t)os, &key, &mc);
    }
    /* Not found: looked up again at "quiesce" (see struct imp). */
    struct imp im = { .app_h = *h, .cur_h = *h, .peer = peer, .key = key, .fab = fab };
    if (!peer && fab) im.fh = *(CUmemFabricHandle *)os;
    if (!peer && !fab) im.ufd = fcntl((int)(uintptr_t)os, F_DUPFD_CLOEXEC, 3);
    if (im.ufd < 0) im.ufd = 0;
    pthread_mutex_lock(&mu);
    if (mc) PUSH(mcs, ((struct mcobj){ .app_h = *h, .cur_h = *h, .peer = peer, .key = key, .fab = fab }));
    else PUSH(imps, im);
    pthread_mutex_unlock(&mu);
    return r;
}

static CUresult w_map(CUdeviceptr va, size_t size, size_t off, CUmemGenericAllocationHandle h,
                      unsigned long long flags)
{
    pthread_mutex_lock(&mu);
    int i = find_imp(h), m = i < 0 ? find_mc(h) : -1;
    if (i >= 0) h = imps[i].cur_h;
    if (m >= 0) h = mcs[m].cur_h;
    if (i < 0 && m < 0) h = own_h(h);
    CUresult r = REAL(r_map, "cuMemMap")(va, size, off, h, flags);
    if (r == CUDA_SUCCESS) {
        if (i >= 0) PUSH(maps, ((struct map){ .va = va, .size = size, .offset = off, .imp = i }));
        else if (m >= 0) PUSH(mcmaps, ((struct map){ .va = va, .size = size, .offset = off, .imp = m }));
        else {
            PUSH(lmaps, ((struct lmap){ va, size, h, off }));
            for (int j = 0; j < n_exps; j++) if (exps[j].h == h && !exps[j].id) { exps[j].id = xreg_id(va, 0); exps[j].h = 0; }
            for (int j = 0; j < n_fexps; j++) if (fexps[j].h == h && !fexps[j].id) { fexps[j].id = xreg_id(va, 0); fexps[j].h = 0; }
        }
    }
    pthread_mutex_unlock(&mu);
    return r;
}

static CUresult w_unmap(CUdeviceptr va, size_t size)
{
    CUresult r = REAL(r_unmap, "cuMemUnmap")(va, size);
    if (r != CUDA_SUCCESS) return r;
    pthread_mutex_lock(&mu);
    for (int i = n_maps - 1; i >= 0; i--)
        if (maps[i].va >= va && maps[i].va < va + size) DEL(maps, i);
    for (int i = n_mcmaps - 1; i >= 0; i--)
        if (mcmaps[i].va >= va && mcmaps[i].va < va + size) DEL(mcmaps, i);
    for (int i = n_lmaps - 1; i >= 0; i--)
        if (lmaps[i].va >= va && lmaps[i].va < va + size) DEL(lmaps, i);
    for (int i = n_rets - 1; i >= 0; i--)
        if (rets[i].va >= va && rets[i].va < va + size) DEL(rets, i);
    xreg_kill(va, size);
    /* Forget imports the app released that are no longer mapped. Imports
     * are referenced by index from maps, so only trailing ones go. */
    while (n_imps && imps[n_imps - 1].app_released) {
        int used = 0;
        for (int j = 0; j < n_maps; j++) used |= maps[j].imp == n_imps - 1;
        if (used) break;
        n_imps--;
    }
    pthread_mutex_unlock(&mu);
    return r;
}

static CUresult w_release(CUmemGenericAllocationHandle h)
{
    pthread_mutex_lock(&mu);
    int i = find_imp(h), m = i < 0 ? find_mc(h) : -1;
    if (i >= 0) { h = imps[i].cur_h; imps[i].app_released = 1; }
    if (i >= 0 && imps[i].ufd && !imp_live(i)) { close(imps[i].ufd); imps[i].ufd = 0; }  /* pins the exporter's memory */
    if (m >= 0) { h = mcs[m].cur_h; mcs[m].app_released = 1; }
    for (int j = n_swaps - 1; i < 0 && m < 0 && j >= 0; j--)
        if (swaps[j].app_h == h) { h = swaps[j].cur_h; DEL(swaps, j); }
    for (int j = 0; i < 0 && m < 0 && j < n_mcbinds; j++)
        if (mcbinds[j].memh == h) mcbinds[j].memh_released = 1;
    /* Exported, never mapped: forget it before the handle value is reused. */
    for (int j = n_exps - 1; i < 0 && m < 0 && j >= 0; j--)
        if (!exps[j].id && exps[j].h == h) { close(exps[j].fd); DEL(exps, j); }
    for (int j = n_fexps - 1; i < 0 && m < 0 && j >= 0; j--)
        if (!fexps[j].id && fexps[j].h == h) DEL(fexps, j);
    CUresult r = REAL(r_release, "cuMemRelease")(h);
    pthread_mutex_unlock(&mu);
    return r;
}

/* cuMemSetAccess changes the access of the locations it names and keeps
 * the others': merge, so that remap grants all of them again. */
static void acc_merge(struct map *m, const CUmemAccessDesc *d, size_t n)
{
    for (size_t k = 0; k < n; k++) {
        int j = 0;
        while (j < m->nacc && (m->acc[j].location.type != d[k].location.type ||
                               m->acc[j].location.id != d[k].location.id)) j++;
        if (d[k].flags == CU_MEM_ACCESS_FLAGS_PROT_NONE) {
            if (j < m->nacc) m->acc[j] = m->acc[--m->nacc];
        } else if (j < m->nacc) {
            m->acc[j] = d[k];
        } else if (m->nacc < MAX_ACC) {
            m->acc[m->nacc++] = d[k];
        } else {
            logf_("access for %#llx: more than %d locations, not all restored", (unsigned long long)m->va, MAX_ACC);
        }
    }
}

static CUresult w_set_access(CUdeviceptr va, size_t size, const CUmemAccessDesc *d, size_t n)
{
    CUresult r = REAL(r_set_access, "cuMemSetAccess")(va, size, d, n);
    if (r != CUDA_SUCCESS) return r;
    pthread_mutex_lock(&mu);
    for (int i = 0; i < n_maps; i++)
        if (maps[i].va >= va && maps[i].va < va + size) acc_merge(&maps[i], d, n);
    for (int i = 0; i < n_mcmaps; i++)
        if (mcmaps[i].va >= va && mcmaps[i].va < va + size) acc_merge(&mcmaps[i], d, n);
    pthread_mutex_unlock(&mu);
    return r;
}

static __typeof__(&cuMemRetainAllocationHandle) r_retain;

/* Remember which VA a retained handle came from (see do_export). */
static CUresult w_retain(CUmemGenericAllocationHandle *h, void *addr)
{
    CUresult r = REAL(r_retain, "cuMemRetainAllocationHandle")(h, addr);
    if (r != CUDA_SUCCESS) return r;
    CUdeviceptr a = (CUdeviceptr)addr, base = a;
    pthread_mutex_lock(&mu);
    for (int i = 0; i < n_lmaps; i++)
        if (a >= lmaps[i].va && a < lmaps[i].va + lmaps[i].size) base = lmaps[i].va;
    int i = 0;
    while (i < n_rets && rets[i].h != *h) i++;
    if (i == n_rets) PUSH(rets, ((struct lmap){ base, 0, *h, 0 }));
    else rets[i].va = base;
    pthread_mutex_unlock(&mu);
    return r;
}

/* ── CUDA IPC on cuMem ───────────────────────────────────────────────── */

/* The driver restores neither legacy CUDA IPC imports nor the ability to
 * export a restored allocation (cuIpcOpenMemHandle: NOT_PERMITTED). So
 * cuMemAlloc of at least VMM_MIN bytes is backed by an exportable cuMem
 * allocation, and its IPC handles are ours: opening one fetches an fd from
 * the exporter's control thread and maps it like any other import, which
 * release/remap then cover. VLLM custom all-reduce shares its buffers and
 * PyTorch's (CUDA graph inputs) this way. */
#define VMM_MIN (2UL << 20)
#define IPC_MAGIC 0x4E56475348ULL  /* "NVGSH" */


struct wc_ipc { uint64_t magic; int32_t pid, pad; uint64_t base, size, key; /* xreg id */ };
_Static_assert(sizeof(struct wc_ipc) <= sizeof(CUipcMemHandle), "IPC handle too small");

static __typeof__(&cuMemAlloc) r_alloc;
static __typeof__(&cuMemFree) r_free;
static __typeof__(&cuIpcGetMemHandle) r_ipc_get;
static __typeof__(&cuIpcOpenMemHandle) r_ipc_open;
static __typeof__(&cuIpcCloseMemHandle) r_ipc_close;

static CUmemAccessDesc rw_on(CUdevice dev)
{
    CUmemAccessDesc a = { { CU_MEM_LOCATION_TYPE_DEVICE, dev }, CU_MEM_ACCESS_FLAGS_PROT_READWRITE };
    return a;
}

/* Peer access the app enabled (cuCtxEnablePeerAccess), [owner][accessor].
 * cuMemAlloc memory gets it from the driver; cuMem memory (ours, w_alloc)
 * only through cuMemSetAccess on each allocation. */
static unsigned char peer_acc[MAX_DEV][MAX_DEV];

/* Access for an allocation on dev: dev, and its peers (mu held). */
static int valloc_access(CUdevice dev, CUmemAccessDesc *a)
{
    int n = 0;
    a[n++] = rw_on(dev);
    for (int d = 0; dev < MAX_DEV && d < MAX_DEV; d++)
        if (peer_acc[dev][d]) a[n++] = rw_on(d);
    return n;
}

static CUmemAllocationProp valloc_prop(CUdevice dev)
{
    CUmemAllocationProp p = { .type = CU_MEM_ALLOCATION_TYPE_PINNED,
        .location = { CU_MEM_LOCATION_TYPE_DEVICE, dev },
        .requestedHandleTypes = CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR };
    return p;
}

static CUresult w_alloc(CUdeviceptr *dptr, size_t size)
{
    CUdevice dev;
    if (size < VMM_MIN || CU(cuCtxGetDevice, &dev) != CUDA_SUCCESS)
        return REAL(r_alloc, "cuMemAlloc_v2")(dptr, size);
    CUmemAllocationProp p = valloc_prop(dev);
    size_t gran = 0;
    CUresult r = CU(cuMemGetAllocationGranularity, &gran, &p, CU_MEM_ALLOC_GRANULARITY_MINIMUM);
    if (r != CUDA_SUCCESS) return r;
    if (!gran) return CUDA_ERROR_INVALID_VALUE;
    size = (size + gran - 1) / gran * gran;
    CUmemGenericAllocationHandle h;
    CUdeviceptr va = 0;
    if ((r = CU(cuMemCreate, &h, size, &p, 0)) != CUDA_SUCCESS) return r;
    if ((r = CU(cuMemAddressReserve, &va, size, 0, 0, 0)) != CUDA_SUCCESS) goto free_h;
    if ((r = REAL(r_map, "cuMemMap")(va, size, 0, h, 0)) != CUDA_SUCCESS) goto free_va;
    CUmemAccessDesc a[MAX_DEV + 1];
    pthread_mutex_lock(&mu);
    int na = valloc_access(dev, a);
    pthread_mutex_unlock(&mu);
    if ((r = REAL(r_set_access, "cuMemSetAccess")(va, size, a, na)) != CUDA_SUCCESS) goto unmap;
    pthread_mutex_lock(&mu);
    PUSH(vallocs, ((struct valloc){ va, size, h, dev, 0, NULL, NULL }));
    pthread_mutex_unlock(&mu);
    start_ctl();  /* "release" must save it (valloc_drop) */
    *dptr = va;
    return CUDA_SUCCESS;
unmap:
    REAL(r_unmap, "cuMemUnmap")(va, size);
free_va:
    CU(cuMemAddressFree, va, size);
free_h:
    REAL(r_release, "cuMemRelease")(h);
    return r;
}

/* On Grace (driver 610) the checkpoint of a TP worker fails (OUT_OF_MEMORY)
 * once it holds a dozen or so of these allocations, while the same memory
 * from cuMemAlloc checkpoints. So "release" saves each one and frees it,
 * keeping the VA reserved; "load" or "remap" (or a peer's export request,
 * whichever comes first) re-creates it at the same VA.
 *
 * "release <store> <ckpt-dir>" saves them to a chunk store instead of host
 * memory: each STAGE_SIZE chunk goes to <store>/chunks/xx/<hash> unless an
 * identical chunk is there already (weights repeat across checkpoints of a
 * model), all-zero chunks are not stored, and <ckpt-dir>/gpu-<pid>.chunks
 * lists the chunks used. Every process saves at once, and the data stays out
 * of a CRIU image of the process. mu held, a context current. */
static CUresult stage_get(void)
{
    return stage ? CUDA_SUCCESS : REAL(r_alloc_host, "cuMemAllocHost_v2")(&stage, STAGE_SIZE);
}

/* Page-locked memory must not outlive "release" or "remap". */
static void stage_put(void)
{
    if (stage) REAL(r_free_host, "cuMemFreeHost")(stage);
    stage = NULL;
}

static inline uint64_t rotl64(uint64_t x, int r) { return (x << r) | (x >> (64 - r)); }
static inline uint64_t fmix64(uint64_t k)
{
    k ^= k >> 33; k *= 0xff51afd7ed558ccdULL; k ^= k >> 33; k *= 0xc4ceb9fe1a85ec53ULL; return k ^ (k >> 33);
}

/* 128-bit content hash (four xxHash64-style lanes): identifies chunks in
 * the store. Not cryptographic; accidental collisions are negligible. */
static struct chunk chunk_hash(const void *p, size_t len)
{
    const uint64_t P1 = 0x9E3779B185EBCA87ULL, P2 = 0xC2B2AE3D27D4EB4FULL;
    const uint64_t *q = p;
    const size_t n = len / 8;
    uint64_t a = P1 + P2, b = P2, c = 0, d = 0 - P1;
    size_t i = 0;
    for (; i + 4 <= n; i += 4) {
        a = rotl64(a + q[i] * P2, 31) * P1;
        b = rotl64(b + q[i + 1] * P2, 31) * P1;
        c = rotl64(c + q[i + 2] * P2, 31) * P1;
        d = rotl64(d + q[i + 3] * P2, 31) * P1;
    }
    for (; i < n; i++) a = rotl64(a ^ q[i] * P2, 27) * P1;
    for (size_t j = n * 8; j < len; j++) b = rotl64(b ^ ((const unsigned char *)p)[j] * P1, 11) * P2;
    /* both halves depend on every lane */
    uint64_t h0 = fmix64(a ^ rotl64(b, 17) ^ rotl64(c, 31) ^ rotl64(d, 47) ^ len);
    uint64_t h1 = fmix64(b ^ rotl64(c, 13) ^ rotl64(d, 29) ^ rotl64(a, 43) ^ (h0 * P1));
    struct chunk h = { { h0, h1 } };
    if (!h.h[0] && !h.h[1]) h.h[1] = 1;  /* 0 means "all zero" */
    return h;
}

static void chunk_path(const char *root, const struct chunk *c, char *out, size_t n)
{
    snprintf(out, n, "%s/chunks/%02x/%016llx%016llx", root, (unsigned)(c->h[0] >> 56),
             (unsigned long long)c->h[0], (unsigned long long)c->h[1]);
}

/* Chunk files bypass the page cache (O_DIRECT): buffered I/O on network
 * block storage (a PVC) ran at a third of its direct-I/O throughput,
 * and nothing re-reads a chunk soon. stage is page aligned; fall back to
 * buffered I/O for odd lengths or filesystems without O_DIRECT. */
static int chunk_open(const char *path, int flags, size_t len)
{
    if (len % 4096 == 0) {
        int fd = open(path, flags | O_DIRECT | O_CLOEXEC, 0644);
        if (fd >= 0 || errno == EEXIST) return fd;
    }
    return open(path, flags | O_CLOEXEC, 0644);
}

/* Read (pread from 0) or write len bytes of stage; returns bytes moved.
 * Some filesystems accept O_DIRECT at open and fail the I/O (EINVAL, e.g.
 * a larger alignment): carry on buffered. */
static size_t chunk_io(int fd, int wr, size_t len)
{
    size_t done = 0;
    while (done < len) {
        ssize_t k = wr ? write(fd, (char *)stage + done, len - done) : pread(fd, (char *)stage + done, len - done, done);
        int fl;
        if (k < 0 && errno == EINVAL && (fl = fcntl(fd, F_GETFL)) >= 0 && (fl & O_DIRECT) &&
            fcntl(fd, F_SETFL, fl & ~O_DIRECT) == 0) continue;
        if (k < 0 && errno == EINTR) continue;
        if (k <= 0) break;
        done += k;
    }
    return done;
}

static int all_zero(const void *p, size_t len)
{
    const uint64_t *q = p;
    for (size_t i = 0; i < len / 8; i++) if (q[i]) return 0;
    for (size_t i = len / 8 * 8; i < len; i++) if (((const unsigned char *)p)[i]) return 0;
    return 1;
}

/* Write the chunk in stage to path: a temp file, synced, then renamed, so
 * neither concurrent writers nor a crash expose a partial one. Writers in
 * other pods (other pid namespaces, same pids) share the store and the
 * cache: the temp name is random and created exclusively. */
static int chunk_write(char *path, size_t len)
{
    char tmp[560];
    *strrchr(path, '/') = 0;  /* <root>/chunks/xx: create both levels */
    *strrchr(path, '/') = 0;
    mkdir(path, 0755);
    path[strlen(path)] = '/';
    mkdir(path, 0755);
    path[strlen(path)] = '/';
    int fd = -1;
    for (int i = 0; fd < 0 && i < 8; i++) {
        uint64_t r;
        if (getrandom(&r, sizeof(r), 0) != sizeof(r)) return -1;
        snprintf(tmp, sizeof(tmp), "%s.%016llx.tmp", path, (unsigned long long)r);
        if ((fd = chunk_open(tmp, O_WRONLY | O_CREAT | O_EXCL, len)) < 0 && errno != EEXIST) return -1;
    }
    if (fd < 0) return -1;
    /* Data on disk before the name: after a crash, a chunk name must never
     * point at torn data (dedupe and cache hits trust the name). */
    int ok = chunk_io(fd, 1, len) == len && fdatasync(fd) == 0;
    close(fd);
    if (!ok || rename(tmp, path) != 0) { unlink(tmp); return -1; }
    return 0;
}

/* Copy the chunk in stage into the node cache (best effort: the store has it). */
static void cache_put(const struct chunk *c, size_t len)
{
    char path[512];
    if (!cache[0]) return;
    chunk_path(cache, c, path, sizeof(path));
    if (access(path, F_OK) != 0) chunk_write(path, len);
}

/* Store one chunk (in stage) unless already there; keep a copy in the cache. */
static CUresult chunk_put(const struct chunk *c, size_t len)
{
    char path[512];
    chunk_path(store, c, path, sizeof(path));
    if (chunk_list) fprintf(chunk_list, "%s\n", strrchr(path, '/') + 1);
    if (access(path, F_OK) == 0) st_dedup += len;
    else if (chunk_write(path, len) == 0) st_written += len;
    else return CUDA_ERROR_FILE_NOT_FOUND;
    cache_put(c, len);
    return CUDA_SUCCESS;
}

/* Read chunk c into stage: from the node cache if there (marking it used,
 * for cache-gc's LRU), else from the store. A miss does not fill the cache:
 * that would put cache writes on the restore's critical path. Saves write
 * through to it, and nvsnap-gpu-suspend cache-prefetch fills it (e.g. in
 * the background after a restore). */
static int chunk_get(const struct chunk *c, size_t len)
{
    char path[512];
    for (int from_cache = !!cache[0]; from_cache >= 0; from_cache--) {
        chunk_path(from_cache ? cache : store, c, path, sizeof(path));
        int fd = chunk_open(path, O_RDONLY, len);
        if (fd < 0) {
            if (!from_cache) logf_("chunk %s: %s", path, strerror(errno));
            continue;
        }
        size_t got = chunk_io(fd, 0, len);
        if (got == len && from_cache) futimens(fd, NULL);
        close(fd);
        if (got < len) continue;
        if (from_cache) st_hit += len;
        else st_miss += len;
        return 0;
    }
    return -1;
}

static CUresult valloc_save(struct valloc *v);

static CUresult valloc_drop(struct valloc *v)
{
    if (v->dropped) return CUDA_SUCCESS;
    int dev = push_dev_ctx(v->dev);
    if (dev < 0) return CUDA_ERROR_INVALID_CONTEXT;
    CUresult r = valloc_save(v);
    pop_ctx(dev);
    return r;
}

/* valloc_drop, with v's device current. */
static CUresult valloc_save(struct valloc *v)
{
    CUresult r = CUDA_SUCCESS;
    if (store[0]) {
        if ((r = stage_get()) != CUDA_SUCCESS) return r;
        free(v->chunks);
        if (!(v->chunks = calloc((v->size + STAGE_SIZE - 1) / STAGE_SIZE, sizeof(*v->chunks))))
            return CUDA_ERROR_OUT_OF_MEMORY;
        for (size_t o = 0; r == CUDA_SUCCESS && o < v->size; o += STAGE_SIZE) {
            size_t len = v->size - o < STAGE_SIZE ? v->size - o : STAGE_SIZE;
            if ((r = CU(cuMemcpyDtoH_v2, stage, v->va + o, len)) != CUDA_SUCCESS) break;
            if (all_zero(stage, len)) { st_zero += len; continue; }  /* "load" memsets it */
            v->chunks[o / STAGE_SIZE] = chunk_hash(stage, len);
            r = chunk_put(&v->chunks[o / STAGE_SIZE], len);
        }
    } else {
        if (!(v->save = malloc(v->size))) return CUDA_ERROR_OUT_OF_MEMORY;
        r = CU(cuMemcpyDtoH_v2, v->save, v->va, v->size);
    }
    if (r == CUDA_SUCCESS) r = REAL(r_unmap, "cuMemUnmap")(v->va, v->size);
    if (r == CUDA_SUCCESS) r = REAL(r_release, "cuMemRelease")(v->h);
    if (r != CUDA_SUCCESS) {
        free(v->save); v->save = NULL;
        free(v->chunks); v->chunks = NULL;
        return r;
    }
    v->dropped = 1;
    return CUDA_SUCCESS;
}

static CUresult valloc_load(struct valloc *v);

static CUresult valloc_restore(struct valloc *v)
{
    if (!v->dropped) return CUDA_SUCCESS;
    int dev = push_dev_ctx(v->dev);
    if (dev < 0) return CUDA_ERROR_INVALID_CONTEXT;
    CUresult r = valloc_load(v);
    pop_ctx(dev);
    return r;
}

/* valloc_restore, with v's device current. */
static CUresult valloc_load(struct valloc *v)
{
    if (v->chunks && stage_get() != CUDA_SUCCESS) return CUDA_ERROR_OUT_OF_MEMORY;
    CUmemAllocationProp p = valloc_prop(v->dev);
    CUmemGenericAllocationHandle h;
    CUresult r = CU(cuMemCreate, &h, v->size, &p, 0);
    if (r != CUDA_SUCCESS) return r;
    CUmemAccessDesc a[MAX_DEV + 1];
    int na = valloc_access(v->dev, a);
    if ((r = REAL(r_map, "cuMemMap")(v->va, v->size, 0, h, 0)) == CUDA_SUCCESS &&
        (r = REAL(r_set_access, "cuMemSetAccess")(v->va, v->size, a, na)) == CUDA_SUCCESS) {
        if (!v->chunks) r = CU(cuMemcpyHtoD_v2, v->va, v->save, v->size);
        for (size_t o = 0; v->chunks && r == CUDA_SUCCESS && o < v->size; o += STAGE_SIZE) {
            size_t len = v->size - o < STAGE_SIZE ? v->size - o : STAGE_SIZE;
            const struct chunk *c = &v->chunks[o / STAGE_SIZE];
            if (!c->h[0] && !c->h[1]) { r = CU(cuMemsetD8_v2, v->va + o, 0, len); continue; }
            if (chunk_get(c, len) != 0) { r = CUDA_ERROR_FILE_NOT_FOUND; break; }
            r = CU(cuMemcpyHtoD_v2, v->va + o, stage, len);
        }
    }
    if (r != CUDA_SUCCESS) {
        REAL(r_unmap, "cuMemUnmap")(v->va, v->size);
        REAL(r_release, "cuMemRelease")(h);
        return r;
    }
    for (int i = 0; i < n_xregs; i++) if (xregs[i].va == v->va && xregs[i].h) xregs[i].h = h;
    v->h = h;
    free(v->save);
    v->save = NULL;
    free(v->chunks);
    v->chunks = NULL;
    v->dropped = 0;
    return CUDA_SUCCESS;
}

static CUresult w_free(CUdeviceptr va)
{
    pthread_mutex_lock(&mu);
    int i = 0;
    while (i < n_vallocs && vallocs[i].va != va) i++;
    if (i == n_vallocs) {
        pthread_mutex_unlock(&mu);
        return REAL(r_free, "cuMemFree_v2")(va);
    }
    struct valloc v = vallocs[i];
    DEL(vallocs, i);
    xreg_kill(v.va, v.size);
    pthread_mutex_unlock(&mu);
    CU(cuCtxSynchronize);  /* cuMemFree waits for work using it; cuMemUnmap does not */
    REAL(r_unmap, "cuMemUnmap")(v.va, v.size);
    REAL(r_release, "cuMemRelease")(v.h);
    return CU(cuMemAddressFree, v.va, v.size);
}

static CUresult w_ipc_get(CUipcMemHandle *out, CUdeviceptr p)
{
    struct wc_ipc w = { .magic = IPC_MAGIC, .pid = getpid() };
    pthread_mutex_lock(&mu);
    for (int i = 0; i < n_vallocs && !w.size; i++)
        if (p >= vallocs[i].va && p < vallocs[i].va + vallocs[i].size) {
            w.base = vallocs[i].va;
            w.size = vallocs[i].size;
            w.key = xreg_id(vallocs[i].va, vallocs[i].h);  /* same id for the same allocation */
        }
    pthread_mutex_unlock(&mu);
    if (!w.size) return REAL(r_ipc_get, "cuIpcGetMemHandle")(out, p);
    start_ctl();
    memset(out, 0, sizeof(*out));
    memcpy(out, &w, sizeof(w));
    return CUDA_SUCCESS;
}

static CUresult w_ipc_open(CUdeviceptr *out, CUipcMemHandle hd, unsigned flags)
{
    struct wc_ipc w;
    memcpy(&w, &hd, sizeof(w));
    if (w.magic != IPC_MAGIC) {
        CUresult r = REAL(r_ipc_open, "cuIpcOpenMemHandle_v2")(out, hd, flags);
        if (r == CUDA_SUCCESS) __atomic_add_fetch(&n_legacy, 1, __ATOMIC_SEQ_CST);
        return r;
    }
    start_ctl();
    char req[64], rep[128];
    int fd = -1;
    snprintf(req, sizeof(req), "export %llu", (unsigned long long)w.key);
    if (request(w.pid, req, -1, rep, sizeof(rep), &fd) < 0 || fd < 0) {
        logf_("IPC open: export from pid %d failed: %s", w.pid, fd < 0 ? rep : strerror(errno));
        return CUDA_ERROR_INVALID_HANDLE;
    }
    CUmemGenericAllocationHandle h;
    CUresult r = REAL(r_import, "cuMemImportFromShareableHandle")(&h, (void *)(uintptr_t)fd,
                                                                CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR);
    close(fd);
    if (r != CUDA_SUCCESS) return r;
    CUdevice dev;
    CUdeviceptr va = 0;
    CUmemAccessDesc a[MAX_DEV + 1];
    int na;
    if ((r = CU(cuCtxGetDevice, &dev)) != CUDA_SUCCESS) goto release;
    pthread_mutex_lock(&mu);
    na = valloc_access(dev, a);  /* opened in this context: its peers have access too */
    pthread_mutex_unlock(&mu);
    if (na > MAX_ACC) na = MAX_ACC;
    if ((r = CU(cuMemAddressReserve, &va, w.size, 0, 0, 0)) != CUDA_SUCCESS) goto release;
    if ((r = REAL(r_map, "cuMemMap")(va, w.size, 0, h, 0)) != CUDA_SUCCESS) goto free_va;
    if ((r = REAL(r_set_access, "cuMemSetAccess")(va, w.size, a, na)) != CUDA_SUCCESS) {
        REAL(r_unmap, "cuMemUnmap")(va, w.size);
        goto free_va;
    }
    pthread_mutex_lock(&mu);
    /* The mapping holds the memory: track it as released by the app. */
    PUSH(imps, ((struct imp){ .app_h = h, .cur_h = h, .peer = w.pid, .key = w.key, .app_released = 1 }));
    struct map m = { .va = va, .size = w.size, .imp = n_imps - 1, .ipc = 1, .nacc = na };
    memcpy(m.acc, a, na * sizeof(*a));
    PUSH(maps, m);
    pthread_mutex_unlock(&mu);
    REAL(r_release, "cuMemRelease")(h);
    *out = va;
    return CUDA_SUCCESS;
free_va:
    CU(cuMemAddressFree, va, w.size);
release:
    REAL(r_release, "cuMemRelease")(h);
    return r;
}

static CUresult w_ipc_close(CUdeviceptr va)
{
    size_t size = 0;
    int gone = 0;
    pthread_mutex_lock(&mu);
    for (int i = n_maps - 1; i >= 0; i--)
        if (maps[i].ipc && maps[i].va == va) {
            size = maps[i].size;
            if ((gone = maps[i].gone)) DEL(maps, i);
        }
    pthread_mutex_unlock(&mu);
    if (gone) return CU(cuMemAddressFree, va, size);
    if (!size) {
        CUresult r = REAL(r_ipc_close, "cuIpcCloseMemHandle")(va);
        if (r == CUDA_SUCCESS) __atomic_sub_fetch(&n_legacy, 1, __ATOMIC_SEQ_CST);
        return r;
    }
    CU(cuCtxSynchronize);
    CUresult r = w_unmap(va, size);
    return r == CUDA_SUCCESS ? CU(cuMemAddressFree, va, size) : r;
}

/* Fabric handles (IMEX). A restored process may not use them, and the
 * driver (610) will not export memory created fabric-capable once the
 * process is restored, so its peers cannot re-import it. With an IMEX
 * channel, NCCL, PyTorch and FlashInfer create such memory even on one
 * node, where POSIX fds would do. So unless NVSNAP_GPUSHARE_FABRIC=1, the
 * shim hides fabric support: the device attribute reads 0 and fabric
 * creates fail as without an IMEX channel (NOT_PERMITTED), and those
 * libraries fall back to POSIX fds. Multi-node NVLink needs fabric
 * handles: set NVSNAP_GPUSHARE_FABRIC=1 there (not checkpointable). */
static int fabric_hidden(void)
{
    static int hidden = -1;
    if (hidden < 0) {
        const char *e = getenv("NVSNAP_GPUSHARE_FABRIC");
        hidden = !(e && !strcmp(e, "1"));
        if (hidden) logf_("hiding fabric handle support (NVSNAP_GPUSHARE_FABRIC=1 keeps it)");
    }
    return hidden;
}

static __typeof__(&cuDeviceGetAttribute) r_dev_attr;

static CUresult w_dev_attr(int *v, CUdevice_attribute a, CUdevice dev)
{
    CUresult r = REAL(r_dev_attr, "cuDeviceGetAttribute")(v, a, dev);
    if (r == CUDA_SUCCESS && a == CU_DEVICE_ATTRIBUTE_HANDLE_TYPE_FABRIC_SUPPORTED && fabric_hidden()) *v = 0;
    return r;
}

/* With NVSNAP_GPUSHARE_FABRIC=1, memory shared as fabric handles can also be
 * exported as a POSIX fd: a restored process may not use fabric handles,
 * and its peers re-import it as an fd instead (see refetch). */

static CUresult w_mem_create(CUmemGenericAllocationHandle *h, size_t size, const CUmemAllocationProp *p,
                             unsigned long long flags)
{
    if ((p->requestedHandleTypes & CU_MEM_HANDLE_TYPE_FABRIC) && fabric_hidden()) return CUDA_ERROR_NOT_PERMITTED;
    CUmemAllocationProp q = *p;
    if (q.requestedHandleTypes & CU_MEM_HANDLE_TYPE_FABRIC)
        q.requestedHandleTypes |= CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR;
    CUresult r = REAL(r_mem_create, "cuMemCreate")(h, size, &q, flags);
    if (r != CUDA_SUCCESS && q.requestedHandleTypes != p->requestedHandleTypes) {
        logf_("cuMemCreate(%zu, loc %d/%d, handles %#x, rdma %d, comp %d) with fds too: %s", size,
              p->location.type, p->location.id, p->requestedHandleTypes, p->allocFlags.gpuDirectRDMACapable,
              p->allocFlags.compressionType, errstr(r));
        r = REAL(r_mem_create, "cuMemCreate")(h, size, p, flags);
    }
    return r;
}

/* ── Multicast (NVLS) ────────────────────────────────────────────────── */

static CUresult w_mc_create(CUmemGenericAllocationHandle *h, const CUmulticastObjectProp *p)
{
    if ((p->handleTypes & CU_MEM_HANDLE_TYPE_FABRIC) && fabric_hidden()) return CUDA_ERROR_NOT_PERMITTED;
    CUmulticastObjectProp q = *p;
    if (q.handleTypes & CU_MEM_HANDLE_TYPE_FABRIC) q.handleTypes |= CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR;
    CUresult r = REAL(r_mc_create, "cuMulticastCreate")(h, &q);
    if (r != CUDA_SUCCESS && q.handleTypes != p->handleTypes) r = REAL(r_mc_create, "cuMulticastCreate")(h, (q = *p, &q));
    if (r != CUDA_SUCCESS) return r;
    pthread_mutex_lock(&mu);
    PUSH(mcs, ((struct mcobj){ .app_h = *h, .cur_h = *h, .prop = q, .creator = 1, .key = next_id++ }));
    pthread_mutex_unlock(&mu);
    return r;
}

/* A fabric handle imported from another node is not known to be a
 * multicast object until the app joins it: track it as one from then on
 * (mu held). */
static int imp_to_mc(CUmemGenericAllocationHandle h)
{
    int i = find_imp(h);
    if (i < 0 || !imps[i].fab || imps[i].peer) return -1;
    for (int j = 0; j < n_maps; j++)
        if (maps[j].imp == i) return -1;
    PUSH(mcs, ((struct mcobj){ .app_h = imps[i].app_h, .cur_h = imps[i].cur_h, .fab = 1, .fh = imps[i].fh }));
    imps[i].app_h = imps[i].cur_h = 0;  /* no longer an import */
    imps[i].app_released = 1;
    return n_mcs - 1;
}

static CUresult w_mc_add(CUmemGenericAllocationHandle h, CUdevice dev)
{
    pthread_mutex_lock(&mu);
    int m = find_mc(h);
    if (m < 0) m = imp_to_mc(h);
    if (m >= 0) h = mcs[m].cur_h;
    CUresult r = REAL(r_mc_add, "cuMulticastAddDevice")(h, dev);
    if (r == CUDA_SUCCESS && m >= 0 && mcs[m].ndev < MAX_DEV) mcs[m].devs[mcs[m].ndev++] = dev;
    pthread_mutex_unlock(&mu);
    return r;
}

/* Binding blocks until every device has joined: not under mu, which the
 * control thread needs to serve the peers' exports. */
static CUresult mc_bind(CUmemGenericAllocationHandle mch, size_t mcoff, CUmemGenericAllocationHandle memh,
                        size_t memoff, CUdeviceptr addr, size_t size, unsigned long long flags)
{
    pthread_mutex_lock(&mu);
    int m = find_mc(mch);
    if (m < 0) m = imp_to_mc(mch);
    if (m >= 0) mch = mcs[m].cur_h;
    if (memh) memh = own_h(memh);
    pthread_mutex_unlock(&mu);
    CUresult r = memh ? REAL(r_mc_bind_mem, "cuMulticastBindMem")(mch, mcoff, memh, memoff, size, flags)
                      : REAL(r_mc_bind_addr, "cuMulticastBindAddr")(mch, mcoff, addr, size, flags);
    if (r != CUDA_SUCCESS || m < 0) return r;
    int dev = -1;
    if (memh) {
        CUmemAllocationProp p;
        if (CU(cuMemGetAllocationPropertiesFromHandle, &p, memh) == CUDA_SUCCESS) dev = p.location.id;
    } else {
        CU(cuPointerGetAttribute, &dev, CU_POINTER_ATTRIBUTE_DEVICE_ORDINAL, addr);
    }
    pthread_mutex_lock(&mu);
    PUSH(mcbinds, ((struct mcbind){ .mc = m, .dev = dev, .mcoff = mcoff, .size = size, .flags = flags,
                                    .memh = memh, .memoff = memoff, .addr = addr }));
    pthread_mutex_unlock(&mu);
    return r;
}

static CUresult w_mc_bind_mem(CUmemGenericAllocationHandle mch, size_t mcoff, CUmemGenericAllocationHandle memh,
                              size_t memoff, size_t size, unsigned long long flags)
{
    return mc_bind(mch, mcoff, memh, memoff, 0, size, flags);
}

static CUresult w_mc_bind_addr(CUmemGenericAllocationHandle mch, size_t mcoff, CUdeviceptr addr, size_t size,
                               unsigned long long flags)
{
    return mc_bind(mch, mcoff, 0, 0, addr, size, flags);
}

static CUresult w_mc_unbind(CUmemGenericAllocationHandle mch, CUdevice dev, size_t mcoff, size_t size)
{
    pthread_mutex_lock(&mu);
    int m = find_mc(mch);
    if (m >= 0) mch = mcs[m].cur_h;
    CUresult r = REAL(r_mc_unbind, "cuMulticastUnbind")(mch, dev, mcoff, size);
    for (int i = n_mcbinds - 1; r == CUDA_SUCCESS && i >= 0; i--)
        if (mcbinds[i].mc == m && mcbinds[i].dev == dev && mcbinds[i].mcoff >= mcoff && mcbinds[i].mcoff < mcoff + size)
            DEL(mcbinds, i);
    pthread_mutex_unlock(&mu);
    return r;
}

/* ── Host memory ─────────────────────────────────────────────────────── */

/* On Grace (driver 580) the driver restores no page-locked host memory
 * (cuMemHostAlloc, cuMemHostRegister: NOT_SUPPORTED). Its contents are
 * ordinary process memory, so "release" unregisters every buffer and
 * "remap" registers it again; the device pointer stays the same. Memory
 * from cuMemHostAlloc is the driver's and cannot be re-registered, so it
 * is allocated here instead (anonymous memory, then registered). */
static __typeof__(&cuMemHostAlloc) r_host_alloc;

static CUresult w_host_alloc(void **pp, size_t size, unsigned flags)
{
    size_t pg = sysconf(_SC_PAGESIZE), len = (size + pg - 1) / pg * pg;
    void *p = mmap(NULL, len ? len : pg, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
    if (p == MAP_FAILED) return CUDA_ERROR_OUT_OF_MEMORY;
    /* WRITECOMBINED has no register equivalent; it only affects speed. */
    unsigned rf = (flags & CU_MEMHOSTALLOC_PORTABLE ? CU_MEMHOSTREGISTER_PORTABLE : 0) |
                  (flags & CU_MEMHOSTALLOC_DEVICEMAP ? CU_MEMHOSTREGISTER_DEVICEMAP : 0);
    pthread_mutex_lock(&mu);
    CUresult r = REAL(r_host_reg, "cuMemHostRegister_v2")(p, len ? len : pg, rf);
    if (r == CUDA_SUCCESS) PUSH(hregs, ((struct hreg){ p, len ? len : pg, rf, 1, 0, 0 }));
    pthread_mutex_unlock(&mu);
    if (r != CUDA_SUCCESS) { munmap(p, len ? len : pg); return r; }
    start_ctl();  /* "release" must reach every process with host memory */
    *pp = p;
    return CUDA_SUCCESS;
}

static CUresult w_alloc_host(void **pp, size_t size) { return w_host_alloc(pp, size, 0); }

static CUresult w_free_host(void *p)
{
    pthread_mutex_lock(&mu);
    int i = 0;
    while (i < n_hregs && !(hregs[i].owned && hregs[i].p == p)) i++;
    if (i == n_hregs) {
        pthread_mutex_unlock(&mu);
        return REAL(r_free_host, "cuMemFreeHost")(p);
    }
    struct hreg h = hregs[i];
    DEL(hregs, i);
    CUresult r = REAL(r_host_unreg, "cuMemHostUnregister")(p);
    pthread_mutex_unlock(&mu);
    munmap(h.p, h.size);
    return r;
}

static CUresult w_host_reg(void *p, size_t size, unsigned flags)
{
    pthread_mutex_lock(&mu);
    CUresult r = REAL(r_host_reg, "cuMemHostRegister_v2")(p, size, flags);
    if (r == CUDA_SUCCESS) PUSH(hregs, ((struct hreg){ p, size, flags, 0, 0, 0 }));
    pthread_mutex_unlock(&mu);
    if (r == CUDA_SUCCESS) start_ctl();
    return r;
}

static CUresult w_host_unreg(void *p)
{
    pthread_mutex_lock(&mu);
    CUresult r = REAL(r_host_unreg, "cuMemHostUnregister")(p);
    if (r == CUDA_SUCCESS)
        for (int i = 0; i < n_hregs; i++)
            if (!hregs[i].owned && hregs[i].p == p) { DEL(hregs, i); break; }
    pthread_mutex_unlock(&mu);
    return r;
}

/* cuCtxEnablePeerAccess gives the current context access to the cuMemAlloc
 * memory of peer's, present and future; ours is cuMem memory, which needs
 * cuMemSetAccess per allocation: grant (or revoke) it here, and on every
 * allocation and re-creation after (valloc_access). Memory opened through
 * CUDA IPC belongs to the opening context, as with the driver's own. */
static __typeof__(&cuCtxEnablePeerAccess) r_peer_on;
static __typeof__(&cuCtxDisablePeerAccess) r_peer_off;

static CUresult peer_access(CUcontext peer, int on)
{
    CUdevice me, owner;
    CUcontext c;
    CUresult r = CU(cuCtxGetDevice, &me);
    if (r != CUDA_SUCCESS || (r = CU(cuCtxPushCurrent, peer)) != CUDA_SUCCESS) return r;
    r = CU(cuCtxGetDevice, &owner);
    CU(cuCtxPopCurrent, &c);
    if (r != CUDA_SUCCESS) return r;
    if (me >= MAX_DEV || owner >= MAX_DEV) {
        logf_("peer access between devices %d and %d: more than %d devices", me, owner, MAX_DEV);
        return CUDA_ERROR_NOT_SUPPORTED;
    }
    CUmemAccessDesc a = rw_on(me);
    if (!on) a.flags = CU_MEM_ACCESS_FLAGS_PROT_NONE;
    pthread_mutex_lock(&mu);
    peer_acc[owner][me] = on;
    for (int i = 0; r == CUDA_SUCCESS && i < n_vallocs; i++)
        if (vallocs[i].dev == owner && !vallocs[i].dropped)
            r = REAL(r_set_access, "cuMemSetAccess")(vallocs[i].va, vallocs[i].size, &a, 1);
    for (int i = 0; r == CUDA_SUCCESS && i < n_maps; i++) {
        struct map *m = &maps[i];
        if (!m->ipc || m->gone || m->acc[0].location.id != owner) continue;
        acc_merge(m, &a, 1);
        if (!imps[m->imp].released) r = REAL(r_set_access, "cuMemSetAccess")(m->va, m->size, &a, 1);
    }
    pthread_mutex_unlock(&mu);
    if (r != CUDA_SUCCESS) logf_("peer access %d -> %d: cuMemSetAccess: %s", me, owner, errstr(r));
    return r;
}

static CUresult w_peer_on(CUcontext peer, unsigned flags)
{
    CUresult r = REAL(r_peer_on, "cuCtxEnablePeerAccess")(peer, flags);
    return r == CUDA_SUCCESS ? peer_access(peer, 1) : r;
}

static CUresult w_peer_off(CUcontext peer)
{
    CUresult r = REAL(r_peer_off, "cuCtxDisablePeerAccess")(peer);
    return r == CUDA_SUCCESS ? peer_access(peer, 0) : r;
}

/* Gated launches. Each has a default-stream and a per-thread-stream
 * (_ptsz) entry point, which cuGetProcAddress returns under one name. */
#define GATED_G(g, name, params, args) \
    static CUresult (*r_##name) params, (*r_##name##_ptsz) params; \
    static CUresult w_##name params \
    { gate_enter(&g); CUresult r_ = REAL(r_##name, #name) args; gate_exit(&g); return r_; } \
    static CUresult w_##name##_ptsz params \
    { gate_enter(&g); CUresult r_ = REAL(r_##name##_ptsz, #name "_ptsz") args; gate_exit(&g); return r_; }
#define GATED(name, params, args) GATED_G(lgate, name, params, args)
#define GATED_M(name, params, args) GATED_G(mgate, name, params, args)

GATED(cuLaunchKernel, (CUfunction f, unsigned gx, unsigned gy, unsigned gz, unsigned bx, unsigned by,
      unsigned bz, unsigned sh, CUstream st, void **p, void **x), (f, gx, gy, gz, bx, by, bz, sh, st, p, x))
GATED(cuLaunchKernelEx, (const CUlaunchConfig *c, CUfunction f, void **p, void **x), (c, f, p, x))
GATED(cuLaunchCooperativeKernel, (CUfunction f, unsigned gx, unsigned gy, unsigned gz, unsigned bx,
      unsigned by, unsigned bz, unsigned sh, CUstream st, void **p), (f, gx, gy, gz, bx, by, bz, sh, st, p))
GATED(cuGraphLaunch, (CUgraphExec g, CUstream st), (g, st))

/* Copies, memsets and stream memory operations wait in the memory gate: an
 * app thread past the drain must not touch memory "release" is freeing. The
 * synchronous ones have a per-thread-default-stream (_ptds) entry point. */
#define GATED_DS(name, params, args) \
    static CUresult (*r_##name) params, (*r_##name##_ptds) params; \
    static CUresult w_##name params \
    { gate_enter(&mgate); CUresult r_ = REAL(r_##name, #name) args; gate_exit(&mgate); return r_; } \
    static CUresult w_##name##_ptds params \
    { gate_enter(&mgate); CUresult r_ = REAL(r_##name##_ptds, #name "_ptds") args; gate_exit(&mgate); return r_; }

GATED_DS(cuMemcpy, (CUdeviceptr d, CUdeviceptr s, size_t n), (d, s, n))
GATED_DS(cuMemcpyPeer, (CUdeviceptr d, CUcontext dc, CUdeviceptr s, CUcontext sc, size_t n), (d, dc, s, sc, n))
GATED_DS(cuMemcpyHtoD_v2, (CUdeviceptr d, const void *s, size_t n), (d, s, n))
GATED_DS(cuMemcpyDtoH_v2, (void *d, CUdeviceptr s, size_t n), (d, s, n))
GATED_DS(cuMemcpyDtoD_v2, (CUdeviceptr d, CUdeviceptr s, size_t n), (d, s, n))
GATED_DS(cuMemcpy2D_v2, (const CUDA_MEMCPY2D *c), (c))
GATED_DS(cuMemcpy2DUnaligned_v2, (const CUDA_MEMCPY2D *c), (c))
GATED_DS(cuMemcpy3D_v2, (const CUDA_MEMCPY3D *c), (c))
GATED_DS(cuMemcpy3DPeer, (const CUDA_MEMCPY3D_PEER *c), (c))
GATED_DS(cuMemsetD8_v2, (CUdeviceptr d, unsigned char v, size_t n), (d, v, n))
GATED_DS(cuMemsetD16_v2, (CUdeviceptr d, unsigned short v, size_t n), (d, v, n))
GATED_DS(cuMemsetD32_v2, (CUdeviceptr d, unsigned int v, size_t n), (d, v, n))
GATED_DS(cuMemsetD2D8_v2, (CUdeviceptr d, size_t p, unsigned char v, size_t w, size_t h), (d, p, v, w, h))
GATED_DS(cuMemsetD2D16_v2, (CUdeviceptr d, size_t p, unsigned short v, size_t w, size_t h), (d, p, v, w, h))
GATED_DS(cuMemsetD2D32_v2, (CUdeviceptr d, size_t p, unsigned int v, size_t w, size_t h), (d, p, v, w, h))
GATED_M(cuMemcpyAsync, (CUdeviceptr d, CUdeviceptr s, size_t n, CUstream st), (d, s, n, st))
GATED_M(cuMemcpyPeerAsync, (CUdeviceptr d, CUcontext dc, CUdeviceptr s, CUcontext sc, size_t n, CUstream st),
      (d, dc, s, sc, n, st))
GATED_M(cuMemcpyHtoDAsync_v2, (CUdeviceptr d, const void *s, size_t n, CUstream st), (d, s, n, st))
GATED_M(cuMemcpyDtoHAsync_v2, (void *d, CUdeviceptr s, size_t n, CUstream st), (d, s, n, st))
GATED_M(cuMemcpyDtoDAsync_v2, (CUdeviceptr d, CUdeviceptr s, size_t n, CUstream st), (d, s, n, st))
GATED_M(cuMemcpy2DAsync_v2, (const CUDA_MEMCPY2D *c, CUstream st), (c, st))
GATED_M(cuMemcpy3DAsync_v2, (const CUDA_MEMCPY3D *c, CUstream st), (c, st))
GATED_M(cuMemcpy3DPeerAsync, (const CUDA_MEMCPY3D_PEER *c, CUstream st), (c, st))
GATED_M(cuMemsetD8Async, (CUdeviceptr d, unsigned char v, size_t n, CUstream st), (d, v, n, st))
GATED_M(cuMemsetD16Async, (CUdeviceptr d, unsigned short v, size_t n, CUstream st), (d, v, n, st))
GATED_M(cuMemsetD32Async, (CUdeviceptr d, unsigned int v, size_t n, CUstream st), (d, v, n, st))
GATED_M(cuMemsetD2D8Async, (CUdeviceptr d, size_t p, unsigned char v, size_t w, size_t h, CUstream st),
      (d, p, v, w, h, st))
GATED_M(cuMemsetD2D16Async, (CUdeviceptr d, size_t p, unsigned short v, size_t w, size_t h, CUstream st),
      (d, p, v, w, h, st))
GATED_M(cuMemsetD2D32Async, (CUdeviceptr d, size_t p, unsigned int v, size_t w, size_t h, CUstream st),
      (d, p, v, w, h, st))
GATED_M(cuStreamWriteValue32_v2, (CUstream st, CUdeviceptr a, cuuint32_t v, unsigned f), (st, a, v, f))
GATED_M(cuStreamWriteValue64_v2, (CUstream st, CUdeviceptr a, cuuint64_t v, unsigned f), (st, a, v, f))
GATED_M(cuStreamWaitValue32_v2, (CUstream st, CUdeviceptr a, cuuint32_t v, unsigned f), (st, a, v, f))
GATED_M(cuStreamWaitValue64_v2, (CUstream st, CUdeviceptr a, cuuint64_t v, unsigned f), (st, a, v, f))
GATED_M(cuStreamBatchMemOp_v2, (CUstream st, unsigned n, CUstreamBatchMemOpParams *p, unsigned f), (st, n, p, f))
GATED_M(cuLaunchHostFunc, (CUstream st, CUhostFn fn, void *u), (st, fn, u))

static void *wrapper_for(const char *name, void *real);

static CUresult w_gpa2(const char *sym, void **pfn, int ver, cuuint64_t flags,
                       CUdriverProcAddressQueryResult *st)
{
    CUresult r = REAL(r_gpa2, "cuGetProcAddress_v2")(sym, pfn, ver, flags, st);
    if (r == CUDA_SUCCESS && *pfn) *pfn = wrapper_for(sym, *pfn);
    return r;
}

static CUresult w_gpa1(const char *sym, void **pfn, int ver, cuuint64_t flags)
{
    CUresult r = REAL(r_gpa1, "cuGetProcAddress")(sym, pfn, ver, flags);
    if (r == CUDA_SUCCESS && *pfn) *pfn = wrapper_for(sym, *pfn);
    return r;
}

static const struct { const char *name; void *wrapper; void **real; } hooks[] = {
    { "cuMemMap", w_map, (void **)&r_map },
    { "cuMemUnmap", w_unmap, (void **)&r_unmap },
    { "cuMemRelease", w_release, (void **)&r_release },
    { "cuMemSetAccess", w_set_access, (void **)&r_set_access },
    { "cuMemImportFromShareableHandle", w_import, (void **)&r_import },
    { "cuMemExportToShareableHandle", w_export, (void **)&r_export },
    { "cuGetProcAddress_v2", w_gpa2, (void **)&r_gpa2 },
    { "cuGetProcAddress", w_gpa1, (void **)&r_gpa1 },
    { "cuMemRetainAllocationHandle", w_retain, (void **)&r_retain },
    { "cuMemAlloc_v2", w_alloc, (void **)&r_alloc },
    { "cuMemFree_v2", w_free, (void **)&r_free },
    { "cuIpcGetMemHandle", w_ipc_get, (void **)&r_ipc_get },
    { "cuIpcOpenMemHandle_v2", w_ipc_open, (void **)&r_ipc_open },
    { "cuIpcCloseMemHandle", w_ipc_close, (void **)&r_ipc_close },
    { "cuMemHostAlloc", w_host_alloc, (void **)&r_host_alloc },
    { "cuMemAllocHost_v2", w_alloc_host, (void **)&r_alloc_host },
    { "cuMemFreeHost", w_free_host, (void **)&r_free_host },
    { "cuMemHostRegister_v2", w_host_reg, (void **)&r_host_reg },
    { "cuMemHostUnregister", w_host_unreg, (void **)&r_host_unreg },
    { "cuMemCreate", w_mem_create, (void **)&r_mem_create },
    { "cuDeviceGetAttribute", w_dev_attr, (void **)&r_dev_attr },
    { "cuMulticastCreate", w_mc_create, (void **)&r_mc_create },
    { "cuMulticastAddDevice", w_mc_add, (void **)&r_mc_add },
    { "cuMulticastBindMem", w_mc_bind_mem, (void **)&r_mc_bind_mem },
    { "cuMulticastBindAddr", w_mc_bind_addr, (void **)&r_mc_bind_addr },
    { "cuMulticastUnbind", w_mc_unbind, (void **)&r_mc_unbind },
    { "cuCtxEnablePeerAccess", w_peer_on, (void **)&r_peer_on },
    { "cuCtxDisablePeerAccess", w_peer_off, (void **)&r_peer_off },
#define G(name) { #name, w_##name, (void **)&r_##name }, { #name "_ptsz", w_##name##_ptsz, (void **)&r_##name##_ptsz }
#define GD(name) { #name, w_##name, (void **)&r_##name }, { #name "_ptds", w_##name##_ptds, (void **)&r_##name##_ptds }
    G(cuLaunchKernel), G(cuLaunchKernelEx), G(cuLaunchCooperativeKernel), G(cuGraphLaunch),
    GD(cuMemcpy), GD(cuMemcpyPeer), GD(cuMemcpyHtoD_v2), GD(cuMemcpyDtoH_v2), GD(cuMemcpyDtoD_v2),
    GD(cuMemcpy2D_v2), GD(cuMemcpy2DUnaligned_v2), GD(cuMemcpy3D_v2), GD(cuMemcpy3DPeer),
    GD(cuMemsetD8_v2), GD(cuMemsetD16_v2), GD(cuMemsetD32_v2),
    GD(cuMemsetD2D8_v2), GD(cuMemsetD2D16_v2), GD(cuMemsetD2D32_v2),
    G(cuMemcpyAsync), G(cuMemcpyPeerAsync), G(cuMemcpyHtoDAsync_v2), G(cuMemcpyDtoHAsync_v2),
    G(cuMemcpyDtoDAsync_v2), G(cuMemcpy2DAsync_v2), G(cuMemcpy3DAsync_v2), G(cuMemcpy3DPeerAsync),
    G(cuMemsetD8Async), G(cuMemsetD16Async), G(cuMemsetD32Async),
    G(cuMemsetD2D8Async), G(cuMemsetD2D16Async), G(cuMemsetD2D32Async),
    G(cuStreamWriteValue32_v2), G(cuStreamWriteValue64_v2), G(cuStreamWaitValue32_v2),
    G(cuStreamWaitValue64_v2), G(cuStreamBatchMemOp_v2), G(cuLaunchHostFunc),
#undef GD
#undef G
};

static void *bind_hook(size_t i, void *real)
{
    if (real == hooks[i].wrapper) return real;
    if (!*hooks[i].real) *hooks[i].real = real;
    return hooks[i].wrapper;
}

/* cuGetProcAddress takes unversioned names and returns the current
 * version ("cuMemAlloc" → cuMemAlloc_v2), or the _ptsz/_ptds variant for
 * per-thread default streams: match the hook by the function returned. */
static void *wrapper_for(const char *name, void *real)
{
    size_t len = strlen(name);
    int exact = -1;
    for (size_t i = 0; i < sizeof(hooks) / sizeof(hooks[0]); i++) {
        if (strncmp(hooks[i].name, name, len)) continue;
        const char *suf = hooks[i].name + len;
        if (!*suf) exact = i;
        else if ((!strcmp(suf, "_v2") || !strcmp(suf, "_ptsz") || !strcmp(suf, "_ptds") ||
                  !strcmp(suf, "_v2_ptsz") || !strcmp(suf, "_v2_ptds")) && real == cuda_sym(hooks[i].name))
            return bind_hook(i, real);
    }
    return exact >= 0 ? bind_hook(exact, real) : real;
}

/* Exported entry points: direct callers (linked against libcuda) and
 * dlsym() lookups reach the wrappers through these. */
#define EXPORT(ret, sym, wrapper, params, args) \
    ret sym##_export params __asm__(#sym) __attribute__((visibility("default"))); \
    ret sym##_export params { return wrapper args; }

EXPORT(CUresult, cuMemMap, w_map, (CUdeviceptr va, size_t s, size_t o, CUmemGenericAllocationHandle h, unsigned long long f), (va, s, o, h, f))
EXPORT(CUresult, cuMemUnmap, w_unmap, (CUdeviceptr va, size_t s), (va, s))
EXPORT(CUresult, cuMemRelease, w_release, (CUmemGenericAllocationHandle h), (h))
EXPORT(CUresult, cuMemSetAccess, w_set_access, (CUdeviceptr va, size_t s, const CUmemAccessDesc *d, size_t n), (va, s, d, n))
EXPORT(CUresult, cuMemImportFromShareableHandle, w_import, (CUmemGenericAllocationHandle *h, void *os, CUmemAllocationHandleType t), (h, os, t))
EXPORT(CUresult, cuMemExportToShareableHandle, w_export, (void *sh, CUmemGenericAllocationHandle h, CUmemAllocationHandleType t, unsigned long long f), (sh, h, t, f))
EXPORT(CUresult, cuMemRetainAllocationHandle, w_retain, (CUmemGenericAllocationHandle *h, void *a), (h, a))
EXPORT(CUresult, cuMemAlloc_v2, w_alloc, (CUdeviceptr *p, size_t s), (p, s))
EXPORT(CUresult, cuMemFree_v2, w_free, (CUdeviceptr p), (p))
EXPORT(CUresult, cuIpcGetMemHandle, w_ipc_get, (CUipcMemHandle *h, CUdeviceptr p), (h, p))
EXPORT(CUresult, cuIpcOpenMemHandle_v2, w_ipc_open, (CUdeviceptr *p, CUipcMemHandle h, unsigned f), (p, h, f))
EXPORT(CUresult, cuIpcCloseMemHandle, w_ipc_close, (CUdeviceptr p), (p))
EXPORT(CUresult, cuMemHostAlloc, w_host_alloc, (void **p, size_t s, unsigned f), (p, s, f))
EXPORT(CUresult, cuMemAllocHost_v2, w_alloc_host, (void **p, size_t s), (p, s))
EXPORT(CUresult, cuMemFreeHost, w_free_host, (void *p), (p))
EXPORT(CUresult, cuMemHostRegister_v2, w_host_reg, (void *p, size_t s, unsigned f), (p, s, f))
EXPORT(CUresult, cuMemHostUnregister, w_host_unreg, (void *p), (p))
EXPORT(CUresult, cuMemCreate, w_mem_create, (CUmemGenericAllocationHandle *h, size_t s, const CUmemAllocationProp *p, unsigned long long f), (h, s, p, f))
EXPORT(CUresult, cuDeviceGetAttribute, w_dev_attr, (int *v, CUdevice_attribute a, CUdevice d), (v, a, d))
EXPORT(CUresult, cuMulticastCreate, w_mc_create, (CUmemGenericAllocationHandle *h, const CUmulticastObjectProp *p), (h, p))
EXPORT(CUresult, cuMulticastAddDevice, w_mc_add, (CUmemGenericAllocationHandle h, CUdevice d), (h, d))
EXPORT(CUresult, cuMulticastBindMem, w_mc_bind_mem, (CUmemGenericAllocationHandle mh, size_t mo, CUmemGenericAllocationHandle h, size_t o, size_t s, unsigned long long f), (mh, mo, h, o, s, f))
EXPORT(CUresult, cuMulticastBindAddr, w_mc_bind_addr, (CUmemGenericAllocationHandle mh, size_t mo, CUdeviceptr p, size_t s, unsigned long long f), (mh, mo, p, s, f))
EXPORT(CUresult, cuMulticastUnbind, w_mc_unbind, (CUmemGenericAllocationHandle mh, CUdevice d, size_t mo, size_t s), (mh, d, mo, s))
EXPORT(CUresult, cuCtxEnablePeerAccess, w_peer_on, (CUcontext c, unsigned f), (c, f))
EXPORT(CUresult, cuCtxDisablePeerAccess, w_peer_off, (CUcontext c), (c))
EXPORT(CUresult, cuGetProcAddress_v2, w_gpa2, (const char *s, void **p, int v, cuuint64_t f, CUdriverProcAddressQueryResult *q), (s, p, v, f, q))

void *dlsym(void *handle, const char *name)
{
    static pthread_once_t once = PTHREAD_ONCE_INIT;
    pthread_once(&once, init_real_dlsym);
    void *real = real_dlsym(handle, name);
    if (real && name[0] == 'c' && name[1] == 'u') return wrapper_for(name, real);
    return real;
}
