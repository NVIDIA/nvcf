#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../../.." && pwd)"
script="$repo_root/tools/workspace_status.sh"

fail() { echo "test-workspace-status: FAIL: $*" >&2; exit 1; }
stable_version() { awk '/^STABLE_VERSION /{print $2}'; }

# NVCF_VERSION, when set, is used verbatim.
got="$( (cd "$repo_root" && env NVCF_VERSION=1.2.3 bash "$script") | stable_version )"
[ "$got" = "1.2.3" ] || fail "NVCF_VERSION=1.2.3 -> '$got' (want 1.2.3)"

# Without NVCF_VERSION the version is mr-<short-sha>, and a path-prefixed tag on
# HEAD must NOT leak in. Use a deterministic fixture that has exactly such a tag.
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT
git -C "$fixture" init -q
git -C "$fixture" config user.name "Test User"
git -C "$fixture" config user.email "test@example.com"
git -C "$fixture" config commit.gpgsign false
printf 'x\n' > "$fixture/f"
git -C "$fixture" add -A
git -C "$fixture" commit -qm init
git -C "$fixture" tag "deploy/stacks/example/v9.9.9"   # path-prefixed tag on HEAD

sha="$(git -C "$fixture" rev-parse --short HEAD)"
got="$( (cd "$fixture" && env -u NVCF_VERSION bash "$script") | stable_version )"
[ "$got" = "mr-${sha}" ] || fail "path-prefixed tag at HEAD -> '$got' (want mr-${sha})"

echo "test-workspace-status: PASS"
