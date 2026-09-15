/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * GPU checkpoint/restore helpers for libnvsnap_intercept.so
 *
 * Provides P2P disable/enable and device sync for multi-GPU checkpoint.
 * Called from the quiesce path (AFTER NCCL destroy, BEFORE CUDA Checkpoint API).
 *
 * All CUDA functions resolved via dlsym at runtime — no link dependency on CUDA.
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>

/* Logging via nvsnap intercept */
extern void nvsnap_log(int level, const char *func, const char *fmt, ...);
#define NVSNAP_INFO(fmt, ...) nvsnap_log(3, __func__, fmt, ##__VA_ARGS__)
#define NVSNAP_WARN(fmt, ...) nvsnap_log(2, __func__, fmt, ##__VA_ARGS__)

typedef int cudaError_t;

/* Resolve a symbol from a specific library, bypassing our own wrappers. */
static void *resolve_real(const char *sym, const char *lib) {
    void *h = dlopen(lib, RTLD_LAZY | RTLD_NOLOAD);
    if (!h) h = dlopen(lib, RTLD_LAZY);
    if (!h) return NULL;
    return dlsym(h, sym);
}

/* Cached function pointers */
static cudaError_t (*fn_getDeviceCount)(int *) = NULL;
static cudaError_t (*fn_setDevice)(int) = NULL;
static cudaError_t (*fn_getDevice)(int *) = NULL;
static cudaError_t (*fn_canAccessPeer)(int *, int, int) = NULL;
static cudaError_t (*fn_disablePeerAccess)(int) = NULL;
static cudaError_t (*fn_enablePeerAccess)(int, unsigned) = NULL;
static cudaError_t (*fn_deviceSync)(void) = NULL;
static int fns_resolved = 0;

static void resolve_all(void) {
    if (fns_resolved) return;
    fn_getDeviceCount = resolve_real("cudaGetDeviceCount", "libcudart.so");
    fn_setDevice = resolve_real("cudaSetDevice", "libcudart.so");
    fn_getDevice = resolve_real("cudaGetDevice", "libcudart.so");
    fn_canAccessPeer = resolve_real("cudaDeviceCanAccessPeer", "libcudart.so");
    fn_disablePeerAccess = resolve_real("cudaDeviceDisablePeerAccess", "libcudart.so");
    fn_enablePeerAccess = resolve_real("cudaDeviceEnablePeerAccess", "libcudart.so");
    fn_deviceSync = resolve_real("cudaDeviceSynchronize", "libcudart.so");
    fns_resolved = 1;
}

/*
 * Disable P2P access between all GPU pairs and sync all devices.
 * Called during quiesce, AFTER NCCL comms are destroyed.
 * Returns: number of P2P pairs disabled, or -1 on error.
 */
int nvsnap_gpu_pre_checkpoint(void) {
    resolve_all();

    if (!fn_getDeviceCount || !fn_setDevice || !fn_getDevice) {
        NVSNAP_WARN("CUDA runtime not available, skipping P2P disable");
        return 0;
    }

    int num_devices = 0;
    fn_getDeviceCount(&num_devices);
    if (num_devices <= 1) return 0; /* Single GPU, nothing to do */

    int current_device = -1;
    fn_getDevice(&current_device);

    /* Disable P2P access FROM this process's device to all other devices.
     *
     * CRITICAL: Do NOT call cudaSetDevice() for other GPUs. That creates new
     * CUDA primary contexts on GPUs this process doesn't own, adding cross-GPU
     * driver state that makes cuCheckpointProcessLock/Checkpoint hang.
     * Each TP worker only owns one GPU — only touch that one. */
    int p2p_disabled = 0;
    if (current_device >= 0 && fn_canAccessPeer && fn_disablePeerAccess) {
        fn_setDevice(current_device);
        for (int j = 0; j < num_devices; j++) {
            if (j == current_device) continue;
            int can_access = 0;
            fn_canAccessPeer(&can_access, current_device, j);
            if (can_access) {
                cudaError_t err = fn_disablePeerAccess(j);
                if (err == 0) {
                    p2p_disabled++;
                }
                /* err=704 (not enabled) is fine — skip silently */
            }
        }
    }

    /* Sync this device only */
    if (fn_deviceSync && current_device >= 0) {
        fn_setDevice(current_device);
        fn_deviceSync();
    }

    NVSNAP_INFO("P2P disabled: %d pairs across %d devices", p2p_disabled, num_devices);
    return p2p_disabled;
}

/*
 * Re-enable P2P access between all GPU pairs.
 * Called during post-restore reinit.
 * Returns: number of P2P pairs enabled, or -1 on error.
 */
int nvsnap_gpu_post_restore(void) {
    resolve_all();

    if (!fn_getDeviceCount || !fn_setDevice || !fn_getDevice) return 0;

    int num_devices = 0;
    fn_getDeviceCount(&num_devices);
    if (num_devices <= 1) return 0;

    int current_device = -1;
    fn_getDevice(&current_device);

    /* Re-enable P2P FROM this device only — same logic as pre_checkpoint. */
    int p2p_enabled = 0;
    if (current_device >= 0 && fn_canAccessPeer && fn_enablePeerAccess) {
        fn_setDevice(current_device);
        for (int j = 0; j < num_devices; j++) {
            if (j == current_device) continue;
            int can_access = 0;
            fn_canAccessPeer(&can_access, current_device, j);
            if (can_access) {
                cudaError_t err = fn_enablePeerAccess(j, 0);
                if (err == 0) p2p_enabled++;
                /* err=704 (already enabled) is fine */
            }
        }
    }

    NVSNAP_INFO("P2P re-enabled: %d pairs across %d devices", p2p_enabled, num_devices);
    return p2p_enabled;
}
