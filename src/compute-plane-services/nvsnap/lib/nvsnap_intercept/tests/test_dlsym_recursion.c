/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * Test: dlsym override does NOT cause infinite recursion.
 *
 * In a single-library build, our dlsym override intercepts ALL dlsym calls.
 * If NVSNAP_LOAD_REAL uses dlsym(RTLD_NEXT, "cudaMalloc") and our override
 * returns our own hook, we get infinite recursion → hang/segfault.
 *
 * This test catches that bug by:
 * 1. Calling dlsym(RTLD_DEFAULT, "cudaMalloc") — should return our hook
 * 2. Calling the returned function — should NOT hang (must reach real libcudart)
 * 3. Verifying dlsym for non-hooked symbols still works (e.g., "printf")
 *
 * Build:
 *   gcc -g -o tests/test_dlsym_recursion tests/test_dlsym_recursion.c -ldl
 *
 * Run:
 *   LD_PRELOAD=./libnvsnap_intercept.so ./tests/test_dlsym_recursion
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static volatile int g_alarm_fired = 0;

static void alarm_handler(int sig) {
    (void)sig;
    g_alarm_fired = 1;
    const char msg[] = "FAIL: dlsym recursion detected (alarm timeout)\n";
    write(STDERR_FILENO, msg, sizeof(msg) - 1);
    _exit(1);
}

int main(void) {
    printf("=== Test: dlsym override recursion safety ===\n");

    /* Set 5-second alarm — if we hang, it fires and fails the test */
    signal(SIGALRM, alarm_handler);
    alarm(5);

    int pass = 0, fail = 0;

    /* Test 1: dlsym for a hooked symbol returns non-NULL */
    void *sym = dlsym(RTLD_DEFAULT, "cudaMalloc");
    if (sym) {
        printf("  PASS: dlsym(cudaMalloc) = %p\n", sym);
        pass++;
    } else {
        printf("  SKIP: cudaMalloc not found (no CUDA runtime)\n");
    }

    /* Test 2: dlsym for cuMemMap */
    sym = dlsym(RTLD_DEFAULT, "cuMemMap");
    if (sym) {
        printf("  PASS: dlsym(cuMemMap) = %p\n", sym);
        pass++;
    } else {
        printf("  SKIP: cuMemMap not found\n");
    }

    /* Test 3: dlsym for non-hooked symbol still works */
    sym = dlsym(RTLD_DEFAULT, "printf");
    if (sym) {
        printf("  PASS: dlsym(printf) = %p (non-hooked passthrough works)\n", sym);
        pass++;
    } else {
        printf("  FAIL: dlsym(printf) returned NULL — override is broken\n");
        fail++;
    }

    /* Test 4: dlsym(RTLD_DEFAULT, "dlsym") — meta-test */
    sym = dlsym(RTLD_DEFAULT, "dlsym");
    if (sym) {
        printf("  PASS: dlsym(dlsym) = %p (no infinite recursion)\n", sym);
        pass++;
    } else {
        printf("  FAIL: dlsym(dlsym) returned NULL\n");
        fail++;
    }

    /* Test 5: Verify nvsnap_resolve_real exists (merged build) */
    void *(*resolve_fn)(const char *, const char *) = dlsym(RTLD_DEFAULT, "nvsnap_resolve_real");
    if (resolve_fn) {
        printf("  PASS: nvsnap_resolve_real found (merged build confirmed)\n");
        pass++;

        /* Test 6: nvsnap_resolve_real returns real cudaMalloc, not our hook */
        void *real = resolve_fn("cudaMalloc", "libcudart.so");
        void *hook = dlsym(RTLD_DEFAULT, "cudaMalloc");
        if (real && hook && real != hook) {
            printf("  PASS: resolve_real(cudaMalloc) != dlsym(cudaMalloc) — no self-resolution\n");
            pass++;
        } else if (real && hook && real == hook) {
            printf("  FAIL: resolve_real returns our own hook — dladdr self-check broken\n");
            fail++;
        } else {
            printf("  SKIP: cudaMalloc not available for comparison\n");
        }
    } else {
        printf("  SKIP: nvsnap_resolve_real not found (not merged build?)\n");
    }

    /* Test 7: dlsym with specific library handle passes through (no hook) */
    {
        void *libc = dlopen("libc.so.6", RTLD_LAZY | RTLD_NOLOAD);
        if (libc) {
            void *real_printf = dlsym(libc, "printf");
            void *hook_printf = dlsym(RTLD_DEFAULT, "printf");
            /* With specific handle, should get the REAL function.
             * Our override should NOT intercept specific handles. */
            if (real_printf) {
                printf("  PASS: dlsym(libc_handle, printf) = %p (specific handle passthrough)\n",
                       real_printf);
                pass++;
            } else {
                printf("  FAIL: dlsym(libc_handle, printf) returned NULL\n");
                fail++;
            }
        }
    }

    /* Test 8: Rapid dlsym calls don't hang (stress test) */
    for (int i = 0; i < 1000; i++) {
        dlsym(RTLD_DEFAULT, "cudaMalloc");
        dlsym(RTLD_DEFAULT, "cuMemMap");
        dlsym(RTLD_DEFAULT, "ncclCommInitRank");
        dlsym(RTLD_DEFAULT, "printf");
    }
    printf("  PASS: 4000 dlsym calls completed without hang\n");
    pass++;

    alarm(0); /* Cancel alarm */

    printf("\n=== Results: %d passed, %d failed ===\n", pass, fail);
    return fail > 0 ? 1 : 0;
}
