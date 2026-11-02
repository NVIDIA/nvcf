#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Compile libnvsnap_gpushare.so and nvsnap-gpu-suspend with warnings as errors,
# once per platform, the way the agent base image builds them (Dockerfile.base,
# gpushare-builder stage: Rocky Linux 8 with the CUDA driver API header), and
# fail if either needs a newer glibc than workload images are documented to
# provide. Then compile the gpushare GPU tests in the CUDA image. Needs Docker,
# and QEMU binfmt for a foreign platform; no GPU.
#
#   scripts/check-gpushare-build.sh                       # amd64 and arm64
#   PLATFORMS=linux/amd64 scripts/check-gpushare-build.sh
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
df="$root/docker/agent/Dockerfile.base"
builder="$(awk '/^FROM .* AS gpushare-builder$/ { print $2 }' "$df")"
cuda="$(awk '/^FROM .* AS cuda-headers$/ { print $2 }' "$df")"
if [ -z "$builder" ] || [ -z "$cuda" ]; then
    echo "check-gpushare-build: no 'AS gpushare-builder' or 'AS cuda-headers' stage in Dockerfile.base" >&2
    exit 1
fi
# Workload images must provide this glibc or newer (docs/design/GPUSHARE.md).
max_glibc="${MAX_GLIBC:-2.17}"

for platform in ${PLATFORMS:-linux/amd64 linux/arm64}; do
    echo "check-gpushare-build: $platform ($builder, $cuda headers)"
    hdr="$(mktemp -d)"
    cid="$(docker create --platform "$platform" "$cuda")"
    docker cp "$cid:/usr/local/cuda/include/cuda.h" "$hdr/cuda.h" >/dev/null
    docker rm "$cid" >/dev/null
    docker run --rm --platform "$platform" -e MAX_GLIBC="$max_glibc" \
        -v "$root:/src:ro" -v "$hdr:/cuda/include:ro" "$builder" bash -ceu '
        dnf -q -y install gcc make >/dev/null
        # -B: copied-in local build outputs must not let make skip a compile.
        cp -r /src/docker/agent/gpushare /tmp/gpushare
        make -B -C /tmp/gpushare CUDA_INC=/cuda/include CFLAGS="-O2 -Wall -Wextra -Werror"
        for bin in /tmp/gpushare/libnvsnap_gpushare.so /tmp/gpushare/nvsnap-gpu-suspend; do
            need="$(objdump -T "$bin" | grep -o "GLIBC_[0-9.]*" | sed "s/GLIBC_//" | sort -V | tail -1)"
            if [ "$(printf "%s\n%s\n" "$need" "$MAX_GLIBC" | sort -V | tail -1)" != "$MAX_GLIBC" ]; then
                echo "$bin needs glibc $need, newer than the documented $MAX_GLIBC" >&2
                exit 1
            fi
            echo "$(basename "$bin"): needs glibc $need"
        done'
    rm -rf "$hdr"
    docker run --rm --platform "$platform" -v "$root:/src:ro" "$cuda" bash -ceu '
        cp -r /src/tests/gpushare /tmp/tests
        make -B -C /tmp/tests CFLAGS="-O2 -Wall -Werror" CUDA_STUBS=/usr/local/cuda/lib64/stubs'
done
echo "check-gpushare-build: ok"
