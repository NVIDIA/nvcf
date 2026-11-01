#!/bin/bash
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

set -e

echo "==> Running timing drift integration test for ess-agent"

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

echo "==> Cleaning up any existing containers"
docker ps -a --filter "name=ess-agent" --format "{{.ID}}" | xargs -r docker rm -f

cleanup() {
    echo "==> Cleaning up container on exit"
    if [ ! -z "$CONTAINER_ID" ]; then
        docker stop $CONTAINER_ID >/dev/null 2>&1 || true
    fi
}

trap cleanup SIGINT SIGTERM

echo "==> Step 1: Cleaning up secrets directory"
rm -rf "${CURRENT_DIR}/nv_releases/test/secrets/mockoon-example.json"

echo "==> Step 2: Running in init mode"
docker run --rm -it \
    -e "ESS_AGENT_INIT=true" \
    -e "SECRET_PATH=functions/ca713143-76d6-4afe-beba-fb23923446f6/secrets" \
    --mount type=bind,source="${CURRENT_DIR}/nv_releases/test/configs",target=/ess-agent/file/configs \
    --mount type=bind,source="${CURRENT_DIR}/nv_releases/test/templates",target=/ess-agent/file/templates \
    --mount type=bind,source="${CURRENT_DIR}/nv_releases/test/secrets",target=/ess-agent/file/secrets \
    ess-agent/distroless-go-multi-arch:latest \
    -config=/ess-agent/file/configs/config-docker-integration-tests.hcl \
    -log-level=trace

echo "==> Step 3: Verifying initial secret file content"
if [ ! -f "${CURRENT_DIR}/nv_releases/test/secrets/mockoon-example.json" ]; then
    echo "Error: Secret file not created in init mode"
    exit 1
fi

INIT_CONTENT=$(cat "${CURRENT_DIR}/nv_releases/test/secrets/mockoon-example.json")

echo "==> Step 4: Running in non-init mode (background)"
CONTAINER_ID=$(docker run --rm -d \
    -e "SECRET_PATH=functions/ca713143-76d6-4afe-beba-fb23923446f6/secrets" \
    --mount type=bind,source="${CURRENT_DIR}/nv_releases/test/configs",target=/ess-agent/file/configs \
    --mount type=bind,source="${CURRENT_DIR}/nv_releases/test/templates",target=/ess-agent/file/templates \
    --mount type=bind,source="${CURRENT_DIR}/nv_releases/test/secrets",target=/ess-agent/file/secrets \
    ess-agent/distroless-go-multi-arch:latest \
    -config=/ess-agent/file/configs/config-docker-integration-tests.hcl \
    -log-level=trace)

echo "==> Container ID: $CONTAINER_ID"
echo "==> To monitor container logs in another terminal, run:"
echo "    docker logs -f $CONTAINER_ID"

echo "==> Step 5: Monitoring secret file content for 2 minutes every 1 second interval"
END_TIME=$((SECONDS + 120))
CHECK_INTERVAL=1

while [ $SECONDS -lt $END_TIME ]; do
    if [ ! -f "${CURRENT_DIR}/nv_releases/test/secrets/mockoon-example.json" ]; then
        echo "Error: Secret file disappeared during monitoring"
        exit 1
    fi

    CURRENT_CONTENT=$(cat "${CURRENT_DIR}/nv_releases/test/secrets/mockoon-example.json")
    if [ "$INIT_CONTENT" != "$CURRENT_CONTENT" ]; then
        echo "Error: Secret file content changed during monitoring"
        exit 1
    fi

    echo "Secret content remains unchanged at $(date +%H:%M:%S)"
    sleep $CHECK_INTERVAL
done

echo "==> Step 6: Analyzing renewal logs"
echo "==> Waiting for 2 more minutes to collect renewal data..."
sleep 120

# Get logs and extract renewal information
RENEWAL_DATA=$(docker logs $CONTAINER_ID 2>&1 | grep -E "ESS request.*ESS Agent Id|next renewal in" | awk '
    /ESS request.*ESS Agent Id/ {
        request_time = $1
        getline
        if ($0 ~ /next renewal in/) {
            renewal_time = $NF
            gsub(/"/, "", renewal_time)
            gsub(/s/, "", renewal_time)
            print request_time, renewal_time
        }
    }
')

echo "==> Renewal Analysis Report"
echo "================================================================================="
echo "Request Time                | Renewal Duration (s)"
echo "---------------------------------------------------------------------------------"
echo "$RENEWAL_DATA" | while read -r line; do
    if [ ! -z "$line" ]; then
        request_time=$(echo "$line" | awk '{print $1}')
        renewal_time=$(echo "$line" | awk '{print $2}')
        printf "%-27s | %s\n" "$request_time" "$renewal_time"
    fi
done
echo "================================================================================="

echo "==> Step 7: Cleaning up"
cleanup

echo "==> Integration test passed successfully"
