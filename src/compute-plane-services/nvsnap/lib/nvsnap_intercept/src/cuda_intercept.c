/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * Stub — CUDA allocation tracking is now handled by NvSnap's
 * nvsnap_interpose_cudart.c + nvsnap_interpose_cudrv.c + nvsnap_tracker.cpp.
 *
 * This file only exists for build compatibility (nvsnap_cuda_symbol_override
 * was previously called from zmq_intercept.c but is no longer used).
 */
#include "nvsnap_intercept.h"

void *nvsnap_cuda_symbol_override(const char *symbol) {
    (void)symbol;
    return NULL;
}
