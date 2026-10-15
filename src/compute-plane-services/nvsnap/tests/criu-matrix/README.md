<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# CRIU test matrix workloads

`chart/` deploys one inference workload in a chosen shape, as an NVCF Helm
function or with plain Helm. `cases/` holds one values file per matrix case.
The matrix, the run procedure and the expected results are in
[docs/CRIU-TEST-MATRIX.md](../../docs/CRIU-TEST-MATRIX.md).
