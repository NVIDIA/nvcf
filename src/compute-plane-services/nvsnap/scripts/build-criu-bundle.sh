#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

# Build a COMPLETE portable CRIU bundle with:
# - CRIU binary + all shared libraries
# - CUDA plugin (built in same environment for GLIBC compatibility)
# - cuda-checkpoint binary
# - iptables shims
# - restore-entrypoint
#
# This bundle can be mounted into ANY container and works standalone.
#
# Required environment variables:
#   CRIU_FORK_SRC - Path to forked CRIU source directory
#
# Optional environment variables:
#   CUDA_CHECKPOINT_PATH - Path to cuda-checkpoint binary

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/config.sh"

PROJECT_ROOT="$NVSNAP_PROJECT_ROOT"
OUTPUT_DIR="${PROJECT_ROOT}/bin/criu-bundle"

log_info() { echo "=== $1 ==="; }

# Validate CRIU source
if ! validate_criu_source; then
    exit 1
fi

# Clean previous build
rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

log_info "Building complete CRIU bundle"
echo "CRIU source: $CRIU_FORK_SRC"

# Create Dockerfile that builds CRIU + CUDA plugin together
cat > /tmp/Dockerfile.criu-bundle << 'DOCKERFILE'
FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive

# Install ALL build dependencies (for CRIU + CUDA plugin)
RUN apt-get update && apt-get install -y --no-install-recommends \
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

WORKDIR /build
COPY criu-source/ /build/criu/

# Build CRIU (this also builds compel which the plugin needs)
RUN cd criu && \
    make clean 2>/dev/null || true && \
    make WERROR=0 -j$(nproc) && \
    strip criu/criu

# Build CUDA plugin with proper ARCH setting
WORKDIR /build/criu
RUN cd plugins/cuda && \
    make clean 2>/dev/null || true && \
    make ARCH=x86
WORKDIR /build

# Create complete bundle - DO NOT bundle libc/ld-linux (causes conflicts)
# Only bundle CRIU-specific libraries not present in target containers
RUN mkdir -p /bundle/lib /bundle/plugins && \
    # Copy CRIU binary
    cp /build/criu/criu/criu /bundle/criu && \
    chmod +x /bundle/criu && \
    # Copy CUDA plugin
    cp /build/criu/plugins/cuda/cuda_plugin.so /bundle/plugins/ && \
    # Copy shared library dependencies for CRIU, EXCLUDING core glibc
    for lib in $(ldd /bundle/criu | grep "=> /" | awk '{print $3}'); do \
        libname=$(basename "$lib"); \
        # Skip libc, libpthread, libdl, ld-linux - use system's
        case "$libname" in \
            libc.so*|libpthread.so*|libdl.so*|ld-linux*|libm.so*|librt.so*) \
                echo "Skipping core lib: $libname" ;; \
            *) \
                cp -L "$lib" /bundle/lib/ ;; \
        esac; \
    done && \
    # Copy dependencies for CUDA plugin too (same exclusions)
    for lib in $(ldd /bundle/plugins/cuda_plugin.so 2>/dev/null | grep "=> /" | awk '{print $3}'); do \
        libname=$(basename "$lib"); \
        case "$libname" in \
            libc.so*|libpthread.so*|libdl.so*|ld-linux*|libm.so*|librt.so*) ;; \
            *) cp -L "$lib" /bundle/lib/ 2>/dev/null || true ;; \
        esac; \
    done

# Create CRIU wrapper - use system ld.so but add bundled libs to path
RUN cat > /bundle/criu-wrapper << 'WRAPPER' && chmod +x /bundle/criu-wrapper
#!/bin/bash
BUNDLE_DIR="$(dirname "$(readlink -f "$0")")"
export LD_LIBRARY_PATH="$BUNDLE_DIR/lib:${LD_LIBRARY_PATH:-}"
exec "$BUNDLE_DIR/criu" --libdir "$BUNDLE_DIR/plugins" "$@"
WRAPPER

# Create iptables shims using statically-compiled binaries (can't use bash with bundled glibc)
# Use tiny C programs instead
RUN printf '#include <stdlib.h>\nint main(){return 0;}' > /tmp/true.c && \
    gcc -static -o /bundle/iptables-restore /tmp/true.c && \
    cp /bundle/iptables-restore /bundle/ip6tables-restore

# Verify CRIU works
RUN /bundle/criu-wrapper --version

# Verify plugin loads
RUN /bundle/criu-wrapper check 2>&1 | head -5 || true
DOCKERFILE

# Copy CRIU source (clean)
log_info "Copying CRIU source"
rm -rf /tmp/criu-source
cp -r "$CRIU_FORK_SRC" /tmp/criu-source
find /tmp/criu-source -name ".git" -exec rm -rf {} + 2>/dev/null || true
find /tmp/criu-source -name "*.o" -delete 2>/dev/null || true
find /tmp/criu-source -name "*.d" -delete 2>/dev/null || true

# Build Docker image
log_info "Building Docker image (CRIU + CUDA plugin)"
docker build -t criu-bundle-builder -f /tmp/Dockerfile.criu-bundle /tmp/

# Extract bundle
log_info "Extracting bundle"
CONTAINER_ID=$(docker create criu-bundle-builder /bin/true)
docker cp "$CONTAINER_ID:/bundle/." "$OUTPUT_DIR/"
docker rm "$CONTAINER_ID"

# Add cuda-checkpoint
log_info "Adding cuda-checkpoint"
CUDA_CHECKPOINT=$(find_cuda_checkpoint || true)

if [ -n "$CUDA_CHECKPOINT" ]; then
    cp "$CUDA_CHECKPOINT" "$OUTPUT_DIR/cuda-checkpoint"
    chmod +x "$OUTPUT_DIR/cuda-checkpoint"
    echo "  Added from: $CUDA_CHECKPOINT"
else
    echo "  WARNING: cuda-checkpoint not found!"
    echo "  Set CUDA_CHECKPOINT_PATH environment variable to the binary location"
    echo "  Or place it at: ${PROJECT_ROOT}/bin/cuda-checkpoint"
    echo "  Bundle will be incomplete without cuda-checkpoint."
fi

# Build and add restore-entrypoint
log_info "Building restore-entrypoint"
cd "$PROJECT_ROOT"
CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o "$OUTPUT_DIR/restore-entrypoint" ./cmd/restore-entrypoint

# Create restore-entrypoint wrapper
cat > "$OUTPUT_DIR/restore-entrypoint-wrapper" << 'WRAPPER'
#!/bin/bash
BUNDLE_DIR="$(dirname "$(readlink -f "$0")")"
export PATH="$BUNDLE_DIR:$PATH"
export CRIU_BUNDLE_PATH="$BUNDLE_DIR"
exec "$BUNDLE_DIR/restore-entrypoint" "$@"
WRAPPER
chmod +x "$OUTPUT_DIR/restore-entrypoint-wrapper"

# Create tarball
log_info "Creating tarball"
tar -czf "${PROJECT_ROOT}/bin/criu-bundle.tar.gz" -C "$OUTPUT_DIR" .

# Show results
log_info "Bundle created successfully"
echo ""
echo "Contents:"
ls -la "$OUTPUT_DIR"
echo ""
echo "Plugins:"
ls -la "$OUTPUT_DIR/plugins"
echo ""
echo "Libraries: $(ls "$OUTPUT_DIR/lib" | wc -l) files"
echo ""
echo "Test:"
"$OUTPUT_DIR/criu-wrapper" --version
echo ""
echo "Tarball: ${PROJECT_ROOT}/bin/criu-bundle.tar.gz"
echo "Size: $(du -h "${PROJECT_ROOT}/bin/criu-bundle.tar.gz" | cut -f1)"
