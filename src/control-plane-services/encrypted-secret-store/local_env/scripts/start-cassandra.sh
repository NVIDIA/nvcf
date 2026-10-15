#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
#
# Start the local Cassandra instance and apply the ESS schema, then block until
# the schema is present so the ESS service can connect immediately after.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

echo "Starting Cassandra (compose project ess-local) ..."
docker compose -f "${COMPOSE_FILE}" up -d

echo "Waiting for the ess keyspace to be created by cassandra-init ..."
until docker compose -f "${COMPOSE_FILE}" exec -T cassandra \
        cqlsh -e 'describe keyspaces' 2>/dev/null | grep -qw ess; do
  echo "  ... waiting for Cassandra to accept connections and apply schema"
  sleep 3
done

echo "Cassandra is ready on 127.0.0.1:9042 (ess keyspace present)."
