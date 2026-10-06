# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# GPU tests for libnvsnap_gpushare.so / nvsnap-gpu-suspend (need a GPU node;
# multi-process tests need 2+ GPUs). Each takes the nvsnap-gpu-suspend path:
#   ./test_feature_restore ../../docker/agent/gpushare/nvsnap-gpu-suspend alloc
# test_checkpoint_nccl (dlopens NCCL), test_multi_gpu and test_release_rollback run under
#   LD_PRELOAD=.../libnvsnap_gpushare.so (see their headers).

CC ?= gcc
CUDA_INC ?= /usr/local/cuda/include
CUDA_STUBS ?= /usr/local/cuda/lib64/stubs
CFLAGS ?= -O2 -Wall

CUDA_TESTS = test_feature_restore test_cumem_release test_ipc_release test_ipc_share test_multi_gpu test_release_rollback

all: $(CUDA_TESTS) test_checkpoint_nccl

$(CUDA_TESTS): %: %.c
	$(CC) $(CFLAGS) -I$(CUDA_INC) -o $@ $< -L$(CUDA_STUBS) -lcuda

test_checkpoint_nccl: test_checkpoint_nccl.c
	$(CC) $(CFLAGS) -o $@ $< -ldl

clean:
	rm -f $(CUDA_TESTS) test_checkpoint_nccl

.PHONY: all clean
