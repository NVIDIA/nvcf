#!/bin/bash

# Usage
namespace="${1:-cassandra-system}"
statefulset="${2:-cassandra}"

# Environment variables must be set
if [ -z "$CASSANDRA_USER" ] || [ -z "$CASSANDRA_PASSWORD" ]; then
  echo "Error: CASSANDRA_USER and CASSANDRA_PASSWORD environment variables must be set"
  exit 1
fi

# Default superuser credentials Cassandra bootstraps on first boot
# (system_auth.roles seeds "cassandra"/"cassandra" when PasswordAuthenticator
# is enabled and the role does not already exist).
DEFAULT_CASSANDRA_USER="cassandra"
DEFAULT_CASSANDRA_PASSWORD="cassandra"

# Run cqlsh against the given pod. "kubectl exec" has no flag to inject
# environment variables into the remote process and does not forward this
# script's own environment, so credentials are sent over stdin (-i) and read
# by the remote shell into its own environment with "read", rather than
# being interpolated into a literal command string or passed as -u/-p
# arguments. That keeps password values out of the kubectl exec argv, out of
# any constructed/echoed command text, and out of this script's own trace
# output. cqlsh itself only accepts credentials via -u/-p, so the value is
# still passed to the cqlsh process argv inside the container; that boundary
# is accepted and unavoidable given cqlsh's own interface.
run_cqlsh() {
  local pod="$1"
  local user="$2"
  local password="$3"
  shift 3
  printf '%s\n%s\n' "${user}" "${password}" | kubectl exec -i "${pod}" -c cassandra -n "${namespace}" -- \
    sh -c 'read -r CQLSH_U; read -r CQLSH_P; cqlsh -u "$CQLSH_U" -p "$CQLSH_P" "$@"' -- "$@"
}

# How many desired replicas

# Ensure the "cassandra" superuser has the desired dbUser.password.
# Idempotent by construction: if the desired password already works, this
# is a no-op; otherwise fall back to the still-default cassandra/cassandra
# credentials and set the desired password. Never echoes password values.
ensure_superuser_password() {
  local pod="$1"
  local probe_err alter_err

  echo "Checking whether the ${CASSANDRA_USER} superuser password is already set"
  probe_err=$(run_cqlsh "${pod}" "${CASSANDRA_USER}" "${CASSANDRA_PASSWORD}" \
    localhost -e "SELECT key FROM system.local;" 2>&1 >/dev/null)
  if [ $? -eq 0 ]; then
    echo "Superuser password already matches the desired value, skipping ALTER ROLE"
    return 0
  fi

  echo "Superuser password not yet set, applying it via the default credentials"

  # CASSANDRA_USER is interpolated into the ALTER ROLE statement as a bare
  # (unquoted) CQL identifier, not a quoted string literal, so CQL
  # string-literal escaping does not apply to it. It is an
  # operator-controlled value (dbUser.user, chart default "cassandra") that
  # is not expected to contain characters requiring identifier quoting;
  # this assumption is accepted rather than defended against here.
  #
  # CASSANDRA_PASSWORD is interpolated into a single-quoted CQL string
  # literal and must be escaped for that context: double every embedded
  # single quote ('). Turn off any shell tracing around this so an escaped
  # or unescaped password value is never written to trace output.
  { set +x; } 2>/dev/null
  local cql_escaped_pw
  cql_escaped_pw=$(printf '%s' "$CASSANDRA_PASSWORD" | sed "s/'/''/g")
  local alter_stmt="ALTER ROLE ${CASSANDRA_USER} WITH PASSWORD = '${cql_escaped_pw}';"

  alter_err=$(run_cqlsh "${pod}" "${DEFAULT_CASSANDRA_USER}" "${DEFAULT_CASSANDRA_PASSWORD}" \
    localhost -e "${alter_stmt}" 2>&1 >/dev/null)
  local alter_status=$?
  unset cql_escaped_pw alter_stmt

  if [ $alter_status -ne 0 ]; then
    echo "Failed to set the ${CASSANDRA_USER} superuser password using default credentials"
    echo "The superuser password is neither the desired value nor the default; it may have been rotated - manual intervention required"
    echo "cqlsh diagnostic (desired-credential probe):"
    printf '%s\n' "${probe_err}" | sed -E "s/(-p )[^ ]+/\1<redacted>/g"
    echo "cqlsh diagnostic (default-credential ALTER ROLE):"
    printf '%s\n' "${alter_err}" | sed -E "s/(-p )[^ ]+/\1<redacted>/g"
    return 1
  fi

  echo "Superuser password set successfully"
}

initialize_db() {
  # Define the end variable for a 10-minute timeout
  local end=$((SECONDS + 600))

  local desired_replicas=$(kubectl get statefulset ${statefulset} -n ${namespace} -o jsonpath='{.spec.replicas}')
  if [[ "$desired_replicas" -eq 0 ]]; then
    echo "StatefulSet may not be configured correctly, 0 desired replicas is not valid. Exiting."
    exit 1
  fi

  for i in $(seq 0 $((desired_replicas - 1))); do
    while [[ $(kubectl get pod $statefulset-${i} -n ${namespace} -o jsonpath='{.status.containerStatuses[0].ready}') != "true" ]]; do
      if [ $SECONDS -gt $end ]; then
        echo "Timeout waiting for pod ${statefulset}-${i} to be ready"
        return 1
      fi
      echo "Waiting for pod ${statefulset}-${i} to be ready..."
      sleep 5
    done
  done
  echo "All Cassandra pods are ready"

  # Always select the 0th pod
  if ! ensure_superuser_password "${statefulset}-0"; then
    return 1
  fi

  echo "Initializing Cassandra cluster with keyspace.cql"
  #
  # A ConfigMap containing the cql script is mounted to the cassandra
  # container. The ConfigMap is created as a pre-install hook (see
  # templates/hook-pre-01-initdb-configmap.yaml) and mounted by the
  # StatefulSet at /opt/nvcf/cassandra/cql (see templates/statefulset.yaml).
  #
  if ! kubectl exec ${statefulset}-0 -c cassandra -n ${namespace} -- \
    cqlsh -u "${CASSANDRA_USER}" -p "${CASSANDRA_PASSWORD}" localhost -f /opt/nvcf/cassandra/cql/keyspace.cql; then
    echo "Failed to successfully execute CQL"
    return 1
  fi
}

if ! initialize_db ${namespace} ${statefulset}; then
  exit 1
else
  echo "Successfully initialized the db"
fi
