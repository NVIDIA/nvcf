#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

CLUSTER_NAME="ncp-local"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
log()  { echo -e "${GREEN}[info]${NC} $1"; }
warn() { echo -e "${YELLOW}[warn]${NC} $1"; }
die()  { echo -e "${RED}[error]${NC} $1"; exit 1; }

for cmd in k3d kubectl helm helmfile; do
    command -v "$cmd" &>/dev/null || die "$cmd is required. See README for install steps."
done
helm plugin list 2>/dev/null | grep -q "^diff" || die "helm-diff plugin is required"

if k3d cluster get "$CLUSTER_NAME" &>/dev/null; then
    warn "cluster $CLUSTER_NAME already exists, skipping create"
else
    log "create cluster"
    k3d cluster create --config "$SCRIPT_DIR/k3d-config.yaml"
fi

log "install kwok"
kubectl apply -f https://github.com/kubernetes-sigs/kwok/releases/latest/download/kwok.yaml
kubectl rollout status deploy/kwok-controller -n kube-system --timeout=120s
kubectl apply -f https://github.com/kubernetes-sigs/kwok/releases/latest/download/stage-fast.yaml

repo_add() { helm repo add "$1" "$2" 2>/dev/null || true && helm repo update "$1"; }

log "install fake gpu operator"
if kubectl get runtimeclass nvidia &>/dev/null; then
    managed=$(kubectl get runtimeclass nvidia \
        -o jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}' 2>/dev/null || true)
    [ "$managed" != "Helm" ] && kubectl delete runtimeclass nvidia
fi
repo_add fake-gpu-operator https://fake-gpu-operator.storage.googleapis.com
helm upgrade --install gpu-operator fake-gpu-operator/fake-gpu-operator \
    -n gpu-operator --create-namespace \
    --set topology.nodePools.default.gpuCount=8 \
    --set topology.nodePools.default.gpuProduct=NVIDIA-H100-80GB-HBM3 \
    --set topology.nodePools.default.gpuMemory=81559 \
    --wait

log "install csi smb"
repo_add csi-driver-smb \
    https://raw.githubusercontent.com/kubernetes-csi/csi-driver-smb/master/charts
helm upgrade --install csi-driver-smb csi-driver-smb/csi-driver-smb -n kube-system --wait

log "install envoy gateway"
repo_add envoy-gateway https://charts.envoyproxy.io
helm upgrade --install envoy-gateway envoy-gateway/gateway-helm \
    -n envoy-gateway-system --create-namespace --wait

log "create gateway resources"
kubectl apply -f - <<'EOF'
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: envoy-gateway
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: shared-gw
  namespace: envoy-gateway-system
spec:
  gatewayClassName: envoy-gateway
  listeners:
    - name: http
      port: 80
      protocol: HTTP
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: grpc-gw
  namespace: envoy-gateway-system
spec:
  gatewayClassName: envoy-gateway
  listeners:
    - name: grpc
      port: 80
      protocol: HTTP
EOF

log "setup done. run helmfile sync to deploy the nvcf stack"
