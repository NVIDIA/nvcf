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
# Stop the local Cassandra instance. Pass --purge (or -v) to also delete the
# cassandra_data volume for a clean, empty database on the next start.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

case "${1:-}" in
  --purge|-v)
    echo "Stopping Cassandra and removing the cassandra_data volume ..."
    docker compose -f "${COMPOSE_FILE}" down -v
    ;;
  "")
    echo "Stopping Cassandra (volume preserved) ..."
    docker compose -f "${COMPOSE_FILE}" down
    ;;
  *)
    echo "Usage: $(basename "$0") [--purge|-v]" >&2
    exit 2
    ;;
esac

echo "Done."
