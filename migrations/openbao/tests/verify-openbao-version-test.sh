#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
verifier="$script_dir/../scripts/verify-openbao.sh"

expect_ge() {
  current=$1
  required=$2
  if ! "$verifier" --version-ge "$current" "$required"; then
    echo "expected $current to satisfy $required" >&2
    exit 1
  fi
}

expect_lt() {
  current=$1
  required=$2
  if "$verifier" --version-ge "$current" "$required"; then
    echo "expected $current not to satisfy $required" >&2
    exit 1
  fi
}

expect_invalid() {
  current=$1
  required=$2
  if "$verifier" --version-ge "$current" "$required"; then
    status=0
  else
    status=$?
  fi
  if [ "$status" -ne 2 ]; then
    echo "expected invalid comparison $current against $required to exit 2, got $status" >&2
    exit 1
  fi
}

expect_ge v1.83.1 v1.83.1
expect_ge v1.83.2-0.20260905120000-deadbeef v1.83.1
expect_ge v0.0.0-20260905120000-deadbeef v0.0.0-20260904120000-feedface
expect_ge v1.83.1-rc.10 v1.83.1-rc.2

expect_lt v1.83.1-rc.1 v1.83.1
expect_lt v1.83.1-0.20260905120000-deadbeef v1.83.1
expect_lt v1.83.1-rc.2 v1.83.1-rc.10

expect_invalid v1.83.1- v1.83.1
expect_invalid v1.83.1+ v1.83.1
expect_invalid v1.83.1-rc..1 v1.83.1
expect_invalid v1.83.1+build..1 v1.83.1
expect_invalid v1.83.1-rc.01 v1.83.1
expect_invalid v01.83.1 v1.83.1

echo "OpenBao dependency version comparisons passed"
