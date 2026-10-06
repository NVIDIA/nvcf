#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Compile libnvsnap_gpushare.so, nvsnap-gpu-suspend and the gpushare GPU tests
# with warnings as errors, once per platform, in the CUDA image the agent base
# image builds them with (Dockerfile.base, gpushare-builder stage). Needs Docker,
# and QEMU binfmt for a foreign platform; no GPU. Also fails if a binary needs a
# newer glibc than the workload images are documented to provide.
#
#   scripts/check-gpushare-build.sh                       # amd64 and arm64
#   PLATFORMS=linux/amd64 scripts/check-gpushare-build.sh
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
image="$(awk '/^FROM .* AS gpushare-builder$/ { print $2 }' "$root/docker/agent/Dockerfile.base")"
if [ -z "$image" ]; then
    echo "check-gpushare-build: no 'FROM <image> AS gpushare-builder' in Dockerfile.base" >&2
    exit 1
fi
# Workload images must provide this glibc or newer (docs/GPUSHARE.md).
max_glibc="${MAX_GLIBC:-2.35}"

for platform in ${PLATFORMS:-linux/amd64 linux/arm64}; do
    echo "check-gpushare-build: $platform ($image)"
    docker run --rm --platform "$platform" -e MAX_GLIBC="$max_glibc" \
        -v "$root:/src:ro" "$image" bash -ceu '
        cp -r /src/docker/agent/gpushare /tmp/gpushare
        cp -r /src/tests/gpushare /tmp/tests
        make -C /tmp/gpushare CFLAGS="-O2 -Wall -Wextra -Werror"
        make -C /tmp/tests CFLAGS="-O2 -Wall -Werror" CUDA_STUBS=/usr/local/cuda/lib64/stubs
        for bin in /tmp/gpushare/libnvsnap_gpushare.so /tmp/gpushare/nvsnap-gpu-suspend; do
            need="$(objdump -T "$bin" | grep -o "GLIBC_[0-9.]*" | sed "s/GLIBC_//" | sort -V | tail -1)"
            if [ "$(printf "%s\n%s\n" "$need" "$MAX_GLIBC" | sort -V | tail -1)" != "$MAX_GLIBC" ]; then
                echo "$bin needs glibc $need, newer than the documented $MAX_GLIBC" >&2
                exit 1
            fi
            echo "$(basename "$bin"): needs glibc $need"
        done'
done
echo "check-gpushare-build: ok"
