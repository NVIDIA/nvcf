#!/bin/bash

if [ -z "$CASSANDRA_HOSTS" ] || [ -z "$CASSANDRA_USER" ] || [ -z "$CASSANDRA_PASSWORD" ]; then
  echo "Error: CASSANDRA_HOSTS, CASSANDRA_USER, and CASSANDRA_PASSWORD environment variables must be set"
  exit 1
fi

CASSANDRA_PORT="${CASSANDRA_PORT:-9042}"
CASSANDRA_READY_HOSTS="${CASSANDRA_READY_HOSTS:-$CASSANDRA_HOSTS}"
CASSANDRA_INIT_MAX_RETRIES="${CASSANDRA_INIT_MAX_RETRIES:-120}"
CASSANDRA_INIT_RETRY_SECONDS="${CASSANDRA_INIT_RETRY_SECONDS:-5}"
CASSANDRA_INIT_STABLE_ATTEMPTS="${CASSANDRA_INIT_STABLE_ATTEMPTS:-3}"
CASSANDRA_INIT_CQL_MAX_RETRIES="${CASSANDRA_INIT_CQL_MAX_RETRIES:-3}"
CASSANDRA_INIT_CQL_RETRY_SECONDS="${CASSANDRA_INIT_CQL_RETRY_SECONDS:-10}"
CASSANDRA_CQLSH_CONNECT_TIMEOUT="${CASSANDRA_CQLSH_CONNECT_TIMEOUT:-5}"
CASSANDRA_CQLSH_REQUEST_TIMEOUT="${CASSANDRA_CQLSH_REQUEST_TIMEOUT:-20}"
CASSANDRA_CQLSH_COMMAND_TIMEOUT="${CASSANDRA_CQLSH_COMMAND_TIMEOUT:-45s}"
CQL_HISTORY="${CQL_HISTORY:-/tmp/cqlsh_history}"
export CQL_HISTORY

# Default superuser credentials Cassandra bootstraps on first boot
# (system_auth.roles seeds "cassandra"/"cassandra" when PasswordAuthenticator
# is enabled and the role does not already exist).
DEFAULT_CASSANDRA_USER="cassandra"
DEFAULT_CASSANDRA_PASSWORD="cassandra"

# Run cqlsh directly from the init job to a Cassandra service or StatefulSet
# pod DNS name. cqlsh accepts credentials only via -u/-p, so the password is
# visible to the cqlsh process argv inside this job container.
run_cqlsh() {
  local host="$1"
  local user="$2"
  local password="$3"
  shift 3
  timeout "$CASSANDRA_CQLSH_COMMAND_TIMEOUT" \
    cqlsh \
      --connect-timeout "$CASSANDRA_CQLSH_CONNECT_TIMEOUT" \
      --request-timeout "$CASSANDRA_CQLSH_REQUEST_TIMEOUT" \
      -u "$user" -p "$password" "$host" "$CASSANDRA_PORT" "$@"
}

probe_cqlsh() {
  local host="$1"
  local user="$2"
  local password="$3"

  run_cqlsh "$host" "$user" "$password" -e "SELECT key FROM system.local;" > /dev/null 2>&1
}

wait_for_cassandra_hosts_stable() {
  local mode="$1"
  local stable_attempts=0
  local attempt=1
  local failed_hosts host

  while [ "$attempt" -le "$CASSANDRA_INIT_MAX_RETRIES" ]; do
    failed_hosts=""

    for host in ${CASSANDRA_READY_HOSTS//,/ }; do
      case "$mode" in
        desired)
          probe_cqlsh "$host" "$CASSANDRA_USER" "$CASSANDRA_PASSWORD" ||
            failed_hosts="${failed_hosts} ${host}"
          ;;
        default)
          probe_cqlsh "$host" "$DEFAULT_CASSANDRA_USER" "$DEFAULT_CASSANDRA_PASSWORD" ||
            failed_hosts="${failed_hosts} ${host}"
          ;;
        desired-or-default)
          probe_cqlsh "$host" "$CASSANDRA_USER" "$CASSANDRA_PASSWORD" ||
            probe_cqlsh "$host" "$DEFAULT_CASSANDRA_USER" "$DEFAULT_CASSANDRA_PASSWORD" ||
            failed_hosts="${failed_hosts} ${host}"
          ;;
        *)
          echo "Unknown Cassandra cqlsh stability mode: ${mode}"
          return 1
          ;;
      esac
    done

    if [ -z "$failed_hosts" ]; then
      stable_attempts=$((stable_attempts + 1))
      echo "Cassandra cqlsh stability check ${stable_attempts}/${CASSANDRA_INIT_STABLE_ATTEMPTS} succeeded for all hosts"
      if [ "$stable_attempts" -ge "$CASSANDRA_INIT_STABLE_ATTEMPTS" ]; then
        echo "All Cassandra hosts are accepting stable cqlsh connections"
        return 0
      fi
    else
      stable_attempts=0
      echo "Waiting for stable Cassandra cqlsh on:${failed_hosts} (${attempt}/${CASSANDRA_INIT_MAX_RETRIES})"
    fi

    attempt=$((attempt + 1))
    sleep "$CASSANDRA_INIT_RETRY_SECONDS"
  done

  echo "Timed out waiting for stable Cassandra cqlsh on all hosts"
  return 1
}

# Ensure the "cassandra" superuser has the desired dbUser.password.
# Idempotent by construction: if the desired password already works, this
# is a no-op; otherwise fall back to the still-default cassandra/cassandra
# credentials and set the desired password. Never echoes password values.
ensure_superuser_password() {
  local host="$1"
  local probe_err alter_err

  echo "Checking whether the ${CASSANDRA_USER} superuser password is already set"
  probe_err=$(run_cqlsh "$host" "$CASSANDRA_USER" "$CASSANDRA_PASSWORD" \
    -e "SELECT key FROM system.local;" 2>&1 >/dev/null)
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

  alter_err=$(run_cqlsh "$host" "$DEFAULT_CASSANDRA_USER" "$DEFAULT_CASSANDRA_PASSWORD" \
    -e "$alter_stmt" 2>&1 >/dev/null)
  local alter_status=$?
  unset cql_escaped_pw alter_stmt

  if [ $alter_status -ne 0 ]; then
    echo "Failed to set the ${CASSANDRA_USER} superuser password using default credentials"
    echo "The superuser password is neither the desired value nor the default; it may have been rotated - manual intervention required"
    echo "cqlsh diagnostic (desired-credential probe):"
    printf '%s\n' "$probe_err" | sed -E "s/(-p )[^ ]+/\1<redacted>/g"
    echo "cqlsh diagnostic (default-credential ALTER ROLE):"
    printf '%s\n' "$alter_err" | sed -E "s/(-p )[^ ]+/\1<redacted>/g"
    return 1
  fi

  echo "Superuser password set successfully"
}

run_keyspace_cql() {
  local user="$1"
  local password="$2"
  local attempt=1

  while [ "$attempt" -le "$CASSANDRA_INIT_CQL_MAX_RETRIES" ]; do
    echo "Initializing Cassandra cluster with keyspace.cql"
    #
    # A ConfigMap containing the cql script is created as a pre-install hook
    # (see templates/hook-pre-01-initdb-configmap.yaml) and mounted into this job.
    #
    if run_cqlsh "$CASSANDRA_HOSTS" "$user" "$password" \
      -f /opt/nvcf/cassandra/cql/keyspace.cql; then
      return 0
    fi

    if [ "$attempt" -eq "$CASSANDRA_INIT_CQL_MAX_RETRIES" ]; then
      echo "Failed to successfully execute CQL"
      return 1
    fi

    echo "keyspace.cql failed on attempt ${attempt}/${CASSANDRA_INIT_CQL_MAX_RETRIES}; retrying in ${CASSANDRA_INIT_CQL_RETRY_SECONDS}s"
    attempt=$((attempt + 1))
    sleep "$CASSANDRA_INIT_CQL_RETRY_SECONDS"
  done
}

initialize_db() {
  wait_for_cassandra_hosts_stable "desired-or-default" || return 1

  if probe_cqlsh "$CASSANDRA_HOSTS" "$CASSANDRA_USER" "$CASSANDRA_PASSWORD"; then
    echo "Desired superuser password is already active"
    wait_for_cassandra_hosts_stable "desired" || return 1
    run_keyspace_cql "$CASSANDRA_USER" "$CASSANDRA_PASSWORD" || return 1
  else
    echo "Desired superuser password is not active yet; initializing schema with bootstrap credentials before password rotation"
    wait_for_cassandra_hosts_stable "default" || return 1

    # On first boot with a non-default desired password, running keyspace.cql
    # immediately after ALTER ROLE can hit Cassandra auth/permission propagation
    # races against system keyspaces. Initialize with the bootstrap superuser,
    # then rotate credentials and require the desired credentials everywhere.
    run_keyspace_cql "$DEFAULT_CASSANDRA_USER" "$DEFAULT_CASSANDRA_PASSWORD" || return 1
    if ! ensure_superuser_password "$CASSANDRA_HOSTS"; then
      return 1
    fi
    wait_for_cassandra_hosts_stable "desired" || return 1
  fi
}

if ! initialize_db; then
  exit 1
else
  echo "Successfully initialized the db"
fi
