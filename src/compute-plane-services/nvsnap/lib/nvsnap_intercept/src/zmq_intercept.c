/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

#include <dlfcn.h>
#include <errno.h>
#include <dirent.h>
#include <pthread.h>
#include <signal.h>
#include <stdarg.h>
#include <stdio.h>
#include <sys/prctl.h>
#include <sys/syscall.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "nvsnap_intercept.h"

#ifndef ZMQ_EVENTS
#define ZMQ_EVENTS 15
#endif
#ifndef ZMQ_TYPE
#define ZMQ_TYPE 16
#endif
#ifndef ZMQ_IO_THREADS
#define ZMQ_IO_THREADS 1
#endif
#ifndef ZMQ_LAST_ENDPOINT
#define ZMQ_LAST_ENDPOINT 32
#endif
#ifndef ZMQ_ROUTER
#define ZMQ_ROUTER 6
#endif
#ifndef ZMQ_DEALER
#define ZMQ_DEALER 5
#endif
#ifndef ZMQ_REQ
#define ZMQ_REQ 3
#endif
#ifndef ZMQ_IDENTITY
#define ZMQ_IDENTITY 5
#endif
#ifndef ZMQ_PROBE_ROUTER
#define ZMQ_PROBE_ROUTER 51
#endif
#ifndef ZMQ_PAIR
#define ZMQ_PAIR 0
#endif
#ifndef ZMQ_EVENT_ALL
#define ZMQ_EVENT_ALL 0xFFFF
#endif
#ifndef ZMQ_ROUTER_MANDATORY
#define ZMQ_ROUTER_MANDATORY 33
#endif
#ifndef ZMQ_IMMEDIATE
#define ZMQ_IMMEDIATE 39
#endif

typedef struct zmq_msg_t zmq_msg_t;

typedef struct zmq_pollitem_t {
    void *socket;
    int fd;
    short events;
    short revents;
} zmq_pollitem_t;

void *zmq_ctx_new(void);
int zmq_ctx_term(void *);
int zmq_ctx_destroy(void *);
int zmq_ctx_shutdown(void *);
int zmq_ctx_set(void *, int, int);
int zmq_ctx_get(void *, int);
void *zmq_socket(void *, int);
int zmq_close(void *);
int zmq_bind(void *, const char *);
int zmq_unbind(void *, const char *);
int zmq_connect(void *, const char *);
int zmq_disconnect(void *, const char *);
int zmq_setsockopt(void *, int, const void *, size_t);
int zmq_getsockopt(void *, int, void *, size_t *);
int zmq_send(void *, const void *, size_t, int);
int zmq_recv(void *, void *, size_t, int);
int zmq_msg_send(zmq_msg_t *, void *, int);
int zmq_msg_recv(zmq_msg_t *, void *, int);
int zmq_msg_init(zmq_msg_t *);
int zmq_msg_init_size(zmq_msg_t *, size_t);
int zmq_msg_init_data(zmq_msg_t *, void *, size_t, void (*)(void *, void *), void *);
int zmq_msg_close(zmq_msg_t *);
void *zmq_msg_data(zmq_msg_t *);
size_t zmq_msg_size(zmq_msg_t *);
int zmq_poll(zmq_pollitem_t *, int, long);
int zmq_proxy(void *, void *, void *);
int zmq_errno(void);

enum nvsnap_zmq_op_type {
    NVSNAP_ZMQ_OP_SETSOCKOPT = 1,
    NVSNAP_ZMQ_OP_BIND,
    NVSNAP_ZMQ_OP_CONNECT,
    NVSNAP_ZMQ_OP_UNBIND,
    NVSNAP_ZMQ_OP_DISCONNECT,
};

struct nvsnap_zmq_op {
    enum nvsnap_zmq_op_type type;
    int option;
    size_t len;
    void *data;
    char *endpoint;
    struct nvsnap_zmq_op *next;
};

struct nvsnap_zmq_socket;

typedef struct nvsnap_zmq_ctx {
    uint64_t magic;
    void *real;
    int reinit_done;
    int destroyed;
    struct nvsnap_zmq_socket *sockets;
    pthread_mutex_t lock;
    struct nvsnap_zmq_ctx *next;
} nvsnap_zmq_ctx_t;

typedef struct nvsnap_zmq_socket {
    uint64_t magic;
    nvsnap_zmq_ctx_t *ctx;
    void *real;
    int type;
    int closed;
    int replay_after_restore;
    int replay_failed;               /* Set if replay bind/connect failed */
    int rebuilding;
    int rebuild_count;
    int forced_connect_done;
    int monitor_started;
    void *monitor_sock;
    pthread_t monitor_thread;
    struct nvsnap_zmq_op *ops_head;
    struct nvsnap_zmq_op *ops_tail;
    struct nvsnap_zmq_socket *next;
} nvsnap_zmq_socket_t;

#define NVSNAP_ZMQ_CTX_MAGIC 0x5a6d715f63747831ULL
#define NVSNAP_ZMQ_SOCK_MAGIC 0x5a6d715f736f636bULL

static pthread_once_t g_zmq_once = PTHREAD_ONCE_INIT;
static int g_zmq_enabled = -1;
static atomic_int g_zmq_restore_seen = 0;
static int g_zmq_trace = -1;
static pthread_mutex_t g_zmq_ctx_list_lock = PTHREAD_MUTEX_INITIALIZER;
static nvsnap_zmq_ctx_t *g_zmq_ctx_list = NULL;
static pthread_mutex_t g_zmq_load_lock = PTHREAD_MUTEX_INITIALIZER;
static void *g_zmq_handle = NULL;
static void *g_zmq_newns_handle = NULL;
static int g_zmq_use_newns = 0;
static pthread_mutex_t g_zmq_recover_lock = PTHREAD_MUTEX_INITIALIZER;
static atomic_int g_zmq_recovering = 0;

static void *(*real_zmq_ctx_new)(void);
static int (*real_zmq_ctx_term)(void *);
static int (*real_zmq_ctx_destroy)(void *);
static int (*real_zmq_ctx_shutdown)(void *);
static int (*real_zmq_ctx_set)(void *, int, int);
static int (*real_zmq_ctx_get)(void *, int);
static void *(*real_zmq_socket)(void *, int);
static int (*real_zmq_close)(void *);
static int (*real_zmq_bind)(void *, const char *);
static int (*real_zmq_unbind)(void *, const char *);
static int (*real_zmq_connect)(void *, const char *);
static int (*real_zmq_disconnect)(void *, const char *);
static int (*real_zmq_setsockopt)(void *, int, const void *, size_t);
static int (*real_zmq_getsockopt)(void *, int, void *, size_t *);
static int (*real_zmq_send)(void *, const void *, size_t, int);
static int (*real_zmq_recv)(void *, void *, size_t, int);
static int (*real_zmq_msg_send)(zmq_msg_t *, void *, int);
static int (*real_zmq_msg_recv)(zmq_msg_t *, void *, int);
static int (*real_zmq_msg_init)(zmq_msg_t *);
static int (*real_zmq_msg_init_size)(zmq_msg_t *, size_t);
static int (*real_zmq_msg_init_data)(zmq_msg_t *, void *, size_t, void (*)(void *, void *), void *);
static int (*real_zmq_msg_close)(zmq_msg_t *);
static void *(*real_zmq_msg_data)(zmq_msg_t *);
static size_t (*real_zmq_msg_size)(zmq_msg_t *);
static int (*real_zmq_poll)(zmq_pollitem_t *, int, long);
static int (*real_zmq_proxy)(void *, void *, void *);
static int (*real_zmq_errno)(void);
static int (*real_zmq_socket_monitor)(void *, const char *, int);
static const char *(*real_zmq_strerror)(int);
static void *(*real_dlsym)(void *, const char *);
static void *(*real_dlopen)(const char *, int);
static int (*real_pthread_setname_np)(pthread_t, const char *);
static int (*real_prctl)(int, ...);
static int (*old_zmq_ctx_shutdown)(void *);
static int (*old_zmq_ctx_term)(void *);
static int (*old_zmq_close)(void *);

static void nvsnap_zmq_reinit_ctx(nvsnap_zmq_ctx_t *ctx);
static void nvsnap_zmq_register_ctx(nvsnap_zmq_ctx_t *ctx);
static void nvsnap_zmq_count_ops(nvsnap_zmq_socket_t *sock, int *connects, int *binds);
static void *nvsnap_zmq_symbol_override(const char *symbol);
static int nvsnap_zmq_restore_detected(void);
static int nvsnap_zmq_trace_enabled(void);
static void nvsnap_zmq_switch_to_newns_if_needed(void);
static void nvsnap_zmq_load_real(void);

static void nvsnap_zmq_reset_real(void) {
    real_zmq_ctx_new = NULL;
    real_zmq_ctx_term = NULL;
    real_zmq_ctx_destroy = NULL;
    real_zmq_ctx_shutdown = NULL;
    real_zmq_ctx_set = NULL;
    real_zmq_ctx_get = NULL;
    real_zmq_socket = NULL;
    real_zmq_close = NULL;
    real_zmq_bind = NULL;
    real_zmq_unbind = NULL;
    real_zmq_connect = NULL;
    real_zmq_disconnect = NULL;
    real_zmq_setsockopt = NULL;
    real_zmq_getsockopt = NULL;
    real_zmq_send = NULL;
    real_zmq_recv = NULL;
    real_zmq_msg_send = NULL;
    real_zmq_msg_recv = NULL;
    real_zmq_msg_init = NULL;
    real_zmq_msg_init_size = NULL;
    real_zmq_msg_init_data = NULL;
    real_zmq_msg_close = NULL;
    real_zmq_poll = NULL;
    real_zmq_proxy = NULL;
    real_zmq_errno = NULL;
}

static void nvsnap_zmq_resolve_symbol(void **fn, const char *name, void *handle) {
    if (*fn) {
        return;
    }
    if (!real_dlsym) {
        real_dlsym = dlvsym(RTLD_NEXT, "dlsym", "GLIBC_2.2.5");
    }
    if (real_dlsym) {
        *fn = real_dlsym(handle, name);
    } else {
        *fn = dlsym(handle, name);
    }
}

static int nvsnap_zmq_is_libzmq_path(const char *filename) {
    if (!filename || !filename[0]) {
        return 0;
    }
    if (strstr(filename, "libzmq") != NULL) {
        return 1;
    }
    return 0;
}

static const char *nvsnap_zmq_override_path(void) {
    const char *path = getenv("NVSNAP_ZMQ_LIB_PATH");
    if (path && path[0]) {
        return path;
    }
    return NULL;
}

static int nvsnap_zmq_force_terminate_enabled(void) {
    const char *env = getenv("NVSNAP_ZMQ_FORCE_TERMINATE");
    return env && env[0] == '1';
}

static int nvsnap_zmq_recovery_delay_ms(void) {
    const char *env = getenv("NVSNAP_ZMQ_RECOVERY_DELAY_MS");
    if (env && *env) {
        char *end = NULL;
        long val = strtol(env, &end, 10);
        if (end && *end == '\0' && val > 0) {
            return (int)val;
        }
    }
    return 0;
}

static int nvsnap_zmq_ehostunreach_max_ms(void) {
    const char *env = getenv("NVSNAP_ZMQ_EHOSTUNREACH_MAX_MS");
    if (env && *env) {
        char *end = NULL;
        long val = strtol(env, &end, 10);
        if (end && *end == '\0' && val > 0) {
            return (int)val;
        }
    }
    return 0;
}

static int nvsnap_zmq_ehostunreach_sleep_ms(void) {
    const char *env = getenv("NVSNAP_ZMQ_EHOSTUNREACH_SLEEP_MS");
    if (env && *env) {
        char *end = NULL;
        long val = strtol(env, &end, 10);
        if (end && *end == '\0' && val > 0) {
            return (int)val;
        }
    }
    return 100;
}

static int nvsnap_zmq_gate_max_ms(void) {
    const char *env = getenv("NVSNAP_ZMQ_GATE_MAX_MS");
    if (env && *env) {
        char *end = NULL;
        long val = strtol(env, &end, 10);
        if (end && *end == '\0' && val > 0) {
            return (int)val;
        }
    }
    return 1000;
}

static int nvsnap_zmq_gate_sleep_ms(void) {
    const char *env = getenv("NVSNAP_ZMQ_GATE_SLEEP_MS");
    if (env && *env) {
        char *end = NULL;
        long val = strtol(env, &end, 10);
        if (end && *end == '\0' && val > 0) {
            return (int)val;
        }
    }
    return 50;
}

static int nvsnap_zmq_kill_bg_threads_enabled(void) {
    const char *env = getenv("NVSNAP_ZMQ_KILL_BG_THREADS");
    return env && env[0] == '1';
}

static int nvsnap_zmq_skip_old_close_enabled(void) {
    const char *env = getenv("NVSNAP_ZMQ_SKIP_OLD_CLOSE");
    return env && env[0] == '1';
}

static int nvsnap_zmq_rebuild_on_einval_enabled(void) {
    const char *env = getenv("NVSNAP_ZMQ_REBUILD_ON_EINVAL");
    if (!env || !env[0]) {
        return 0;
    }
    return env[0] == '1';
}

static int nvsnap_zmq_rebuild_on_first_send_enabled(void) {
    const char *env = getenv("NVSNAP_ZMQ_REBUILD_ON_FIRST_SEND");
    if (!env || !env[0]) {
        return 0;
    }
    return env[0] == '1';
}

static int nvsnap_zmq_is_bg_thread_name(const char *name) {
    if (!name || !name[0]) {
        return 0;
    }
    return strncmp(name, "ZMQbg", 5) == 0;
}

static void nvsnap_zmq_maybe_exit_bg_thread(const char *name, const char *src) {
    if (!nvsnap_zmq_kill_bg_threads_enabled()) {
        return;
    }
    if (!nvsnap_zmq_restore_detected()) {
        return;
    }
    if (!nvsnap_zmq_is_bg_thread_name(name)) {
        return;
    }
    NVSNAP_WARN("ZMQ bg thread detected post-restore, exiting name=%s src=%s tid=%ld",
               name ? name : "(null)", src ? src : "(unknown)", (long)syscall(SYS_gettid));
    pthread_exit(NULL);
}

static int nvsnap_zmq_io_threads_override(void) {
    const char *env = getenv("NVSNAP_ZMQ_IO_THREADS");
    if (env && *env) {
        char *end = NULL;
        long val = strtol(env, &end, 10);
        if (end && *end == '\0' && val >= 0) {
            return (int)val;
        }
    }
    return -1;
}

static void nvsnap_zmq_kill_bg_threads(void) {
    if (!nvsnap_zmq_kill_bg_threads_enabled()) {
        return;
    }
    DIR *dir = opendir("/proc/self/task");
    if (!dir) {
        NVSNAP_WARN("ZMQ kill threads: failed to open /proc/self/task errno=%d", errno);
        return;
    }
    pid_t self_tid = (pid_t)syscall(SYS_gettid);
    int killed = 0;
    struct dirent *ent = NULL;
    while ((ent = readdir(dir)) != NULL) {
        if (ent->d_name[0] < '0' || ent->d_name[0] > '9') {
            continue;
        }
        pid_t tid = (pid_t)strtol(ent->d_name, NULL, 10);
        if (tid <= 0 || tid == self_tid) {
            continue;
        }
        char comm_path[256];
        snprintf(comm_path, sizeof(comm_path), "/proc/self/task/%s/comm", ent->d_name);
        FILE *f = fopen(comm_path, "r");
        if (!f) {
            continue;
        }
        char comm[64];
        if (fgets(comm, sizeof(comm), f)) {
            comm[strcspn(comm, "\n")] = '\0';
            if (strncmp(comm, "ZMQbg", 5) == 0) {
                NVSNAP_INFO("ZMQ kill thread tid=%d comm=%s", tid, comm);
                kill(tid, SIGKILL);
                killed++;
            }
        }
        fclose(f);
    }
    closedir(dir);
    NVSNAP_INFO("ZMQ kill threads completed killed=%d", killed);
}

static void nvsnap_zmq_set_recovering(int recovering) {
    pthread_mutex_lock(&g_zmq_recover_lock);
    g_zmq_recovering = recovering;
    pthread_mutex_unlock(&g_zmq_recover_lock);
}

static int nvsnap_zmq_is_recovering(void) {
    int recovering = 0;
    pthread_mutex_lock(&g_zmq_recover_lock);
    recovering = g_zmq_recovering;
    pthread_mutex_unlock(&g_zmq_recover_lock);
    return recovering;
}

static void nvsnap_zmq_count_state(int *ctxs, int *socks) {
    int ctx_count = 0;
    int sock_count = 0;
    pthread_mutex_lock(&g_zmq_ctx_list_lock);
    nvsnap_zmq_ctx_t *ctx = g_zmq_ctx_list;
    while (ctx) {
        ctx_count++;
        nvsnap_zmq_socket_t *sock = ctx->sockets;
        while (sock) {
            sock_count++;
            sock = sock->next;
        }
        ctx = ctx->next;
    }
    pthread_mutex_unlock(&g_zmq_ctx_list_lock);
    if (ctxs) {
        *ctxs = ctx_count;
    }
    if (socks) {
        *socks = sock_count;
    }
}

static void nvsnap_zmq_gate_if_recovering(const char *op) {
    if (!nvsnap_zmq_restore_detected()) {
        return;
    }
    if (!nvsnap_zmq_is_recovering()) {
        return;
    }
    int max_ms = nvsnap_zmq_gate_max_ms();
    int sleep_ms = nvsnap_zmq_gate_sleep_ms();
    int waited = 0;
    while (nvsnap_zmq_is_recovering() && waited < max_ms) {
        usleep((useconds_t)sleep_ms * 1000);
        waited += sleep_ms;
    }
    if (nvsnap_zmq_is_recovering()) {
        NVSNAP_WARN("ZMQ gate timed out op=%s waited_ms=%d", op ? op : "unknown", waited);
    } else if (waited > 0 && nvsnap_zmq_trace_enabled()) {
        NVSNAP_INFO("ZMQ gate cleared op=%s waited_ms=%d", op ? op : "unknown", waited);
    }
}

static void nvsnap_zmq_load_old(void) {
    if (old_zmq_ctx_shutdown || old_zmq_ctx_term || old_zmq_close) {
        return;
    }
    if (!real_dlsym) {
        real_dlsym = dlvsym(RTLD_NEXT, "dlsym", "GLIBC_2.2.5");
    }
    if (real_dlsym) {
        old_zmq_ctx_shutdown = real_dlsym(RTLD_NEXT, "zmq_ctx_shutdown");
        old_zmq_ctx_term = real_dlsym(RTLD_NEXT, "zmq_ctx_term");
        old_zmq_close = real_dlsym(RTLD_NEXT, "zmq_close");
    } else {
        old_zmq_ctx_shutdown = dlsym(RTLD_NEXT, "zmq_ctx_shutdown");
        old_zmq_ctx_term = dlsym(RTLD_NEXT, "zmq_ctx_term");
        old_zmq_close = dlsym(RTLD_NEXT, "zmq_close");
    }
    if (!old_zmq_ctx_shutdown || !old_zmq_ctx_term || !old_zmq_close) {
        void *lib = dlopen("libzmq.so.5", RTLD_LAZY);
        if (!lib) {
            lib = dlopen("libzmq.so", RTLD_LAZY);
        }
        if (lib) {
            if (!old_zmq_ctx_shutdown) {
                old_zmq_ctx_shutdown = dlsym(lib, "zmq_ctx_shutdown");
            }
            if (!old_zmq_ctx_term) {
                old_zmq_ctx_term = dlsym(lib, "zmq_ctx_term");
            }
            if (!old_zmq_close) {
                old_zmq_close = dlsym(lib, "zmq_close");
            }
        }
    }
}

static void nvsnap_zmq_shutdown_ctxs_before_switch(void) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!real_zmq_ctx_shutdown) {
        return;
    }
    pthread_mutex_lock(&g_zmq_ctx_list_lock);
    nvsnap_zmq_ctx_t *ctx = g_zmq_ctx_list;
    while (ctx) {
        if (ctx->real && !ctx->destroyed) {
            NVSNAP_INFO("ZMQ pre-switch shutdown ctx=%p real=%p", ctx, ctx->real);
            real_zmq_ctx_shutdown(ctx->real);
        }
        ctx = ctx->next;
    }
    pthread_mutex_unlock(&g_zmq_ctx_list_lock);
}

static int nvsnap_zmq_pre_shutdown_enabled(void) {
    const char *env = getenv("NVSNAP_ZMQ_PRE_SHUTDOWN");
    return env && env[0] == '1';
}

void *dlopen(const char *filename, int flags) {
    if (!real_dlsym) {
        real_dlsym = dlvsym(RTLD_NEXT, "dlsym", "GLIBC_2.2.5");
    }
    if (!real_dlopen) {
        real_dlopen = real_dlsym ? real_dlsym(RTLD_NEXT, "dlopen") : dlsym(RTLD_NEXT, "dlopen");
    }
    if (nvsnap_zmq_restore_detected() && nvsnap_zmq_is_libzmq_path(filename)) {
        pthread_mutex_lock(&g_zmq_load_lock);
        if (!g_zmq_newns_handle) {
            const char *override = nvsnap_zmq_override_path();
            void *lib = NULL;
            if (override) {
                lib = dlmopen(LM_ID_NEWLM, override, flags);
            }
            if (!lib) {
                lib = dlmopen(LM_ID_NEWLM, filename, flags);
            }
            if (!lib) {
                lib = dlmopen(LM_ID_NEWLM, "libzmq.so.5", flags);
            }
            if (!lib) {
                lib = dlmopen(LM_ID_NEWLM, "libzmq.so", flags);
            }
            if (lib) {
                g_zmq_newns_handle = lib;
                g_zmq_use_newns = 1;
                nvsnap_zmq_reset_real();
                nvsnap_zmq_load_real();
                NVSNAP_INFO("ZMQ dlopen redirected to newns handle=%p path=%s",
                           lib, override ? override : (filename ? filename : "(null)"));
            }
        }
        pthread_mutex_unlock(&g_zmq_load_lock);
        if (g_zmq_newns_handle) {
            return g_zmq_newns_handle;
        }
    }
    return real_dlopen ? real_dlopen(filename, flags) : NULL;
}

static void nvsnap_zmq_load_real(void) {
    void *handle = g_zmq_use_newns && g_zmq_newns_handle ? g_zmq_newns_handle : RTLD_NEXT;
    if (!real_dlsym) {
        real_dlsym = dlvsym(RTLD_NEXT, "dlsym", "GLIBC_2.2.5");
    }
    g_zmq_handle = handle;

    nvsnap_zmq_resolve_symbol((void **)&real_zmq_ctx_new, "zmq_ctx_new", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_ctx_term, "zmq_ctx_term", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_ctx_destroy, "zmq_ctx_destroy", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_ctx_shutdown, "zmq_ctx_shutdown", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_ctx_set, "zmq_ctx_set", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_ctx_get, "zmq_ctx_get", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_socket, "zmq_socket", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_close, "zmq_close", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_bind, "zmq_bind", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_unbind, "zmq_unbind", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_connect, "zmq_connect", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_disconnect, "zmq_disconnect", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_setsockopt, "zmq_setsockopt", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_getsockopt, "zmq_getsockopt", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_send, "zmq_send", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_recv, "zmq_recv", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_msg_send, "zmq_msg_send", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_msg_recv, "zmq_msg_recv", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_msg_init, "zmq_msg_init", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_msg_init_size, "zmq_msg_init_size", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_msg_init_data, "zmq_msg_init_data", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_msg_close, "zmq_msg_close", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_msg_data, "zmq_msg_data", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_msg_size, "zmq_msg_size", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_poll, "zmq_poll", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_proxy, "zmq_proxy", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_errno, "zmq_errno", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_strerror, "zmq_strerror", handle);
    nvsnap_zmq_resolve_symbol((void **)&real_zmq_socket_monitor, "zmq_socket_monitor", handle);

    if (!g_zmq_use_newns && (!real_zmq_ctx_new || !real_zmq_socket)) {
        const char *override = nvsnap_zmq_override_path();
        void *lib = NULL;
        if (override) {
            lib = dlopen(override, RTLD_LAZY);
        }
        if (!lib) {
            lib = dlopen("libzmq.so.5", RTLD_LAZY);
        }
        if (!lib) {
            lib = dlopen("libzmq.so", RTLD_LAZY);
        }
        if (lib) {
            g_zmq_handle = lib;
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_ctx_new, "zmq_ctx_new", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_ctx_term, "zmq_ctx_term", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_ctx_destroy, "zmq_ctx_destroy", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_ctx_shutdown, "zmq_ctx_shutdown", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_ctx_set, "zmq_ctx_set", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_ctx_get, "zmq_ctx_get", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_socket, "zmq_socket", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_close, "zmq_close", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_bind, "zmq_bind", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_unbind, "zmq_unbind", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_connect, "zmq_connect", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_disconnect, "zmq_disconnect", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_setsockopt, "zmq_setsockopt", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_getsockopt, "zmq_getsockopt", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_send, "zmq_send", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_recv, "zmq_recv", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_msg_send, "zmq_msg_send", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_msg_recv, "zmq_msg_recv", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_msg_init, "zmq_msg_init", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_msg_init_size, "zmq_msg_init_size", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_msg_init_data, "zmq_msg_init_data", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_msg_close, "zmq_msg_close", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_msg_data, "zmq_msg_data", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_msg_size, "zmq_msg_size", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_poll, "zmq_poll", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_proxy, "zmq_proxy", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_errno, "zmq_errno", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_strerror, "zmq_strerror", lib);
            nvsnap_zmq_resolve_symbol((void **)&real_zmq_socket_monitor, "zmq_socket_monitor", lib);
        }
    }
}

static void nvsnap_zmq_switch_to_newns_if_needed(void) {
    if (!nvsnap_zmq_restore_detected()) {
        return;
    }
    pthread_mutex_lock(&g_zmq_load_lock);
    if (!g_zmq_use_newns) {
        if (nvsnap_zmq_pre_shutdown_enabled()) {
            nvsnap_zmq_shutdown_ctxs_before_switch();
        }
        const char *override = nvsnap_zmq_override_path();
        void *lib = NULL;
        if (override) {
            lib = dlmopen(LM_ID_NEWLM, override, RTLD_LAZY | RTLD_LOCAL);
        }
        if (!lib) {
            lib = dlmopen(LM_ID_NEWLM, "libzmq.so.5", RTLD_LAZY | RTLD_LOCAL);
        }
        if (!lib) {
            lib = dlmopen(LM_ID_NEWLM, "libzmq.so", RTLD_LAZY | RTLD_LOCAL);
        }
        if (lib) {
            g_zmq_newns_handle = lib;
            g_zmq_use_newns = 1;
            nvsnap_zmq_reset_real();
            nvsnap_zmq_load_real();
            NVSNAP_INFO("ZMQ switched to new link namespace handle=%p path=%s",
                       lib, override ? override : "libzmq.so.5");
        } else {
            NVSNAP_WARN("ZMQ newns dlmopen failed: %s", dlerror());
        }
    }
    pthread_mutex_unlock(&g_zmq_load_lock);
}

static void *nvsnap_zmq_symbol_override(const char *symbol) {
    if (!symbol) {
        return NULL;
    }
    if (strcmp(symbol, "zmq_ctx_new") == 0) return (void *)zmq_ctx_new;
    if (strcmp(symbol, "zmq_ctx_term") == 0) return (void *)zmq_ctx_term;
    if (strcmp(symbol, "zmq_ctx_destroy") == 0) return (void *)zmq_ctx_destroy;
    if (strcmp(symbol, "zmq_ctx_shutdown") == 0) return (void *)zmq_ctx_shutdown;
    if (strcmp(symbol, "zmq_ctx_set") == 0) return (void *)zmq_ctx_set;
    if (strcmp(symbol, "zmq_ctx_get") == 0) return (void *)zmq_ctx_get;
    if (strcmp(symbol, "zmq_socket") == 0) return (void *)zmq_socket;
    if (strcmp(symbol, "zmq_close") == 0) return (void *)zmq_close;
    if (strcmp(symbol, "zmq_bind") == 0) return (void *)zmq_bind;
    if (strcmp(symbol, "zmq_unbind") == 0) return (void *)zmq_unbind;
    if (strcmp(symbol, "zmq_connect") == 0) return (void *)zmq_connect;
    if (strcmp(symbol, "zmq_disconnect") == 0) return (void *)zmq_disconnect;
    if (strcmp(symbol, "zmq_setsockopt") == 0) return (void *)zmq_setsockopt;
    if (strcmp(symbol, "zmq_getsockopt") == 0) return (void *)zmq_getsockopt;
    if (strcmp(symbol, "zmq_send") == 0) return (void *)zmq_send;
    if (strcmp(symbol, "zmq_recv") == 0) return (void *)zmq_recv;
    if (strcmp(symbol, "zmq_msg_send") == 0) return (void *)zmq_msg_send;
    if (strcmp(symbol, "zmq_msg_recv") == 0) return (void *)zmq_msg_recv;
    if (strcmp(symbol, "zmq_msg_init") == 0) return (void *)zmq_msg_init;
    if (strcmp(symbol, "zmq_msg_init_size") == 0) return (void *)zmq_msg_init_size;
    if (strcmp(symbol, "zmq_msg_init_data") == 0) return (void *)zmq_msg_init_data;
    if (strcmp(symbol, "zmq_msg_close") == 0) return (void *)zmq_msg_close;
    if (strcmp(symbol, "zmq_poll") == 0) return (void *)zmq_poll;
    if (strcmp(symbol, "zmq_proxy") == 0) return (void *)zmq_proxy;
    if (strcmp(symbol, "zmq_errno") == 0) return (void *)zmq_errno;
    return NULL;
}

/*
 * dlsym override — unversioned, same as the working 2-library setup.
 * Unversioned dlsym override — routes symbol lookups to our wrappers.
 * NvSnap's symbol table handles CUDA + NCCL routing.
 * NvSnap handles ZMQ routing.
 */
extern void *nvsnap_lookup_symbol(const char *name); /* nvsnap_symbol_table.c */

void *dlsym(void *handle, const char *symbol) {
    if (!real_dlsym) {
        real_dlsym = dlvsym(RTLD_NEXT, "dlsym", "GLIBC_2.2.5");
    }
    /* Only intercept global lookups (RTLD_DEFAULT, RTLD_NEXT).
     * When a specific library handle is passed (e.g., nvsnap_resolve_real
     * calling dlsym(libcudart_handle, "cudaSetDevice")), pass through directly.
     * Without this, we return our own hooks instead of the real functions,
     * causing NULL resolution → segfault during CUDA init. */
    if (handle == RTLD_DEFAULT || handle == RTLD_NEXT) {
        void *override = nvsnap_zmq_symbol_override(symbol);
        if (override) return override;
        override = nvsnap_lookup_symbol(symbol);
        if (override) return override;
    }
    return real_dlsym ? real_dlsym(handle, symbol) : NULL;
}

int pthread_setname_np(pthread_t thread, const char *name) {
    if (!real_pthread_setname_np) {
        if (!real_dlsym) {
            real_dlsym = dlvsym(RTLD_NEXT, "dlsym", "GLIBC_2.2.5");
        }
        if (real_dlsym)
            real_pthread_setname_np = real_dlsym(RTLD_NEXT, "pthread_setname_np");
    }
    if (!real_pthread_setname_np) {
        errno = ENOSYS;
        return -1;
    }
    if (nvsnap_zmq_kill_bg_threads_enabled() &&
        nvsnap_zmq_restore_detected() &&
        nvsnap_zmq_is_bg_thread_name(name)) {
        if (pthread_equal(thread, pthread_self())) {
            nvsnap_zmq_maybe_exit_bg_thread(name, "pthread_setname_np");
        } else {
            NVSNAP_WARN("ZMQ bg thread named in other thread post-restore name=%s", name);
        }
    }
    return real_pthread_setname_np(thread, name);
}

int prctl(int option, ...) {
    if (!real_prctl) {
        if (!real_dlsym) {
            real_dlsym = dlvsym(RTLD_NEXT, "dlsym", "GLIBC_2.2.5");
        }
        real_prctl = real_dlsym ? real_dlsym(RTLD_NEXT, "prctl")
                                : dlsym(RTLD_NEXT, "prctl");
    }
    if (!real_prctl) {
        errno = ENOSYS;
        return -1;
    }
    va_list ap;
    unsigned long arg2 = 0;
    unsigned long arg3 = 0;
    unsigned long arg4 = 0;
    unsigned long arg5 = 0;
    va_start(ap, option);
    arg2 = va_arg(ap, unsigned long);
    arg3 = va_arg(ap, unsigned long);
    arg4 = va_arg(ap, unsigned long);
    arg5 = va_arg(ap, unsigned long);
    va_end(ap);
    if (option == PR_SET_NAME && nvsnap_zmq_kill_bg_threads_enabled() &&
        nvsnap_zmq_restore_detected()) {
        const char *name = (const char *)arg2;
        if (nvsnap_zmq_is_bg_thread_name(name)) {
            nvsnap_zmq_maybe_exit_bg_thread(name, "prctl");
        }
    }
    return real_prctl(option, arg2, arg3, arg4, arg5);
}

static int nvsnap_zmq_is_enabled(void) {
    if (g_zmq_enabled >= 0) {
        return g_zmq_enabled;
    }
    const char *env = getenv("NVSNAP_ZMQ_INTERCEPT");
    if (!env) {
        g_zmq_enabled = 1;
        return g_zmq_enabled;
    }
    if (env[0] != '\0' && env[0] != '0' && env[0] != 'f' && env[0] != 'F') {
        g_zmq_enabled = 1;
    } else {
        g_zmq_enabled = 0;
    }
    return g_zmq_enabled;
}

static int nvsnap_zmq_trace_enabled(void) {
    if (g_zmq_trace >= 0) {
        return g_zmq_trace;
    }
    const char *env = getenv("NVSNAP_ZMQ_TRACE");
    if (env && env[0] != '\0' && env[0] != '0' && env[0] != 'f' && env[0] != 'F') {
        g_zmq_trace = 1;
    } else {
        g_zmq_trace = 0;
    }
    return g_zmq_trace;
}

static int nvsnap_zmq_restore_detected(void) {
    if (g_zmq_restore_seen) {
        return 1;
    }
    if (access("/nvsnap-lib/.restored", F_OK) == 0 ||
        access("/nvsnap/.restored", F_OK) == 0 ||
        access("/var/run/nvsnap/.restored", F_OK) == 0) {
        g_zmq_restore_seen = 1;
    }
    return g_zmq_restore_seen;
}

static nvsnap_zmq_ctx_t *nvsnap_zmq_ctx_from_ptr(void *ctx) {
    nvsnap_zmq_ctx_t *proxy = (nvsnap_zmq_ctx_t *)ctx;
    if (proxy && proxy->magic == NVSNAP_ZMQ_CTX_MAGIC) {
        return proxy;
    }
    if (!ctx || !nvsnap_zmq_is_enabled()) {
        return NULL;
    }
    pthread_mutex_lock(&g_zmq_ctx_list_lock);
    nvsnap_zmq_ctx_t *iter = g_zmq_ctx_list;
    while (iter) {
        if (iter->real == ctx) {
            pthread_mutex_unlock(&g_zmq_ctx_list_lock);
            return iter;
        }
        iter = iter->next;
    }
    pthread_mutex_unlock(&g_zmq_ctx_list_lock);
    nvsnap_zmq_ctx_t *wrapped = calloc(1, sizeof(*wrapped));
    if (!wrapped) {
        return NULL;
    }
    wrapped->magic = NVSNAP_ZMQ_CTX_MAGIC;
    wrapped->real = ctx;
    pthread_mutex_init(&wrapped->lock, NULL);
    nvsnap_zmq_register_ctx(wrapped);
    NVSNAP_INFO("ZMQ ctx wrap pid=%d ctx=%p real=%p", getpid(), wrapped, wrapped->real);
    return wrapped;
}

static nvsnap_zmq_socket_t *nvsnap_zmq_sock_from_ptr(void *sock) {
    nvsnap_zmq_socket_t *proxy = (nvsnap_zmq_socket_t *)sock;
    if (proxy && proxy->magic == NVSNAP_ZMQ_SOCK_MAGIC) {
        return proxy;
    }
    return NULL;
}

static int nvsnap_zmq_errno(void) {
    if (real_zmq_errno) {
        return real_zmq_errno();
    }
    return errno;
}

static const char *nvsnap_zmq_strerror(int err) {
    if (real_zmq_strerror) {
        return real_zmq_strerror(err);
    }
    return strerror(err);
}

int zmq_errno(void) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!nvsnap_zmq_is_enabled()) {
        return real_zmq_errno ? real_zmq_errno() : errno;
    }
    return nvsnap_zmq_errno();
}

static int nvsnap_zmq_ipc_path(const char *endpoint, const char **path_out) {
    static const char prefix[] = "ipc://";
    size_t len;
    const char *path;

    if (!endpoint) {
        return 0;
    }
    len = strlen(prefix);
    if (strncmp(endpoint, prefix, len) != 0) {
        return 0;
    }
    path = endpoint + len;
    if (!path[0] || path[0] == '@') {
        return 0;
    }
    if (path_out) {
        *path_out = path;
    }
    return 1;
}

static void nvsnap_zmq_add_op(nvsnap_zmq_socket_t *sock, enum nvsnap_zmq_op_type type,
                            int option, const void *data, size_t len, const char *endpoint) {
    if (nvsnap_zmq_trace_enabled() || nvsnap_zmq_restore_detected()) {
        NVSNAP_INFO("ZMQ op add pid=%d sock=%p real=%p type=%d endpoint=%s",
                   getpid(), sock, sock ? sock->real : NULL, type,
                   endpoint ? endpoint : "(null)");
    }
    struct nvsnap_zmq_op *op = calloc(1, sizeof(*op));
    if (!op) {
        return;
    }
    op->type = type;
    op->option = option;
    if (data && len > 0) {
        op->data = malloc(len);
        if (op->data) {
            memcpy(op->data, data, len);
            op->len = len;
        }
    }
    if (endpoint) {
        op->endpoint = strdup(endpoint);
    }
    if (!sock->ops_head) {
        sock->ops_head = op;
        sock->ops_tail = op;
    } else {
        sock->ops_tail->next = op;
        sock->ops_tail = op;
    }
}

static void nvsnap_zmq_replay_ops_ex(nvsnap_zmq_socket_t *sock, int allow_bind) {
    struct nvsnap_zmq_op *op = sock->ops_head;
    int op_count = 0;
    if (nvsnap_zmq_restore_detected() || nvsnap_zmq_trace_enabled()) {
        NVSNAP_INFO("ZMQ replay_ops_ex starting pid=%d sock=%p real=%p allow_bind=%d",
                   getpid(), sock, sock ? sock->real : NULL, allow_bind);
    }
    while (op) {
        op_count++;
        switch (op->type) {
            case NVSNAP_ZMQ_OP_SETSOCKOPT:
                if (op->data && op->len) {
                    if (real_zmq_setsockopt(sock->real, op->option, op->data, op->len) != 0) {
                        NVSNAP_WARN("ZMQ replay setsockopt failed opt=%d errno=%d", op->option, nvsnap_zmq_errno());
                    }
                }
                break;
            case NVSNAP_ZMQ_OP_BIND:
                if (op->endpoint) {
                    if (!allow_bind && nvsnap_zmq_restore_detected()) {
                        if (nvsnap_zmq_trace_enabled()) {
                            NVSNAP_INFO("ZMQ replay bind deferred endpoint=%s", op->endpoint);
                        }
                        break;
                    }
                    const char *ipc_path = NULL;
                    if (nvsnap_zmq_ipc_path(op->endpoint, &ipc_path)) {
                        unlink(ipc_path);
                    }
                    int attempts = 0;
                    int rc = -1;
                    int err = 0;
                    int max_attempts = nvsnap_zmq_restore_detected() ? 10 : 1;
                    do {
                        rc = real_zmq_bind(sock->real, op->endpoint);
                        if (rc == 0) {
                            break;
                        }
                        err = nvsnap_zmq_errno();
                        attempts++;
                        usleep(100 * 1000);
                    } while (attempts < max_attempts);
                    if (rc != 0) {
                        NVSNAP_WARN("ZMQ replay bind failed endpoint=%s errno=%d err=%s attempts=%d",
                                   op->endpoint, err, nvsnap_zmq_strerror(err), attempts);
                        /* For IPC: try unlinking stale socket file and retry once more */
                        const char *retry_ipc_path = NULL;
                        if (nvsnap_zmq_ipc_path(op->endpoint, &retry_ipc_path)) {
                            NVSNAP_INFO("ZMQ replay bind: unlinking stale IPC socket %s and retrying",
                                       retry_ipc_path);
                            unlink(retry_ipc_path);
                            rc = real_zmq_bind(sock->real, op->endpoint);
                            if (rc == 0) {
                                NVSNAP_INFO("ZMQ replay bind succeeded after IPC unlink endpoint=%s",
                                           op->endpoint);
                            } else {
                                NVSNAP_WARN("ZMQ replay bind still failed after IPC unlink endpoint=%s errno=%d",
                                           op->endpoint, nvsnap_zmq_errno());
                                sock->replay_failed = 1;
                            }
                        } else {
                            sock->replay_failed = 1;
                        }
                    } else if (nvsnap_zmq_trace_enabled()) {
                        NVSNAP_INFO("ZMQ replay bind ok endpoint=%s", op->endpoint);
                    }
                }
                break;
            case NVSNAP_ZMQ_OP_CONNECT:
                if (op->endpoint) {
                    int attempts = 0;
                    int rc = -1;
                    int err = 0;
                    const char *ipc_path = NULL;
                    if (nvsnap_zmq_ipc_path(op->endpoint, &ipc_path)) {
                        int wait_attempts = 0;
                        while (access(ipc_path, F_OK) != 0 && wait_attempts < 50) {
                            usleep(100 * 1000);
                            wait_attempts++;
                        }
                    }
                    do {
                        rc = real_zmq_connect(sock->real, op->endpoint);
                        if (rc == 0) {
                            break;
                        }
                        err = nvsnap_zmq_errno();
                        if (err != ENOENT && err != ECONNREFUSED) {
                            break;
                        }
                        attempts++;
                        usleep(100 * 1000);
                    } while (attempts < 50);
                    if (rc != 0) {
                        NVSNAP_WARN("ZMQ replay connect failed endpoint=%s errno=%d attempts=%d", op->endpoint, err, attempts);
                        sock->replay_failed = 1;
                    } else if (nvsnap_zmq_trace_enabled()) {
                        NVSNAP_INFO("ZMQ replay connect ok endpoint=%s attempts=%d", op->endpoint, attempts);
                    }
                }
                break;
            case NVSNAP_ZMQ_OP_UNBIND:
                if (op->endpoint) {
                    if (real_zmq_unbind(sock->real, op->endpoint) != 0) {
                        NVSNAP_WARN("ZMQ replay unbind failed endpoint=%s errno=%d", op->endpoint, nvsnap_zmq_errno());
                    }
                }
                break;
            case NVSNAP_ZMQ_OP_DISCONNECT:
                if (op->endpoint) {
                    if (real_zmq_disconnect(sock->real, op->endpoint) != 0) {
                        NVSNAP_WARN("ZMQ replay disconnect failed endpoint=%s errno=%d", op->endpoint, nvsnap_zmq_errno());
                    }
                }
                break;
            default:
                break;
        }
        op = op->next;
    }
    if (nvsnap_zmq_restore_detected() || nvsnap_zmq_trace_enabled()) {
        NVSNAP_INFO("ZMQ replay_ops_ex completed pid=%d sock=%p ops_processed=%d",
                   getpid(), sock, op_count);
    }
}

static void nvsnap_zmq_force_reconnect(nvsnap_zmq_socket_t *sock) {
    if (!sock || !sock->real) {
        return;
    }
    struct nvsnap_zmq_op *op = sock->ops_head;
    while (op) {
        if (op->type == NVSNAP_ZMQ_OP_CONNECT && op->endpoint) {
            int rc = real_zmq_connect(sock->real, op->endpoint);
            NVSNAP_WARN("ZMQ force reconnect sock=%p endpoint=%s rc=%d errno=%d",
                       sock, op->endpoint, rc, nvsnap_zmq_errno());
        }
        op = op->next;
    }
}

static void nvsnap_zmq_force_connect_from_last_endpoint(nvsnap_zmq_socket_t *sock) {
    if (!sock || !sock->real || sock->forced_connect_done) {
        return;
    }
    if (!nvsnap_zmq_restore_detected()) {
        return;
    }
    int connects = 0;
    int binds = 0;
    nvsnap_zmq_count_ops(sock, &connects, &binds);
    if (connects > 0) {
        return;
    }
    if (sock->type == ZMQ_ROUTER) {
        return;
    }
    if (!real_zmq_getsockopt) {
        return;
    }
    char endpoint[256];
    size_t endpoint_len = sizeof(endpoint);
    endpoint[0] = '\0';
    if (real_zmq_getsockopt(sock->real, ZMQ_LAST_ENDPOINT, endpoint, &endpoint_len) != 0 ||
        !endpoint[0]) {
        return;
    }
    if (strncmp(endpoint, "ipc://", 6) != 0) {
        return;
    }
    const char *path = endpoint + 6;
    if (!path[0]) {
        return;
    }
    if (access(path, F_OK) != 0) {
        NVSNAP_WARN("ZMQ forced connect skipped; ipc path missing endpoint=%s", endpoint);
        return;
    }
    sock->forced_connect_done = 1;
    nvsnap_zmq_add_op(sock, NVSNAP_ZMQ_OP_CONNECT, 0, NULL, 0, endpoint);
    int rc = real_zmq_connect(sock->real, endpoint);
    NVSNAP_WARN("ZMQ forced connect from last endpoint sock=%p endpoint=%s rc=%d errno=%d",
               sock, endpoint, rc, nvsnap_zmq_errno());
}

static void nvsnap_zmq_dump_ctx_sockets(nvsnap_zmq_ctx_t *ctx, const char *tag) {
    if (!ctx) {
        return;
    }
    nvsnap_zmq_socket_t *sock = ctx->sockets;
    while (sock) {
        int connects = 0;
        int binds = 0;
        nvsnap_zmq_count_ops(sock, &connects, &binds);
        int type = sock->type;
        char endpoint[256];
        size_t endpoint_len = sizeof(endpoint);
        endpoint[0] = '\0';
        if (real_zmq_getsockopt) {
            if (real_zmq_getsockopt(sock->real, ZMQ_LAST_ENDPOINT, endpoint, &endpoint_len) != 0) {
                endpoint[0] = '\0';
            }
        }
        NVSNAP_INFO("ZMQ ctx dump %s pid=%d sock=%p real=%p type=%d ops_connect=%d ops_bind=%d endpoint=%s",
                   tag ? tag : "restore", getpid(), sock, sock->real, type, connects, binds,
                   endpoint[0] ? endpoint : "(none)");
        sock = sock->next;
    }
}

static void nvsnap_zmq_dump_ctx_sockets_to_file(nvsnap_zmq_ctx_t *ctx, const char *tag) {
    if (!ctx) {
        return;
    }
    char path[256];
    snprintf(path, sizeof(path), "/tmp/nvsnap-zmq-%d.log", (int)getpid());
    FILE *fp = fopen(path, "a");
    if (!fp) {
        return;
    }
    nvsnap_zmq_socket_t *sock = ctx->sockets;
    while (sock) {
        int connects = 0;
        int binds = 0;
        nvsnap_zmq_count_ops(sock, &connects, &binds);
        int type = sock->type;
        char endpoint[256];
        size_t endpoint_len = sizeof(endpoint);
        endpoint[0] = '\0';
        if (real_zmq_getsockopt) {
            if (real_zmq_getsockopt(sock->real, ZMQ_LAST_ENDPOINT, endpoint, &endpoint_len) != 0) {
                endpoint[0] = '\0';
            }
        }
        fprintf(fp,
                "ZMQ ctx dump %s pid=%d sock=%p real=%p type=%d ops_connect=%d ops_bind=%d endpoint=%s\n",
                tag ? tag : "restore", (int)getpid(), sock, sock->real, type, connects, binds,
                endpoint[0] ? endpoint : "(none)");
        sock = sock->next;
    }
    fclose(fp);
}

static void nvsnap_zmq_log_first_call(const char *where) {
    static __thread int logged = 0;
    if (logged) {
        return;
    }
    logged = 1;
    char cmdline[512];
    cmdline[0] = '\0';
    FILE *cfp = fopen("/proc/self/cmdline", "r");
    if (cfp) {
        size_t n = fread(cmdline, 1, sizeof(cmdline) - 1, cfp);
        fclose(cfp);
        cmdline[n] = '\0';
        for (size_t i = 0; i < n; i++) {
            if (cmdline[i] == '\0') {
                cmdline[i] = ' ';
            }
        }
    }
    const char *bases[] = {"/tmp", "/nvsnap-lib"};
    for (size_t i = 0; i < sizeof(bases) / sizeof(bases[0]); i++) {
        char path[256];
        snprintf(path, sizeof(path), "%s/nvsnap-zmq-%d.log", bases[i], (int)getpid());
        FILE *fp = fopen(path, "a");
        if (!fp) {
            continue;
        }
        fprintf(fp, "ZMQ first call pid=%d where=%s cmdline=%s\n",
                (int)getpid(), where ? where : "unknown", cmdline[0] ? cmdline : "(none)");
        fclose(fp);
    }
}

static size_t nvsnap_zmq_capture_identity(void *sock, unsigned char *buf, size_t bufsize) {
    if (!sock || !buf || bufsize == 0 || !real_zmq_getsockopt) {
        return 0;
    }
    size_t size = bufsize;
    if (real_zmq_getsockopt(sock, ZMQ_IDENTITY, buf, &size) != 0) {
        return 0;
    }
    if (size > 0) {
        NVSNAP_INFO("ZMQ identity captured pid=%d sock=%p len=%zu first=%02x%02x%02x%02x",
                   getpid(), sock, size,
                   buf[0], size > 1 ? buf[1] : 0,
                   size > 2 ? buf[2] : 0, size > 3 ? buf[3] : 0);
    }
    return size;
}

static void nvsnap_zmq_apply_identity(void *sock, const unsigned char *buf, size_t size) {
    if (!sock || !buf || size == 0 || !real_zmq_setsockopt) {
        return;
    }
    NVSNAP_INFO("ZMQ identity apply pid=%d sock=%p len=%zu first=%02x%02x%02x%02x",
               getpid(), sock, size,
               buf[0], size > 1 ? buf[1] : 0,
               size > 2 ? buf[2] : 0, size > 3 ? buf[3] : 0);
    if (real_zmq_setsockopt(sock, ZMQ_IDENTITY, buf, size) != 0) {
        NVSNAP_WARN("ZMQ restore sockopt IDENTITY failed errno=%d", nvsnap_zmq_errno());
    }
}

typedef struct nvsnap_zmq_monitor_ctx {
    void *mon;
    void *target;
} nvsnap_zmq_monitor_ctx_t;

static void *nvsnap_zmq_monitor_thread(void *arg) {
    nvsnap_zmq_monitor_ctx_t *ctx = (nvsnap_zmq_monitor_ctx_t *)arg;
    if (!ctx || !ctx->mon || !real_zmq_recv) {
        free(ctx);
        return NULL;
    }
    for (;;) {
        unsigned char event_buf[64];
        char addr_buf[256];
        int rc = real_zmq_recv(ctx->mon, event_buf, sizeof(event_buf), 0);
        if (rc == -1) {
            break;
        }
        size_t size = (size_t)rc;
        const unsigned char *data = event_buf;
        unsigned int event = 0;
        unsigned int value = 0;
        if (size >= 6 && data) {
            event = (unsigned int)(data[0] | (data[1] << 8));
            value = (unsigned int)(data[2] | (data[3] << 8) | (data[4] << 16) | (data[5] << 24));
        }
        rc = real_zmq_recv(ctx->mon, addr_buf, sizeof(addr_buf) - 1, 0);
        if (rc >= 0) {
            size_t copy_len = (size_t)rc < sizeof(addr_buf) - 1 ? (size_t)rc : sizeof(addr_buf) - 1;
            addr_buf[copy_len] = '\0';
            NVSNAP_INFO("ZMQ monitor event pid=%d sock=%p event=%u value=%u addr=%s",
                       getpid(), ctx->target, event, value, addr_buf);
        }
    }
    free(ctx);
    return NULL;
}

static void nvsnap_zmq_start_monitor(nvsnap_zmq_socket_t *sock) {
    if (!sock) {
        return;
    }
    if (sock->monitor_started) {
        NVSNAP_INFO("ZMQ monitor already started pid=%d sock=%p", getpid(), sock);
        return;
    }
    NVSNAP_INFO("ZMQ monitor start pid=%d sock=%p real=%p type=%d",
               getpid(), sock, sock->real, sock->type);
    if (!real_zmq_socket_monitor) {
        NVSNAP_INFO("ZMQ monitor unavailable (no zmq_socket_monitor) pid=%d sock=%p", getpid(), sock);
        return;
    }
    if (!real_zmq_socket) {
        NVSNAP_INFO("ZMQ monitor unavailable (no zmq_socket) pid=%d sock=%p", getpid(), sock);
        return;
    }
    char endpoint[128];
    snprintf(endpoint, sizeof(endpoint), "inproc://nvsnap-mon-%d-%p", getpid(), (void *)sock);
    if (real_zmq_socket_monitor(sock->real, endpoint, ZMQ_EVENT_ALL) != 0) {
        NVSNAP_WARN("ZMQ monitor enable failed pid=%d sock=%p errno=%d", getpid(), sock, nvsnap_zmq_errno());
        return;
    }
    void *mon = real_zmq_socket(sock->ctx->real, ZMQ_PAIR);
    if (!mon) {
        NVSNAP_WARN("ZMQ monitor socket create failed pid=%d sock=%p errno=%d", getpid(), sock, nvsnap_zmq_errno());
        return;
    }
    if (real_zmq_connect && real_zmq_connect(mon, endpoint) != 0) {
        NVSNAP_WARN("ZMQ monitor connect failed pid=%d sock=%p errno=%d", getpid(), sock, nvsnap_zmq_errno());
        real_zmq_close(mon);
        return;
    }
    nvsnap_zmq_monitor_ctx_t *ctx = (nvsnap_zmq_monitor_ctx_t *)calloc(1, sizeof(*ctx));
    if (!ctx) {
        real_zmq_close(mon);
        return;
    }
    ctx->mon = mon;
    ctx->target = sock;
    sock->monitor_sock = mon;
    sock->monitor_started = 1;
    if (pthread_create(&sock->monitor_thread, NULL, nvsnap_zmq_monitor_thread, ctx) != 0) {
        NVSNAP_WARN("ZMQ monitor thread failed pid=%d sock=%p", getpid(), sock);
        real_zmq_close(mon);
        sock->monitor_sock = NULL;
        sock->monitor_started = 0;
        free(ctx);
        return;
    }
    pthread_detach(sock->monitor_thread);
    NVSNAP_INFO("ZMQ monitor started pid=%d sock=%p endpoint=%s", getpid(), sock, endpoint);
}

static void nvsnap_zmq_apply_restore_sockopts(nvsnap_zmq_socket_t *sock) {
    if (!sock || !sock->real) {
        return;
    }
    if (!nvsnap_zmq_restore_detected()) {
        return;
    }
    if (!real_zmq_setsockopt) {
        return;
    }
    int one = 1;
    if (real_zmq_setsockopt(sock->real, ZMQ_IMMEDIATE, &one, sizeof(one)) != 0) {
        NVSNAP_WARN("ZMQ restore sockopt IMMEDIATE failed errno=%d", nvsnap_zmq_errno());
    }
    if (sock->type == ZMQ_ROUTER) {
        if (real_zmq_setsockopt(sock->real, ZMQ_ROUTER_MANDATORY, &one, sizeof(one)) != 0) {
            NVSNAP_WARN("ZMQ restore sockopt ROUTER_MANDATORY failed errno=%d", nvsnap_zmq_errno());
        }
    }
    if (sock->type == ZMQ_DEALER || sock->type == ZMQ_REQ) {
        if (real_zmq_setsockopt(sock->real, ZMQ_PROBE_ROUTER, &one, sizeof(one)) != 0) {
            NVSNAP_WARN("ZMQ restore sockopt PROBE_ROUTER failed errno=%d", nvsnap_zmq_errno());
        }
    }
}

static int nvsnap_zmq_rebuild_socket(nvsnap_zmq_socket_t *sock, const char *reason) {
    if (!sock || !sock->ctx || sock->closed) {
        return -1;
    }
    if (!real_zmq_socket) {
        return -1;
    }
    unsigned char identity[256];
    size_t identity_len = nvsnap_zmq_capture_identity(sock->real, identity, sizeof(identity));
    pthread_mutex_lock(&sock->ctx->lock);
    if (sock->rebuilding) {
        pthread_mutex_unlock(&sock->ctx->lock);
        return 0;
    }
    sock->rebuilding = 1;
    void *old_sock = sock->real;
    NVSNAP_WARN("ZMQ rebuild socket pid=%d sock=%p real=%p type=%d reason=%s",
               getpid(), sock, old_sock, sock->type, reason ? reason : "unknown");
    sock->real = real_zmq_socket(sock->ctx->real, sock->type);
    if (!sock->real) {
        NVSNAP_WARN("ZMQ rebuild failed: socket create type=%d errno=%d",
                   sock->type, nvsnap_zmq_errno());
        sock->real = old_sock;
        sock->rebuilding = 0;
        pthread_mutex_unlock(&sock->ctx->lock);
        return -1;
    }
    if (identity_len > 0) {
        nvsnap_zmq_apply_identity(sock->real, identity, identity_len);
    }
    nvsnap_zmq_apply_restore_sockopts(sock);
    nvsnap_zmq_start_monitor(sock);
    nvsnap_zmq_replay_ops_ex(sock, 1);
    nvsnap_zmq_force_connect_from_last_endpoint(sock);
    sock->replay_after_restore = 1;
    sock->rebuild_count++;
    if (old_sock && old_sock != sock->real && !nvsnap_zmq_skip_old_close_enabled()) {
        nvsnap_zmq_load_old();
        int (*close_fn)(void *) = old_zmq_close ? old_zmq_close : real_zmq_close;
        if (close_fn && close_fn(old_sock) != 0) {
            NVSNAP_WARN("ZMQ rebuild: closing old socket failed errno=%d", nvsnap_zmq_errno());
        }
    }
    sock->rebuilding = 0;
    pthread_mutex_unlock(&sock->ctx->lock);
    return 0;
}

static void nvsnap_zmq_replay_ops(nvsnap_zmq_socket_t *sock) {
    nvsnap_zmq_replay_ops_ex(sock, 1);
}

static void nvsnap_zmq_count_ops(nvsnap_zmq_socket_t *sock, int *connects, int *binds) {
    if (connects) {
        *connects = 0;
    }
    if (binds) {
        *binds = 0;
    }
    if (!sock) {
        return;
    }
    struct nvsnap_zmq_op *op = sock->ops_head;
    while (op) {
        if (op->type == NVSNAP_ZMQ_OP_CONNECT) {
            if (connects) {
                (*connects)++;
            }
            if (nvsnap_zmq_restore_detected() || nvsnap_zmq_trace_enabled()) {
                NVSNAP_INFO("ZMQ op count: connect endpoint=%s", op->endpoint ? op->endpoint : "(null)");
            }
        } else if (op->type == NVSNAP_ZMQ_OP_BIND) {
            if (binds) {
                (*binds)++;
            }
            if (nvsnap_zmq_restore_detected() || nvsnap_zmq_trace_enabled()) {
                NVSNAP_INFO("ZMQ op count: bind endpoint=%s", op->endpoint ? op->endpoint : "(null)");
            }
        }
        op = op->next;
    }
}

static void nvsnap_zmq_maybe_replay_after_restore(nvsnap_zmq_socket_t *sock) {
    if (!sock || !sock->ctx) {
        return;
    }
    if (!nvsnap_zmq_restore_detected()) {
        return;
    }
    /* Socket replay disabled — CRIU restores socket FDs directly.
     * Replaying (close + reopen + rebind) destroys the CRIU-restored
     * connections and breaks IPC channels. */
    return;
    if (sock->replay_after_restore && !sock->replay_failed) {
        return;
    }
    /* If replay_failed, attempt re-replay once */
    if (sock->replay_failed) {
        NVSNAP_WARN("ZMQ re-replay after previous failure sock=%p real=%p", sock, sock->real);
        sock->replay_failed = 0;  /* Reset to allow one retry */
    }
    sock->replay_after_restore = 1;
    int connects = 0;
    int binds = 0;
    nvsnap_zmq_count_ops(sock, &connects, &binds);
    NVSNAP_INFO("ZMQ replay after restore sock=%p real=%p ops_connect=%d ops_bind=%d",
               sock, sock->real, connects, binds);
    pthread_mutex_lock(&sock->ctx->lock);
    nvsnap_zmq_replay_ops(sock);
    pthread_mutex_unlock(&sock->ctx->lock);
}

/*
 * nvsnap_zmq_reinit_ctx: Full destroy/recreate of ZMQ context after CRIU restore
 *
 * ZMQ contexts contain kernel thread state that cannot be safely restored by CRIU.
 * This function implements a clean-slate approach:
 * 1. Create NEW context with IO threads enabled (required for bind/connect)
 * 2. Create NEW sockets (old sockets have stale kernel state)
 * 3. Replay ALL tracked operations (setsockopt, bind, connect) IMMEDIATELY
 * 4. Clean up old context and sockets (optional, controlled by env vars)
 *
 * CRITICAL FIX (2026-02-01):
 * Previously, binds were deferred until first I/O operation (allow_bind=0).
 * This caused failures because:
 * - With IO_THREADS=0: No threads available for bind, returned EAGAIN
 * - With IO_THREADS=1: Threads not ready yet, timing-dependent failures
 *
 * Solution: Set IO_THREADS=1 by default and replay binds IMMEDIATELY during reinit.
 * This ensures fresh threads are available and endpoints are established before
 * any application I/O operations.
 */
static void nvsnap_zmq_reinit_ctx(nvsnap_zmq_ctx_t *ctx) {
    if (!ctx || ctx->reinit_done || ctx->destroyed) {
        return;
    }
    if (!nvsnap_zmq_restore_detected()) {
        return;
    }
    void *old_ctx = ctx->real;
    void *new_ctx = real_zmq_ctx_new();
    if (!new_ctx) {
        NVSNAP_WARN("ZMQ reinit failed: ctx_new error");
        return;
    }
    int io_threads = nvsnap_zmq_io_threads_override();
    if (io_threads < 0) {
        // Default to 1 IO thread for restore (required for bind/connect)
        io_threads = 1;
    }
    if (real_zmq_ctx_set) {
        if (real_zmq_ctx_set(new_ctx, ZMQ_IO_THREADS, io_threads) == 0) {
            NVSNAP_INFO("ZMQ reinit ctx set io_threads=%d", io_threads);
        } else {
            NVSNAP_WARN("ZMQ reinit ctx set io_threads=%d failed errno=%d",
                       io_threads, nvsnap_zmq_errno());
        }
    }
    NVSNAP_INFO("ZMQ reinit ctx pid=%d ctx=%p old_real=%p new_real=%p force_term=%d",
               getpid(), ctx, old_ctx, new_ctx, nvsnap_zmq_force_terminate_enabled());
    ctx->real = new_ctx;
    nvsnap_zmq_socket_t *sock = ctx->sockets;
    if (!sock) {
        NVSNAP_WARN("ZMQ reinit ctx pid=%d ctx=%p has NO sockets", getpid(), ctx);
    }
    /* Collect old socket pointers so we can close them AFTER context shutdown.
     * zmq_ctx_shutdown() sends ETERM to threads blocked on sockets in the old
     * context, but only if those sockets haven't been zmq_close()'d yet.
     * Previous bug: closing old sockets first removed them from the context's
     * socket list, so shutdown found nothing to signal → threads stayed blocked. */
    void *old_socks[64];
    int old_sock_count = 0;
    while (sock) {
        if (!sock->closed) {
            int connects = 0;
            int binds = 0;
            void *old_sock = sock->real;
            unsigned char identity[256];
            size_t identity_len = nvsnap_zmq_capture_identity(old_sock, identity, sizeof(identity));
            nvsnap_zmq_count_ops(sock, &connects, &binds);
            NVSNAP_INFO("ZMQ reinit socket pid=%d sock=%p real=%p ops_connect=%d ops_bind=%d",
                       getpid(), sock, sock->real, connects, binds);
            sock->real = real_zmq_socket(ctx->real, sock->type);
            if (sock->real) {
                if (identity_len > 0) {
                    nvsnap_zmq_apply_identity(sock->real, identity, identity_len);
                }
                nvsnap_zmq_apply_restore_sockopts(sock);
                /* Monitor start is deferred to after reinit completes —
                 * see nvsnap_zmq_reinit_all_if_restored() below. Starting
                 * monitors here would create ZMQ sockets and threads while
                 * ctx->lock is held, risking deadlock if SIGUSR2 interrupts
                 * the internal ZMQ calls or if callbacks re-enter the lock. */
                // CRITICAL FIX: Enable binds during reinit (allow_bind=1)
                // Previously allowed_bind=0 deferred binds until first I/O,
                // but by then IO threads may not be ready, causing EAGAIN errors.
                // With fresh context + IO threads, we can bind immediately.
                nvsnap_zmq_replay_ops_ex(sock, 1);
                nvsnap_zmq_force_connect_from_last_endpoint(sock);
                sock->replay_after_restore = 1; // Mark as replayed
            } else {
                NVSNAP_WARN("ZMQ reinit failed: socket create type=%d errno=%d", sock->type, nvsnap_zmq_errno());
            }
            /* Save old socket for deferred close (don't close yet!) */
            if (old_sock && old_sock != sock->real && old_sock_count < 64) {
                old_socks[old_sock_count++] = old_sock;
            }
        }
        sock = sock->next;
    }
    /* STEP 1: Shutdown old context FIRST to wake threads blocked in zmq_poll/recv/send.
     * zmq_ctx_shutdown() sends a "stop" command to each socket's owner thread via
     * the socket's mailbox signaler (eventfd). This causes poll() to return on the
     * app thread, and zmq_recv/zmq_msg_recv returns -1 with errno=ETERM (156).
     * CRITICAL: Old sockets must still be in the context's socket list for shutdown
     * to find them and send ETERM. That's why we close them AFTER shutdown. */
    if (old_ctx && old_ctx != ctx->real) {
        nvsnap_zmq_load_old();
        if (old_zmq_ctx_shutdown) {
            NVSNAP_INFO("ZMQ shutdown old ctx=%p to wake blocked threads (ETERM)", old_ctx);
            old_zmq_ctx_shutdown(old_ctx);
        }
    }
    /* STEP 2: Brief delay to let ETERM propagate to blocked threads.
     * After shutdown, threads wake from poll(), process the stop command,
     * and return ETERM. Our intercepted zmq_recv/zmq_msg_recv catches ETERM
     * and retries on the new socket (sock->real already updated above).
     * The gate mechanism (g_zmq_recovering) holds them until we finish. */
    usleep(10000); /* 10ms for ETERM delivery */
    /* STEP 3: Now close old sockets (threads have exited them by now). */
    if (!nvsnap_zmq_skip_old_close_enabled()) {
        nvsnap_zmq_load_old();
        int (*close_fn)(void *) = old_zmq_close ? old_zmq_close : real_zmq_close;
        for (int i = 0; i < old_sock_count; i++) {
            if (close_fn && close_fn(old_socks[i]) != 0) {
                NVSNAP_WARN("ZMQ reinit: closing old socket %p failed errno=%d",
                           old_socks[i], nvsnap_zmq_errno());
            }
        }
    }
    ctx->reinit_done = 1;
    NVSNAP_INFO("ZMQ reinit completed (shutdown-before-close, %d old sockets closed)",
               old_sock_count);
}

void nvsnap_zmq_reinit_all_if_restored(void) {
    if (!nvsnap_zmq_is_enabled()) {
        return;
    }
    if (!nvsnap_zmq_restore_detected()) {
        return;
    }
    /* Context teardown deadlocks: zmq_ctx_destroy waits for IO threads, but
     * IO threads are stuck in epoll_wait on stale fds. Instead, rely on the
     * patched libzmq's epoll reinit (checks /run/criu-restored) + SIGUSR2
     * to wake IO threads from epoll_wait. */
    NVSNAP_INFO("ZMQ restore detected — relying on patched libzmq epoll reinit + SIGUSR2 wake");
    return;
    nvsnap_zmq_log_first_call("reinit_all");
    nvsnap_zmq_set_recovering(1);
    nvsnap_zmq_kill_bg_threads();
    int delay_ms = nvsnap_zmq_recovery_delay_ms();
    if (delay_ms > 0) {
        NVSNAP_INFO("ZMQ recovery delay %d ms", delay_ms);
        usleep((useconds_t)delay_ms * 1000);
    }
    nvsnap_zmq_switch_to_newns_if_needed();
    int ctx_count = 0;
    int sock_count = 0;
    nvsnap_zmq_count_state(&ctx_count, &sock_count);
    NVSNAP_INFO("ZMQ reinit all (pid=%d) starting ctxs=%d socks=%d handle=%p newns_handle=%p",
               getpid(), ctx_count, sock_count, g_zmq_handle, g_zmq_newns_handle);
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    pthread_mutex_lock(&g_zmq_ctx_list_lock);
    int count = 0;
    nvsnap_zmq_ctx_t *ctx = g_zmq_ctx_list;
    while (ctx) {
        pthread_mutex_lock(&ctx->lock);
        nvsnap_zmq_reinit_ctx(ctx);
        nvsnap_zmq_dump_ctx_sockets(ctx, "post_reinit");
        nvsnap_zmq_dump_ctx_sockets_to_file(ctx, "post_reinit");
        pthread_mutex_unlock(&ctx->lock);
        count++;
        ctx = ctx->next;
    }
    pthread_mutex_unlock(&g_zmq_ctx_list_lock);

    /* Start socket monitors AFTER all locks are released. Monitor startup
     * creates ZMQ inproc sockets and threads — doing this under ctx->lock
     * risks deadlock if SIGUSR2 interrupts the internal ZMQ calls or if
     * libzmq callbacks re-enter our wrapper code. */
    ctx = g_zmq_ctx_list;
    while (ctx) {
        nvsnap_zmq_socket_t *sock = ctx->sockets;
        while (sock) {
            if (!sock->closed && sock->real && !sock->monitor_started) {
                nvsnap_zmq_start_monitor(sock);
            }
            sock = sock->next;
        }
        ctx = ctx->next;
    }

    NVSNAP_INFO("ZMQ reinit all (pid=%d) completed ctxs=%d", getpid(), count);
    nvsnap_zmq_set_recovering(0);
}

static void nvsnap_zmq_register_ctx(nvsnap_zmq_ctx_t *ctx) {
    pthread_mutex_lock(&g_zmq_ctx_list_lock);
    ctx->next = g_zmq_ctx_list;
    g_zmq_ctx_list = ctx;
    pthread_mutex_unlock(&g_zmq_ctx_list_lock);
    NVSNAP_INFO("ZMQ ctx register pid=%d ctx=%p real=%p", getpid(), ctx, ctx->real);
}

static void nvsnap_zmq_unregister_ctx(nvsnap_zmq_ctx_t *ctx) {
    pthread_mutex_lock(&g_zmq_ctx_list_lock);
    nvsnap_zmq_ctx_t **iter = &g_zmq_ctx_list;
    while (*iter) {
        if (*iter == ctx) {
            *iter = ctx->next;
            break;
        }
        iter = &(*iter)->next;
    }
    pthread_mutex_unlock(&g_zmq_ctx_list_lock);
    NVSNAP_INFO("ZMQ ctx unregister pid=%d ctx=%p real=%p", getpid(), ctx, ctx ? ctx->real : NULL);
}

static void nvsnap_zmq_maybe_reinit(nvsnap_zmq_ctx_t *ctx) {
    if (!ctx) {
        return;
    }
    if (!nvsnap_zmq_restore_detected()) {
        return;
    }
    /* Context teardown disabled — libzmq's epoll reinit is sufficient.
     * Tearing down contexts breaks async ZMQ (zmq.asyncio) used by SGLang. */
    return;
}

void *zmq_ctx_new(void) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!real_zmq_ctx_new) {
        errno = ENOSYS;
        return NULL;
    }
    if (!nvsnap_zmq_is_enabled()) {
        return real_zmq_ctx_new();
    }
    void *real = real_zmq_ctx_new();
    if (!real) {
        return NULL;
    }
    nvsnap_zmq_ctx_t *ctx = calloc(1, sizeof(*ctx));
    if (!ctx) {
        real_zmq_ctx_term(real);
        return NULL;
    }
    ctx->magic = NVSNAP_ZMQ_CTX_MAGIC;
    ctx->real = real;
    pthread_mutex_init(&ctx->lock, NULL);
    nvsnap_zmq_register_ctx(ctx);
    return ctx;
}

int zmq_ctx_term(void *ctxp) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!real_zmq_ctx_term) {
        errno = ENOSYS;
        return -1;
    }
    if (!nvsnap_zmq_is_enabled()) {
        return real_zmq_ctx_term(ctxp);
    }
    nvsnap_zmq_ctx_t *ctx = nvsnap_zmq_ctx_from_ptr(ctxp);
    if (!ctx) {
        return real_zmq_ctx_term(ctxp);
    }
    ctx->destroyed = 1;
    nvsnap_zmq_unregister_ctx(ctx);
    return real_zmq_ctx_term(ctx->real);
}

int zmq_ctx_destroy(void *ctxp) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!real_zmq_ctx_destroy && real_zmq_ctx_term) {
        real_zmq_ctx_destroy = real_zmq_ctx_term;
    }
    if (!real_zmq_ctx_destroy) {
        errno = ENOSYS;
        return -1;
    }
    if (!nvsnap_zmq_is_enabled()) {
        return real_zmq_ctx_destroy(ctxp);
    }
    nvsnap_zmq_ctx_t *ctx = nvsnap_zmq_ctx_from_ptr(ctxp);
    if (!ctx) {
        return real_zmq_ctx_destroy(ctxp);
    }
    ctx->destroyed = 1;
    nvsnap_zmq_unregister_ctx(ctx);
    return real_zmq_ctx_destroy(ctx->real);
}

int zmq_ctx_shutdown(void *ctxp) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!real_zmq_ctx_shutdown) {
        errno = ENOSYS;
        return -1;
    }
    if (!nvsnap_zmq_is_enabled()) {
        return real_zmq_ctx_shutdown(ctxp);
    }
    nvsnap_zmq_ctx_t *ctx = nvsnap_zmq_ctx_from_ptr(ctxp);
    if (!ctx) {
        return real_zmq_ctx_shutdown(ctxp);
    }
    return real_zmq_ctx_shutdown(ctx->real);
}

int zmq_ctx_set(void *ctxp, int option, int optval) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!nvsnap_zmq_is_enabled()) {
        return real_zmq_ctx_set(ctxp, option, optval);
    }
    nvsnap_zmq_ctx_t *ctx = nvsnap_zmq_ctx_from_ptr(ctxp);
    if (!ctx) {
        return real_zmq_ctx_set(ctxp, option, optval);
    }
    return real_zmq_ctx_set(ctx->real, option, optval);
}

int zmq_ctx_get(void *ctxp, int option) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!nvsnap_zmq_is_enabled()) {
        return real_zmq_ctx_get(ctxp, option);
    }
    nvsnap_zmq_ctx_t *ctx = nvsnap_zmq_ctx_from_ptr(ctxp);
    if (!ctx) {
        return real_zmq_ctx_get(ctxp, option);
    }
    return real_zmq_ctx_get(ctx->real, option);
}

void *zmq_socket(void *ctxp, int type) {
    nvsnap_zmq_log_first_call("zmq_socket");
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!nvsnap_zmq_is_enabled()) {
        return real_zmq_socket(ctxp, type);
    }
    nvsnap_zmq_ctx_t *ctx = nvsnap_zmq_ctx_from_ptr(ctxp);
    if (!ctx) {
        return real_zmq_socket(ctxp, type);
    }
    nvsnap_zmq_maybe_reinit(ctx);
    void *real = real_zmq_socket(ctx->real, type);
    if (!real) {
        return NULL;
    }
    nvsnap_zmq_socket_t *sock = calloc(1, sizeof(*sock));
    if (!sock) {
        real_zmq_close(real);
        return NULL;
    }
    sock->magic = NVSNAP_ZMQ_SOCK_MAGIC;
    sock->ctx = ctx;
    sock->real = real;
    sock->type = type;
    nvsnap_zmq_apply_restore_sockopts(sock);
    if (nvsnap_zmq_restore_detected()) {
        nvsnap_zmq_start_monitor(sock);
    }
    pthread_mutex_lock(&ctx->lock);
    sock->next = ctx->sockets;
    ctx->sockets = sock;
    pthread_mutex_unlock(&ctx->lock);
    nvsnap_zmq_dump_ctx_sockets_to_file(ctx, "socket_create");
    if (nvsnap_zmq_trace_enabled()) {
        NVSNAP_INFO("ZMQ socket pid=%d sock=%p real=%p type=%d", getpid(), sock, sock->real, type);
    }
    return sock;
}

int zmq_close(void *sockp) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!nvsnap_zmq_is_enabled()) {
        return real_zmq_close(sockp);
    }
    nvsnap_zmq_socket_t *sock = nvsnap_zmq_sock_from_ptr(sockp);
    if (!sock) {
        return real_zmq_close(sockp);
    }
    sock->closed = 1;
    return real_zmq_close(sock->real);
}

int zmq_bind(void *sockp, const char *endpoint) {
    nvsnap_zmq_log_first_call("zmq_bind");
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!nvsnap_zmq_is_enabled()) {
        return real_zmq_bind(sockp, endpoint);
    }
    nvsnap_zmq_socket_t *sock = nvsnap_zmq_sock_from_ptr(sockp);
    if (!sock) {
        return real_zmq_bind(sockp, endpoint);
    }
    nvsnap_zmq_maybe_reinit(sock->ctx);
    nvsnap_zmq_add_op(sock, NVSNAP_ZMQ_OP_BIND, 0, NULL, 0, endpoint);
    int rc = real_zmq_bind(sock->real, endpoint);
    if (nvsnap_zmq_trace_enabled()) {
        NVSNAP_INFO("ZMQ bind pid=%d sock=%p real=%p endpoint=%s rc=%d errno=%d",
                   getpid(), sockp, sock->real, endpoint ? endpoint : "(null)", rc, nvsnap_zmq_errno());
    }
    return rc;
}

int zmq_unbind(void *sockp, const char *endpoint) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!nvsnap_zmq_is_enabled()) {
        return real_zmq_unbind(sockp, endpoint);
    }
    nvsnap_zmq_socket_t *sock = nvsnap_zmq_sock_from_ptr(sockp);
    if (!sock) {
        return real_zmq_unbind(sockp, endpoint);
    }
    nvsnap_zmq_add_op(sock, NVSNAP_ZMQ_OP_UNBIND, 0, NULL, 0, endpoint);
    return real_zmq_unbind(sock->real, endpoint);
}

int zmq_connect(void *sockp, const char *endpoint) {
    nvsnap_zmq_log_first_call("zmq_connect");
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!nvsnap_zmq_is_enabled()) {
        return real_zmq_connect(sockp, endpoint);
    }
    nvsnap_zmq_socket_t *sock = nvsnap_zmq_sock_from_ptr(sockp);
    if (!sock) {
        return real_zmq_connect(sockp, endpoint);
    }
    nvsnap_zmq_maybe_reinit(sock->ctx);
    nvsnap_zmq_add_op(sock, NVSNAP_ZMQ_OP_CONNECT, 0, NULL, 0, endpoint);
    int rc = real_zmq_connect(sock->real, endpoint);
    if (nvsnap_zmq_trace_enabled() || nvsnap_zmq_restore_detected()) {
        NVSNAP_INFO("ZMQ connect pid=%d sock=%p real=%p endpoint=%s rc=%d errno=%d",
                   getpid(), sockp, sock->real, endpoint ? endpoint : "(null)", rc, nvsnap_zmq_errno());
    }
    return rc;
}

int zmq_disconnect(void *sockp, const char *endpoint) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!nvsnap_zmq_is_enabled()) {
        return real_zmq_disconnect(sockp, endpoint);
    }
    nvsnap_zmq_socket_t *sock = nvsnap_zmq_sock_from_ptr(sockp);
    if (!sock) {
        return real_zmq_disconnect(sockp, endpoint);
    }
    nvsnap_zmq_add_op(sock, NVSNAP_ZMQ_OP_DISCONNECT, 0, NULL, 0, endpoint);
    return real_zmq_disconnect(sock->real, endpoint);
}

int zmq_setsockopt(void *sockp, int option, const void *optval, size_t optvallen) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!nvsnap_zmq_is_enabled()) {
        return real_zmq_setsockopt(sockp, option, optval, optvallen);
    }
    nvsnap_zmq_socket_t *sock = nvsnap_zmq_sock_from_ptr(sockp);
    if (!sock) {
        return real_zmq_setsockopt(sockp, option, optval, optvallen);
    }
    nvsnap_zmq_add_op(sock, NVSNAP_ZMQ_OP_SETSOCKOPT, option, optval, optvallen, NULL);
    return real_zmq_setsockopt(sock->real, option, optval, optvallen);
}

int zmq_getsockopt(void *sockp, int option, void *optval, size_t *optvallen) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!nvsnap_zmq_is_enabled()) {
        return real_zmq_getsockopt(sockp, option, optval, optvallen);
    }
    nvsnap_zmq_socket_t *sock = nvsnap_zmq_sock_from_ptr(sockp);
    if (!sock) {
        return real_zmq_getsockopt(sockp, option, optval, optvallen);
    }
    return real_zmq_getsockopt(sock->real, option, optval, optvallen);
}

static void nvsnap_zmq_log_sock_state(const char *tag, nvsnap_zmq_socket_t *sock) {
    if (!sock || !sock->real) {
        return;
    }
    int type = 0;
    int events = 0;
    size_t type_len = sizeof(type);
    size_t events_len = sizeof(events);
    char endpoint[256];
    endpoint[0] = '\0';
    size_t endpoint_len = sizeof(endpoint);
    if (real_zmq_getsockopt) {
        (void)real_zmq_getsockopt(sock->real, ZMQ_TYPE, &type, &type_len);
        (void)real_zmq_getsockopt(sock->real, ZMQ_EVENTS, &events, &events_len);
        if (real_zmq_getsockopt(sock->real, ZMQ_LAST_ENDPOINT, endpoint, &endpoint_len) == 0) {
            endpoint[sizeof(endpoint) - 1] = '\0';
        } else {
            endpoint[0] = '\0';
        }
    }
    NVSNAP_INFO("ZMQ state %s pid=%d sock=%p real=%p type=%d events=0x%x endpoint=%s",
               tag ? tag : "unknown", getpid(), sock, sock->real, type, events,
               endpoint[0] ? endpoint : "(none)");
}

int zmq_send(void *sockp, const void *buf, size_t len, int flags) {
    /* ALWAYS LOG: Verify library is loaded */
    NVSNAP_TRACE("zmq_send entry pid=%d sock=%p len=%zu flags=0x%x", getpid(), sockp, len, flags);

    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!nvsnap_zmq_is_enabled()) {
        NVSNAP_TRACE("zmq_send disabled, passthrough pid=%d", getpid());
        return real_zmq_send(sockp, buf, len, flags);
    }
    if (nvsnap_zmq_trace_enabled()) {
        NVSNAP_INFO("ZMQ send entry pid=%d sock=%p len=%zu flags=0x%x",
                   getpid(), sockp, len, flags);
    }
    nvsnap_zmq_socket_t *sock = nvsnap_zmq_sock_from_ptr(sockp);
    if (!sock) {
        NVSNAP_WARN("ZMQ send untracked sock pid=%d sock=%p len=%zu flags=0x%x",
                   getpid(), sockp, len, flags);
        return real_zmq_send(sockp, buf, len, flags);
    }
    nvsnap_zmq_maybe_reinit(sock->ctx);
    nvsnap_zmq_maybe_replay_after_restore(sock);
    nvsnap_zmq_gate_if_recovering("send");
    int rc = real_zmq_send(sock->real, buf, len, flags);
    int send_err = nvsnap_zmq_errno();
    /* After restore, SIGUSR2 causes EINTR or old context shutdown causes ETERM.
     * Re-resolve sock->real (now points to new socket) and retry once. */
    if (rc < 0 && (send_err == EINTR || send_err == 156 /* ETERM */)
        && nvsnap_zmq_restore_detected()) {
        NVSNAP_INFO("ZMQ send got %s pid=%d sock=%p - switching to new socket",
                   send_err == EINTR ? "EINTR" : "ETERM", getpid(), sockp);
        nvsnap_zmq_maybe_reinit(sock->ctx);
        nvsnap_zmq_maybe_replay_after_restore(sock);
        nvsnap_zmq_gate_if_recovering("send_restore_retry");
        rc = real_zmq_send(sock->real, buf, len, flags);
    }
    if (rc < 0 && nvsnap_zmq_errno() == EHOSTUNREACH && nvsnap_zmq_restore_detected()) {
        NVSNAP_WARN("ZMQ send EHOSTUNREACH pid=%d sock=%p real=%p", getpid(), sock, sock->real);
        nvsnap_zmq_force_reconnect(sock);
        int max_ms = nvsnap_zmq_ehostunreach_max_ms();
        int sleep_ms = nvsnap_zmq_ehostunreach_sleep_ms();
        int waited = 0;
        while (max_ms > 0 && waited < max_ms) {
            usleep((useconds_t)sleep_ms * 1000);
            waited += sleep_ms;
            rc = real_zmq_send(sock->real, buf, len, flags);
            if (rc >= 0 || nvsnap_zmq_errno() != EHOSTUNREACH) {
                NVSNAP_WARN("ZMQ send retry rc=%d errno=%d waited_ms=%d", rc, nvsnap_zmq_errno(), waited);
                break;
            }
        }
        if (rc < 0 && nvsnap_zmq_errno() == EHOSTUNREACH) {
            if (nvsnap_zmq_rebuild_socket(sock, "send_ehostunreach") == 0) {
                nvsnap_zmq_force_reconnect(sock);
                rc = real_zmq_send(sock->real, buf, len, flags);
                NVSNAP_WARN("ZMQ send after rebuild rc=%d errno=%d", rc, nvsnap_zmq_errno());
            }
        }
    }
    if (rc < 0 && nvsnap_zmq_errno() == EAGAIN) {
        nvsnap_zmq_log_sock_state("send_eagain", sock);
        nvsnap_zmq_maybe_replay_after_restore(sock);
        rc = real_zmq_send(sock->real, buf, len, flags);
    }
    if (nvsnap_zmq_trace_enabled()) {
        NVSNAP_INFO("ZMQ send sock=%p real=%p len=%zu flags=0x%x rc=%d errno=%d",
                   sockp, sock->real, len, flags, rc, nvsnap_zmq_errno());
    }
    return rc;
}

int zmq_recv(void *sockp, void *buf, size_t len, int flags) {
    /* ALWAYS LOG: Verify library is loaded */
    NVSNAP_TRACE("zmq_recv entry pid=%d sock=%p len=%zu flags=0x%x", getpid(), sockp, len, flags);

    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!nvsnap_zmq_is_enabled()) {
        NVSNAP_TRACE("zmq_recv disabled, passthrough pid=%d", getpid());
        return real_zmq_recv(sockp, buf, len, flags);
    }
    if (nvsnap_zmq_trace_enabled()) {
        NVSNAP_INFO("ZMQ recv entry pid=%d sock=%p len=%zu flags=0x%x",
                   getpid(), sockp, len, flags);
    }
    nvsnap_zmq_socket_t *sock = nvsnap_zmq_sock_from_ptr(sockp);
    if (!sock) {
        NVSNAP_WARN("ZMQ recv untracked sock pid=%d sock=%p len=%zu flags=0x%x",
                   getpid(), sockp, len, flags);
        return real_zmq_recv(sockp, buf, len, flags);
    }
    nvsnap_zmq_maybe_reinit(sock->ctx);
    nvsnap_zmq_maybe_replay_after_restore(sock);
    nvsnap_zmq_gate_if_recovering("recv");
    int rc = real_zmq_recv(sock->real, buf, len, flags);
    int recv_err = nvsnap_zmq_errno();
    /* After restore, SIGUSR2 interrupts blocked recv with EINTR, or old
     * context shutdown delivers ETERM. Either way, re-resolve sock->real
     * (now points to new socket after reinit) and retry on the new socket. */
    if (rc < 0 && (recv_err == EINTR || recv_err == 156 /* ETERM */)
        && nvsnap_zmq_restore_detected()) {
        NVSNAP_INFO("ZMQ recv got %s pid=%d sock=%p - switching to new socket",
                   recv_err == EINTR ? "EINTR" : "ETERM", getpid(), sockp);
        nvsnap_zmq_maybe_reinit(sock->ctx);
        nvsnap_zmq_maybe_replay_after_restore(sock);
        nvsnap_zmq_gate_if_recovering("recv_restore_retry");
        rc = real_zmq_recv(sock->real, buf, len, flags);
        recv_err = nvsnap_zmq_errno();
    }
    if (rc < 0 && recv_err == EAGAIN) {
        nvsnap_zmq_log_sock_state("recv_eagain", sock);
        nvsnap_zmq_maybe_replay_after_restore(sock);
        rc = real_zmq_recv(sock->real, buf, len, flags);
    }
    if (nvsnap_zmq_trace_enabled()) {
        NVSNAP_INFO("ZMQ recv sock=%p real=%p len=%zu flags=0x%x rc=%d errno=%d",
                   sockp, sock->real, len, flags, rc, nvsnap_zmq_errno());
    }
    return rc;
}

int zmq_msg_send(zmq_msg_t *msg, void *sockp, int flags) {
    /* ALWAYS LOG: Verify library is loaded */
    NVSNAP_TRACE("zmq_msg_send entry pid=%d sock=%p flags=0x%x", getpid(), sockp, flags);

    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!nvsnap_zmq_is_enabled()) {
        NVSNAP_TRACE("zmq_msg_send disabled, passthrough pid=%d", getpid());
        return real_zmq_msg_send(msg, sockp, flags);
    }
    if (nvsnap_zmq_trace_enabled()) {
        NVSNAP_INFO("ZMQ msg_send entry pid=%d sock=%p flags=0x%x",
                   getpid(), sockp, flags);
    }
    nvsnap_zmq_socket_t *sock = nvsnap_zmq_sock_from_ptr(sockp);
    if (!sock) {
        NVSNAP_WARN("ZMQ msg_send untracked sock pid=%d sock=%p flags=0x%x",
                   getpid(), sockp, flags);
        return real_zmq_msg_send(msg, sockp, flags);
    }
    if (nvsnap_zmq_restore_detected() && nvsnap_zmq_rebuild_on_first_send_enabled() &&
        sock->rebuild_count == 0) {
        (void)nvsnap_zmq_rebuild_socket(sock, "first_send_after_restore");
    }
    nvsnap_zmq_maybe_reinit(sock->ctx);
    nvsnap_zmq_maybe_replay_after_restore(sock);
    nvsnap_zmq_gate_if_recovering("msg_send");
    int rc = real_zmq_msg_send(msg, sock->real, flags);
    int msg_send_err = nvsnap_zmq_errno();
    /* After restore, SIGUSR2 causes EINTR or old context shutdown causes ETERM.
     * Re-resolve sock->real (now points to new socket) and retry once. */
    if (rc < 0 && (msg_send_err == EINTR || msg_send_err == 156 /* ETERM */)
        && nvsnap_zmq_restore_detected()) {
        NVSNAP_INFO("ZMQ msg_send got %s pid=%d sock=%p - switching to new socket",
                   msg_send_err == EINTR ? "EINTR" : "ETERM", getpid(), sockp);
        nvsnap_zmq_maybe_reinit(sock->ctx);
        nvsnap_zmq_maybe_replay_after_restore(sock);
        nvsnap_zmq_gate_if_recovering("msg_send_restore_retry");
        rc = real_zmq_msg_send(msg, sock->real, flags);
    }

    /* DETAILED EINVAL LOGGING */
    if (rc < 0 && nvsnap_zmq_errno() == 22) {  /* EINVAL */
        NVSNAP_WARN("!!! zmq_msg_send EINVAL pid=%d sock=%p real=%p rc=%d",
                   getpid(), sockp, sock->real, rc);
        nvsnap_zmq_log_sock_state("msg_send_EINVAL", sock);

        /* Check socket options */
        int type = 0, events = 0;
        size_t sz = sizeof(type);
        if (real_zmq_getsockopt && real_zmq_getsockopt(sock->real, ZMQ_TYPE, &type, &sz) == 0) {
            NVSNAP_WARN("!!! Socket type=%d", type);
        }
        sz = sizeof(events);
        if (real_zmq_getsockopt && real_zmq_getsockopt(sock->real, ZMQ_EVENTS, &events, &sz) == 0) {
            NVSNAP_WARN("!!! Socket events=0x%x", events);
        }

        /* Try to get more error context */
        NVSNAP_WARN("!!! Attempting replay_after_restore due to EINVAL");
        nvsnap_zmq_maybe_replay_after_restore(sock);
        rc = real_zmq_msg_send(msg, sock->real, flags);
        NVSNAP_WARN("!!! After replay: rc=%d errno=%d", rc, nvsnap_zmq_errno());

        if (rc < 0 && nvsnap_zmq_restore_detected() && nvsnap_zmq_rebuild_on_einval_enabled()) {
            if (nvsnap_zmq_rebuild_socket(sock, "msg_send_einval") == 0) {
                rc = real_zmq_msg_send(msg, sock->real, flags);
                NVSNAP_WARN("!!! After rebuild: rc=%d errno=%d", rc, nvsnap_zmq_errno());
            }
        }
    } else if (rc < 0 && nvsnap_zmq_errno() == EHOSTUNREACH && nvsnap_zmq_restore_detected()) {
        NVSNAP_WARN("!!! zmq_msg_send EHOSTUNREACH pid=%d sock=%p real=%p",
                   getpid(), sockp, sock->real);
        nvsnap_zmq_force_reconnect(sock);
        int max_ms = nvsnap_zmq_ehostunreach_max_ms();
        int sleep_ms = nvsnap_zmq_ehostunreach_sleep_ms();
        int waited = 0;
        while (max_ms > 0 && waited < max_ms) {
            usleep((useconds_t)sleep_ms * 1000);
            waited += sleep_ms;
            rc = real_zmq_msg_send(msg, sock->real, flags);
            if (rc >= 0 || nvsnap_zmq_errno() != EHOSTUNREACH) {
                NVSNAP_WARN("!!! msg_send retry rc=%d errno=%d waited_ms=%d",
                           rc, nvsnap_zmq_errno(), waited);
                break;
            }
        }
        if (rc < 0 && nvsnap_zmq_errno() == EHOSTUNREACH) {
            if (nvsnap_zmq_rebuild_socket(sock, "msg_send_ehostunreach") == 0) {
                nvsnap_zmq_force_reconnect(sock);
                rc = real_zmq_msg_send(msg, sock->real, flags);
                NVSNAP_WARN("!!! After rebuild: rc=%d errno=%d", rc, nvsnap_zmq_errno());
            }
        }
    } else if (rc < 0 && nvsnap_zmq_errno() == EAGAIN) {
        nvsnap_zmq_log_sock_state("msg_send_eagain", sock);
        nvsnap_zmq_maybe_replay_after_restore(sock);
        rc = real_zmq_msg_send(msg, sock->real, flags);
    }
    if (rc >= 0 && nvsnap_zmq_errno() == EAGAIN) {
        nvsnap_zmq_log_sock_state("msg_send_errno_eagain", sock);
    }
    if (nvsnap_zmq_trace_enabled()) {
        NVSNAP_INFO("ZMQ msg_send pid=%d sock=%p real=%p flags=0x%x rc=%d errno=%d",
                   getpid(), sockp, sock->real, flags, rc, nvsnap_zmq_errno());
    }
    return rc;
}

int zmq_msg_recv(zmq_msg_t *msg, void *sockp, int flags) {
    /* ALWAYS LOG: Verify library is loaded */
    NVSNAP_TRACE("zmq_msg_recv entry pid=%d sock=%p flags=0x%x", getpid(), sockp, flags);

    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!nvsnap_zmq_is_enabled()) {
        NVSNAP_TRACE("zmq_msg_recv disabled, passthrough pid=%d", getpid());
        return real_zmq_msg_recv(msg, sockp, flags);
    }
    if (nvsnap_zmq_trace_enabled()) {
        NVSNAP_INFO("ZMQ msg_recv entry pid=%d sock=%p flags=0x%x",
                   getpid(), sockp, flags);
    }
    nvsnap_zmq_socket_t *sock = nvsnap_zmq_sock_from_ptr(sockp);
    if (!sock) {
        NVSNAP_WARN("ZMQ msg_recv untracked sock pid=%d sock=%p flags=0x%x",
                   getpid(), sockp, flags);
        return real_zmq_msg_recv(msg, sockp, flags);
    }
    nvsnap_zmq_maybe_reinit(sock->ctx);
    nvsnap_zmq_maybe_replay_after_restore(sock);
    nvsnap_zmq_gate_if_recovering("msg_recv");
    int rc = real_zmq_msg_recv(msg, sock->real, flags);
    int msg_recv_err = nvsnap_zmq_errno();
    /* After restore, SIGUSR2 interrupts blocked msg_recv with EINTR, or old
     * context shutdown delivers ETERM. Either way, re-resolve sock->real
     * (now points to new socket after reinit) and retry on the new socket. */
    if (rc < 0 && (msg_recv_err == EINTR || msg_recv_err == 156 /* ETERM */)
        && nvsnap_zmq_restore_detected()) {
        NVSNAP_INFO("ZMQ msg_recv got %s pid=%d sock=%p - switching to new socket",
                   msg_recv_err == EINTR ? "EINTR" : "ETERM", getpid(), sockp);
        nvsnap_zmq_maybe_reinit(sock->ctx);
        nvsnap_zmq_maybe_replay_after_restore(sock);
        nvsnap_zmq_gate_if_recovering("msg_recv_restore_retry");
        rc = real_zmq_msg_recv(msg, sock->real, flags);
        msg_recv_err = nvsnap_zmq_errno();
    }
    if (rc < 0 && msg_recv_err == EAGAIN) {
        nvsnap_zmq_log_sock_state("msg_recv_eagain", sock);
        nvsnap_zmq_maybe_replay_after_restore(sock);
        rc = real_zmq_msg_recv(msg, sock->real, flags);
    }
    if (nvsnap_zmq_trace_enabled()) {
        NVSNAP_INFO("ZMQ msg_recv pid=%d sock=%p real=%p flags=0x%x rc=%d errno=%d",
                   getpid(), sockp, sock->real, flags, rc, nvsnap_zmq_errno());
    }
    return rc;
}

int zmq_msg_init(zmq_msg_t *msg) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    return real_zmq_msg_init(msg);
}

int zmq_msg_init_size(zmq_msg_t *msg, size_t size) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    return real_zmq_msg_init_size(msg, size);
}

int zmq_msg_init_data(zmq_msg_t *msg, void *data, size_t size, void (*ffn)(void *, void *), void *hint) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    return real_zmq_msg_init_data(msg, data, size, ffn, hint);
}

int zmq_msg_close(zmq_msg_t *msg) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    return real_zmq_msg_close(msg);
}

static pthread_once_t g_zmq_poll_first_call = PTHREAD_ONCE_INIT;
static void nvsnap_zmq_poll_first_call_reinit(void) {
    int restore_detected = nvsnap_zmq_restore_detected();
    NVSNAP_WARN("ZMQ poll first-call check pid=%d restore_detected=%d", getpid(), restore_detected);
    if (restore_detected) {
        NVSNAP_WARN("ZMQ poll on first call post-restore pid=%d - forcing global reinit", getpid());
        nvsnap_zmq_reinit_all_if_restored();
    }
}

int zmq_poll(zmq_pollitem_t *items, int nitems, long timeout) {
    NVSNAP_TRACE("zmq_poll entry pid=%d nitems=%d", getpid(), nitems);

    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);

    if (!nvsnap_zmq_is_enabled()) {
        NVSNAP_TRACE("zmq_poll disabled, passthrough pid=%d", getpid());
        return real_zmq_poll(items, nitems, timeout);
    }

    NVSNAP_TRACE("zmq_poll enabled, first-call check pid=%d", getpid());
    /* CRITICAL: Check for restore on first poll call to catch early polls before library init */
    pthread_once(&g_zmq_poll_first_call, nvsnap_zmq_poll_first_call_reinit);

    nvsnap_zmq_socket_t **wrapped = NULL;
    const int log_poll = (nvsnap_zmq_trace_enabled() || nvsnap_zmq_restore_detected());
    if (log_poll && nitems > 0) {
        wrapped = calloc((size_t)nitems, sizeof(*wrapped));
        NVSNAP_INFO("ZMQ poll entry pid=%d nitems=%d timeout=%ld restore_detected=%d",
                   getpid(), nitems, timeout, nvsnap_zmq_restore_detected());
    }
    int wrapped_count = 0;
    int unwrapped_count = 0;
    for (int i = 0; i < nitems; i++) {
        nvsnap_zmq_socket_t *sock = nvsnap_zmq_sock_from_ptr(items[i].socket);
        if (sock) {
            wrapped_count++;
            nvsnap_zmq_maybe_reinit(sock->ctx);
            nvsnap_zmq_maybe_replay_after_restore(sock);
            if (wrapped) {
                wrapped[i] = sock;
            }
            items[i].socket = sock->real;
        } else {
            unwrapped_count++;
            if (log_poll) {
                NVSNAP_WARN("ZMQ poll pid=%d item[%d] socket=%p NOT WRAPPED (magic check failed)",
                           getpid(), i, items[i].socket);
            }
        }
    }
    if (log_poll && unwrapped_count > 0) {
        NVSNAP_WARN("ZMQ poll pid=%d has %d unwrapped sockets out of %d total - triggering global reinit as fallback",
                   getpid(), unwrapped_count, nitems);
        /* Fallback: if we have unwrapped sockets and we're post-restore, force global reinit */
        if (nvsnap_zmq_restore_detected()) {
            nvsnap_zmq_reinit_all_if_restored();
        }
    }
    nvsnap_zmq_gate_if_recovering("poll");
    int rc = real_zmq_poll(items, nitems, timeout);
    int err = nvsnap_zmq_errno();
    /* After restore, old context shutdown causes ETERM (156) on blocked poll.
     * Re-resolve socket pointers from wrappers (now point to new sockets) and retry. */
    if (rc < 0 && nvsnap_zmq_restore_detected() &&
        (err == EINTR || err == EAGAIN || err == 156 /* ETERM */)) {
        if (err == 156) {
            NVSNAP_INFO("ZMQ poll got ETERM pid=%d - old ctx shutdown, re-resolving to new sockets",
                       getpid());
            /* Re-resolve socket pointers: wrappers now point to new (reinited) sockets */
            for (int i = 0; i < nitems; i++) {
                nvsnap_zmq_socket_t *sock = wrapped ? wrapped[i] : nvsnap_zmq_sock_from_ptr(items[i].socket);
                if (sock) {
                    nvsnap_zmq_maybe_reinit(sock->ctx);
                    nvsnap_zmq_maybe_replay_after_restore(sock);
                    items[i].socket = sock->real;  /* Now points to NEW socket */
                    if (wrapped) wrapped[i] = sock;
                }
            }
            nvsnap_zmq_gate_if_recovering("poll_eterm_retry");
        }
        for (int retry = 0; retry < 3 && rc < 0 &&
             (err == EINTR || err == EAGAIN || err == 156); retry++) {
            if (log_poll) {
                NVSNAP_INFO("ZMQ poll retry pid=%d attempt=%d errno=%d",
                           getpid(), retry + 1, err);
            }
            rc = real_zmq_poll(items, nitems, timeout);
            err = nvsnap_zmq_errno();
        }
    }
    if (log_poll) {
        NVSNAP_INFO("ZMQ poll pid=%d nitems=%d timeout=%ld rc=%d errno=%d",
                   getpid(), nitems, timeout, rc, err);
    }
    if (wrapped && rc <= 0) {
        for (int i = 0; i < nitems; i++) {
            if (!wrapped[i]) {
                continue;
            }
            NVSNAP_INFO("ZMQ poll item pid=%d idx=%d events=0x%x revents=0x%x",
                       getpid(), i, items[i].events, items[i].revents);
            nvsnap_zmq_log_sock_state("poll_rc_le_0", wrapped[i]);
        }
    }
    if (wrapped) {
        free(wrapped);
    }
    return rc;
}

int zmq_proxy(void *frontend, void *backend, void *capture) {
    pthread_once(&g_zmq_once, nvsnap_zmq_load_real);
    if (!nvsnap_zmq_is_enabled()) {
        return real_zmq_proxy(frontend, backend, capture);
    }
    nvsnap_zmq_socket_t *front = nvsnap_zmq_sock_from_ptr(frontend);
    nvsnap_zmq_socket_t *back = nvsnap_zmq_sock_from_ptr(backend);
    nvsnap_zmq_socket_t *cap = nvsnap_zmq_sock_from_ptr(capture);
    return real_zmq_proxy(front ? front->real : frontend,
                          back ? back->real : backend,
                          cap ? cap->real : capture);
}

/*
 * =============================================================================
 * ZMQ CHECKPOINT/RESTORE SUPPORT
 * =============================================================================
 */

/* New ZMQ checkpoint API function pointers */
typedef int (*zmq_get_all_contexts_fn_t)(void ***contexts, int *count);
typedef int (*zmq_ctx_checkpoint_fn_t)(void *context, void **checkpoint, int flags);
typedef int (*zmq_ctx_restore_fn_t)(void *checkpoint, void **context, int flags);
typedef int (*zmq_checkpoint_destroy_fn_t)(void *checkpoint);

static zmq_get_all_contexts_fn_t real_zmq_get_all_contexts = NULL;
static zmq_ctx_checkpoint_fn_t real_zmq_ctx_checkpoint = NULL;
static zmq_ctx_restore_fn_t real_zmq_ctx_restore = NULL;
static zmq_checkpoint_destroy_fn_t real_zmq_checkpoint_destroy = NULL;

static pthread_once_t g_zmq_ckpt_once = PTHREAD_ONCE_INIT;
static void **g_saved_checkpoints = NULL;
static int g_num_checkpoints = 0;

#define ZMQ_CKPT_FILE "/var/run/nvsnap/zmq-ckpt.dat"

/**
 * Load ZMQ checkpoint API symbols
 */
static void nvsnap_zmq_load_checkpoint_api(void) {
    if (!g_zmq_handle) {
        return; /* libzmq not loaded */
    }

    real_zmq_get_all_contexts = dlsym(g_zmq_handle, "zmq_get_all_contexts");
    real_zmq_ctx_checkpoint = dlsym(g_zmq_handle, "zmq_ctx_checkpoint");
    real_zmq_ctx_restore = dlsym(g_zmq_handle, "zmq_ctx_restore");
    real_zmq_checkpoint_destroy = dlsym(g_zmq_handle, "zmq_checkpoint_destroy");

    if (real_zmq_ctx_checkpoint && real_zmq_ctx_restore) {
        NVSNAP_INFO("ZMQ checkpoint API available");
    } else {
        NVSNAP_WARN("ZMQ checkpoint API not available - using standard libzmq");
    }
}

/**
 * Checkpoint all ZMQ contexts (called on SIGUSR1)
 */
void nvsnap_zmq_checkpoint(void) {
    pthread_once(&g_zmq_ckpt_once, nvsnap_zmq_load_checkpoint_api);

    if (!real_zmq_get_all_contexts || !real_zmq_ctx_checkpoint) {
        NVSNAP_WARN("ZMQ checkpoint API not available");
        return;
    }

    void **contexts = NULL;
    int count = 0;

    /* Get all ZMQ contexts */
    int rc = real_zmq_get_all_contexts(&contexts, &count);
    if (rc != 0) {
        NVSNAP_ERROR("zmq_get_all_contexts failed: %d", rc);
        return;
    }

    NVSNAP_INFO("ZMQ checkpoint: found %d contexts", count);

    if (count == 0) {
        free(contexts);
        return;
    }

    /* Allocate checkpoint array */
    g_saved_checkpoints = malloc(sizeof(void*) * count);
    if (!g_saved_checkpoints) {
        NVSNAP_ERROR("Out of memory for checkpoints");
        free(contexts);
        return;
    }

    /* Checkpoint each context */
    for (int i = 0; i < count; i++) {
        void *checkpoint = NULL;
        rc = real_zmq_ctx_checkpoint(contexts[i], &checkpoint, 0);
        if (rc != 0) {
            NVSNAP_ERROR("zmq_ctx_checkpoint failed for context %d: %d", i, rc);
            continue;
        }

        g_saved_checkpoints[i] = checkpoint;
        g_num_checkpoints++;

        NVSNAP_INFO("ZMQ checkpoint: saved context %d/%d (ctx=%p ckpt=%p)",
                   i + 1, count, contexts[i], checkpoint);
    }

    free(contexts);

    /* Save checkpoints to file for CRIU plugin */
    FILE *fp = fopen(ZMQ_CKPT_FILE, "w");
    if (fp) {
        fprintf(fp, "%d\n", g_num_checkpoints);
        for (int i = 0; i < g_num_checkpoints; i++) {
            fprintf(fp, "%p\n", g_saved_checkpoints[i]);
        }
        fclose(fp);
        NVSNAP_INFO("ZMQ checkpoint: saved %d checkpoints to %s",
                   g_num_checkpoints, ZMQ_CKPT_FILE);
    } else {
        NVSNAP_ERROR("Failed to save checkpoints to %s", ZMQ_CKPT_FILE);
    }
}

/**
 * Restore ZMQ contexts (called on restore detection)
 */
void nvsnap_zmq_restore(void) {
    pthread_once(&g_zmq_ckpt_once, nvsnap_zmq_load_checkpoint_api);

    if (!real_zmq_ctx_restore) {
        NVSNAP_WARN("ZMQ restore API not available");
        return;
    }

    /* Load checkpoints from file */
    FILE *fp = fopen(ZMQ_CKPT_FILE, "r");
    if (!fp) {
        NVSNAP_DEBUG("No ZMQ checkpoint file found (normal for non-ZMQ apps)");
        return;
    }

    int count = 0;
    if (fscanf(fp, "%d\n", &count) != 1) {
        NVSNAP_ERROR("Failed to read checkpoint count");
        fclose(fp);
        return;
    }

    NVSNAP_INFO("ZMQ restore: restoring %d contexts", count);

    for (int i = 0; i < count; i++) {
        void *checkpoint = NULL;
        if (fscanf(fp, "%p\n", &checkpoint) != 1) {
            NVSNAP_ERROR("Failed to read checkpoint %d", i);
            continue;
        }

        void *new_ctx = NULL;
        int rc = real_zmq_ctx_restore(checkpoint, &new_ctx, 0);
        if (rc != 0) {
            NVSNAP_ERROR("zmq_ctx_restore failed for context %d: %d", i, rc);
            continue;
        }

        NVSNAP_INFO("ZMQ restore: restored context %d/%d (ckpt=%p new_ctx=%p)",
                   i + 1, count, checkpoint, new_ctx);

        /* TODO: Update application's context pointers
         * This requires tracking original context addresses
         * For now, just create the contexts - app will use new ones
         */
    }

    fclose(fp);
    NVSNAP_INFO("ZMQ restore: completed restoring %d contexts", count);
}

/**
 * Public API for quiesce.c to call during checkpoint
 */
void nvsnap_zmq_handle_checkpoint(void) {
    nvsnap_zmq_checkpoint();
}

/**
 * Public API for init.c to call during restore detection
 */
void nvsnap_zmq_handle_restore(void) {
    nvsnap_zmq_restore();
}
