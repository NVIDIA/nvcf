#!/usr/bin/env bash
set -euo pipefail

expected_dir="${1:?expected golden directory is required}"
actual_dir="${2:?actual manifest directory is required}"

printf '>>> Comparing %s against golden %s...\n' "$actual_dir" "$expected_dir"
if ! "$(dirname "$0")/compare-golden.sh" "$expected_dir" "$actual_dir"; then
  printf '\n'
  printf 'ERROR: rendered output in %s differs from golden %s.\n' "$actual_dir" "$expected_dir"
  printf '       Review the diff above. If the change is intentional, refresh the golden files:\n'
  printf '         make generate-golden\n'
  exit 1
fi
printf '>>> Local render matches golden %s.\n' "$expected_dir"
