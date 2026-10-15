#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2023-2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

# Deploy the webhook to Kubernetes

echo "🚀 Deploying NVCF Pod Mutator Webhook"
echo ""

# Check if namespace exists
if ! kubectl get namespace nvcf-webhook &>/dev/null; then
    echo "📦 Creating namespace..."
    kubectl create namespace nvcf-webhook
fi

# Deploy the webhook
echo "📝 Applying webhook deployment..."
kubectl apply -f deploy/webhook-deployment.yaml

# Wait for deployment to be ready
echo "⏳ Waiting for deployment to be ready..."
kubectl rollout status deployment/nvcf-pod-mutator -n nvcf-webhook --timeout=300s

# Show status
echo ""
echo "✅ Deployment complete!"
echo ""
echo "📊 Status:"
kubectl get pods -n nvcf-webhook -l app=nvcf-pod-mutator
echo ""
kubectl get service -n nvcf-webhook nvcf-pod-mutator
echo ""
kubectl get mutatingwebhookconfiguration nvcf-pod-mutator

echo ""
echo "🎯 Webhook is ready to mutate pods!"
echo ""
echo "📋 To test:"
echo "   1. Create a pod in a namespace with label:"
echo "      nvca.nvcf.nvidia.io/instance-type=pod_spec"
echo "   2. Check the pod has init containers: a-toolbox, fast-merge-certs"
echo "   3. Check the pod has custom DNS configuration"
echo ""
echo "🔍 To view webhook logs:"
echo "   kubectl logs -n nvcf-webhook -l app=nvcf-pod-mutator -f"


