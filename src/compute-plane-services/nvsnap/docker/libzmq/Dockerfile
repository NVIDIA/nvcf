# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# libzmq builder image for NVSNAP
#
# Builds our forked libzmq with CRIU checkpoint/restore support:
# - Epoll reinit after CRIU restore (epoll.cpp)
# - EINTR handling for blocked recv/send (socket_base.cpp)
# - Checkpoint/restore draft API (zmq_ctx_checkpoint, zmq_ctx_restore)
#
# Build context: the libzmq fork source directory
#   docker build -t libzmq-builder:TAG -f docker/libzmq/Dockerfile /path/to/libzmq
#
# IMPORTANT: ENABLE_DRAFTS must be ON — the checkpoint API is a draft API.
# Without it, pyzmq can't find zmq_ctx_checkpoint and fails to load.

FROM ubuntu:22.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    cmake \
    pkg-config \
    libsodium-dev \
    binutils \
    && rm -rf /var/lib/apt/lists/*

COPY . /libzmq-src
WORKDIR /libzmq-src

# Clean any stale build artifacts from host
RUN rm -rf build/

RUN mkdir -p build && cd build && \
    cmake -DCMAKE_INSTALL_PREFIX=/usr/local \
          -DCMAKE_BUILD_TYPE=Release \
          -DBUILD_TESTS=OFF \
          -DENABLE_DRAFTS=ON \
          .. && \
    make -j$(nproc) && \
    make install && \
    ldconfig

# Verify checkpoint API symbols exist (draft API)
RUN nm -D /usr/local/lib/libzmq.so | grep -E "zmq_ctx_checkpoint|zmq_ctx_restore|zmq_get_all_contexts" || \
    (echo "ERROR: Checkpoint API symbols not found. Was -DENABLE_DRAFTS=ON set?"; exit 1)

# Verify no GLIBC dependency newer than 2.35 (Ubuntu 22.04)
RUN ! objdump -p /usr/local/lib/libzmq.so.5.* | grep -E "GLIBC_2\.(3[6-9]|[4-9][0-9])" || \
    (echo "ERROR: libzmq requires GLIBC newer than 2.35 — host build artifacts leaked"; exit 1)

# Print build info
RUN echo "=== libzmq build info ===" && \
    echo "Version:" && strings /usr/local/lib/libzmq.so | grep -E "^4\.[0-9]\.[0-9]" | head -1 && \
    echo "Draft API: enabled" && \
    echo "Checkpoint symbols:" && \
    nm -D /usr/local/lib/libzmq.so | grep checkpoint && \
    echo "GLIBC deps:" && \
    objdump -p /usr/local/lib/libzmq.so.5.* | grep GLIBC | sort -u && \
    echo "File size:" && ls -la /usr/local/lib/libzmq.so.5.*

CMD ["/bin/bash"]
