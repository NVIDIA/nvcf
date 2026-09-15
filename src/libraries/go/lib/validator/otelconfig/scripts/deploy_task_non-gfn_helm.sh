#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


NVCT_BACKEND=nvcf-qa-cluster-gcp
NVCT_BACKEND_GPU_TYPE=H100
NVCT_BACKEND_INSTANCE_TYPE=GCP.GPU.H100_1x

TASK_NAME=byoo-metrics-validating-test-non-gfn-$(whoami)-$(date '+%H%M%S')
TASK_WORKLOAD_TYPE="helm"
TASK_HELM_CHART="tadiathdfetp/task-helmchart-byoo:0.3"
TASK_TELEMETRY_METRICS_ID="569444f0-d9cc-4384-98b0-78bf44d76d44" # grafana-cloud-nvidiacloudfunctions
TASK_TELEMETRY_LOGS_ID="569444f0-d9cc-4384-98b0-78bf44d76d44"    # grafana-cloud-nvidiacloudfunctions

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/deploy_task.sh"

# make sure byoo-otel-collector has enough time to export metrics
echo "Waiting for metrics to be exported..."
sleep 60

echo "Task is completed."
