#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -u

test_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
subtree_dir=$(CDPATH='' cd -- "${test_dir}/.." && pwd)
script="${subtree_dir}/execute_sqls.sh"
keyspaces="${subtree_dir}/keyspaces"
status=0

fail()
{
  printf 'FAIL: %s\n' "$1" >&2
  status=1
}

if grep -R -n -F 'envOrDefault "REPLICA_COUNT"' "${keyspaces}"; then
  fail "keyspace migrations contain templates unsupported by stock golang-migrate"
fi

envsubst_vars=$(sed -n "s/^ENVSUBST_VARS='\\(.*\\)'$/\\1/p" "${script}")
# shellcheck disable=SC2016
if [ "${envsubst_vars}" != '$SERVICE_ROLE_PASSWORD $REPLICA_COUNT' ]; then
  fail "execute_sqls.sh does not allow REPLICA_COUNT substitution"
fi

# shellcheck disable=SC2016
if ! grep -F -q 'REPLICA_COUNT=${REPLICA_COUNT:-3}' "${script}"; then
  fail "execute_sqls.sh does not preserve the default replica count"
fi

# shellcheck disable=SC2016
replica_count_validation=$(
  sed -n '/^REPLICA_COUNT=${REPLICA_COUNT:-3}$/,/^export REPLICA_COUNT$/p' "${script}"
)
for replica_count in 1 3 2147483647; do
  if ! REPLICA_COUNT="${replica_count}" sh -c "${replica_count_validation}"; then
    fail "execute_sqls.sh rejects valid replica count ${replica_count}"
  fi
done
for replica_count in 0 01 -1 invalid 2147483648 99999999999; do
  if REPLICA_COUNT="${replica_count}" sh -c "${replica_count_validation}" 2>/dev/null; then
    fail "execute_sqls.sh accepts invalid replica count ${replica_count}"
  fi
done

for migration in "${keyspaces}"/*/01_init_keyspace.up.sql; do
  rendered=$(
    REPLICA_COUNT=2 SERVICE_ROLE_PASSWORD=test-password \
      envsubst "${envsubst_vars}" < "${migration}"
  )

  if printf '%s\n' "${rendered}" | grep -q '{{'; then
    fail "${migration} leaves a Go template in rendered CQL"
  fi

  if ! printf '%s\n' "${rendered}" | grep -F -q "'ncp': '2'"; then
    fail "${migration} does not substitute REPLICA_COUNT"
  fi
done

if grep -Eq '(^|[[:space:]])nc([[:space:]]|$)' "${script}"; then
  fail "execute_sqls.sh depends on netcat even though the image does not install it"
fi

if ! grep -q '^until cqlsh ' "${script}"; then
  fail "execute_sqls.sh must retain the Cassandra authentication readiness check"
fi

exit "${status}"
