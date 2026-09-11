#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

manifest_dir="${1:?rendered manifest directory is required}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
renderer="$script_dir/control-plane-owner-label.sh"
control_plane_owner="${NVCF_CONTROL_PLANE_OWNER:-default}"

fail() {
  echo "apply-control-plane-owner-labels-to-output-dir: $*" >&2
  exit 1
}

owner_for_file() {
  case "$1" in
    */01-dependencies.yaml-*/*)
      printf '%s\n' "shared"
      ;;
    *)
      printf '%s\n' "$control_plane_owner"
      ;;
  esac
}

test -d "$manifest_dir" || fail "$manifest_dir does not exist"
test -x "$renderer" || fail "renderer is not executable: $renderer"

manifest_files=()
while IFS= read -r -d '' file; do
  manifest_files+=("$file")
done < <(find "$manifest_dir" -type f \( -name '*.yaml' -o -name '*.yml' \) -print0 | sort -z)

test "${#manifest_files[@]}" -gt 0 || fail "no YAML manifests found under $manifest_dir"

for file in "${manifest_files[@]}"; do
  owner="$(owner_for_file "$file")"
  tmp_file="$(mktemp)"
  "$renderer" --owner "$owner" <"$file" >"$tmp_file"
  if [[ -s "$tmp_file" ]]; then
    mv "$tmp_file" "$file"
  else
    rm -f "$tmp_file" "$file"
  fi
done

echo "apply-control-plane-owner-labels-to-output-dir: labelled ${#manifest_files[@]} rendered manifest files"
