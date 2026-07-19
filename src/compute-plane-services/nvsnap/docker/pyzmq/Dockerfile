# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# pyzmq wheel builder for NVSNAP
#
# Builds pyzmq linked against our patched libzmq (not bundled stock libzmq).
# The patched libzmq has CRIU checkpoint/restore support.
#
# Build context must contain:
#   pyzmq-src/   - the pyzmq fork source
#   libzmq-src/  - the libzmq fork source (built first, then pyzmq links against it)
#
# Build:
#   BUILD_CTX=$(mktemp -d)
#   rsync -a --exclude='.git' --exclude='build/' /path/to/pyzmq/ $BUILD_CTX/pyzmq-src/
#   rsync -a --exclude='.git' --exclude='build/' /path/to/libzmq/ $BUILD_CTX/libzmq-src/
#   docker build -t pyzmq-builder:TAG -f docker/pyzmq/Dockerfile $BUILD_CTX
#
# Output: wheel at /wheels/pyzmq-*.whl
# IMPORTANT: PYZMQ_NO_BUNDLE=1 ensures pyzmq links against system libzmq
# (our patched version), not a bundled copy.

# Build stage
FROM vllm/vllm-openai:v0.11.2 AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    cmake \
    pkg-config \
    libsodium-dev \
    && rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir "cython>=3.0.0" "packaging" "scikit-build-core>=0.10"

# Build patched libzmq first (pyzmq links against it)
COPY libzmq-src/ /libzmq-src/
WORKDIR /libzmq-src
RUN rm -rf build/ && mkdir -p build && cd build && \
    cmake -DCMAKE_INSTALL_PREFIX=/usr/local \
          -DCMAKE_BUILD_TYPE=Release \
          -DBUILD_TESTS=OFF \
          -DENABLE_DRAFTS=ON \
          .. && \
    make -j$(nproc) && \
    make install && \
    ldconfig

# Build pyzmq wheel linked against our libzmq
COPY pyzmq-src/ /pyzmq-src/
WORKDIR /pyzmq-src
RUN rm -rf build/ dist/ *.egg-info

# PYZMQ_NO_BUNDLE=1: don't bundle stock libzmq, link against system (ours)
# ZMQ_PREFIX: tell build where to find our libzmq
RUN ZMQ_PREFIX=/usr/local PYZMQ_NO_BUNDLE=1 \
    pip wheel . -w /wheels/ --no-build-isolation

RUN ls -la /wheels/pyzmq-*.whl || (echo "ERROR: No pyzmq wheel built"; exit 1)

# Final image: just the wheel
FROM python:3.12-slim
COPY --from=builder /wheels/ /wheels/

# Smoke test: install and import
RUN pip install /wheels/pyzmq-*.whl && \
    python3 -c "import zmq; print(f'pyzmq {zmq.__version__} zmq {zmq.zmq_version()} OK')" && \
    pip uninstall -y pyzmq

CMD ["/bin/bash"]
