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
trap 'rm -rf "$TEST_DIR"' EXIT
export CLUSTER_STATE="$TEST_DIR/clusters.json"
export DELETE_LOG="$TEST_DIR/deleted"
export LIST_COUNT="$TEST_DIR/list-count"
mkdir "$TEST_DIR/bin"

fail() { echo "FAIL: $*" >&2; exit 1; }
assert_eq() { [ "$1" = "$2" ] || fail "$3: expected '$1', got '$2'"; }

# Keep jq real: malformed inventory must fail before any deletion.
cat >"$TEST_DIR/bin/k3d" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  'cluster list -o json')
    count=$(cat "$LIST_COUNT")
    count=$((count + 1))
    printf '%s\n' "$count" >"$LIST_COUNT"
    [ "$count" != "${FAIL_LIST_AT:-0}" ] || exit 23
    cat "$CLUSTER_STATE"
    ;;
  cluster\ get\ *)
    jq -e --arg name "$3" 'any(.[]; .name == $name)' "$CLUSTER_STATE" >/dev/null
    ;;
  cluster\ delete\ *)
    printf '%s\n' "$3" >>"$DELETE_LOG"
    [ "$3" != "${FAIL_DELETE:-}" ] || exit 24
    [ "$3" != "${LEAVE_CLUSTER:-}" ] || exit 0
    jq --arg name "$3" 'map(select(.name != $name))' "$CLUSTER_STATE" >"$CLUSTER_STATE.next"
    mv "$CLUSTER_STATE.next" "$CLUSTER_STATE"
    ;;
  *) echo "unexpected k3d arguments: $*" >&2; exit 64 ;;
esac
EOF
chmod +x "$TEST_DIR/bin/k3d"
export PATH="$TEST_DIR/bin:$PATH"

reset_state() {
  printf '%s\n' "$1" >"$CLUSTER_STATE"
  printf '0\n' >"$LIST_COUNT"
  : >"$DELETE_LOG"
  unset FAIL_LIST_AT FAIL_DELETE LEAVE_CLUSTER
}
run_make() {
  make --no-print-directory -s -C "$ROOT_DIR" SHELL="$MAKE_SHELL" "$@" >"$TEST_DIR/output" 2>&1
}
expect_failure() {
  if run_make "$@"; then cat "$TEST_DIR/output" >&2; fail "$* should fail"; fi
  if grep -q 'Cluster .* destroyed.' "$TEST_DIR/output"; then
    cat "$TEST_DIR/output" >&2; fail "failure must not report successful deletion"
  fi
}

reset_state '[]'
run_make destroy CLUSTER_NAME=ncp-local
assert_eq '' "$(cat "$DELETE_LOG")" 'absent exact cluster'
run_make destroy-all-ncp-local
assert_eq 'No ncp-local* clusters present.' "$(cat "$TEST_DIR/output")" 'empty inventory'

inventory='[{"name":"ncp-local"},{"name":"unrelated"},{"name":"ncp-local-cp"}]'
reset_state "$inventory"
run_make destroy CLUSTER_NAME=ncp-local
assert_eq 'ncp-local' "$(cat "$DELETE_LOG")" 'exact deletion'
assert_eq $'unrelated\nncp-local-cp' "$(jq -r '.[].name' "$CLUSTER_STATE")" 'exact match preserves other clusters'

reset_state "$inventory"
run_make destroy-all-ncp-local
assert_eq $'ncp-local\nncp-local-cp' "$(cat "$DELETE_LOG")" 'prefix deletion'
assert_eq 'unrelated' "$(jq -r '.[].name' "$CLUSTER_STATE")" 'unrelated cluster preserved'

for target in destroy destroy-all-ncp-local; do
  reset_state "$inventory"
  export FAIL_LIST_AT=1
  expect_failure "$target" CLUSTER_NAME=ncp-local
  assert_eq '' "$(cat "$DELETE_LOG")" 'inventory failure prevents deletion'

  reset_state 'invalid-json'
  expect_failure "$target" CLUSTER_NAME=ncp-local
  assert_eq '' "$(cat "$DELETE_LOG")" 'invalid JSON prevents deletion'

  reset_state "$inventory"
  export FAIL_DELETE=ncp-local
  expect_failure "$target" CLUSTER_NAME=ncp-local
  assert_eq 'ncp-local' "$(cat "$DELETE_LOG")" 'delete failure stops cleanup'

  reset_state "$inventory"
  export LEAVE_CLUSTER=ncp-local
  expect_failure "$target" CLUSTER_NAME=ncp-local
  grep -q 'ERROR Cluster ncp-local remains after deletion.' "$TEST_DIR/output" || fail 'missing remaining-cluster error'
  assert_eq 'ncp-local' "$(cat "$DELETE_LOG")" 'incomplete deletion stops cleanup'
done

reset_state "$inventory"
export FAIL_LIST_AT=2
expect_failure destroy CLUSTER_NAME=ncp-local
assert_eq 'ncp-local' "$(cat "$DELETE_LOG")" 'post-delete inventory failure'

reset_state '[{"name":"ncp-local-cp"},{"name":"ncp-local-compute-1"}]'
export FAIL_DELETE=ncp-local-cp
expect_failure destroy-multicluster
assert_eq 'ncp-local-cp' "$(cat "$DELETE_LOG")" 'multi-cluster failure propagation'

echo "PASS: verified cluster cleanup Makefile tests ($MAKE_SHELL)"
