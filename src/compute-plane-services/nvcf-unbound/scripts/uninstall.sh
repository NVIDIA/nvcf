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

# Uninstall the webhook from Kubernetes

echo "🗑️  Uninstalling NVCF Pod Mutator Webhook"
echo ""

# Delete webhook configuration first (important!)
echo "📝 Removing webhook configuration..."
kubectl delete mutatingwebhookconfiguration nvcf-pod-mutator 2>/dev/null || echo "   (not found)"

# Delete deployment
echo "📝 Removing deployment..."
kubectl delete -f deploy/webhook-deployment.yaml 2>/dev/null || echo "   (not found)"

# Optionally delete namespace
read -p "Delete nvcf-webhook namespace? (y/N): " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    echo "📝 Removing namespace..."
    kubectl delete namespace nvcf-webhook
fi

echo ""
echo "✅ Uninstall complete!"


