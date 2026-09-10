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
runtime_dockerfile="${repo_dir}/infra/cassandra/Dockerfile"
init_script="${repo_dir}/deploy/helm/cassandra/helm/scripts/initdb.sh"
init_hook="${repo_dir}/deploy/helm/cassandra/helm/templates/hook-post-01-initdb.yaml"
migration_hook="${repo_dir}/deploy/helm/cassandra/helm/templates/hook-post-02-migrations.yaml"
legacy_rbac_cleanup_hook="${repo_dir}/deploy/helm/cassandra/helm/templates/hook-pre-00-cleanup-legacy-rbac.yaml"
statefulset="${repo_dir}/deploy/helm/cassandra/helm/templates/statefulset.yaml"
init_cql_hook="${repo_dir}/deploy/helm/cassandra/helm/templates/hook-pre-01-initdb-configmap.yaml"
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

if ! grep -F -q 'wait_for_stable_cassandra_hosts' "${script}" ||
  ! grep -F -q 'CASSANDRA_READY_HOSTS=${CASSANDRA_READY_HOSTS:-$CASSANDRA_HOSTS}' "${script}" ||
  ! grep -F -q 'CASSANDRA_STABLE_ATTEMPTS=${CASSANDRA_STABLE_ATTEMPTS:-3}' "${script}" ||
  ! grep -F -q 'CASSANDRA_CQLSH_COMMAND_TIMEOUT=${CASSANDRA_CQLSH_COMMAND_TIMEOUT:-45s}' "${script}" ||
  ! grep -F -q 'timeout "$CASSANDRA_CQLSH_COMMAND_TIMEOUT"' "${script}" ||
  ! grep -F -q 'CQL_HISTORY=${CQL_HISTORY:-/tmp/cqlsh_history}' "${script}"; then
  fail "execute_sqls.sh must retain an all-host Cassandra authentication stability check"
fi

cqlsh_wrapper=$(sed -n '/^run_cqlsh()/,/^}/p' "${script}")
if ! printf '%s\n' "${cqlsh_wrapper}" | grep -F -q 'host="$1"' ||
  ! printf '%s\n' "${cqlsh_wrapper}" | grep -F -q 'shift' ||
  printf '%s\n' "${cqlsh_wrapper}" | grep -F -q '"$1" "$CASSANDRA_PORT" "$@"'; then
  fail "execute_sqls.sh cqlsh wrapper must shift the host argument before passing extra cqlsh args"
fi

if ! grep -F -q 'MIGRATE_MAX_RETRIES=${MIGRATE_MAX_RETRIES:-8}' "${script}" ||
  ! grep -F -q 'MIGRATE_CONSISTENCY=${MIGRATE_CONSISTENCY:-LOCAL_QUORUM}' "${script}" ||
  ! grep -F -q 'consistency=${MIGRATE_CONSISTENCY}' "${script}" ||
  ! grep -F -q 'MIGRATE_CONNECT_TIMEOUT=${MIGRATE_CONNECT_TIMEOUT:-30s}' "${script}" ||
  ! grep -F -q 'MIGRATE_DISABLE_HOST_LOOKUP=${MIGRATE_DISABLE_HOST_LOOKUP:-true}' "${script}" ||
  ! grep -F -q 'run_migrations_for_keyspace "${each}"' "${script}"; then
  fail "execute_sqls.sh must retry transient migrate connection failures per keyspace"
fi

percent_encode_function=$(sed -n '/^percent_encode()/,/^}/p' "${script}")
encoded_credentials=$(
  sh -c "${percent_encode_function}
percent_encode \"\$1\"" sh 'user+name&role=admin#100%'
)
if [ "${encoded_credentials}" != 'user%2Bname%26role%3Dadmin%23100%25' ]; then
  fail "execute_sqls.sh does not percent-encode reserved characters in Cassandra credentials"
fi

if ! grep -F -q 'username=${CASSANDRA_USER_ENCODED}&password=${CASSANDRA_PASSWORD_ENCODED}' "${script}"; then
  fail "execute_sqls.sh must use percent-encoded credentials in the migrate DSN"
fi

migration_function=$(sed -n '/^run_migrations_for_keyspace()/,/^}/p' "${script}")
fake_bin=$(mktemp -d)
fake_migrate_calls="${fake_bin}/calls"
printf '%s\n' '#!/bin/sh' \
  'calls=$(cat "$FAKE_MIGRATE_CALLS")' \
  'calls=$((calls + 1))' \
  'printf "%s\n" "$calls" > "$FAKE_MIGRATE_CALLS"' \
  'echo "Dirty database version 7. Fix and force version."' \
  'exit 1' > "${fake_bin}/migrate"
chmod +x "${fake_bin}/migrate"
printf '0\n' > "${fake_migrate_calls}"

dirty_output=$(
  PATH="${fake_bin}:${PATH}" \
    FAKE_MIGRATE_CALLS="${fake_migrate_calls}" \
    MIGRATE_MAX_RETRIES=8 \
    MIGRATE_RETRY_SECONDS=0 \
    CASSANDRA_HOSTS=host \
    CASSANDRA_PORT=9042 \
    CASSANDRA_USER_ENCODED=user \
    CASSANDRA_PASSWORD_ENCODED=password \
    MIGRATE_CONSISTENCY=LOCAL_QUORUM \
    MIGRATE_PROTOCOL=4 \
    MIGRATE_TIMEOUT=2m \
    MIGRATE_CONNECT_TIMEOUT=30s \
    MIGRATE_DISABLE_HOST_LOOKUP=true \
    sh -c "${migration_function}
run_migrations_for_keyspace /tmp/test_keyspace" 2>&1
)
dirty_status=$?
if [ "${dirty_status}" -eq 0 ]; then
  fail "execute_sqls.sh accepts a dirty migration state"
fi
if [ "$(cat "${fake_migrate_calls}")" -ne 1 ] ||
  printf '%s\n' "${dirty_output}" | grep -F -q 'retrying in'; then
  fail "execute_sqls.sh retries a deterministic dirty migration failure"
fi
rm -rf "${fake_bin}"

if grep -Eq '(^|[[:space:]])kubectl([[:space:]]|$)' "${dockerfile}" "${init_script}" "${init_hook}" "${legacy_rbac_cleanup_hook}"; then
  fail "Cassandra init and migration runtime must not depend on kubectl"
fi

if grep -F -q 'cp -a /etc/cassandra' "${statefulset}" ||
  ! grep -F -q 'cp -R /etc/cassandra/. /shared-conf/' "${statefulset}"; then
  fail "Cassandra config init copy must avoid archive-mode metadata preservation so it can run as non-root"
fi

if ! grep -F -q 'defaultMode: 0555' "${init_hook}" ||
  ! grep -F -q 'defaultMode: 0444' "${init_hook}" ||
  ! grep -F -q 'defaultMode: 0444' "${statefulset}"; then
  fail "ConfigMap-mounted Cassandra hook scripts and CQL must be readable or executable by non-root containers"
fi

if grep -Eq 'DEBIAN_FRONTEND=noninteractive apt-get install .* curl([[:space:]]|$)' "${dockerfile}"; then
  fail "the migrations image must not install curl in the final runtime stage"
fi

if ! grep -F -q 'USER 999:999' "${runtime_dockerfile}" ||
  ! grep -F -q 'USER 999:999' "${dockerfile}"; then
  fail "Cassandra runtime and migration images must declare the non-root Cassandra user as their default image user"
fi

if grep -F -q 'SELECT * FROM system_auth.roles' "${init_cql_hook}" ||
  grep -F -q 'salted_hash' "${init_cql_hook}" ||
  ! grep -F -q 'helm.sh/hook: pre-install,pre-upgrade' "${init_cql_hook}" ||
  ! grep -F -q 'helm.sh/hook-delete-policy: before-hook-creation' "${init_cql_hook}" ||
  ! grep -F -q 'SELECT role FROM system_auth.roles LIMIT 1;' "${init_cql_hook}" ||
  ! grep -F -q 'SELECT role FROM system_auth.role_permissions LIMIT 1;' "${init_cql_hook}"; then
  fail "init CQL hook must refresh on upgrade and avoid verbose system_auth role and permission output"
fi

if ! grep -F -q 'CASSANDRA_READY_HOSTS' "${init_script}" ||
  ! grep -F -q 'CASSANDRA_READY_HOSTS' "${init_hook}" ||
  ! grep -F -q 'CASSANDRA_READY_HOSTS' "${migration_hook}" ||
  ! grep -F -q 'CASSANDRA_HOSTS' "${init_hook}" ||
  ! grep -F -q 'CASSANDRA_CQLSH_COMMAND_TIMEOUT="${CASSANDRA_CQLSH_COMMAND_TIMEOUT:-45s}"' "${init_script}" ||
  ! grep -F -q 'timeout "$CASSANDRA_CQLSH_COMMAND_TIMEOUT"' "${init_script}" ||
  ! grep -F -q 'CQL_HISTORY="${CQL_HISTORY:-/tmp/cqlsh_history}"' "${init_script}"; then
  fail "initdb.sh must use direct cqlsh connectivity from Helm-rendered Cassandra hosts"
fi

if ! grep -F -q 'cassandra.firstReplicaHost' "${init_hook}" ||
  ! grep -F -q 'cassandra.firstReplicaHost' "${migration_hook}"; then
  fail "Cassandra hooks must use a stable StatefulSet pod DNS contact point instead of the load-balanced Service"
fi

if ! grep -F -q 'activeDeadlineSeconds: {{ .Values.cassandra.hooks.initializeCluster.activeDeadlineSeconds }}' "${init_hook}" ||
  ! grep -F -q 'activeDeadlineSeconds: {{ .Values.cassandra.hooks.migrations.activeDeadlineSeconds }}' "${migration_hook}"; then
  fail "Cassandra hook Jobs must enforce their configured active deadlines"
fi
if ! grep -F -q 'backoffLimit: {{ .Values.cassandra.hooks.initializeCluster.backoffLimit }}' "${init_hook}" ||
  ! grep -F -q 'backoffLimit: {{ .Values.cassandra.hooks.migrations.backoffLimit }}' "${migration_hook}" ||
  ! grep -F -q 'restartPolicy: Never' "${migration_hook}"; then
  fail "Cassandra hook retries must be bounded by their scripts and Job specifications"
fi

if ! grep -F -q 'wait_for_cassandra_hosts_stable "default"' "${init_script}" ||
  ! grep -F -q 'run_keyspace_cql "$DEFAULT_CASSANDRA_USER" "$DEFAULT_CASSANDRA_PASSWORD"' "${init_script}" ||
  ! grep -F -q 'ensure_superuser_password "$CASSANDRA_HOSTS"' "${init_script}" ||
  ! grep -F -q 'wait_for_cassandra_hosts_stable "desired"' "${init_script}"; then
  fail "initdb.sh must initialize schema with bootstrap credentials before rotating a first-boot non-default password"
fi

if ! grep -F -q 'CASSANDRA_INIT_CQL_MAX_RETRIES="${CASSANDRA_INIT_CQL_MAX_RETRIES:-3}"' "${init_script}" ||
  ! grep -F -q 'keyspace.cql failed on attempt' "${init_script}"; then
  fail "initdb.sh must retry transient keyspace.cql failures before failing the hook pod"
fi

if ! grep -F -q 'cleanupLegacyInitializeClusterRbac.enabled' "${legacy_rbac_cleanup_hook}" ||
  ! grep -F -q 'helm.sh/hook: pre-upgrade' "${legacy_rbac_cleanup_hook}" ||
  grep -F -q 'helm.sh/hook: pre-install' "${legacy_rbac_cleanup_hook}" ||
  ! grep -F -q 'resourceNames: [{{ $legacyName | quote }}]' "${legacy_rbac_cleanup_hook}" ||
  ! grep -F -q 'resources: ["serviceaccounts"]' "${legacy_rbac_cleanup_hook}" ||
  ! grep -F -q 'resources: ["roles", "rolebindings"]' "${legacy_rbac_cleanup_hook}" ||
  ! grep -F -q 'command: ["/usr/bin/python3", "-c"]' "${legacy_rbac_cleanup_hook}"; then
  fail "legacy init RBAC cleanup hook must be pre-upgrade-only, name-scoped, and avoid kubectl"
fi

for template in \
  "${statefulset}" \
  "${init_hook}" \
  "${migration_hook}" \
  "${legacy_rbac_cleanup_hook}"
do
  if ! grep -F -q '.Values.cassandra.containerSecurityContext' "${template}"; then
    fail "${template} must apply the Cassandra container security context"
  fi
  if ! grep -F -q '.Values.cassandra.podSecurityContext' "${template}"; then
    fail "${template} must apply the Cassandra pod security context"
  fi
done

if ! grep -F -q 'runAsNonRoot: true' "${repo_dir}/deploy/helm/cassandra/helm/values.yaml" ||
  ! grep -F -q 'runAsUser: 999' "${repo_dir}/deploy/helm/cassandra/helm/values.yaml" ||
  ! grep -F -q 'runAsGroup: 999' "${repo_dir}/deploy/helm/cassandra/helm/values.yaml" ||
  ! grep -F -q 'allowPrivilegeEscalation: false' "${repo_dir}/deploy/helm/cassandra/helm/values.yaml" ||
  ! grep -F -q 'drop:' "${repo_dir}/deploy/helm/cassandra/helm/values.yaml" ||
  ! grep -F -q 'seccompProfile:' "${repo_dir}/deploy/helm/cassandra/helm/values.yaml" ||
  ! grep -F -q 'type: RuntimeDefault' "${repo_dir}/deploy/helm/cassandra/helm/values.yaml"; then
  fail "default Cassandra container security context must run as non-root and drop privileges"
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
