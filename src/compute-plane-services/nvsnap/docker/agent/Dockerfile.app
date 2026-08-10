# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP Agent Application Image
# Builds on top of nvsnap-agent-base with Go binaries and intercept library
# This is the image that gets rebuilt frequently during development
#
# Prerequisites: Build base image first with Dockerfile.base
# Build: docker build -t nvsnap-agent:v0.x.x -f Dockerfile.app .

ARG BASE_IMAGE=nvcr.io/0651155215864979/ncp-dev/nvsnap-agent-base:v0.0.7

# Builder images we fold INTO the agent. Lets the BYOC auto-inject
# webhook use ONE init container (this image) instead of four —
# saves init container startup overhead and image-pull bandwidth on
# cold nodes. Trade-off is a ~30MB increase in the agent image,
# which is fine because the agent already carries the CRIU bundle.
ARG UVLOOP_IMAGE=nvcr.io/0651155215864979/ncp-dev/uvloop-builder:v0.0.1
ARG LIBUV_IMAGE=nvcr.io/0651155215864979/ncp-dev/libuv-builder:v0.0.1
ARG LIBZMQ_IMAGE=nvcr.io/0651155215864979/ncp-dev/libzmq-builder:v0.0.1

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
# Stage 2: Build intercept library
# ============================================================================
FROM ubuntu:22.04 AS intercept-builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    python3-dev \
    binutils \
    cmake \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY lib/nvsnap_intercept/ .

ARG ENABLE_LIBUV_INTERCEPT=0
RUN make clean && make ENABLE_LIBUV_INTERCEPT=${ENABLE_LIBUV_INTERCEPT} && \
    ls -la libnvsnap_intercept.so

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
# Builder-payload stages (just pulled for COPY --from in final stage)
# ============================================================================
FROM ${UVLOOP_IMAGE} AS uvloop-payload
FROM ${LIBUV_IMAGE}  AS libuv-payload
FROM ${LIBZMQ_IMAGE} AS libzmq-payload

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

# Copy intercept library (includes uv_loop_fork call via Python C API)
COPY --from=intercept-builder /app/libnvsnap_intercept.so /criu-bundle/lib/libnvsnap_intercept.so

# Bundle the runtime sitecustomize. Init container get-criu (or any
# init that copies /criu-bundle into /nvsnap-lib) delivers this onto the
# emptyDir volume; workload Pods then set PYTHONPATH=/nvsnap-lib/sitecustomize
# and our patched site-packages-cpXY wins on import. See
# docs/GENERIC-PYTHON-INJECTION-DESIGN.md.
COPY lib/sitecustomize/sitecustomize.py /criu-bundle/sitecustomize/sitecustomize.py

# Bundle the BYOC auto-inject payload: uvloop wheels (multi-python),
# patched libuv.so, patched libzmq.so. The webhook injects ONE init
# container running this agent image with auto-inject-init.sh, which
# copies these payloads into the workload pod's /nvsnap-lib emptyDir.
# Replaces the previous 4-init-container fan-out and removes the
# separate nvsnap-init image (one less artifact to keep version-matched).
COPY --from=uvloop-payload /wheels/                  /criu-bundle/payload/wheels/
COPY --from=libuv-payload  /usr/local/lib/libuv.so   /criu-bundle/payload/lib/libuv.so
COPY --from=libuv-payload  /usr/local/lib/libuv.so.1 /criu-bundle/payload/lib/libuv.so.1
COPY --from=libzmq-payload /usr/local/lib/libzmq.so  /criu-bundle/payload/lib/libzmq.so
COPY --from=libzmq-payload /usr/local/lib/libzmq.so.5 /criu-bundle/payload/lib/libzmq.so.5
COPY scripts/auto-inject-init.sh /criu-bundle/auto-inject-init.sh
RUN chmod +x /criu-bundle/auto-inject-init.sh

# nvsnap#147: Restore-bundle init payload. The mutating webhook injects
# one init container running this same agent image with restore-bundle-init.sh
# as its command; the script copies /criu-bundle/. → /nvsnap onto a shared
# emptyDir so the rewritten workload command /nvsnap/restore-entrypoint can
# exec the CRIU restore. Companion to auto-inject-init.sh.
COPY scripts/restore-bundle-init.sh /criu-bundle/restore-bundle-init.sh
RUN chmod +x /criu-bundle/restore-bundle-init.sh
# Copy NvSnap GPU interposition library (built from source in intercept-builder)

# Copy nvsnap-gpu-restore binary (restores GPU memory via CUDA VMM APIs after CRIU restore)
COPY --from=intercept-builder /tmp/nvsnap-gpu-restore /criu-bundle/nvsnap-gpu-restore
COPY --from=intercept-builder /tmp/nvsnap_restore_helper/nvsnap-restore-helper /criu-bundle/nvsnap-restore-helper

# Override cuda-checkpoint wrapper: isolate from LD_PRELOAD intercept library.
# Without this, the intercept library's log messages corrupt cuda-checkpoint's
# output, causing the CRIU CUDA plugin to get tid=0 and GPU resume to fail.
COPY cuda-checkpoint-wrapper.sh /criu-bundle/cuda-checkpoint
RUN chmod +x /criu-bundle/cuda-checkpoint

# Create symlinks
RUN ln -sf /criu-bundle/nvsnap-agent /usr/local/bin/nvsnap-agent && \
    ln -sf /criu-bundle/criu /usr/local/sbin/criu && \
    ln -sf /criu-bundle/nvsnap-mount-prep /nvsnap-mount-prep

# Verify everything works
RUN echo "=== Agent ===" && /criu-bundle/nvsnap-agent --help 2>&1 | head -3 || true && \
    echo "=== Bundle contents ===" && ls -la /criu-bundle/ && \
    echo "=== Intercept library ===" && ls -la /criu-bundle/lib/libnvsnap_intercept.so

# Set ENTRYPOINT (not CMD) so K8s args append properly
ENTRYPOINT ["/criu-bundle/nvsnap-agent"]
