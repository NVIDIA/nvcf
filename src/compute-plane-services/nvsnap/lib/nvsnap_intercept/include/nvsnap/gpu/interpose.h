/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NvSnap — Interposition helper macros.
 */
#ifndef NVSNAP_INTERPOSE_H
#define NVSNAP_INTERPOSE_H

#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif

#include <dlfcn.h>
#include <stdio.h>
#include <stdlib.h>

/* C11 <stdatomic.h> conflicts with C++ <atomic> on GCC 11.
 * interpose.h doesn't use atomics directly — the .c files that
 * need them include <stdatomic.h> themselves. */

#ifdef __cplusplus
extern "C" {
#endif

/* ── Log levels ──────────────────────────────────────────────────────── */

enum {
    NVSNAP_GPU_LOG_OFF   = 0,
    NVSNAP_GPU_LOG_ERROR = 1,
    NVSNAP_GPU_LOG_WARN  = 2,
    NVSNAP_GPU_LOG_INFO  = 3,
    NVSNAP_GPU_LOG_DEBUG = 4
};

extern int nvsnap_gpu_log_level;

/*
 * Thread-safe logging using write() instead of fprintf().
 * fprintf is NOT thread-safe — concurrent writes from 4+ TP workers
 * can corrupt FILE* internal buffer → segfault.
 * write() is atomic for small writes (<PIPE_BUF=4096 on Linux).
 */
void nvsnap_log_write(int level, const char *fmt, ...)
    __attribute__((format(printf, 2, 3)));

#define NVSNAP_GPU_LOG(level, fmt, ...)                                       \
    do {                                                                    \
        if (__builtin_expect(nvsnap_gpu_log_level >= (level), 0)) {           \
            nvsnap_log_write((level), fmt, ##__VA_ARGS__);                \
        }                                                                   \
    } while (0)

#define NVSNAP_GPU_LOG_ERROR(fmt, ...) NVSNAP_GPU_LOG(NVSNAP_GPU_LOG_ERROR, fmt, ##__VA_ARGS__)
#define NVSNAP_GPU_LOG_WARN(fmt, ...)  NVSNAP_GPU_LOG(NVSNAP_GPU_LOG_WARN,  fmt, ##__VA_ARGS__)
#define NVSNAP_GPU_LOG_INFO(fmt, ...)  NVSNAP_GPU_LOG(NVSNAP_GPU_LOG_INFO,  fmt, ##__VA_ARGS__)
#define NVSNAP_GPU_LOG_DEBUG(fmt, ...) NVSNAP_GPU_LOG(NVSNAP_GPU_LOG_DEBUG, fmt, ##__VA_ARGS__)

/* ── Thread-local state ──────────────────────────────────────────────── */

#ifdef __cplusplus
extern thread_local int nvsnap_current_device;
extern thread_local int nvsnap_in_graph_capture;
#else
extern _Thread_local int nvsnap_current_device;
extern _Thread_local int nvsnap_in_graph_capture;
#endif

/* ── Lazy-loading real function pointers ─────────────────────────────── */

/*
 * Resolve the real function pointer for an intercepted symbol.
 *
 * First tries RTLD_NEXT (works when the real library is in the link chain).
 * If that fails (e.g., NCCL loaded lazily via dlopen by PyTorch), tries
 * opening the library explicitly and resolving from that handle.
 */
void *nvsnap_resolve_real(const char *func_name, const char *lib_hint);

/* In the merged single-library build, dlsym(RTLD_NEXT) goes through our
 * dlsym override which returns our OWN hooks → infinite recursion.
 * Use nvsnap_resolve_real() which opens libraries explicitly and checks
 * dladdr() to ensure the result is NOT from our own library. */
#define NVSNAP_LOAD_REAL(func_name)                                       \
    do {                                                                    \
        if (__builtin_expect(real_##func_name == NULL, 0)) {                \
            real_##func_name = nvsnap_resolve_real(#func_name, NULL);     \
            if (!real_##func_name) {                                        \
                NVSNAP_GPU_LOG_ERROR("resolve failed for " #func_name);       \
            }                                                               \
        }                                                                   \
    } while (0)

/*
 * NVSNAP_INTERPOSE — Declares a real-function pointer, provides a wrapper
 * function body preamble that lazy-loads the real function.
 *
 * Usage:
 *   static cudaError_t (*real_cudaMalloc)(void**, size_t) = NULL;
 *   cudaError_t cudaMalloc(void **devPtr, size_t size) {
 *       NVSNAP_LOAD_REAL(cudaMalloc);
 *       // ... pre-call logic ...
 *       cudaError_t err = real_cudaMalloc(devPtr, size);
 *       // ... post-call logic ...
 *       return err;
 *   }
 *
 * For simple pass-through wrappers, use this convenience macro:
 */
#define NVSNAP_DECLARE_REAL(ret_type, func_name, ...)                     \
    static ret_type (*real_##func_name)(__VA_ARGS__) = NULL

#ifdef __cplusplus
}
#endif

#endif /* NVSNAP_INTERPOSE_H */
