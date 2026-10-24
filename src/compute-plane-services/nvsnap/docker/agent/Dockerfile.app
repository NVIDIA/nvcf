# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP Agent Application Image
# Builds on top of nvsnap-agent-base with the Go binaries
# This is the image that gets rebuilt frequently during development
#
# Prerequisites: Build base image first with Dockerfile.base
# Build: docker build -t nvsnap-agent:v0.x.x -f Dockerfile.app .

ARG BASE_IMAGE=nvcr.io/0651155215864979/ncp-dev/nvsnap-agent-base:v0.0.28

# ============================================================================
# Stage 1: Build Go binaries
# ============================================================================
# Go binaries cross-compile natively on the build host for the target
# architecture (amd64 system nodes, arm64 Grace GPU nodes).
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS go-builder
ARG TARGETARCH

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o /bin/nvsnap-agent ./cmd/agent
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o /bin/nvsnap-mount-prep ./cmd/nvsnap-mount-prep
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o /bin/nvsnap-rootfs-restore ./cmd/nvsnap-rootfs-restore
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o /bin/nvsnap-helper ./cmd/nvsnap-helper

# nvsnap-idle (cmd/nvsnap-idle/c) is C, built static for the target platform
# itself; the image's gcc runs under emulation for a foreign one.
FROM golang:1.25-bookworm AS idle-builder
COPY cmd/nvsnap-idle/c/nvsnap-idle.c /src/nvsnap-idle.c
RUN gcc -static -Os -Wall -Wextra -Werror -o /bin/nvsnap-idle /src/nvsnap-idle.c && strip /bin/nvsnap-idle

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
COPY --from=go-builder /bin/nvsnap-mount-prep /criu-bundle/nvsnap-mount-prep
COPY --from=go-builder /bin/nvsnap-rootfs-restore /criu-bundle/nvsnap-rootfs-restore
# Runs the steps the webhook adds to pods, from the node bundle (cmd/nvsnap-helper).
COPY --from=go-builder /bin/nvsnap-helper /criu-bundle/nvsnap-helper
# The node bundle version this image stages (docs/design/
# VERSIONED-NODE-BUNDLE.md): its tag, the same for every architecture, so
# a pod admitted by any node's agent mounts a version its own node stages.
ARG APP_VERSION=dev
RUN echo "${APP_VERSION}" > /criu-bundle/BUNDLE_VERSION
# Keeps restore placeholders and staging pods alive, from the node bundle.
COPY --from=idle-builder /bin/nvsnap-idle /criu-bundle/nvsnap-idle

# Bundle staging script: the agent DaemonSet's nvsnap-bundle-stage init
# container runs it to copy /criu-bundle/. onto a node hostPath that the
# rootfs L2 restore mounts as /nvsnap (nvsnap-rootfs-restore).
COPY scripts/restore-bundle-init.sh /criu-bundle/restore-bundle-init.sh
RUN chmod +x /criu-bundle/restore-bundle-init.sh

# cuda-checkpoint wrapper: resolves the host driver library path and strips
# any preload from the environment before exec'ing the real binary. Paths are
# relative to the nvsnap root, so the image also builds with that directory as
# the context and no staging step.
COPY docker/agent/cuda-checkpoint-wrapper.sh /criu-bundle/cuda-checkpoint
RUN chmod +x /criu-bundle/cuda-checkpoint
# Inside a workload, where the webhook mounts this bundle read-only at
# /criu-bundle, CRIU's CUDA plugin runs cuda-checkpoint from PATH, and the
# workload's driver libraries are already in its loader path: in-container/,
# first on that PATH, holds the real binary without the wrapper
# (internal/agent/bundle_tools.go, bundlePATH).
RUN mkdir -p /criu-bundle/in-container && \
    ln -s ../cuda-checkpoint.real /criu-bundle/in-container/cuda-checkpoint

# Create symlinks
RUN ln -sf /criu-bundle/nvsnap-agent /usr/local/bin/nvsnap-agent && \
    ln -sf /criu-bundle/criu /usr/local/sbin/criu && \
    ln -sf /criu-bundle/nvsnap-mount-prep /nvsnap-mount-prep

# Verify everything works
RUN echo "=== Agent ===" && /criu-bundle/nvsnap-agent --help 2>&1 | head -3 || true && \
    echo "=== Bundle contents ===" && ls -la /criu-bundle/

# Set ENTRYPOINT (not CMD) so K8s args append properly
ENTRYPOINT ["/criu-bundle/nvsnap-agent"]
