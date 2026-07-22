# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Combined init container for NvSnap restore pods.
# Replaces 5 separate init containers (uvloop, libuv, libzmq, pyzmq, criu)
# with a single container that copies all dependencies in one step.
#
# Build: docker build -t nvsnap-init:VERSION --build-arg AGENT_IMAGE=... -f docker/init/Dockerfile .

ARG UVLOOP_IMAGE=nvcr.io/0651155215864979/ncp-dev/uvloop-builder:v0.0.1
ARG LIBUV_IMAGE=nvcr.io/0651155215864979/ncp-dev/libuv-builder:v0.0.1
ARG LIBZMQ_IMAGE=nvcr.io/0651155215864979/ncp-dev/libzmq-builder:v0.0.1
ARG PYZMQ_IMAGE=nvcr.io/0651155215864979/ncp-dev/pyzmq-builder:v0.0.1
ARG AGENT_IMAGE=nvcr.io/0651155215864979/ncp-dev/nvsnap-agent:v0.0.1

# Stage 1-4: Pull artifacts from builder images
FROM ${UVLOOP_IMAGE} AS uvloop
FROM ${LIBUV_IMAGE} AS libuv
FROM ${LIBZMQ_IMAGE} AS libzmq
FROM ${PYZMQ_IMAGE} AS pyzmq
FROM ${AGENT_IMAGE} AS agent

# Stage 5: Combine everything into a minimal image
FROM ubuntu:22.04

# uvloop wheel -> site-packages
COPY --from=uvloop /wheels/ /staging/wheels/uvloop/
# libuv shared libs
COPY --from=libuv /usr/local/lib/libuv.so* /staging/lib/
# libzmq shared libs
COPY --from=libzmq /usr/local/lib/libzmq.so* /staging/lib/
# pyzmq wheel -> site-packages
COPY --from=pyzmq /wheels/ /staging/wheels/pyzmq/
# criu bundle (agent binaries, CRIU, intercept lib, cuda-checkpoint)
COPY --from=agent /criu-bundle/ /staging/criu-bundle/
# py-spy (optional debug tool)
COPY --from=agent /usr/local/bin/py-spy /staging/criu-bundle/py-spy

# Pre-extract wheels so init just copies dirs. Loop because the multi-
# python uvloop builder produces multiple wheels (cp310/311/312/313)
# and `python3 -m zipfile -e` only accepts a single input file. Same
# target dir for all wheels is fine — Python picks the right
# cpython-<ver>-*.so at runtime by interpreter tag.
RUN apt-get update && apt-get install -y --no-install-recommends python3 && rm -rf /var/lib/apt/lists/* && \
    mkdir -p /staging/site-packages && \
    for w in /staging/wheels/uvloop/uvloop-*.whl; do python3 -m zipfile -e "$w" /staging/site-packages/; done && \
    for w in /staging/wheels/pyzmq/pyzmq-*.whl; do python3 -m zipfile -e "$w" /staging/site-packages/; done && \
    rm -rf /staging/wheels

# The init entrypoint copies everything to the shared volumes
COPY docker/init/init.sh /init.sh
RUN chmod +x /init.sh

ENTRYPOINT ["/init.sh"]
