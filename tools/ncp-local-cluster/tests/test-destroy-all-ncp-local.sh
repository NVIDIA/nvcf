#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MAKE_SHELL="${MAKE_SHELL:-/bin/sh}"
TEST_DIR="$(mktemp -d)"
FAKE_BIN_DIR="$TEST_DIR/bin"
DELETE_LOG="$TEST_DIR/deleted-clusters"
OUTPUT_FILE="$TEST_DIR/output"

cleanup() {
  rm -rf "$TEST_DIR"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_eq() {
  local expected="$1"
  local actual="$2"
  local label="$3"

  if [ "$actual" != "$expected" ]; then
    fail "${label}: expected '${expected}', got '${actual}'"
  fi
}

mkdir -p "$FAKE_BIN_DIR"

cat >"$FAKE_BIN_DIR/k3d" <<'EOF'
#!/bin/sh
if [ "$#" -eq 4 ] && [ "$1" = "cluster" ] && [ "$2" = "list" ] && [ "$3" = "-o" ] && [ "$4" = "json" ]; then
  printf '%s\n' "${FAKE_K3D_JSON:-[]}"
  exit "${FAKE_K3D_LIST_EXIT:-0}"
fi
if [ "$#" -eq 3 ] && [ "$1" = "cluster" ] && [ "$2" = "delete" ]; then
  printf '%s\n' "$3" >>"$DELETE_LOG"
  exit 0
fi
echo "unexpected k3d arguments: $*" >&2
exit 64
EOF

cat >"$FAKE_BIN_DIR/jq" <<'EOF'
#!/bin/sh
input="$(cat)"
if [ "$#" -ne 2 ] || [ "$1" != "-r" ] || [ "$2" != '.[] | select(.name|startswith("ncp-local")) | .name' ]; then
  echo "unexpected jq arguments: $*" >&2
  exit 64
fi
if [ "$input" != "${FAKE_JQ_EXPECT_INPUT:-[]}" ]; then
  echo "unexpected jq input: $input" >&2
  exit 65
fi
if [ "${FAKE_JQ_EXIT:-0}" -ne 0 ]; then
  exit "$FAKE_JQ_EXIT"
fi
printf '%b' "${FAKE_JQ_OUTPUT:-}"
EOF

chmod +x "$FAKE_BIN_DIR/k3d" "$FAKE_BIN_DIR/jq"

run_make() {
  PATH="$FAKE_BIN_DIR:$PATH" \
    DELETE_LOG="$DELETE_LOG" \
    make --no-print-directory -s -C "$ROOT_DIR" \
      SHELL="$MAKE_SHELL" destroy-all-ncp-local
}

: >"$DELETE_LOG"
if ! FAKE_K3D_JSON='[]' FAKE_JQ_EXPECT_INPUT='[]' run_make >"$OUTPUT_FILE" 2>&1; then
  cat "$OUTPUT_FILE" >&2
  fail "empty cluster inventory should succeed under $MAKE_SHELL"
fi
assert_eq "No ncp-local* clusters present." "$(cat "$OUTPUT_FILE")" "empty inventory output"
assert_eq "" "$(cat "$DELETE_LOG")" "empty inventory deletion log"

: >"$DELETE_LOG"
cluster_json='[{"name":"ncp-local"},{"name":"unrelated"},{"name":"ncp-local-cp"}]'
FAKE_K3D_JSON="$cluster_json" \
  FAKE_JQ_EXPECT_INPUT="$cluster_json" \
  FAKE_JQ_OUTPUT='ncp-local\nncp-local-cp\n' \
  run_make >"$OUTPUT_FILE" 2>&1 || {
    cat "$OUTPUT_FILE" >&2
    fail "matching cluster inventory should succeed"
  }
assert_eq $'ncp-local\nncp-local-cp' "$(cat "$DELETE_LOG")" "deleted cluster names"

: >"$DELETE_LOG"
if FAKE_K3D_LIST_EXIT=23 FAKE_JQ_EXPECT_INPUT='[]' run_make >"$OUTPUT_FILE" 2>&1; then
  cat "$OUTPUT_FILE" >&2
  fail "k3d cluster list failure should propagate"
fi
assert_eq "" "$(cat "$DELETE_LOG")" "k3d failure deletion log"

: >"$DELETE_LOG"
if FAKE_K3D_JSON='[]' FAKE_JQ_EXPECT_INPUT='[]' FAKE_JQ_EXIT=24 run_make >"$OUTPUT_FILE" 2>&1; then
  cat "$OUTPUT_FILE" >&2
  fail "jq failure should propagate"
fi
assert_eq "" "$(cat "$DELETE_LOG")" "jq failure deletion log"

echo "PASS: destroy-all-ncp-local Makefile tests"
