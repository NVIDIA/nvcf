/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * Direct io_uring test for seccomp interception
 *
 * This uses raw syscalls to test that seccomp intercepts io_uring,
 * independent of any library linking.
 *
 * Compile: gcc -o test_seccomp_direct tests/test_seccomp_direct.c
 * Run: NVSNAP_SECCOMP_ENABLED=1 NVSNAP_LOG_LEVEL=3 LD_PRELOAD=./libnvsnap_intercept.so ./test_seccomp_direct
 */

#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/syscall.h>
#include <sys/mman.h>
#include <errno.h>
#include <stdint.h>

/* io_uring syscall numbers */
#ifndef __NR_io_uring_setup
#define __NR_io_uring_setup 425
#endif
#ifndef __NR_io_uring_enter
#define __NR_io_uring_enter 426
#endif

/* io_uring structures */
struct io_uring_params {
    uint32_t sq_entries;
    uint32_t cq_entries;
    uint32_t flags;
    uint32_t sq_thread_cpu;
    uint32_t sq_thread_idle;
    uint32_t features;
    uint32_t wq_fd;
    uint32_t resv[3];
    struct {
        uint32_t head, tail, ring_mask, ring_entries;
        uint32_t flags, dropped, array, resv1;
        uint64_t resv2;
    } sq_off;
    struct {
        uint32_t head, tail, ring_mask, ring_entries;
        uint32_t overflow, cqes, flags, resv1;
        uint64_t resv2;
    } cq_off;
};

int main(void) {
    printf("Direct io_uring syscall test\n");
    printf("PID: %d\n\n", getpid());
    
    /* Test io_uring_setup */
    printf("Calling io_uring_setup(32, params)...\n");
    
    struct io_uring_params params;
    memset(&params, 0, sizeof(params));
    
    int fd = syscall(__NR_io_uring_setup, 32, &params);
    
    if (fd < 0) {
        printf("io_uring_setup failed: %s (errno=%d)\n", strerror(errno), errno);
        if (errno == ENOSYS) {
            printf("Kernel does not support io_uring\n");
        }
        return 1;
    }
    
    printf("io_uring_setup returned fd=%d\n", fd);
    printf("sq_entries=%u, cq_entries=%u, features=0x%x\n",
           params.sq_entries, params.cq_entries, params.features);
    
    /* Test io_uring_enter (with nothing to do) */
    printf("\nCalling io_uring_enter(fd=%d, to_submit=0, min_complete=0, flags=0)...\n", fd);
    
    int ret = syscall(__NR_io_uring_enter, fd, 0, 0, 0, NULL);
    
    if (ret < 0) {
        printf("io_uring_enter failed: %s (errno=%d)\n", strerror(errno), errno);
    } else {
        printf("io_uring_enter returned %d\n", ret);
    }
    
    /* Test io_uring_enter again */
    printf("\nCalling io_uring_enter(fd=%d, to_submit=0, min_complete=0, flags=1 GETEVENTS)...\n", fd);
    
    ret = syscall(__NR_io_uring_enter, fd, 0, 0, 1, NULL); /* 1 = IORING_ENTER_GETEVENTS */
    
    if (ret < 0) {
        printf("io_uring_enter failed: %s (errno=%d)\n", strerror(errno), errno);
    } else {
        printf("io_uring_enter returned %d\n", ret);
    }
    
    /* Cleanup */
    close(fd);
    
    printf("\nTest completed!\n");
    return 0;
}
