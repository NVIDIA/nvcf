# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# uvloop wheel builder for NvSnap — multi-Python (cp310/311/312/313)
#
# Builds our forked uvloop with CRIU checkpoint/restore support
# (uv_loop_fork() inside C-level uv_run, reinit libuv after restore).
#
# Builder base: quay.io/pypa/manylinux_2_28_x86_64.
#   - AlmaLinux 8 → GLIBC 2.28
#   - ships cp310 / cp311 / cp312 / cp313 under /opt/python/cp3XX-cp3XX/
#   - gcc-toolset-14, autotools, libtool already installed
#
# Output: 4 wheels at /wheels/ named
#   uvloop-<ver>-cpXY-cpXY-linux_x86_64.whl   for XY in 310, 311, 312, 313
#
# Wheels built against GLIBC 2.28 run forward on the GLIBC 2.35 hosts
# we deploy on. arm64 is deferred (see docs/GENERIC-PYTHON-INJECTION-DESIGN.md).
#
# Build context: the uvloop fork source directory
#   docker build -t uvloop-builder:TAG -f docker/uvloop/Dockerfile /path/to/uvloop

FROM quay.io/pypa/manylinux_2_28_x86_64 AS builder

COPY . /uvloop-src
WORKDIR /uvloop-src

# Loop over the four interpreters. For each:
#   - wipe Cython output so loop.c is regenerated for that Python
#   - install matching setuptools / wheel / Cython (no build isolation)
#   - pip wheel writes uvloop-*-cpXY-cpXY-linux_x86_64.whl into /wheels/
RUN set -eux; \
    for py in /opt/python/cp310-cp310 \
              /opt/python/cp311-cp311 \
              /opt/python/cp312-cp312 \
              /opt/python/cp313-cp313; do \
        echo "=== uvloop build: $($py/bin/python --version) ==="; \
        rm -rf build/ dist/ *.egg-info .eggs/ uvloop/loop.c; \
        "$py/bin/pip" install --no-cache-dir "setuptools>=60,<80" wheel "Cython>=3.1,<3.2"; \
        "$py/bin/pip" wheel . -w /wheels/ --no-build-isolation; \
    done

# Smoke test: each wheel must import under its matching interpreter.
# `cd /tmp` to avoid importing the source tree rather than the installed wheel.
RUN set -eux; \
    for py in /opt/python/cp310-cp310 \
              /opt/python/cp311-cp311 \
              /opt/python/cp312-cp312 \
              /opt/python/cp313-cp313; do \
        tag=$(basename "$py" | cut -d- -f1); \
        whl=$(ls /wheels/uvloop-*-${tag}-${tag}-*.whl); \
        echo "=== smoke test $whl under $tag ==="; \
        "$py/bin/pip" install --force-reinstall --no-deps "$whl"; \
        (cd /tmp && "$py/bin/python" -c "import uvloop, sys; print(f'  {sys.version_info.major}.{sys.version_info.minor}: uvloop', uvloop.__version__, 'OK')"); \
    done

# Wheel count gate — fail loud if any interpreter silently produced nothing
RUN test "$(ls /wheels/uvloop-*.whl | wc -l)" -eq 4 || (ls -la /wheels/; echo 'expected 4 uvloop wheels'; exit 1)

# Output stage — wheels only, small image
FROM python:3.12-slim
COPY --from=builder /wheels/ /wheels/
RUN ls -la /wheels/uvloop-*.whl
CMD ["/bin/bash"]
