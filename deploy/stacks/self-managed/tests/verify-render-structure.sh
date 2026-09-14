#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

manifest_dir="${1:?rendered manifest directory is required}"

fail() {
  echo "verify-render-structure: $*" >&2
  exit 1
}

command -v yq >/dev/null 2>&1 || fail "yq is required"
test -d "$manifest_dir" || fail "$manifest_dir does not exist"

if grep -R -n -E '[^[:space:]-]---[[:space:]]*$|^[[:space:]]*---[^[:space:]#-]' "$manifest_dir" >/tmp/nvcf-self-managed-glued-yaml.$$ 2>/dev/null; then
  cat /tmp/nvcf-self-managed-glued-yaml.$$ >&2
  rm -f /tmp/nvcf-self-managed-glued-yaml.$$
  fail "rendered output contains glued YAML separators"
fi
rm -f /tmp/nvcf-self-managed-glued-yaml.$$

manifest_files=()
while IFS= read -r -d '' file; do
  manifest_files+=("$file")
done < <(find "$manifest_dir" -type f \( -name '*.yaml' -o -name '*.yml' \) -print0 | sort -z)

test "${#manifest_files[@]}" -gt 0 || fail "no YAML manifests found under $manifest_dir"

bad_output=""

check_expression() {
  local file="$1"
  local expression="$2"
  local output

  if ! output="$(yq ea -r "$expression" "$file" 2>&1)"; then
    bad_output+="$file: YAML parse failed"$'\n'"$output"$'\n'
    return
  fi
  if test -n "$output"; then
    bad_output+="$file:"$'\n'"$output"$'\n'
  fi
}

for file in "${manifest_files[@]}"; do
  check_expression "$file" '
    select(tag == "!!map") |
    select((.apiVersion == null) or (.kind == null) or (.metadata.name == null)) |
    "missing apiVersion/kind/metadata.name in " +
      ((.kind // "<missing-kind>") | tostring) + "/" +
      ((.metadata.name // "<missing-name>") | tostring)
  '
  check_expression "$file" '
    select(tag == "!!map" and .metadata.name != null) |
    select((.metadata.name | length) > 253) |
    ((.kind // "<missing-kind>") | tostring) + "/" +
      (.metadata.name | tostring) + " metadata.name exceeds 253 characters"
  '
  check_expression "$file" '
    select(tag == "!!map" and .kind == "Namespace" and .metadata.name != null) |
    select((.metadata.name | length) > 63) |
    "Namespace/" + (.metadata.name | tostring) +
      " metadata.name exceeds 63 characters"
  '
  check_expression "$file" '
    select(tag == "!!map" and .metadata.namespace != null) |
    select((.metadata.namespace | length) > 63) |
    ((.kind // "<missing-kind>") | tostring) + "/" +
      ((.metadata.name // "<missing-name>") | tostring) +
      " metadata.namespace exceeds 63 characters: " +
      (.metadata.namespace | tostring)
  '
  check_expression "$file" '
    select(tag == "!!map") as $doc |
    $doc.subjects[]? |
    select(.namespace != null and (.namespace | length) > 63) |
    (($doc.kind // "<missing-kind>") | tostring) + "/" +
      (($doc.metadata.name // "<missing-name>") | tostring) +
      " subjects[].namespace exceeds 63 characters: " +
      (.namespace | tostring)
  '
  check_expression "$file" '
    select(tag == "!!map" and (.kind == "Policy" or .kind == "ClusterPolicy")) as $doc |
    $doc.spec.rules[]? |
    select(.name != null and (.name | length) > 63) |
    (($doc.kind // "<missing-kind>") | tostring) + "/" +
      (($doc.metadata.name // "<missing-name>") | tostring) +
      " spec.rules[].name exceeds 63 characters: " +
      (.name | tostring)
  '
done

if test -n "$bad_output"; then
  printf '%s' "$bad_output" >&2
  fail "rendered manifests failed structural checks"
fi

echo "verify-render-structure: all checks passed"
