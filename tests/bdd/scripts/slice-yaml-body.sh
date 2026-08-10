#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Read stdin, write to stdout the lines from `clusterID:` onward.
#
# The EKS Helmfile feature uses this to extract just the YAML body
# from `nvcf-cli cluster register`'s mixed stdout (status logs and
# YAML body share the same stream -- a known CLI limitation). When
# the CLI honors --json or routes the preamble to stderr, this
# helper goes away.
#
# Kept as a one-line helper because the BDD runner uses
# shlex + exec.Command rather than a shell, so the slice cannot be
# expressed inline in `When I run command`. The feature pipes
# nvcf-cli output through this helper inside a single `bash -c`
# invocation.

set -euo pipefail
exec sed -n '/^clusterID:/,$p'
