# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP Agent Application Image
# Builds on top of nvsnap-agent-base with the Go binaries and C helpers.
# This is the image that gets rebuilt frequently during development
#
# Prerequisites: Build base image first with Dockerfile.base
# Build: docker build -t nvsnap-agent:v0.x.x -f Dockerfile.app .

ARG BASE_IMAGE=nvcr.io/0651155215864979/ncp-dev/nvsnap-agent-base:v0.0.19

# ============================================================================
# Stage 1: Build Go binaries
# ============================================================================
FROM golang:1.25-bookworm AS go-builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /bin/nvsnap-agent ./cmd/agent
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /bin/restore-entrypoint ./cmd/restore-entrypoint
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /bin/nvsnap-mount-prep ./cmd/nvsnap-mount-prep
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /bin/nvsnap-rootfs-restore ./cmd/nvsnap-rootfs-restore

# ============================================================================
# Stage 2: Build the standalone C helpers
# ============================================================================
FROM ubuntu:22.04 AS c-builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    python3-dev \
    binutils \
    cmake \
    && rm -rf /var/lib/apt/lists/*

# Build nvsnap-gpu-restore (standalone C binary, links libcuda at runtime via dlopen)
COPY cmd/nvsnap-gpu-restore/main.c /tmp/nvsnap-gpu-restore.c
RUN gcc -O2 -o /tmp/nvsnap-gpu-restore /tmp/nvsnap-gpu-restore.c -ldl && \
    ls -la /tmp/nvsnap-gpu-restore


# Build nvsnap-restore-helper (single-threaded C binary that does
# open_tree + setns + move_mount + execve(criu) for cross-mntns restore)
COPY lib/nvsnap_restore_helper/ /tmp/nvsnap_restore_helper/
RUN cd /tmp/nvsnap_restore_helper && make && \
    ls -la nvsnap-restore-helper

# Build NvSnap from source (ensures glibc compatibility with Ubuntu 22.04)

# ============================================================================
# Stage 3: Final image (fast - just copies binaries into base)
# ============================================================================
FROM ${BASE_IMAGE}

# Add debugging tools
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 \
    python3-pip \
    gdb \
    strace \
    curl \
    && rm -rf /var/lib/apt/lists/*

RUN pip3 install --no-cache-dir py-spy

# Copy Go binaries
COPY --from=go-builder /bin/nvsnap-agent /criu-bundle/nvsnap-agent
COPY --from=go-builder /bin/restore-entrypoint /criu-bundle/restore-entrypoint
COPY --from=go-builder /bin/nvsnap-mount-prep /criu-bundle/nvsnap-mount-prep
COPY --from=go-builder /bin/nvsnap-rootfs-restore /criu-bundle/nvsnap-rootfs-restore

# nvsnap#147: Restore-bundle init payload. The mutating webhook injects
# one init container running this same agent image with restore-bundle-init.sh
# as its command; the script copies /criu-bundle/. → /nvsnap onto a shared
# emptyDir so the rewritten workload command /nvsnap/restore-entrypoint can
# exec the CRIU restore.
COPY scripts/restore-bundle-init.sh /criu-bundle/restore-bundle-init.sh
RUN chmod +x /criu-bundle/restore-bundle-init.sh
# Standalone C helpers built in the c-builder stage above.

# Copy nvsnap-gpu-restore binary (restores GPU memory via CUDA VMM APIs after CRIU restore)
COPY --from=c-builder /tmp/nvsnap-gpu-restore /criu-bundle/nvsnap-gpu-restore
COPY --from=c-builder /tmp/nvsnap_restore_helper/nvsnap-restore-helper /criu-bundle/nvsnap-restore-helper

# Override cuda-checkpoint wrapper: keep stray stdout out of the tool's output.
# Historically this isolated it from the LD_PRELOAD interceptor, whose logs
# output, causing the CRIU CUDA plugin to get tid=0 and GPU resume to fail.
COPY cuda-checkpoint-wrapper.sh /criu-bundle/cuda-checkpoint
RUN chmod +x /criu-bundle/cuda-checkpoint

# Create symlinks
RUN ln -sf /criu-bundle/nvsnap-agent /usr/local/bin/nvsnap-agent && \
    ln -sf /criu-bundle/criu /usr/local/sbin/criu && \
    ln -sf /criu-bundle/nvsnap-mount-prep /nvsnap-mount-prep

# Verify everything works
RUN echo "=== Agent ===" && /criu-bundle/nvsnap-agent --help 2>&1 | head -3 || true && \
    echo "=== Bundle contents ===" && ls -la /criu-bundle/

# Set ENTRYPOINT (not CMD) so K8s args append properly
ENTRYPOINT ["/criu-bundle/nvsnap-agent"]
