# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Use Ubuntu 22.04 to match CRIU's glibc 2.35
FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive

# Install Python 3.12 from deadsnakes PPA
RUN apt-get update && apt-get install -y \
    software-properties-common \
    && add-apt-repository -y ppa:deadsnakes/ppa \
    && apt-get update \
    && apt-get install -y \
        build-essential \
        autoconf \
        automake \
        libtool \
        pkg-config \
        python3.12 \
        python3.12-venv \
        python3.12-dev \
        python3-pip \
    && rm -rf /var/lib/apt/lists/*

# Make python3.12 the default
RUN update-alternatives --install /usr/bin/python3 python3 /usr/bin/python3.12 1 \
    && update-alternatives --install /usr/bin/python python /usr/bin/python3.12 1

# Install pip for Python 3.12
RUN python3.12 -m ensurepip --upgrade && python3.12 -m pip install --upgrade pip

ENV CFLAGS="-O0 -g"

# Build uvloop from local source with patched libuv
COPY uvloop /src/uvloop
RUN python3.12 -m pip install --no-cache-dir /src/uvloop aiohttp

WORKDIR /app
COPY nvsnap/tests/uvloop-mp-test/app.py .
COPY nvsnap/tests/uvloop-mp-test/app_simple.py .
COPY nvsnap/tests/uvloop-mp-test/sitecustomize.py .

# Expose port
EXPOSE 8000

# Use simple app (no multiprocessing) for testing
CMD ["python3.12", "app_simple.py"]
