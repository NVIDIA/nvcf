/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/
/*
 * New cuMem handles after a suspend + resume, with
 * LD_PRELOAD=libnvsnap_gpushare.so.
 *
 * "release" frees allocations whose handles the app keeps: E's shared
 * allocation (exported, so saved and re-created) and I's import of it.
 * Driver handles are heap addresses, so the driver may give one of those
 * values to a new allocation, as vLLM's sleep mode makes many right after
 * a capture. The shim must still map and release what the app asked for.
 *
 * E creates NSHARED allocations (2 MiB, exported) and a local one; I
 * imports the shared ones. After suspend + resume, each process creates,
 * maps and fills many new allocations, checks every allocation's contents
 * and releases the new ones. The shared ones must stay intact and shared
 * (E writes, I reads). Several are shared so that "load" and "remap" do
 * not simply take back every freed value.
 *
 * Build: gcc -O2 -I/usr/local/cuda/include -o test_handle_reuse \
 *            test_handle_reuse.c -lcuda
 * Run:   LD_PRELOAD=libnvsnap_gpushare.so ./test_handle_reuse <nvsnap-gpu-suspend> [de=0] [di=0] [n=256] [cycles=2]
 */
#include <cuda.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/wait.h>
#include <unistd.h>

#define SIZE (2UL << 20)
#define MAXA 4096
#define NSHARED 8
#define CK(x) do { CUresult r_ = (x); if (r_ != CUDA_SUCCESS) { const char *s_ = "?"; \
    cuGetErrorString(r_, &s_); fprintf(stderr, "[%d] %s: %s\n", getpid(), #x, s_); exit(2); } } while (0)

struct chan { int cmd[2], rep[2]; };
static void put(int fd, const void *p, size_t n) { if (write(fd, p, n) != (ssize_t)n) exit(2); }
static void get(int fd, void *p, size_t n) { if (read(fd, p, n) != (ssize_t)n) exit(2); }

static uint64_t call(struct chan *c, char op, uint32_t arg)
{
    uint64_t r;
    put(c->cmd[1], &op, 1); put(c->cmd[1], &arg, 4); get(c->rep[0], &r, 8);
    return r;
}

static void send_fd(int sock, int fd)
{
    char c = 0, buf[CMSG_SPACE(sizeof(int))] = {0};
    struct iovec iov = { &c, 1 };
    struct msghdr m = { .msg_iov = &iov, .msg_iovlen = 1, .msg_control = buf, .msg_controllen = sizeof(buf) };
    struct cmsghdr *h = CMSG_FIRSTHDR(&m);
    h->cmsg_level = SOL_SOCKET; h->cmsg_type = SCM_RIGHTS; h->cmsg_len = CMSG_LEN(sizeof(int));
    memcpy(CMSG_DATA(h), &fd, sizeof(int));
    if (sendmsg(sock, &m, 0) != 1) exit(2);
}

static int recv_fd(int sock)
{
    char c, buf[CMSG_SPACE(sizeof(int))];
    struct iovec iov = { &c, 1 };
    struct msghdr m = { .msg_iov = &iov, .msg_iovlen = 1, .msg_control = buf, .msg_controllen = sizeof(buf) };
    int fd;
    if (recvmsg(sock, &m, 0) != 1) exit(2);
    memcpy(&fd, CMSG_DATA(CMSG_FIRSTHDR(&m)), sizeof(int));
    return fd;
}

/* The process's allocations: handle as the app holds it, VA, fill value. */
static struct { CUmemGenericAllocationHandle h; CUdeviceptr va; uint32_t v; int live; } a[MAXA];
static int na, dev;
static CUmemGenericAllocationHandle seen[MAXA];  /* values held before the suspend */
static int nseen;

static CUdeviceptr map_at(CUmemGenericAllocationHandle h)
{
    CUdeviceptr va;
    CUmemAccessDesc d = { { CU_MEM_LOCATION_TYPE_DEVICE, dev }, CU_MEM_ACCESS_FLAGS_PROT_READWRITE };
    CK(cuMemAddressReserve(&va, SIZE, 0, 0, 0));
    CK(cuMemMap(va, SIZE, 0, h, 0));
    CK(cuMemSetAccess(va, SIZE, &d, 1));
    return va;
}

static int create(uint32_t v, int fdtype)
{
    CUmemAllocationProp p = {0};
    p.type = CU_MEM_ALLOCATION_TYPE_PINNED;
    p.location.type = CU_MEM_LOCATION_TYPE_DEVICE;
    p.location.id = dev;
    p.requestedHandleTypes = fdtype ? CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR : CU_MEM_HANDLE_TYPE_NONE;
    CK(cuMemCreate(&a[na].h, SIZE, &p, 0));
    a[na].va = map_at(a[na].h);
    a[na].v = v;
    a[na].live = 1;
    CK(cuMemsetD32(a[na].va, v, SIZE / 4));
    return na++;
}

/* Allocations whose contents are not what was written to them. */
static int bad(void)
{
    static uint32_t buf[SIZE / 4];
    int nb = 0;
    CK(cuCtxSynchronize());
    for (int i = 0; i < na; i++) {
        if (!a[i].live) continue;
        CK(cuMemcpyDtoH(buf, a[i].va, SIZE));
        for (size_t k = 0; k < SIZE / 4; k++)
            if (buf[k] != a[i].v) {
                fprintf(stderr, "[%d] allocation %d (h %#llx va %#llx): %#x at word %zu, want %#x\n", getpid(), i,
                        (unsigned long long)a[i].h, (unsigned long long)a[i].va, buf[k], k, a[i].v);
                nb++;
                break;
            }
    }
    return nb;
}

/* E: 's' create S and send its fd, 'l' a local allocation, 'w' write S.
 * I: 'i' import S. Both: 'k' snapshot handle values, 'n' create, map and
 * check arg allocations then release them (reply: wrong ones, or 1000 + n
 * new values equal to an old one), 'c' check all, 'v' set S's value. */
static void child(struct chan *c, int sock, int d)
{
    CUdevice cd; CUcontext ctx;
    dev = d;
    CK(cuInit(0)); CK(cuDeviceGet(&cd, d)); CK(cuDevicePrimaryCtxRetain(&ctx, cd)); CK(cuCtxSetCurrent(ctx));
    for (;;) {
        char op; uint32_t arg; uint64_t r = 0;
        get(c->cmd[0], &op, 1); get(c->cmd[0], &arg, 4);
        switch (op) {
        case 's': {
            int i = create(arg, 1), fd;
            CK(cuMemExportToShareableHandle(&fd, a[i].h, CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR, 0));
            send_fd(sock, fd);
            close(fd);
            break;
        }
        case 'l': create(arg, 0); break;
        case 'i': {
            int fd = recv_fd(sock);
            CK(cuMemImportFromShareableHandle(&a[na].h, (void *)(uintptr_t)fd, CU_MEM_HANDLE_TYPE_POSIX_FILE_DESCRIPTOR));
            close(fd);
            a[na].va = map_at(a[na].h);
            a[na].v = arg;
            a[na].live = 1;
            na++;
            break;
        }
        case 'v': a[0].v = arg; break;
        case 'w': CK(cuMemsetD32(a[0].va, arg, SIZE / 4)); CK(cuCtxSynchronize()); a[0].v = arg; break;
        case 'k': for (int i = 0; i < na; i++) seen[nseen++] = a[i].h; break;
        case 'n': {
            int first = na, same = 0;
            for (uint32_t k = 0; k < arg && na < MAXA; k++) {
                int i = create(0x5000000u + (getpid() << 12) + k, k & 1);
                for (int j = 0; j < nseen; j++) same += seen[j] == a[i].h;
            }
            r = bad();
            for (int i = na - 1; i >= first; i--) {
                CK(cuMemUnmap(a[i].va, SIZE));
                CK(cuMemAddressFree(a[i].va, SIZE));
                CK(cuMemRelease(a[i].h));
            }
            na = first;
            r += bad();
            if (!r && same) r = 1000 + same;
            break;
        }
        case 'c': r = bad(); break;
        case 'q': put(c->rep[1], &r, 8); exit(0);
        }
        put(c->rep[1], &r, 8);
    }
}

static const char *tool;
static int run(const char *action, pid_t x, pid_t y)
{
    char cmd[512];
    snprintf(cmd, sizeof cmd, "%s %s %d %d", tool, action, x, y);
    printf("$ %s\n", cmd); fflush(stdout);
    int st = system(cmd);
    return WIFEXITED(st) ? WEXITSTATUS(st) : -1;
}

#define EXPECT(cond, msg) do { int ok_ = (cond); printf("  %s: %s\n", ok_ ? "ok  " : "FAIL", msg); if (!ok_) fails++; } while (0)

int main(int argc, char **argv)
{
    if (argc < 2) { fprintf(stderr, "usage: %s <nvsnap-gpu-suspend> [de] [di] [n] [cycles]\n", argv[0]); return 1; }
    tool = argv[1];
    int de = argc > 2 ? atoi(argv[2]) : 0, di = argc > 3 ? atoi(argv[3]) : 0;
    int n = argc > 4 ? atoi(argv[4]) : 256, cycles = argc > 5 ? atoi(argv[5]) : 2, fails = 0;
    struct chan ce, ci;
    int sv[2];
    setvbuf(stdout, NULL, _IOLBF, 0);
    if (pipe(ce.cmd) || pipe(ce.rep) || pipe(ci.cmd) || pipe(ci.rep) || socketpair(AF_UNIX, SOCK_STREAM, 0, sv)) return 1;
    pid_t pe = fork();
    if (!pe) child(&ce, sv[0], de);
    pid_t pi = fork();
    if (!pi) child(&ci, sv[1], di);
    printf("exporter pid %d (dev %d), importer pid %d (dev %d)\n", pe, de, pi, di);

    uint32_t v = 0x11111111;
    for (int i = 0; i < NSHARED; i++) {
        call(&ce, 's', v + i);
        call(&ci, 'i', v + i);
    }
    call(&ce, 'l', 0x22222222);
    call(&ci, 'l', 0x33333333);
    EXPECT(call(&ce, 'c', 0) == 0 && call(&ci, 'c', 0) == 0, "contents before suspend");
    for (int c = 1; c <= cycles && !fails; c++) {
        printf("--- cycle %d/%d\n", c, cycles);
        call(&ce, 'k', 0); call(&ci, 'k', 0);
        EXPECT(run("suspend", pe, pi) == 0, "suspend E I");
        EXPECT(run("resume", pe, pi) == 0, "resume E I");
        EXPECT(call(&ce, 'c', 0) == 0 && call(&ci, 'c', 0) == 0, "contents after resume");
        uint64_t re = call(&ce, 'n', n), ri = call(&ci, 'n', n);
        if (re >= 1000 || ri >= 1000) printf("  new handles equal to old ones: E %llu, I %llu\n",
                                              re >= 1000 ? (unsigned long long)re - 1000 : 0ULL,
                                              ri >= 1000 ? (unsigned long long)ri - 1000 : 0ULL);
        EXPECT(re == 0 && ri == 0, "new allocations map and release what the app asked for");
        v += 0x11111111;
        call(&ce, 'w', v);
        call(&ci, 'v', v);
        EXPECT(call(&ci, 'c', 0) == 0, "sharing live: exporter write visible to importer");
    }
    call(&ce, 'q', 0); call(&ci, 'q', 0);
    waitpid(pe, NULL, 0); waitpid(pi, NULL, 0);
    printf(fails ? "=== FAIL (%d)\n" : "=== PASS\n", fails);
    return fails != 0;
}
