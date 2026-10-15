#!/usr/bin/env bash
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
      normalized_path="$(
        printf '%s\n' "$relative_path" |
          sed -E 's#^([^/]+)-[0-9a-f]{8}-#\1-HASH-#'
      )"
      printf '%s  %s\n' "$(git hash-object "$file")" "$normalized_path"
    done |
    sort >"$output_file"
}

index_tree "$expected_dir" "$work_dir/expected"
index_tree "$actual_dir" "$work_dir/actual"
diff -u "$work_dir/expected" "$work_dir/actual"
