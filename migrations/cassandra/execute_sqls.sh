#!/bin/sh
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

REPLICA_COUNT=${REPLICA_COUNT:-3}
case "$REPLICA_COUNT" in
  ''|*[!0-9]*|0*)
    echo "ERROR: REPLICA_COUNT must be an integer from 1 to 2147483647, got: $REPLICA_COUNT" >&2
    exit 1
    ;;
esac
if [ "${#REPLICA_COUNT}" -gt 10 ] || [ "$REPLICA_COUNT" -gt 2147483647 ]; then
  echo "ERROR: REPLICA_COUNT must be an integer from 1 to 2147483647, got: $REPLICA_COUNT" >&2
  exit 1
fi
export REPLICA_COUNT

CASSANDRA_PORT=${CASSANDRA_PORT:-9042}
CASSANDRA_READY_HOSTS=${CASSANDRA_READY_HOSTS:-$CASSANDRA_HOSTS}
CASSANDRA_WAIT_MAX_RETRIES=${CASSANDRA_WAIT_MAX_RETRIES:-120}
CASSANDRA_WAIT_RETRY_SECONDS=${CASSANDRA_WAIT_RETRY_SECONDS:-5}
CASSANDRA_STABLE_ATTEMPTS=${CASSANDRA_STABLE_ATTEMPTS:-3}
CASSANDRA_CQLSH_CONNECT_TIMEOUT=${CASSANDRA_CQLSH_CONNECT_TIMEOUT:-5}
CASSANDRA_CQLSH_REQUEST_TIMEOUT=${CASSANDRA_CQLSH_REQUEST_TIMEOUT:-20}
CASSANDRA_CQLSH_COMMAND_TIMEOUT=${CASSANDRA_CQLSH_COMMAND_TIMEOUT:-45s}
CQL_HISTORY=${CQL_HISTORY:-/tmp/cqlsh_history}
export CQL_HISTORY

MIGRATE_MAX_RETRIES=${MIGRATE_MAX_RETRIES:-8}
MIGRATE_RETRY_SECONDS=${MIGRATE_RETRY_SECONDS:-10}
MIGRATE_CONSISTENCY=${MIGRATE_CONSISTENCY:-LOCAL_QUORUM}
MIGRATE_PROTOCOL=${MIGRATE_PROTOCOL:-4}
MIGRATE_TIMEOUT=${MIGRATE_TIMEOUT:-2m}
MIGRATE_CONNECT_TIMEOUT=${MIGRATE_CONNECT_TIMEOUT:-30s}
MIGRATE_DISABLE_HOST_LOOKUP=${MIGRATE_DISABLE_HOST_LOOKUP:-true}
for retry_setting in CASSANDRA_WAIT_MAX_RETRIES CASSANDRA_WAIT_RETRY_SECONDS CASSANDRA_STABLE_ATTEMPTS MIGRATE_MAX_RETRIES MIGRATE_RETRY_SECONDS; do
  eval "retry_value=\${${retry_setting}}"
  case "$retry_value" in
    ''|*[!0-9]*|0*)
      echo "ERROR: ${retry_setting} must be a positive integer, got: ${retry_value}" >&2
      exit 1
      ;;
  esac
done
case "$MIGRATE_PROTOCOL" in
  ''|*[!0-9]*|0*)
    echo "ERROR: MIGRATE_PROTOCOL must be a positive integer, got: ${MIGRATE_PROTOCOL}" >&2
    exit 1
    ;;
esac
case "$MIGRATE_DISABLE_HOST_LOOKUP" in
  true|false) ;;
  *)
    echo "ERROR: MIGRATE_DISABLE_HOST_LOOKUP must be true or false, got: ${MIGRATE_DISABLE_HOST_LOOKUP}" >&2
    exit 1
    ;;
esac

run_cqlsh() {
  host="$1"
  shift

  timeout "$CASSANDRA_CQLSH_COMMAND_TIMEOUT" \
    cqlsh \
      --connect-timeout "$CASSANDRA_CQLSH_CONNECT_TIMEOUT" \
      --request-timeout "$CASSANDRA_CQLSH_REQUEST_TIMEOUT" \
      -u "$CASSANDRA_USER" -p "$CASSANDRA_PASSWORD" "$host" "$CASSANDRA_PORT" "$@"
}

probe_cassandra_host() {
  run_cqlsh "$1" -e "select key from system.local;" > /dev/null 2>&1
}

wait_for_stable_cassandra_hosts() {
  stable_attempts=0
  attempt=1

  while [ "$attempt" -le "$CASSANDRA_WAIT_MAX_RETRIES" ]; do
    failed_hosts=""

    for host in $CASSANDRA_READY_HOSTS; do
      if ! probe_cassandra_host "$host"; then
        failed_hosts="${failed_hosts} ${host}"
      fi
    done

    if [ -z "$failed_hosts" ]; then
      stable_attempts=$((stable_attempts + 1))
      echo "[wait-for-cassandra] Stability check ${stable_attempts}/${CASSANDRA_STABLE_ATTEMPTS} succeeded for all hosts"
      if [ "$stable_attempts" -ge "$CASSANDRA_STABLE_ATTEMPTS" ]; then
        echo "Cassandra cqlsh superuser is stable on all requested hosts"
        return 0
      fi
    else
      stable_attempts=0
      echo "[wait-for-cassandra] Waiting for stable cqlsh on:${failed_hosts} (${attempt}/${CASSANDRA_WAIT_MAX_RETRIES})"
    fi

    attempt=$((attempt + 1))
    sleep "$CASSANDRA_WAIT_RETRY_SECONDS"
  done

  echo "Failed to observe stable Cassandra cqlsh after ${CASSANDRA_WAIT_MAX_RETRIES} attempts"
  return 1
}

if ! wait_for_stable_cassandra_hosts; then
  exit 1
fi

#
# Pre-process SQL files with environment variable substitution
# This allows configurable values like SERVICE_ROLE_PASSWORD and REPLICA_COUNT
#
# SECURITY: Only explicitly listed variables are substituted to prevent
# unintended substitution of other environment variables
# shellcheck disable=SC2016
ENVSUBST_VARS='$SERVICE_ROLE_PASSWORD $REPLICA_COUNT'

TEMP_KEYSPACES="/tmp/keyspaces"
echo "Pre-processing SQL files with environment variable substitution..."
echo "Allowed variables: ${ENVSUBST_VARS}"

for keyspace_dir in /app/keyspaces/*; do
  # Only directories are keyspaces. keyspaces/ also holds README.md, which would
  # otherwise become a keyspace named README.md and fail the migrate call below.
  [ -d "$keyspace_dir" ] || continue

  keyspace_name=$(basename "$keyspace_dir")
  mkdir -p "$TEMP_KEYSPACES/$keyspace_name"

  for sql_file in "$keyspace_dir"/*.sql "$keyspace_dir"/*.cql; do
    # Skip if no files match the glob pattern
    [ -e "$sql_file" ] || continue

    filename=$(basename "$sql_file")
    envsubst "${ENVSUBST_VARS}" < "$sql_file" > "$TEMP_KEYSPACES/$keyspace_name/$filename"
  done
done

echo "SQL files pre-processed successfully"

percent_encode() {
  printf '%s' "$1" | python3 -c \
    'import sys; from urllib.parse import quote; print(quote(sys.stdin.read(), safe=""))'
}

{ set +x; } 2>/dev/null
CASSANDRA_USER_ENCODED=$(percent_encode "$CASSANDRA_USER") || {
  echo "Failed to percent-encode the Cassandra username" >&2
  exit 1
}
CASSANDRA_PASSWORD_ENCODED=$(percent_encode "$CASSANDRA_PASSWORD") || {
  echo "Failed to percent-encode the Cassandra password" >&2
  exit 1
}

run_migrations_for_keyspace() {
  keyspace_dir="$1"
  migration_table_name=$(basename "$keyspace_dir")
  attempt=1

  while [ "$attempt" -le "$MIGRATE_MAX_RETRIES" ]; do
    migrate_output=$(migrate \
      -path "$keyspace_dir" \
      -database "cassandra://${CASSANDRA_HOSTS}:${CASSANDRA_PORT}/schema_migrations?x-multi-statement=true&x-migrations-table=${migration_table_name}&username=${CASSANDRA_USER_ENCODED}&password=${CASSANDRA_PASSWORD_ENCODED}&consistency=${MIGRATE_CONSISTENCY}&protocol=${MIGRATE_PROTOCOL}&timeout=${MIGRATE_TIMEOUT}&connect-timeout=${MIGRATE_CONNECT_TIMEOUT}&disable-host-lookup=${MIGRATE_DISABLE_HOST_LOOKUP}" \
      up 2>&1)
    migrate_status=$?
    printf '%s\n' "$migrate_output"

    if [ "$migrate_status" -eq 0 ]; then
      return 0
    fi

    case "$migrate_output" in
      *"Dirty database version "*". Fix and force version."*)
        echo "Migration state is dirty for ${keyspace_dir}; refusing to retry automatically" >&2
        return "$migrate_status"
        ;;
    esac

    if [ "$attempt" -eq "$MIGRATE_MAX_RETRIES" ]; then
      return "$migrate_status"
    fi

    echo "Migration failed for ${keyspace_dir} on attempt ${attempt}/${MIGRATE_MAX_RETRIES}; retrying in ${MIGRATE_RETRY_SECONDS}s"
    attempt=$((attempt + 1))
    sleep "$MIGRATE_RETRY_SECONDS"
  done
}

#
# For each of our keyspaces, execute the *.up.sql files in order
#
for each in $TEMP_KEYSPACES/*
do
  [ -d "${each}" ] || continue

  echo "Applying ${each}"

  if ! run_migrations_for_keyspace "${each}"; then
    echo "Migration failed for ${each}"
    exit 1
  fi
done

echo "All Cassandra migrations completed successfully"
