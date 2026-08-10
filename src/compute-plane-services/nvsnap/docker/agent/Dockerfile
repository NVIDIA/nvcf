# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP Agent Container - Multi-Stage Build
#
# Builds everything from source to ensure consistency:
# - Stage 1: Build CRIU from our fork (github.com/balajinvda/criu)
# - Stage 2: Build Go binaries (nvsnap-agent, restore-entrypoint)
# - Stage 3: Final minimal runtime image
#
# CRIU fork is cloned from GitHub with a specific tag for reproducibility.

# =============================================================================
# Stage 1: Build CRIU from source (cloned from GitHub fork)
# =============================================================================
FROM ubuntu:22.04 AS criu-builder

# CRIU fork version - change this to update CRIU
ARG CRIU_REPO=https://github.com/balajinvda/criu.git
ARG CRIU_TAG=nvsnap-v0.3.0
ARG CRIU_LOCAL_DIR=criu-src-local

# Install CRIU build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    pkg-config \
    python3 \
    python3-protobuf \
    libprotobuf-dev \
    libprotobuf-c-dev \
    protobuf-c-compiler \
    protobuf-compiler \
    libnet1-dev \
    libnl-3-dev \
    libnl-genl-3-dev \
    libcap-dev \
    libgnutls28-dev \
    libselinux1-dev \
    libnftables-dev \
    libdrm-dev \
    uuid-dev \
    liburing-dev \
    asciidoctor \
    xmlto \
    git \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Prefer local CRIU fork if provided in build context
COPY ${CRIU_LOCAL_DIR}/ /criu-local/
RUN if [ -f /criu-local/Makefile ]; then \
        cp -a /criu-local /criu-src; \
        echo "CRIU version: local (from build context)"; \
    else \
        git clone --branch ${CRIU_TAG} --depth 1 ${CRIU_REPO} /criu-src && \
        echo "CRIU version: $(cd /criu-src && git describe --tags --always)"; \
    fi

WORKDIR /criu-src

# Ensure no host build artifacts leak into container build
RUN make mrproper || make clean

# Create asm symlink for x86 architecture (required for plugin build)
RUN ln -sf ../arch/x86/include/asm criu/include/asm

# Build CRIU
RUN make -j$(nproc)

# Build CUDA plugin
RUN make -C plugins/cuda

# Build ZMQ plugin
RUN make -C plugins/zmq

# Collect CRIU and its library dependencies
RUN mkdir -p /criu-bundle/lib /criu-bundle/plugins && \
    cp criu/criu /criu-bundle/ && \
    cp plugins/cuda/cuda_plugin.so /criu-bundle/plugins/ && \
    cp plugins/zmq/zmq_plugin.so /criu-bundle/plugins/ && \
    # Collect runtime library dependencies (excluding core glibc)
    for lib in $(ldd /criu-bundle/criu | grep "=>" | awk '{print $3}' | sort -u); do \
        case "$lib" in \
            */libc.so*|*/ld-linux*|*/libpthread*|*/libdl*|*/librt*|*/libm.so*) \
                echo "Skipping core lib: $lib" ;; \
            *) \
                echo "Copying: $lib" && cp -L "$lib" /criu-bundle/lib/ 2>/dev/null || true ;; \
        esac \
    done

# Create iptables shims inside the CRIU bundle.
# Restore pods run CRIU inside arbitrary workload images, which often do not include
# iptables-restore/ip6tables-restore. CRIU's network lock/unlock may exec these binaries,
# so we provide no-op static shims to keep restore generic and self-contained.
RUN printf '#include <stdlib.h>\nint main(){return 0;}\n' > /tmp/true.c && \
    gcc -static -O2 -s -o /criu-bundle/iptables-restore /tmp/true.c && \
    cp /criu-bundle/iptables-restore /criu-bundle/ip6tables-restore && \
    chmod +x /criu-bundle/iptables-restore /criu-bundle/ip6tables-restore

# Create wrapper script that sets library path
RUN echo '#!/bin/sh' > /criu-bundle/criu-wrapper && \
    echo 'DIR="$(dirname "$(readlink -f "$0")")"' >> /criu-bundle/criu-wrapper && \
    echo 'export LD_LIBRARY_PATH="$DIR/lib:$LD_LIBRARY_PATH"' >> /criu-bundle/criu-wrapper && \
    echo 'exec "$DIR/criu" "$@"' >> /criu-bundle/criu-wrapper && \
    chmod +x /criu-bundle/criu-wrapper

# =============================================================================
# Stage 2a: Build libnvsnap_intercept.so (io_uring & libuv quiescence)
# =============================================================================
FROM ubuntu:22.04 AS intercept-builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    python3-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY lib/nvsnap_intercept/ .

ARG ENABLE_LIBUV_INTERCEPT=0
RUN make clean && make ENABLE_LIBUV_INTERCEPT=${ENABLE_LIBUV_INTERCEPT} && \
    ls -la libnvsnap_intercept.so

# =============================================================================
# Stage 2b: Build Go binaries
# =============================================================================
FROM golang:1.25-bookworm AS go-builder

WORKDIR /app

# Copy go mod files first for caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build agent and restore-entrypoint (use -mod=mod to ignore vendor, download fresh)
RUN CGO_ENABLED=0 GOOS=linux go build -mod=mod -ldflags="-s -w" -o /bin/nvsnap-agent ./cmd/agent && \
    CGO_ENABLED=0 GOOS=linux go build -mod=mod -ldflags="-s -w" -o /bin/restore-entrypoint ./cmd/restore-entrypoint && \
    CGO_ENABLED=0 GOOS=linux go build -mod=mod -ldflags="-s -w" -o /bin/nvsnap-mount-prep ./cmd/nvsnap-mount-prep

# =============================================================================
# Stage 3: Final runtime image
# =============================================================================
FROM ubuntu:22.04

# Install runtime dependencies for CRIU
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    util-linux \
    procps \
    iproute2 \
    iptables \
    libnftables1 \
    libnet1 \
    libnl-3-200 \
    libprotobuf-c1 \
    liburing2 \
    python3 \
    python3-pip \
    gdb \
    && rm -rf /var/lib/apt/lists/*

RUN pip3 install --no-cache-dir py-spy

# Create directories
RUN mkdir -p /var/lib/nvsnap/checkpoints /criu-bundle

# Indicate containerized environment
RUN touch /.dockerenv

# Copy CRIU bundle from builder
COPY --from=criu-builder /criu-bundle/ /criu-bundle/

# Copy Go binaries
COPY --from=go-builder /bin/nvsnap-agent /criu-bundle/nvsnap-agent
COPY --from=go-builder /bin/restore-entrypoint /criu-bundle/restore-entrypoint
COPY --from=go-builder /bin/nvsnap-mount-prep /criu-bundle/nvsnap-mount-prep

# Copy libnvsnap_intercept.so for io_uring/libuv quiescence
COPY --from=intercept-builder /app/libnvsnap_intercept.so /criu-bundle/lib/libnvsnap_intercept.so

# Copy cuda-checkpoint (placed in build context root by build-agent-image.sh)
COPY cuda-checkpoint /criu-bundle/cuda-checkpoint

# Make everything executable
RUN chmod +x /criu-bundle/*

# Add bundled libs to library path
RUN echo "/criu-bundle/lib" > /etc/ld.so.conf.d/criu.conf && ldconfig

# Create convenience symlinks
RUN ln -sf /criu-bundle/nvsnap-agent /usr/local/bin/nvsnap-agent && \
    ln -sf /criu-bundle/criu /usr/local/sbin/criu && \
    ln -sf /criu-bundle/nvsnap-mount-prep /nvsnap-mount-prep

# Set PATH and LD_LIBRARY_PATH
ENV PATH="/criu-bundle:$PATH"
ENV LD_LIBRARY_PATH="/criu-bundle/lib"

# Verify build
RUN echo "=== CRIU version ===" && /criu-bundle/criu --version && \
    echo "=== Agent ===" && /criu-bundle/nvsnap-agent --help 2>&1 | head -3 || true && \
    echo "=== Bundle contents ===" && ls -la /criu-bundle/

EXPOSE 8081

ENTRYPOINT ["/criu-bundle/nvsnap-agent"]
CMD ["--listen", ":8081", \
     "--checkpoint-dir", "/var/lib/nvsnap/checkpoints", \
     "--containerd-socket", "/run/containerd/containerd.sock", \
     "--criu-path", "/criu-bundle/criu", \
     "--cuda-checkpoint-path", "/criu-bundle/cuda-checkpoint", \
     "--log-level", "debug"]
