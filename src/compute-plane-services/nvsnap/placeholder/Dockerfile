# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Self-Restoring Placeholder for GPU Container Checkpoint/Restore
#
# This Dockerfile creates a placeholder image that can restore a checkpointed
# process using CRIU. The key insight: CRIU must be built with the SAME glibc
# as the target container, so we build it inside the target base image.
#
# Usage:
#   docker build --build-arg BASE_IMAGE=nvidia/cuda:12.4.0-base-ubuntu22.04 \
#                -t nvsnap-placeholder:cuda-12.4 -f placeholder/Dockerfile .

ARG BASE_IMAGE=nvidia/cuda:12.4.0-base-ubuntu22.04

# NOTE: The restore-entrypoint binary must be pre-built and placed in the build context
# Build it with: CGO_ENABLED=0 go build -ldflags="-w -s" -o bin/restore-entrypoint ./cmd/restore-entrypoint

#############################################
# Stage 2: Build CRIU (inside target base image for glibc match)
#############################################
FROM ${BASE_IMAGE} AS criu-builder

# Install CRIU build dependencies
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

# Copy and build forked CRIU (with GPU checkpoint support)
# The forked CRIU source must be copied to placeholder/criu-source/ before building
WORKDIR /build
COPY criu-source/ /build/criu/
RUN cd criu \
    && make clean || true \
    && make -j$(nproc) \
    && make install-criu DESTDIR=/criu-install

#############################################
# Stage 3: Runtime image (same base as target)
#############################################
FROM ${BASE_IMAGE}

# Install only CRIU runtime dependencies (not build deps)
RUN apt-get update && apt-get install -y --no-install-recommends \
    libbsd0 \
    libcap2 \
    libnet1 \
    libnl-3-200 \
    libnl-route-3-200 \
    libprotobuf-c1 \
    libnftables1 \
    libgnutls30 \
    iproute2 \
    iptables \
    procps \
    && rm -rf /var/lib/apt/lists/*

# Copy CRIU from builder
COPY --from=criu-builder /criu-install/usr/local/sbin/criu /usr/local/sbin/criu
RUN chmod +x /usr/local/sbin/criu && criu --version

# Copy pre-built restore-entrypoint binary
COPY restore-entrypoint /restore-entrypoint
RUN chmod +x /restore-entrypoint

# Create checkpoint directory
RUN mkdir -p /checkpoints /var/run/criu

# Environment
ENV CHECKPOINT_BASE_DIR=/checkpoints \
    DEBUG=1 \
    CRIU_LOG_LEVEL=4

ENTRYPOINT ["/restore-entrypoint"]
