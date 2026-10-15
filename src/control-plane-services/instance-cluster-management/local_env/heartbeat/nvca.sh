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
set -euo pipefail

HEADER="Authorization: Bearer auth_token"
CLUSTER_ID="cluster_id_here"
BODY='{"status":"healthy","gpuUsage":{"A300":{"capacity":20,"allocated":11,"available":9},"GPU2":{"capacity":30,"allocated":10,"available":20}}}'
URL="http://localhost:8080/v1/nvca/clusters"

while true; do
    set +e
    HTTP_STATUS=$(curl -o /dev/null -s -w "%{http_code}" \
        --connect-timeout 5 --max-time 10 \
        -X POST \
        -H "Content-Type: application/json" \
        -H "${HEADER}" \
        -d "${BODY}" \
        "${URL}/${CLUSTER_ID}/heartbeat")
    CURL_EXIT=$?
    set -e
    if [ "${CURL_EXIT}" -ne 0 ]; then
        HTTP_STATUS="000"
        echo "NVCA heartbeat status: ${HTTP_STATUS} (curl failed, exit code ${CURL_EXIT})"
    else
        echo "NVCA heartbeat status: ${HTTP_STATUS}"
    fi
    sleep 30
done
