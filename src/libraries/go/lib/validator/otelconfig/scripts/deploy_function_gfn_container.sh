#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


NVCF_BACKEND=GFN
NVCF_BACKEND_GPU_TYPE=A10G
NVCF_BACKEND_INSTANCE_TYPE=ga10g_1.br20_2xlarge

FUNCTION_NAME=byoo-metrics-validating-test-gfn-$(whoami)-$(date '+%H%M%S')
FUNCTION_WORKLOAD_TYPE="container"
FUNCTION_CONTAINER_IMAGE="stg.nvcr.io/tadiathdfetp/byoo-test:0.11"
FUNCTION_INFERENCE_PATH="/byoo"
FUNCTION_INFERENCE_PORT=8000
FUNCTION_HEALTH_PATH="/health"
FUNCTION_HEALTH_PORT=8000
FUNCTION_TELEMETRY_METRICS_ID="569444f0-d9cc-4384-98b0-78bf44d76d44" # grafana-cloud-nvidiacloudfunctions
FUNCTION_TELEMETRY_LOGS_ID="569444f0-d9cc-4384-98b0-78bf44d76d44" # grafana-cloud-nvidiacloudfunctions

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/deploy_function.sh"

# Repeat 3 times to generate more data
for i in {1..3}; do
    source "${SCRIPT_DIR}/invoke_function.sh"
    sleep 5
done

# make sure byoo-otel-collector has enough time to export metrics
echo "Waiting for metrics to be exported..."
sleep 60

echo "Deploy and invoke Success."
