#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
# shellcheck source=infra/openbao/scripts/semver.sh
. "$script_dir/semver.sh"

assert_ge() {
  if ! semver_ge "$1" "$2"; then
    echo "expected $1 to satisfy $2" >&2
    exit 1
  fi
}

assert_lt() {
  if semver_ge "$1" "$2"; then
    echo "expected $1 not to satisfy $2" >&2
    exit 1
  fi
}

assert_ge v0.56.0 v0.56.0
assert_ge v0.57.0 v0.56.0
assert_ge v0.56.0+incompatible v0.56.0
assert_ge v0.56.1-rc.1 v0.56.0
assert_lt v0.56.0-rc.1 v0.56.0
assert_lt v0.55.9 v0.56.0
assert_ge v0.0.0-20260905120000-bbbbbbbbbbbb v0.0.0-20260904120000-aaaaaaaaaaaa
assert_lt v0.0.0-20260903120000-bbbbbbbbbbbb v0.0.0-20260904120000-aaaaaaaaaaaa
assert_lt v0.0.0-20260904120000-aaaaaaaaaaaa v0.56.0

echo "semantic version comparisons passed"
