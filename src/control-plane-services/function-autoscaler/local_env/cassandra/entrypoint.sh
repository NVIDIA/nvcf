#!/usr/bin/env bash

# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

cd /docker-entrypoint-initdb.d || exit 1
while ! cqlsh -e 'describe cluster' -u cassandra -p cassandra >/dev/null 2>&1; do sleep 6; done
echo "Cassandra cluster ready: executing cql scripts found in docker-entrypoint-initdb.d"
for f in $(find . -type f -name "*.cql" -print | sort); do
  echo "running $f"
  cqlsh -f "$f" -u cassandra -p cassandra
  echo "$f executed"
done
echo "Cassandra init scripts executed"
