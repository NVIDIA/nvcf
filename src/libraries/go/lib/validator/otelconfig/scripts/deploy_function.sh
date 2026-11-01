#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/environment.sh"

echo -e "==================="
echo "Create Function"
date
echo -e "==================="

if [[ $FUNCTION_WORKLOAD_TYPE == "helm" ]]; then
    curl -sS -w '\n' "$NGC_CLI_API_URL/v2/orgs/$NGC_CLI_ORG/nvcf/functions" \
        --header "Authorization: Bearer $NGC_CLI_API_KEY" \
        --header "Content-Type: application/json" \
        --data "{
        \"name\": \"$FUNCTION_NAME\",
        \"inferenceUrl\": \"$FUNCTION_INFERENCE_PATH\",
        \"inferencePort\": $FUNCTION_INFERENCE_PORT,
        \"helmChartServiceName\": \"$FUNCTION_HELM_CHART_SERVICE_NAME\",
        \"helmChart\": \"$FUNCTION_HELM_CHART\",    
        \"health\": {
            \"protocol\": \"HTTP\",
            \"uri\": \"$FUNCTION_HEALTH_PATH\",
            \"port\": $FUNCTION_HEALTH_PORT,
            \"timeout\": \"PT10S\",
            \"expectedStatusCode\": 200
        },
        \"telemetries\": {
            \"metricsTelemetryId\": \"$FUNCTION_TELEMETRY_METRICS_ID\",
            \"logsTelemetryId\": \"$FUNCTION_TELEMETRY_LOGS_ID\"
        }
    }" >deploy_result.json
else
    curl -sS -w '\n' "$NGC_CLI_API_URL/v2/orgs/$NGC_CLI_ORG/nvcf/functions" \
        --header "Authorization: Bearer $NGC_CLI_API_KEY" \
        --header "Content-Type: application/json" \
        --data "{
        \"name\": \"$FUNCTION_NAME\",
        \"inferenceUrl\": \"$FUNCTION_INFERENCE_PATH\",
        \"inferencePort\": $FUNCTION_INFERENCE_PORT, 
        \"containerImage\": \"$FUNCTION_CONTAINER_IMAGE\",
        \"health\": {
            \"protocol\": \"HTTP\",
            \"uri\": \"$FUNCTION_HEALTH_PATH\",
            \"port\": $FUNCTION_HEALTH_PORT,
            \"timeout\": \"PT10S\",
            \"expectedStatusCode\": 200
        },
        \"telemetries\": {
            \"metricsTelemetryId\": \"$FUNCTION_TELEMETRY_METRICS_ID\",
            \"logsTelemetryId\": \"$FUNCTION_TELEMETRY_LOGS_ID\"
        }
    }" >deploy_result.json
fi

cat deploy_result.json

echo -e "==================="
echo "Deploy Function"
date
echo -e "==================="

read -r FUNCTION_ID FUNCTION_VERSION_ID < <(cat deploy_result.json | jq -r '[ .function.id, .function.versionId] | join(" ")')
echo "FUNCTION_ID=${FUNCTION_ID}, FUNCTION_VERSION_ID=${FUNCTION_VERSION_ID}"

if [[ $FUNCTION_WORKLOAD_TYPE == "helm" ]]; then
    value=$(yq -o=json -I 0 ${SCRIPT_DIR}/values-function.yaml)
    ngc cf function deploy create \
        $FUNCTION_ID:$FUNCTION_VERSION_ID \
        --deployment-specification $NVCF_BACKEND:$NVCF_BACKEND_GPU_TYPE:$NVCF_BACKEND_INSTANCE_TYPE:1:1 \
        --configuration "$value" \
        --org ${NGC_CLI_ORG} --team ${NGC_CLI_TEAM}
else
    ngc cf function deploy create \
        $FUNCTION_ID:$FUNCTION_VERSION_ID \
        --deployment-specification $NVCF_BACKEND:$NVCF_BACKEND_GPU_TYPE:$NVCF_BACKEND_INSTANCE_TYPE:1:1 \
        --org ${NGC_CLI_ORG} --team ${NGC_CLI_TEAM}
fi

if [[ -z "$FUNCTION_ID" ]]; then
    exit 1
fi

echo -e "==================="
echo "Check Function Status"
date
echo -e "==================="

# for loop following command until status is ACTIVE or ERROR
while true; do
    response=$(ngc cf function info $FUNCTION_ID:$FUNCTION_VERSION_ID --org ${NGC_CLI_ORG} --team ${NGC_CLI_TEAM} --format_type json)
    status=$(echo "$response" | jq -r ".status")
    echo "Current status: $status"

    if [[ "$status" == "ERROR" || "$status" == "INACTIVE" ]]; then
        exit 1
    elif [[ "$status" == "ACTIVE" ]]; then
        echo "$response"
        break
    fi

    sleep 30
done
