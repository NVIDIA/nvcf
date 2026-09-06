#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -u

test_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
subtree_dir=$(CDPATH='' cd -- "${test_dir}/.." && pwd)
repo_dir=$(CDPATH='' cd -- "${subtree_dir}/../.." && pwd)
script="${subtree_dir}/execute_sqls.sh"
keyspaces="${subtree_dir}/keyspaces"
dockerfile="${subtree_dir}/Dockerfile"
init_script="${repo_dir}/deploy/helm/cassandra/helm/scripts/initdb.sh"
status=0

fail()
{
  printf 'FAIL: %s\n' "$1" >&2
  status=1
}

schema_inventory=$(
  # shellcheck disable=SC2016
  sed -n 's/^| `\([^`]*\)`[[:space:]]*| \[[^]]*\].*$/\1/p' \
    "${keyspaces}/README.md"
)
if [ -z "${schema_inventory}" ]; then
  fail "schema inventory is empty or malformed"
fi
for keyspace_name in ${schema_inventory}; do
  for migration_name in \
    01_init_keyspace.up.sql \
    02_init_roles.up.sql \
    03_init_tables.up.sql
  do
    migration="${keyspaces}/${keyspace_name}/${migration_name}"
    if [ ! -f "${migration}" ]; then
      fail "schema inventory entry ${keyspace_name} is missing ${migration_name}"
    fi
  done
done

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

if ! sed '/^[[:space:]]*#/d' "${init_script}" |
  grep -Eq '(^|[[:space:]])kubectl[[:space:]]+exec([[:space:]]|$)'; then
  fail "Cassandra cluster initialization no longer requires kubectl; reconsider bundling it"
fi

if ! grep -F -q \
  'COPY --from=kubectl-builder --chmod=0555 /out/kubectl /usr/local/bin/kubectl' \
  "${dockerfile}"; then
  fail "the migrations image must include kubectl for Cassandra cluster initialization"
fi

kubectl_version=$(sed -n 's/^ARG KUBECTL_VERSION=//p' "${dockerfile}")
if ! printf '%s\n' "${kubectl_version}" | grep -Eq '^v1\.36\.[0-9]+$'; then
  fail "the migrations image must use kubectl v1.36 for v1.35-v1.37 compatibility"
fi

if ! grep -Eq '^ARG KUBECTL_GO_IMAGE=golang:1\.26\.6-[^@]+@sha256:[0-9a-f]{64}$' \
  "${dockerfile}" ||
  ! grep -F -q 'GOPROXY=off' "${dockerfile}" ||
  ! grep -F -q 'go build -mod=vendor' "${dockerfile}" ||
  ! grep -F -q '/tmp/kubectl-host version --client -o json' "${dockerfile}"; then
  fail "the migrations image must reproducibly rebuild and validate kubectl with Go 1.26.6"
fi

nvct_schema="${keyspaces}/nvct_api/03_init_tables.up.sql"
if ! grep -F -q 'health                         TEXT' "${nvct_schema}"; then
  fail "NVCT fresh schema is missing tasks_v2.health"
fi

nvct_health_migration="${keyspaces}/nvct_api/04_add_health.up.sql"
if ! grep -F -q 'ALTER TABLE nvct_api.tasks_v2 ADD IF NOT EXISTS health TEXT;' \
  "${nvct_health_migration}"; then
  fail "NVCT upgrade migration does not add tasks_v2.health idempotently"
fi

exit "${status}"
