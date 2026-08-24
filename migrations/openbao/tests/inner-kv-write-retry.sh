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
    curl -s -o /dev/null -X POST -H "X-Vault-Token: ${BAO_TOKEN}" \
      -d '{"x":"y"}' "${BAO_ADDR}/v1/${mount}/seed${i}"
  done
}

# Open the kv-v2 upgrade window deterministically: tune a seeded kv-v1
# mount to version 2. The tune re-runs the same backend setup and upgrade
# routine as a fresh kv-v2 enable, but with KEYS entries to migrate the
# window stays open for hundreds of milliseconds instead of a few.
open_upgrade_window() {
  local mount=$1
  bao secrets enable -path="${mount}" kv >/dev/null
  seed_keys "${mount}" "${KEYS}"
  bao secrets tune -version=2 "${mount}" >/dev/null
}

# S1: a write issued inside the upgrade window succeeds.
open_upgrade_window s1
if write_secrets_kv "s1" "cassandra/creds" "username=x password=y" \
    && [ "$(bao kv get -field=username s1/cassandra/creds)" = "x" ]; then
  pass "write inside the upgrade window succeeds"
else
  fail "write inside the upgrade window succeeds"
fi

# S2: an existing secret is not overwritten when the existence check runs
# inside the upgrade window. Before the retry fix, the transient 400 on
# the get was read as "secret missing".
bao secrets enable -path=s2 kv >/dev/null
curl -s -o /dev/null -X POST -H "X-Vault-Token: ${BAO_TOKEN}" \
  -d '{"username":"original"}' "${BAO_ADDR}/v1/s2/cassandra/creds"
seed_keys s2 "${KEYS}"
bao secrets tune -version=2 s2 >/dev/null
if write_secrets_kv "s2" "cassandra/creds" "username=replacement" \
    && [ "$(bao kv get -field=username s2/cassandra/creds)" = "original" ]; then
  pass "existing secret preserved across the upgrade window"
else
  fail "existing secret preserved across the upgrade window"
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
