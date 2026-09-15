#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/environment.sh"

echo -e "==================="
echo "Create Task"
date
echo -e "==================="

if [[ $TASK_WORKLOAD_TYPE == "helm" ]]; then
    ngc cf task create \
        --name $TASK_NAME \
        --helm-chart $TASK_HELM_CHART \
        --gpu-specification $NVCT_BACKEND_GPU_TYPE:$NVCT_BACKEND_INSTANCE_TYPE:$NVCT_BACKEND \
        --result-handling-strategy UPLOAD \
        --result-location $NGC_CLI_ORG/byoo \
        --max-runtime-duration 1H \
        --max-queued-duration 1H \
        --termination-grace-period-duration 1H \
        --logs-telemetry-id $TASK_TELEMETRY_LOGS_ID \
        --metrics-telemetry-id $TASK_TELEMETRY_METRICS_ID \
        --format_type json \
        --secret NGC_API_KEY:$NGC_CLI_API_KEY >deploy_result.json
else
    ngc cf task create \
        --name $TASK_NAME \
        --container-image $TASK_CONTAINER_IMAGE \
        --gpu-specification $NVCT_BACKEND_GPU_TYPE:$NVCT_BACKEND_INSTANCE_TYPE:$NVCT_BACKEND \
        --result-handling-strategy UPLOAD \
        --result-location $NGC_CLI_ORG/byoo \
        --max-runtime-duration 1H \
        --max-queued-duration 1H \
        --termination-grace-period-duration 1H \
        --logs-telemetry-id $TASK_TELEMETRY_LOGS_ID \
        --metrics-telemetry-id $TASK_TELEMETRY_METRICS_ID \
        --secret NGC_API_KEY:$NGC_CLI_API_KEY \
        --format_type json \
        --container-environment-variable NUM_OF_RESULTS:3 >deploy_result.json
fi

cat deploy_result.json

echo -e "==================="
echo "Check Task Status"
date
echo -e "==================="

read -r TASK_ID < <(cat deploy_result.json | jq -r '.id')
echo "TASK_ID=${TASK_ID}"

# for loop following command until status is ACTIVE or ERROR
while true; do
    response=$(ngc cf task info $TASK_ID --org ${NGC_CLI_ORG} --team ${NGC_CLI_TEAM} --format_type json)
    status=$(echo "$response" | jq -r ".status")
    echo "Current status: $status"

    if [[ "$status" == "ERRORED" || "$status" == "CANCELED" ]]; then
        exit 1
    elif [[ "$status" == "COMPLETED" ]]; then
        echo "$response"
        break
    fi

    sleep 30
done
