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

set -euo pipefail

# Setup Gateway API infrastructure for k3d cluster with Envoy Gateway
# This script installs the Gateway API CRDs and Envoy Gateway.
# It is idempotent and can be run multiple times.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Source the shared utility functions
# shellcheck source=./lib.sh
source "${SCRIPT_DIR}/lib.sh"

# --- Configuration ---
ENVOY_GATEWAY_NAMESPACE="envoy-gateway-system"
ENVOY_GATEWAY_RELEASE_NAME="eg"
ENVOY_GATEWAY_VERSION="v1.5.4"
ENVOY_GATEWAY_CHART="oci://docker.io/envoyproxy/gateway-helm"

# --- Pre-flight Checks ---
if ! command -v kubectl &> /dev/null; then
    log_error "kubectl not found. Please install kubectl."
    exit 1
fi

if ! command -v helm &> /dev/null; then
    log_error "helm not found. Please install helm."
    exit 1
fi

if ! kubectl cluster-info &> /dev/null; then
    log_error "Cannot connect to Kubernetes cluster. Please check your kubeconfig."
    exit 1
fi

# --- Main Logic ---
log_info "Setting up Gateway API infrastructure..."

# Step 1: Install or Upgrade Envoy Gateway using Helm (OCI registry)
# The chart installs Gateway API CRDs from the experimental channel by default,
# which includes TCPRoute, GRPCRoute, BackendTLSPolicy, and other alpha resources.
log_info "Installing/Upgrading Envoy Gateway (${ENVOY_GATEWAY_VERSION})..."
helm upgrade --install ${ENVOY_GATEWAY_RELEASE_NAME} ${ENVOY_GATEWAY_CHART} \
    --version ${ENVOY_GATEWAY_VERSION} \
    --namespace ${ENVOY_GATEWAY_NAMESPACE} \
    --create-namespace \
    --wait
log_info "OK Envoy Gateway Helm chart applied successfully."

# Step 2: Apply GatewayClass and Gateway resources
log_info "Applying Envoy Gateway configuration..."
kubectl apply -k "${PROJECT_ROOT}/apps/envoy-gateway"
log_info "OK Envoy Gateway configuration applied."

# Step 3: Wait for GatewayClass to become ready
log_info "Waiting for Envoy GatewayClass to become ready..."
for i in {1..30}; do
    if kubectl get gatewayclass eg &> /dev/null; then
        STATUS=$(kubectl get gatewayclass eg -o jsonpath='{.status.conditions[?(@.type=="Accepted")].status}' 2>/dev/null || echo "")
        if [ "$STATUS" = "True" ]; then
            log_info "  OK GatewayClass 'eg' is ready and accepted."
            break
        fi
    fi

    if [ $i -eq 30 ]; then
        log_error "Timeout waiting for GatewayClass 'eg' to become ready."
        exit 1
    fi
    sleep 2
done

log_info ""
log_info "OK Gateway API infrastructure setup is complete!"
log_info "Available GatewayClasses:"
kubectl get gatewayclass -o wide
log_info ""
