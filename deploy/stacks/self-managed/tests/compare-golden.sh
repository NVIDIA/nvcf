#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

expected_dir="${1:?expected golden directory is required}"
actual_dir="${2:?actual manifest directory is required}"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

index_tree() {
  local root_dir="$1"
  local output_file="$2"

  find "$root_dir" -type f -print |
    while IFS= read -r file; do
      relative_path="${file#"$root_dir"/}"
      printf '%s  %s\n' "$(git hash-object "$file")" "$relative_path"
    done |
    sort >"$output_file"
}

index_tree "$expected_dir" "$work_dir/expected"
index_tree "$actual_dir" "$work_dir/actual"
diff -u "$work_dir/expected" "$work_dir/actual"
