#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../../.." && pwd)"
script_path="$repo_root/tools/scripts/check-imports.sh"

if ! command -v yq >/dev/null 2>&1; then
  echo "test-check-imports: SKIP (yq not installed)"
  exit 0
fi

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

upstream="$workdir/upstream"
synthetic="$workdir/synthetic"

mkdir -p "$upstream" "$synthetic"

git -C "$upstream" init -q
git -C "$upstream" config user.name "Test User"
git -C "$upstream" config user.email "test@example.com"

mkdir -p "$upstream/nested"
printf 'hello\n' > "$upstream/README.md"
printf 'world\n' > "$upstream/nested/value.txt"

git -C "$upstream" add README.md nested
git -C "$upstream" commit -q -m "initial import"

commit_sha="$(git -C "$upstream" rev-parse HEAD)"

mkdir -p "$synthetic/demo-service"
(cd "$upstream" && tar cf - --exclude .git .) | (cd "$synthetic/demo-service" && tar xf -)
mkdir -p "$synthetic/native-lib"
printf 'module github.com/NVIDIA/native-lib\n' > "$synthetic/native-lib/go.mod"

cat > "$synthetic/imports.yaml" <<EOF
imports:
  - path: demo-service
    repo: $upstream
    commit: $commit_sha
    authoritative_source: upstream
  - path: native-lib
    authoritative_source: native
EOF

output="$workdir/output.txt"

if ! "$script_path" --root "$synthetic" --manifest "$synthetic/imports.yaml" >"$output" 2>&1; then
  echo "expected checker to succeed on matching import"
  cat "$output"
  exit 1
fi

if ! grep -q "SKIP native-lib authoritative_source=native" "$output"; then
  echo "expected native import roots to be skipped"
  cat "$output"
  exit 1
fi

printf 'drift\n' >> "$synthetic/demo-service/README.md"

if "$script_path" --root "$synthetic" --manifest "$synthetic/imports.yaml" >"$output" 2>&1; then
  echo "expected checker to fail on drifted import"
  exit 1
fi

if ! grep -q "demo-service" "$output"; then
  echo "expected drift output to mention the mismatched import"
  cat "$output"
  exit 1
fi

echo "test-check-imports: PASS"
