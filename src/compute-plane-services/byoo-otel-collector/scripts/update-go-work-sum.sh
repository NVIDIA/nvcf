#!/usr/bin/env bash
#
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Regenerate go.work.sum from scratch.
#
# go.work.sum holds the checksums the go.work workspace needs that neither
# member go.sum has. The //otelcol:otelcol-contrib-bin genrule builds in
# workspace mode and declares go.work.sum as an input, so the file is
# committed: a clean checkout must have it, and the build then checks those
# modules against the repo instead of fetching their checksums at build time.
#
# When to run:
#   - After changing go.mod in this directory or in otelcol/, including
#     after scripts/regenerate-otelcol.sh.
#
# CI runs this script and fails if go.work.sum changes.

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${PROJECT_ROOT}"

# Start empty so entries that are no longer needed drop out. The go
# command then adds exactly the checksums the commands below need.
: >go.work.sum

export GOWORK="${PROJECT_ROOT}/go.work"
export GOFLAGS=-mod=readonly
export CGO_ENABLED=0

# The genrule builds otelcol/ for linux/amd64 and linux/arm64. The root
# pattern also covers the wrapper and its tests.
for arch in amd64 arm64; do
  GOOS=linux GOARCH="${arch}" go -C otelcol list -deps . >/dev/null
  GOOS=linux GOARCH="${arch}" go list -deps -test ./... >/dev/null
done

echo "update-go-work-sum: wrote ${PROJECT_ROOT}/go.work.sum"
