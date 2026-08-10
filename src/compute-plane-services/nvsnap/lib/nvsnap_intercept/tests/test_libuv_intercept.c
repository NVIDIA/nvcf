/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * Test libuv interception with explicit loop initialization.
 * 
 * Compile: gcc -o test_libuv_intercept test_libuv_intercept.c -luv
 * Run: NVSNAP_LOG_LEVEL=3 LD_PRELOAD=../libnvsnap_intercept.so ./test_libuv_intercept
 */

#include <stdio.h>
#include <stdlib.h>
#include <uv.h>

static int timer_count = 0;

static void timer_callback(uv_timer_t* handle) {
    timer_count++;
    printf("Timer fired! count=%d\n", timer_count);
    if (timer_count >= 3) {
        uv_timer_stop(handle);
        uv_close((uv_handle_t*)handle, NULL);
    }
}

int main() {
    int r;
    
    printf("=== Libuv Interception Test ===\n");
    fflush(stdout);
    
    /* Allocate and explicitly init a loop (tests uv_loop_init interception) */
    uv_loop_t* loop = malloc(sizeof(uv_loop_t));
    if (!loop) {
        fprintf(stderr, "Failed to allocate loop\n");
        return 1;
    }
    
    printf("Calling uv_loop_init()...\n");
    fflush(stdout);
    r = uv_loop_init(loop);
    if (r != 0) {
        fprintf(stderr, "uv_loop_init failed: %s (code %d)\n", uv_strerror(r), r);
        free(loop);
        return 1;
    }
    printf("Loop initialized: %p\n", (void*)loop);
    fflush(stdout);
    
    /* Create a timer (tests uv_timer_init interception) */
    uv_timer_t* timer = malloc(sizeof(uv_timer_t));
    printf("Calling uv_timer_init()...\n");
    fflush(stdout);
    r = uv_timer_init(loop, timer);
    if (r != 0) {
        fprintf(stderr, "uv_timer_init failed: %s\n", uv_strerror(r));
        return 1;
    }
    printf("Timer initialized\n");
    fflush(stdout);
    
    /* Start timer - fire every 50ms, 3 times */
    printf("Calling uv_timer_start()...\n");
    fflush(stdout);
    r = uv_timer_start(timer, timer_callback, 50, 50);
    if (r != 0) {
        fprintf(stderr, "uv_timer_start failed: %s\n", uv_strerror(r));
        return 1;
    }
    printf("Timer started, running loop...\n");
    fflush(stdout);
    
    /* Run the event loop (tests uv_run interception) */
    printf("Calling uv_run()...\n");
    fflush(stdout);
    r = uv_run(loop, UV_RUN_DEFAULT);
    printf("Loop exited with: %d\n", r);
    fflush(stdout);
    
    /* Cleanup */
    printf("Calling uv_loop_close()...\n");
    fflush(stdout);
    r = uv_loop_close(loop);
    if (r != 0) {
        fprintf(stderr, "uv_loop_close failed: %s\n", uv_strerror(r));
    }
    free(timer);
    free(loop);
    
    printf("=== Test passed ===\n");
    return 0;
}
