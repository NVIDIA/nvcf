#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu -o pipefail

# Small convenience script for running the tests with various combinations of
# arch/tags. This assumes we're running on amd64 and have qemu available.

go test ./...
go test -tags purego ./...
GOARCH=arm64 go test
GOARCH=arm64 go test -tags purego
