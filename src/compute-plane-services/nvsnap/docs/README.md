<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# nvsnap documentation

Start with the top-level [README](../README.md) for what nvsnap is and a
benchmark summary. This directory holds the operator and developer docs.

## Getting started

- [Getting started (contributors)](dev/getting-started.md) — build → deploy →
  capture → validate → merge, end to end.
- [Install](INSTALL.md) — deploy nvsnap on a cluster (not from source).
- [Pull-secret setup](PULL-SECRET-SETUP.md) — registry credentials.
- [Quick reference](QUICK-REFERENCE.md) — components, CRDs, common commands.

## Using nvsnap

- [Example: REST API client](../examples/checkpoint-restore/) — a dependency-free
  Go client that drives checkpoint → poll → list → restore → delete.
- [OpenAPI spec](../internal/server/openapi.yaml) — the server API contract.

## Architecture

- [System architecture](architecture/NVSNAP-ARCHITECTURE.md)
- [Runtime-agnostic discovery](architecture/04-RUNTIME-AGNOSTIC.md) — `/proc` +
  cgroup process discovery.
- [cuda-checkpoint call flow](architecture/06-CUDA-CHECKPOINT-CALL-FLOW.md)
- [Quiescence / io_uring / libuv](architecture/QUIESCENCE-ARCHITECTURE.md)
- [vLLM multi-process deep dive](architecture/VLLM-DEEP-DIVE.md)
- [Data transport (L1/L2/L3)](TRANSPORT-ARCHITECTURE.md)

## Design notes & reference

- [Third-party forks + fork-maintenance policy](THIRD-PARTY-FORKS.md)
- [Benchmarks](BENCHMARK.md) · [PDF benchmark matrix](PDF-BENCH-RESULTS.md)
- [Generic Python injection](GENERIC-PYTHON-INJECTION-DESIGN.md)
- [L2 per-capture PVC (CRIU)](L2-PVC-CRIU-DESIGN.md)
- [Multi-GPU rootfs fan-out](MULTI-GPU-ROOTFS-FANOUT-DESIGN.md)

## Contributing & policy

- [CONTRIBUTING](../CONTRIBUTING.md) — dev setup, build, PR workflow.
- [Security policy](../SECURITY.md) · [Code of conduct](../CODE_OF_CONDUCT.md)

Superseded designs and project history live in [archive/](archive/) — historical
context, not authoritative for the current code.
