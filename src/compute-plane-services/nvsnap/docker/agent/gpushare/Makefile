# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# libnvsnap_gpushare.so: LD_PRELOAD shim that releases and re-maps GPU memory
# shared between processes around a CUDA checkpoint (see gpushare.h).
# nvsnap-gpu-suspend: drives it and the CUDA checkpoint API.
# Needs CUDA >= 13 headers (cuda.h: CUcheckpointGpuPair for --gpu-map).

CC ?= gcc
CUDA_INC ?= /usr/local/cuda/include
CFLAGS ?= -O2 -Wall -Wextra

all: libnvsnap_gpushare.so nvsnap-gpu-suspend

libnvsnap_gpushare.so: gpushare.c gpushare.h
	$(CC) $(CFLAGS) -fPIC -shared -I. -I$(CUDA_INC) -o $@ gpushare.c -ldl -lpthread

nvsnap-gpu-suspend: nvsnap-gpu-suspend.c gpushare.h
	$(CC) $(CFLAGS) -I. -I$(CUDA_INC) -o $@ nvsnap-gpu-suspend.c -ldl -lpthread

clean:
	rm -f libnvsnap_gpushare.so nvsnap-gpu-suspend

.PHONY: all clean
