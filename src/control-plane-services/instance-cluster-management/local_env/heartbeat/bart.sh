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

BART_URL="http://localhost:8080/v1/bart/heartbeat"
BODY='{"status":"healthy"}'
HEADER="Authorization: Bearer auth_token"

while true; do
    set +e
    HTTP_STATUS=$(curl -o /dev/null -s -w "%{http_code}" \
        --connect-timeout 5 --max-time 10 \
        -X PUT \
        -H "Content-Type: application/json" \
        -H "${HEADER}" \
        -d "${BODY}" \
        "${BART_URL}")
    CURL_EXIT=$?
    set -e
    if [ "${CURL_EXIT}" -ne 0 ]; then
        HTTP_STATUS="000"
        echo "BART heartbeat status: ${HTTP_STATUS} (curl failed, exit code ${CURL_EXIT})"
    else
        echo "BART heartbeat status: ${HTTP_STATUS}"
    fi
    sleep 60
done
