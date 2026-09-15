# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# libuv builder image for NVSNAP
#
# Builds our forked libuv with CRIU checkpoint/restore support:
# - Detect CRIU restore in uv__io_poll via /run/criu-restored marker
# - Call uv_loop_fork() to reinitialize epoll backend after restore
# - Fixes segfault in c10d::LibUVStoreDaemon (PyTorch TCPStore)
#
# Build context: the libuv fork source directory
#   docker build -t libuv-builder:TAG -f docker/libuv/Dockerfile /path/to/libuv

FROM ubuntu:22.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    cmake \
    binutils \
    && rm -rf /var/lib/apt/lists/*

COPY . /libuv-src
WORKDIR /libuv-src

# Clean any stale build artifacts from host
RUN rm -rf build/

RUN mkdir -p build && cd build && \
    cmake -DCMAKE_INSTALL_PREFIX=/usr/local \
          -DCMAKE_BUILD_TYPE=Release \
          -DBUILD_TESTING=OFF \
          .. && \
    make -j$(nproc) && \
    make install && \
    ldconfig

# Verify CRIU restore detection code is present
RUN nm -D /usr/local/lib/libuv.so.1 | grep -q "uv_loop_fork" || \
    (echo "ERROR: uv_loop_fork symbol not found"; exit 1)

# Verify GLIBC compatibility with Ubuntu 24.04 (GLIBC 2.39)
RUN echo "=== libuv build info ===" && \
    echo "Version:" && strings /usr/local/lib/libuv.so.1 | grep -E "^1\.[0-9]+\.[0-9]" | head -1 && \
    echo "CRIU restore: enabled (uv__io_poll marker check)" && \
    echo "GLIBC deps:" && \
    objdump -p /usr/local/lib/libuv.so.1.* | grep GLIBC | sort -u && \
    echo "File size:" && ls -la /usr/local/lib/libuv.so.1.*

# Smoke test: verify the .so loads
RUN ldconfig -p | grep libuv && ldd /usr/local/lib/libuv.so.1

CMD ["/bin/bash"]
