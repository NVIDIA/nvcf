#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Scenario driver for kv-write-retry-test.sh. Runs inside the OpenBao
# container and exercises the real helper functions against live kv-v2
# storage-upgrade windows. Expects BAO_ADDR and BAO_TOKEN in the
# environment; KEYS controls how long the deterministic window stays open.

set -u

source /test/utils/utils.sh
source /test/utils/functions.sh

: "${BAO_ADDR:?}"
: "${BAO_TOKEN:?}"
KEYS=${KEYS:-400}

failures=0

pass() { echo "PASS: $1"; }
fail() { echo "FAIL: $1"; failures=$((failures + 1)); }

seed_keys() {
  local mount=$1
  local count=$2
  local i
  for i in $(seq 1 "${count}"); do
    curl -sf -o /dev/null -X POST -H "X-Vault-Token: ${BAO_TOKEN}" \
      -d '{"x":"y"}' "${BAO_ADDR}/v1/${mount}/seed${i}" || return 1
  done
}

# Open the kv-v2 upgrade window deterministically: tune a seeded kv-v1
# mount to version 2. The tune re-runs the same backend setup and upgrade
# routine as a fresh kv-v2 enable, but with KEYS entries to migrate the
# window stays open for hundreds of milliseconds instead of a few.
open_upgrade_window() {
  local mount=$1
  bao secrets enable -path="${mount}" kv >/dev/null || return 1
  seed_keys "${mount}" "${KEYS}" || return 1
  bao secrets tune -version=2 "${mount}" >/dev/null || return 1
}

# S0: the classifier accepts each upgrade-window message independently and
# rejects other errors. The standby variant cannot be produced by this
# single-node harness, so it is checked directly here.
if is_kv_upgrade_window_error "Code: 400. Errors: * Upgrading from non-versioned to versioned data. This backend will be unavailable for a brief period and will resume service shortly." \
    && is_kv_upgrade_window_error "Code: 400. Errors: * Waiting for the primary to upgrade from non-versioned to versioned data. This backend will be unavailable for a brief period and will resume service shortly." \
    && ! is_kv_upgrade_window_error "Code: 403. Errors: * permission denied" \
    && ! is_kv_upgrade_window_error "No value found at s1/data/cassandra/creds"; then
  pass "classifier accepts only the two upgrade-window messages"
else
  fail "classifier accepts only the two upgrade-window messages"
fi

# S1 requires that the wait actually observed the upgrade window (its
# retry notice), so it cannot pass vacuously if the setup failed to hold
# the window open.

# S1: the readiness wait blocks through the upgrade window, after which a
# plain write succeeds.
open_upgrade_window s1 || { echo "setup failed for s1" >&2; exit 2; }
s1_wait_out=$(wait_kv_v2_mount_data_path_ready "s1" 2>&1)
s1_wait_rc=$?
if [ "${s1_wait_rc}" -eq 0 ] \
    && grep -q "retrying in" <<<"${s1_wait_out}" \
    && write_secrets_kv "s1" "cassandra/creds" "username=x password=y" \
    && [ "$(bao kv get -field=username s1/cassandra/creds)" = "x" ]; then
  pass "readiness wait rides out the upgrade window, then the write succeeds"
else
  echo "${s1_wait_out}"
  fail "readiness wait rides out the upgrade window, then the write succeeds"
fi

# S2: a write issued inside the upgrade window without the readiness wait
# fails safely: the migration aborts and the existing secret is not
# overwritten. Before the fix, the 400 on the existence check was read as
# "secret missing" and the write went through.
bao secrets enable -path=s2 kv >/dev/null || { echo "setup failed for s2" >&2; exit 2; }
curl -sf -o /dev/null -X POST -H "X-Vault-Token: ${BAO_TOKEN}" \
  -d '{"username":"original"}' "${BAO_ADDR}/v1/s2/cassandra/creds" \
  || { echo "setup failed for s2" >&2; exit 2; }
seed_keys s2 "${KEYS}" || { echo "setup failed for s2" >&2; exit 2; }
bao secrets tune -version=2 s2 >/dev/null || { echo "setup failed for s2" >&2; exit 2; }
s2_out=$(write_secrets_kv "s2" "cassandra/creds" "username=replacement" 2>&1)
s2_rc=$?
wait_kv_v2_mount_data_path_ready "s2" >/dev/null 2>&1
if [ "${s2_rc}" -ne 0 ] \
    && grep -q "Upgrading from non-versioned" <<<"${s2_out}" \
    && [ "$(bao kv get -field=username s2/cassandra/creds)" = "original" ]; then
  pass "unwaited write inside the window aborts and preserves the secret"
else
  echo "${s2_out}"
  fail "unwaited write inside the window aborts and preserves the secret"
fi

# S3: the production sequence, a fresh kv-v2 enable followed by an
# immediate write. The fresh-mount window is short, so this is usually
# green even without the fix on fast hardware, but it pins the readiness
# wait and catches regressions on slow runners.
if enable_secrets_mount "s3/kv" "kv-v2" \
    && write_secrets_kv "s3/kv" "cassandra/creds" "username=x password=y"; then
  pass "fresh kv-v2 enable then immediate write"
else
  fail "fresh kv-v2 enable then immediate write"
fi

# S4: non-transient errors are not retried. A write to a mount that does
# not exist must fail, and fail fast.
start=${SECONDS}
if write_secrets_kv "does-not-exist/kv" "foo" "bar=baz"; then
  fail "write to a missing mount fails"
elif (( SECONDS - start > 15 )); then
  fail "write to a missing mount fails fast (took $((SECONDS - start))s)"
else
  pass "write to a missing mount fails fast"
fi

if [ "${failures}" -gt 0 ]; then
  echo "RESULT: ${failures} scenario(s) failed"
  exit 1
fi
echo "RESULT: all scenarios passed"
