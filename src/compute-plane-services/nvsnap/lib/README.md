<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# lib/

Non-Go runtime pieces that ship inside workload pods (not the agent).

## Contents

- [`nvsnap_restore_helper/`](nvsnap_restore_helper/) — small C restore helper.

The `LD_PRELOAD` interception library, its Python `sitecustomize` shim, and the
patched uvloop/libuv/libzmq that went with them were removed once criu-v2
replaced the approach. criu-v2 handles the workload at the OS level and injects
nothing into it. The implementation is preserved at the tag
`archive/nvsnap-injection-stack`.

## Rules

What ships here runs inside workload pods, never by modifying the application
image. C pieces must link against the same glibc as the workloads they run in
(built on ubuntu:22.04; see [CONTRIBUTING.md](../CONTRIBUTING.md)).
