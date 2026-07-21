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


timeout=120
interval=5
while [ $timeout -gt 0 ]; do
  if nc -z -w 1 ${CASSANDRA_HOSTS} 9042; then
    echo "Cassandra hosts are up"
    break
  fi
  echo "Waiting for Cassandra hosts to be available..."
  sleep $interval
  timeout=$((timeout - interval))
done

# Verify the superuser and the cqlsh listener is available
max_retries=10 # 10 * 1s = 10s
attempt=1
until cqlsh -u "$CASSANDRA_USER" -p "$CASSANDRA_PASSWORD" "$CASSANDRA_HOSTS" 9042 -e "select key from system.local;" > /dev/null 2>&1; do
    echo "[wait-for-cassandra] Attempt ${attempt}/${max_retries} failed. Waiting before retry..."
    if [ "$attempt" -eq "$max_retries" ]; then
      echo "Failed to connect to Cassandra after ${max_retries} attempts"
      exit 1
    fi
    attempt=$((attempt+1))
    sleep 1
done
echo "Cassandra cqlsh superuser is available"

#
# Pre-process SQL files with environment variable substitution
# This allows configurable values like SERVICE_ROLE_PASSWORD
#
# SECURITY: Only explicitly listed variables are substituted to prevent
# unintended substitution of other environment variables
ENVSUBST_VARS='$SERVICE_ROLE_PASSWORD'

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

#
# For each of our keyspaces, execute the *.up.sql files in order
#
for each in $TEMP_KEYSPACES/*
do
  [ -d "${each}" ] || continue

  migration_table_name=`basename ${each}`
  echo "Applying ${each}"
  migrate \
  -path "${each}" \
  -database "cassandra://${CASSANDRA_HOSTS}:9042/schema_migrations?x-multi-statement=true&x-migrations-table=${migration_table_name}&username=${CASSANDRA_USER}&password=${CASSANDRA_PASSWORD}" \
  up

  # Check for errors in migration process
  if [ $? -ne 0 ]; then
    echo "Migration failed for ${each}"
    exit 1
  fi
done

echo "All Cassandra migrations completed successfully"
