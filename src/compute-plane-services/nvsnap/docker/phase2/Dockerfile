# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP: GPU Checkpoint/Restore Container
#
# Production-ready container for GPU workload migration
# Includes: CRIU (forked, built from source), cuda-checkpoint, scripts
#
# Build:
#   ./scripts/build-container.sh [version]

#############################################
# Stage 1: Build CRIU from source
#############################################
FROM nvidia/cuda:12.4.0-devel-ubuntu22.04 AS criu-builder

ENV DEBIAN_FRONTEND=noninteractive

# Install CRIU build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    pkg-config \
    libprotobuf-dev \
    libprotobuf-c-dev \
    protobuf-compiler \
    protobuf-c-compiler \
    python3-protobuf \
    libcap-dev \
    libnl-3-dev \
    libnl-route-3-dev \
    libnet1-dev \
    libaio-dev \
    uuid-dev \
    python3 \
    git \
    && rm -rf /var/lib/apt/lists/*

# Copy forked CRIU source and build
WORKDIR /build
COPY criu-source/ /build/criu/
RUN cd /build/criu && \
    make clean || true && \
    make -j$(nproc) && \
    cp criu/criu /usr/local/bin/criu

#############################################
# Stage 2: Build test program
#############################################
FROM nvidia/cuda:12.4.0-devel-ubuntu22.04 AS app-builder

# Create test programs
WORKDIR /app
RUN cat > /app/gpu_loop.cu << 'EOF' && \
    nvcc -o /app/gpu_loop /app/gpu_loop.cu -lcuda
#include <cuda_runtime.h>
#include <stdio.h>
#include <unistd.h>
#include <signal.h>

volatile sig_atomic_t running = 1;
volatile sig_atomic_t checkpoint_requested = 0;

void sigterm_handler(int sig) { running = 0; }
void sigusr1_handler(int sig) { checkpoint_requested = 1; }

int main() {
    signal(SIGTERM, sigterm_handler);
    signal(SIGINT, sigterm_handler);
    signal(SIGUSR1, sigusr1_handler);
    
    float *d_data;
    size_t size = 256 * 1024 * 1024;
    cudaError_t err = cudaMalloc(&d_data, size);
    if (err != cudaSuccess) {
        printf("cudaMalloc failed: %s\n", cudaGetErrorString(err));
        return 1;
    }
    printf("Allocated 256MB GPU memory at %p\n", (void*)d_data);
    
    size_t n_floats = size / sizeof(float);
    float *h_data = (float*)malloc(size);
    for (size_t i = 0; i < n_floats; i++) {
        h_data[i] = (float)(i % 12345);
    }
    cudaMemcpy(d_data, h_data, size, cudaMemcpyHostToDevice);
    printf("GPU memory initialized with pattern (checksum base: 12345)\n");
    
    int iter = 0;
    while (running) {
        if (checkpoint_requested) {
            printf("SIGUSR1 received - ready for checkpoint!\n");
            checkpoint_requested = 0;
        }
        printf("GPU test running... iteration %d (PID: %d, GPU ptr: %p)\n", 
               iter++, getpid(), (void*)d_data);
        fflush(stdout);
        sleep(5);
    }
    
    float *h_verify = (float*)malloc(size);
    cudaMemcpy(h_verify, d_data, size, cudaMemcpyDeviceToHost);
    int errors = 0;
    for (size_t i = 0; i < n_floats && errors < 10; i++) {
        float expected = (float)(i % 12345);
        if (h_verify[i] != expected) {
            printf("MISMATCH at %zu: got %f, expected %f\n", i, h_verify[i], expected);
            errors++;
        }
    }
    if (errors == 0) {
        printf("Data integrity verified - all %zu floats correct!\n", n_floats);
    }
    
    free(h_verify);
    free(h_data);
    cudaFree(d_data);
    printf("Cleanup complete. Exiting.\n");
    return errors > 0 ? 1 : 0;
}
EOF

#############################################
# Stage 3: Runtime
#############################################
FROM nvidia/cuda:12.4.0-base-ubuntu22.04

ARG VERSION=dev
ARG BUILD_DATE=unknown
ARG GIT_COMMIT=unknown

LABEL maintainer="balaji@nvidia.com"
LABEL description="NVSNAP - GPU Checkpoint/Restore for Kubernetes"
LABEL version="${VERSION}"

ENV DEBIAN_FRONTEND=noninteractive

# Install runtime dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    libprotobuf-c1 \
    libnl-3-200 \
    libnl-route-3-200 \
    libnet1 \
    libaio1 \
    iproute2 \
    iptables \
    procps \
    && rm -rf /var/lib/apt/lists/*

# Copy CRIU from builder
COPY --from=criu-builder /usr/local/bin/criu /usr/local/bin/criu
RUN chmod +x /usr/local/bin/criu

# Copy CRIU plugins (especially cuda_plugin.so)
COPY --from=criu-builder /build/criu/plugins/cuda/cuda_plugin.so /app/plugins/cuda_plugin.so
RUN chmod +x /app/plugins/cuda_plugin.so || true

# Create criu-wrapper script that sets up paths for the CUDA plugin
RUN mkdir -p /app && \
    printf '#!/bin/bash\nexec /usr/local/bin/criu --libdir /app/plugins "$@"\n' > /app/criu-wrapper && \
    chmod +x /app/criu-wrapper

# Copy cuda-checkpoint
COPY cuda-checkpoint /usr/local/bin/cuda-checkpoint
RUN chmod +x /usr/local/bin/cuda-checkpoint || true

# Copy nvsnap-agent binary
COPY nvsnap-agent /app/bin/nvsnap-agent
RUN chmod +x /app/bin/nvsnap-agent
# Also add cuda-checkpoint to /app/bin so it's in PATH for the agent
RUN mkdir -p /app/bin && cp /usr/local/bin/cuda-checkpoint /app/bin/cuda-checkpoint

# Copy restore-entrypoint if present
COPY restore-entrypoint /app/bin/restore-entrypoint
RUN chmod +x /app/bin/restore-entrypoint || true

# Copy test program
COPY --from=app-builder /app/gpu_loop /app/gpu_loop

# Create checkpoint directory
RUN mkdir -p /checkpoints

# Copy scripts
WORKDIR /app
COPY checkpoint.sh /app/checkpoint.sh
COPY restore.sh /app/restore.sh
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/*.sh

# Verify
RUN echo "=== NVSNAP Container ===" && \
    criu --version && \
    ls -la /app/gpu_loop && \
    (cuda-checkpoint --help 2>&1 | head -1 || echo "cuda-checkpoint: present") && \
    echo "=== Ready ==="

ENTRYPOINT ["/app/entrypoint.sh"]
CMD ["/app/gpu_loop"]
