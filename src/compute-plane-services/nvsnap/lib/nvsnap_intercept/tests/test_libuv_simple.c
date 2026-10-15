/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * Simple libuv test to verify interception works with dynamically linked libuv.
 * 
 * Compile: gcc -o test_libuv_simple test_libuv_simple.c -luv
 * Run: LD_PRELOAD=../libnvsnap_intercept.so ./test_libuv_simple
 */

#include <stdio.h>
#include <stdlib.h>
#include <uv.h>

static void timer_callback(uv_timer_t* handle) {
    printf("Timer fired!\n");
    uv_timer_stop(handle);
    uv_stop(uv_default_loop());
}

int main() {
    printf("=== Simple libuv test ===\n");
    
    /* Initialize the default loop */
    uv_loop_t* loop = uv_default_loop();
    if (!loop) {
        fprintf(stderr, "Failed to get default loop\n");
        return 1;
    }
    printf("Loop initialized: %p\n", (void*)loop);
    
    /* Create a timer */
    uv_timer_t timer;
    int r = uv_timer_init(loop, &timer);
    if (r != 0) {
        fprintf(stderr, "uv_timer_init failed: %s\n", uv_strerror(r));
        return 1;
    }
    printf("Timer initialized\n");
    
    /* Start timer - fire after 100ms */
    r = uv_timer_start(&timer, timer_callback, 100, 0);
    if (r != 0) {
        fprintf(stderr, "uv_timer_start failed: %s\n", uv_strerror(r));
        return 1;
    }
    printf("Timer started, running loop...\n");
    
    /* Run the event loop */
    r = uv_run(loop, UV_RUN_DEFAULT);
    printf("Loop exited with: %d\n", r);
    
    /* Cleanup */
    uv_loop_close(loop);
    
    printf("=== Test passed ===\n");
    return 0;
}
