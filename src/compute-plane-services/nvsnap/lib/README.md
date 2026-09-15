<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# lib/

Non-Go runtime pieces that ship inside workload pods (not the agent).

## Contents

- [`nvsnap_intercept/`](nvsnap_intercept/) — **libnvsnap_intercept.so**, the
  `LD_PRELOAD` C library that reinitializes io_uring (uvloop/libuv) and libzmq
  epoll after a CRIU restore. See its [README](nvsnap_intercept/README.md).
- [`nvsnap_restore_helper/`](nvsnap_restore_helper/) — small C restore helper
  bundled alongside the intercept library.
- [`sitecustomize/`](sitecustomize/) — `sitecustomize.py`; Python's `site.py`
  auto-imports it to prepend the patched-uvloop site-packages onto `sys.path`
  at runtime (no edits to the workload image). See
  [docs/GENERIC-PYTHON-INJECTION-DESIGN.md](../docs/GENERIC-PYTHON-INJECTION-DESIGN.md).

## Rules

These are injected into unmodified workload containers via init containers +
env (`LD_PRELOAD`, `PYTHONPATH`) — never by modifying the application image.
The C library must link against the same glibc as the workloads it preloads
into (built on ubuntu:22.04; see [CONTRIBUTING.md](../CONTRIBUTING.md)).
