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
if [ "${envsubst_vars}" != '$SERVICE_ROLE_PASSWORD $REPLICA_COUNT $NOTARY_BASE_URL $ESS_JWKS_URL $ESS_ISSUER_URL' ]; then
  fail "execute_sqls.sh does not allow the complete, explicit migration variable set"
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

control_plane_derivation=$(
  sed -n '/^CONTROL_PLANE_ID=${CONTROL_PLANE_ID:-}$/,/^export NOTARY_BASE_URL ESS_JWKS_URL ESS_ISSUER_URL$/p' "${script}"
)
derive_control_plane_endpoints()
{
  CONTROL_PLANE_ID="$1" sh -c "${control_plane_derivation}
    printf '%s\n%s\n%s\n' \"\$NOTARY_BASE_URL\" \"\$ESS_JWKS_URL\" \"\$ESS_ISSUER_URL\""
}

expected_plane_a_endpoints='http://notary.plane-a-nvcf.svc.cluster.local:8080
http://plane-a-openbao-server.plane-a-vault-system.svc.cluster.local:8200/v1/services/ess-api/jwt/jwks
http://ess-api.plane-a-ess.svc.cluster.local'
if [ "$(derive_control_plane_endpoints plane-a)" != "${expected_plane_a_endpoints}" ]; then
  fail "execute_sqls.sh does not derive the exact plane-a authorization endpoints"
fi

expected_legacy_endpoints='http://notary.nvcf.svc.cluster.local:8080
http://openbao-server.vault-system.svc.cluster.local:8200/v1/services/ess-api/jwt/jwks
http://ess-api.ess.svc.cluster.local'
if [ "$(derive_control_plane_endpoints '')" != "${expected_legacy_endpoints}" ]; then
  fail "execute_sqls.sh does not preserve the legacy authorization endpoints"
fi

for control_plane_id in default Plane-A plane_a -plane-a plane-a- 'plane-a'';DROP' 123456789012345678901; do
  if CONTROL_PLANE_ID="${control_plane_id}" sh -c "${control_plane_derivation}" 2>/dev/null; then
    fail "execute_sqls.sh accepts invalid control-plane ID ${control_plane_id}"
  fi
done

notary_base_url='http://notary.plane-a-nvcf.svc.cluster.local:8080'
ess_jwks_url='http://plane-a-openbao-server.plane-a-vault-system.svc.cluster.local:8200/v1/services/ess-api/jwt/jwks'
ess_issuer_url='http://ess-api.plane-a-ess.svc.cluster.local'

for migration in \
  "${keyspaces}/ess_api/04_init_ncp_namespace.up.sql" \
  "${keyspaces}/ess_api/06_fix_nvcf_api_notary_issuer.up.sql" \
  "${keyspaces}/ess_api/08_add_oauth_authorizations.up.sql" \
  "${keyspaces}/ess_api/09_reconcile_control_plane_authorizations.up.sql"
do
  rendered=$(
    REPLICA_COUNT=2 \
    SERVICE_ROLE_PASSWORD=test-password \
    NOTARY_BASE_URL="${notary_base_url}" \
    ESS_JWKS_URL="${ess_jwks_url}" \
    ESS_ISSUER_URL="${ess_issuer_url}" \
      envsubst "${envsubst_vars}" < "${migration}"
  )

  if printf '%s\n' "${rendered}" | grep -F -q '${'; then
    fail "${migration} leaves an unsubstituted migration variable"
  fi

  if grep -F -q '${NOTARY_BASE_URL}' "${migration}" &&
     ! printf '%s\n' "${rendered}" | grep -F -q "${notary_base_url}"; then
    fail "${migration} does not substitute the plane-scoped Notary URL"
  fi
  if grep -F -q '${ESS_JWKS_URL}' "${migration}" &&
     { ! printf '%s\n' "${rendered}" | grep -F -q "${ess_jwks_url}" ||
       ! printf '%s\n' "${rendered}" | grep -F -q "${ess_issuer_url}"; }; then
    fail "${migration} does not substitute the plane-scoped ESS authorization URLs"
  fi
done

reconcile_migration="${keyspaces}/ess_api/09_reconcile_control_plane_authorizations.up.sql"
reconcile_schema=$(
  sed -n '/^ALTER TABLE ess_api.namespaces ADD IF NOT EXISTS (/,/^);$/p' "${reconcile_migration}"
)
for compatibility_column in oauth_authorizations ssa_authorizations authorizations_version; do
  if ! printf '%s\n' "${reconcile_schema}" | grep -F -q "${compatibility_column}"; then
    fail "forward migration does not create missing ${compatibility_column} upgrade schema"
  fi
done
for authorization_map in ssa_authorizations oauth_authorizations notary_authorizations; do
  if ! grep -F -q "${authorization_map} = ${authorization_map} +" "${reconcile_migration}"; then
    fail "forward migration does not reconcile ${authorization_map}"
  fi
done
if ! grep -F -q 'authorizations_version = now()' "${reconcile_migration}"; then
  fail "forward migration does not bump authorizations_version"
fi
for service_id in nvcf-api nvct-api; do
  if [ "$(grep -F -c "'${service_id}':" "${reconcile_migration}")" -ne 3 ]; then
    fail "forward migration does not reconcile all three authorization maps for ${service_id}"
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
