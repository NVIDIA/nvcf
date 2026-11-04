# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Build CRIU with compatible glibc
# Ubuntu 22.04 has glibc 2.35 which works in most modern containers
FROM ubuntu:22.04 AS builder

ENV DEBIAN_FRONTEND=noninteractive

# Install build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    build-essential \
    pkg-config \
    libbsd-dev \
    libcap-dev \
    libnet1-dev \
    libnl-3-dev \
    libnl-route-3-dev \
    libprotobuf-dev \
    libprotobuf-c-dev \
    protobuf-c-compiler \
    protobuf-compiler \
    python3-protobuf \
    libgnutls28-dev \
    libnftables-dev \
    libaio-dev \
    uuid-dev \
    && rm -rf /var/lib/apt/lists/*

# Copy forked CRIU source
WORKDIR /build
COPY criu-source/ /build/criu/

# Build CRIU
RUN cd criu && \
    make clean || true && \
    make -j$(nproc) && \
    strip criu/criu

# Output stage - just the binary
FROM scratch
COPY --from=builder /build/criu/criu/criu /criu
