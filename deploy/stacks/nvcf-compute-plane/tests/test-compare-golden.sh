#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

expected_dir="$work_dir/expected"
actual_dir="$work_dir/actual"
mkdir -p "$expected_dir/nested" "$actual_dir/nested"

printf 'key: value\n\n' >"$expected_dir/nested/manifest.yaml"
printf 'key: value  \n \t\n' >"$actual_dir/nested/manifest.yaml"
"$script_dir/compare-golden.sh" "$expected_dir" "$actual_dir"

printf 'key: changed\n' >"$actual_dir/nested/manifest.yaml"
if "$script_dir/compare-golden.sh" "$expected_dir" "$actual_dir" >/dev/null; then
  echo "expected semantic content changes to fail golden comparison" >&2
  exit 1
fi

echo "compare-golden: all checks passed"
