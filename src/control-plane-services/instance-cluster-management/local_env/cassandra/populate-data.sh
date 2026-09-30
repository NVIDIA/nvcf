#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# populate-data.sh - seed the local Cassandra with cqlsh COPY TO style CSV exports.
#
# Expects one <table>.csv per table in CSV_DIR (cqlsh COPY TO format with a header
# row). CSV columns that do not exist in the local table are dropped before
# loading, so exports taken from a different schema version still load. The
# surviving columns are passed to cqlsh COPY as an explicit column list because
# cqlsh 6.x (Cassandra 5) only skips the header row and otherwise expects table
# column order.
#
# Configuration via environment variables:
#   CSV_DIR            directory with CSV files            (default /csv)
#   KEYSPACE           target keyspace                     (default test)
#   CASSANDRA_HOST     Cassandra host                      (default cassandra)
#   CASSANDRA_PORT     CQL port                            (default 9042)
#   CASSANDRA_USER     CQL user                            (default cassandra)
#   CASSANDRA_PASSWORD CQL password                        (default cassandra)
#   SCHEMA_TIMEOUT_S   max wait for schema init, seconds   (default 600)
#
# The script waits for CQL and for each target table to appear, so it can start
# at the same time as the cassandra service and its schema init entrypoint.

set -euo pipefail

CSV_DIR="${CSV_DIR:-/csv}"
KEYSPACE="${KEYSPACE:-test}"
CASSANDRA_HOST="${CASSANDRA_HOST:-cassandra}"
CASSANDRA_PORT="${CASSANDRA_PORT:-9042}"
CASSANDRA_USER="${CASSANDRA_USER:-cassandra}"
CASSANDRA_PASSWORD="${CASSANDRA_PASSWORD:-cassandra}"
SCHEMA_TIMEOUT_S="${SCHEMA_TIMEOUT_S:-600}"

WORKDIR="$(mktemp -d /tmp/populate-data.XXXXXX)"
trap 'rm -rf "$WORKDIR"' EXIT

cql() {
  cqlsh "$CASSANDRA_HOST" "$CASSANDRA_PORT" -u "$CASSANDRA_USER" -p "$CASSANDRA_PASSWORD" "$@"
}

log() {
  echo "[populate-data] $*"
}

wait_for_cql() {
  log "waiting for CQL on ${CASSANDRA_HOST}:${CASSANDRA_PORT}"
  local waited=0
  until cql -e 'DESCRIBE CLUSTER' >/dev/null 2>&1; do
    sleep 5
    waited=$((waited + 5))
    if [ "$waited" -ge "$SCHEMA_TIMEOUT_S" ]; then
      log "ERROR: CQL not ready after ${SCHEMA_TIMEOUT_S}s"
      exit 1
    fi
  done
}

# Prints one "name:type" token per column of the given table. Whitespace is
# stripped so the tokens survive unquoted word splitting. Polls until the
# table appears, so it doubles as the schema-init wait.
table_columns() {
  cql -e "SELECT column_name, type FROM system_schema.columns \
WHERE keyspace_name='${KEYSPACE}' AND table_name='$1';" 2>/dev/null \
    | awk -F'|' '$2 != "" && $1 !~ /column_name/ {
        name = $1; type = $2
        gsub(/[ \t]/, "", name); gsub(/[ \t]/, "", type)
        if (name != "") print name ":" type
      }'
}

wait_for_table() {
  local table="$1"
  local waited=0
  local cols=""
  while true; do
    cols="$(table_columns "$table" || true)"
    if [ -n "$cols" ]; then
      printf '%s' "$cols"
      return 0
    fi
    sleep 5
    waited=$((waited + 5))
    if [ "$waited" -ge "$SCHEMA_TIMEOUT_S" ]; then
      log "ERROR: table ${KEYSPACE}.${table} not created after ${SCHEMA_TIMEOUT_S}s"
      return 1
    fi
  done
}

FILTER_PY="$WORKDIR/filter-csv-columns.py"
cat > "$FILTER_PY" <<'PYEOF'
import csv
import sys

# argv[1] is the output path, the rest are "name:type" tokens for every column
# of the target table. Reads the source CSV from stdin, drops columns that do
# not exist in the table, and prints the kept column names on stdout.
output_path = sys.argv[1]
column_types = {}
for token in sys.argv[2:]:
    name, _, col_type = token.partition(":")
    column_types[name] = col_type

reader = csv.reader(sys.stdin)
header = next(reader, None)
if header is None:
    sys.exit(0)

keep_idx = [i for i, name in enumerate(header) if name in column_types]
dropped = [name for name in header if name not in column_types]
if dropped:
    sys.stderr.write("[populate-data] dropping unknown columns: %s\n" % ",".join(dropped))

with open(output_path, "w", newline="") as out_file:
    writer = csv.writer(out_file, lineterminator="\n")
    writer.writerow([header[i] for i in keep_idx])
    for row in reader:
        out = []
        for i in keep_idx:
            value = row[i] if i < len(row) else ""
            # cqlsh exports empty collections as {}, which COPY FROM cannot
            # parse back; load them as null instead.
            base_type = column_types[header[i]].split("<")[0]
            if value == "{}" and base_type in ("set", "map", "list", "frozen"):
                value = ""
            out.append(value)
        writer.writerow(out)

sys.stdout.write(",".join(header[i] for i in keep_idx))
PYEOF

wait_for_cql
log "CQL is ready, scanning ${CSV_DIR} for CSV files"

shopt -s nullglob
csv_files=("$CSV_DIR"/*.csv)
if [ ${#csv_files[@]} -eq 0 ]; then
  log "ERROR: no CSV files found in ${CSV_DIR}"
  exit 1
fi

for csv in "${csv_files[@]}"; do
  table="$(basename "$csv" .csv)"
  log "loading ${KEYSPACE}.${table} from ${csv}"
  columns="$(wait_for_table "$table")"
  filtered="$WORKDIR/$table.csv"
  # shellcheck disable=SC2086
  kept_columns="$(python3 "$FILTER_PY" "$filtered" $columns < "$csv")"
  if [ -z "$kept_columns" ]; then
    log "SKIP ${KEYSPACE}.${table}: no CSV columns match the table"
    continue
  fi
  # MAXBATCHSIZE=1: cqlsh COPY groups rows into UNLOGGED batches and whole
  # batches are rejected once they exceed batch_size_fail_threshold_in_kb
  # (50KiB), which drops rows silently on tables with large text columns.
  # NUMPROCESSES=1 keeps the import single threaded; a few thousand rows load
  # in seconds and multi-worker error paths drop rows on UDT tables.
  cql -e "COPY ${KEYSPACE}.${table} (${kept_columns}) FROM '${filtered}' \
WITH HEADER=true AND NUMPROCESSES=1 AND MINBATCHSIZE=1 AND MAXBATCHSIZE=1;"
  log "loaded ${KEYSPACE}.${table}"
done

log "all CSV files loaded"
