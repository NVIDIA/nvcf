#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# bringup-cluster.sh — one-command "fresh GKE cluster → ready for NvSnap".
#
# What this script does, in order:
#   1. Refresh kubeconfig via gcloud
#   2. Apply NvSnap namespace
#   3. Seed nvsnap-pull-secret from ~/.docker/config.json (nvcr.io creds)
#   4. Allow-list system priorities (GKE ResourceQuota required for
#      gpu-operator + cert-manager DaemonSets that use
#      system-node-critical PriorityClass)
#   5. helm dependency update (cert-manager + gpu-operator subcharts)
#   6. helm install nvsnap — single release that brings up:
#         cert-manager + CRDs
#         NVIDIA GPU Operator (driver R580.95.05 + toolkit)
#         nvsnap agent + server + blobstore + webhook + Certificate
#   7. Wait for GPU Operator drivers on every H100 node
#   8. Smoke check (kubectl get pods)
#
# Assumes `terraform apply` already ran for the cluster (creates the
# nodes with gpu_driver_version="INSTALLATION_DISABLED"). This script
# is the K8s-side bring-up after the GCP-side infra is up.
#
# Usage:
#   ./scripts/bringup-cluster.sh GCP-H100-a
#   ./scripts/bringup-cluster.sh GCP-H100-b

set -euo pipefail

CLUSTER="${1:?usage: $0 <cluster-name>}"
ZONE="${ZONE:-asia-southeast1-c}"
PROJECT="${PROJECT:-YOUR_GPU_PROJECT}"
KUBECONFIG_PATH="${HOME}/${CLUSTER}.kubeconfig"

# Version-pinning has moved into the NvSnap Helm chart's Chart.yaml +
# values.yaml (subchart dependencies for cert-manager + gpu-operator,
# driver version inside the gpu-operator values block). Override via
# `--set` to helm upgrade below if a one-off bump is needed.

cd "$(dirname "$(readlink -f "$0")")/.."

echo "=== [1/8] Refreshing kubeconfig for ${CLUSTER} ==="
KUBECONFIG="${KUBECONFIG_PATH}" gcloud container clusters get-credentials \
  "${CLUSTER}" --zone="${ZONE}" --project="${PROJECT}"
export KUBECONFIG="${KUBECONFIG_PATH}"

echo "=== [2/8] Applying nvsnap-system namespace ==="
kubectl apply -f deploy/k8s/namespace.yaml

echo "=== [2b/8] Applying NVCF storage classes (nvcf-sc, nvcf-function-storage-sc, hyperdisk-ml) ==="
# Idempotent — re-applying just sync-updates parameters. hyperdisk-ml is
# the per-capture L2 PVC tier (nvsnap#63). Skipping this leaves the
# cluster without an RWX-class StorageClass, which NvSnap treats as a
# fatal-startup prerequisite (see docs/L2-PVC-CRIU-DESIGN.md).
kubectl apply -f deploy/k8s/nvcf-cluster-prep/storage-classes.yaml

echo "=== [3/8] Seeding nvsnap-pull-secret from ~/.docker/config.json ==="
kubectl create secret generic nvsnap-pull-secret \
  -n nvsnap-system \
  --from-file=.dockerconfigjson="${HOME}/.docker/config.json" \
  --type=kubernetes.io/dockerconfigjson \
  --dry-run=client -o yaml | kubectl apply -f -

echo "=== [4/8] Allow system priorities for sub-charts ==="
# GKE Standard requires an explicit ResourceQuota to allow pods using
# system-{node,cluster}-critical priority classes in non-system
# namespaces. Without this, GPU Operator's DaemonSets fail to schedule
# with "insufficient quota to match these scopes". Pre-apply both
# namespaces (Helm chart will deploy resources into them).
for ns in gpu-operator cert-manager; do
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ResourceQuota
metadata:
  name: allow-system-priorities
  namespace: $ns
spec:
  hard:
    pods: "1000"
  scopeSelector:
    matchExpressions:
    - operator: In
      scopeName: PriorityClass
      values: ["system-node-critical", "system-cluster-critical"]
EOF
done

echo "=== [5/8] helm dependency update (cert-manager + gpu-operator subcharts) ==="
CHART_DIR="$(dirname "$(readlink -f "$0")")/../deploy/helm/nvsnap"
helm repo add jetstack https://charts.jetstack.io --force-update
helm repo add nvidia https://helm.ngc.nvidia.com/nvidia --force-update
helm repo update jetstack nvidia
helm dependency update "${CHART_DIR}"

echo "=== [6/8] helm install nvsnap (cert-manager + gpu-operator + nvsnap, single release) ==="
# --timeout 20m: GPU Operator deploys ~7 DaemonSets that each compile/
# install the NVIDIA driver per node; the 5-min Helm default is too
# short for first-time driver install.
helm upgrade --install nvsnap "${CHART_DIR}" \
  --namespace nvsnap-system --create-namespace \
  --wait --timeout 20m

echo "=== [7/8] Waiting for GPU Operator driver installation on H100 nodes ==="
# Each node reports nvidia.com/gpu allocatable once driver+toolkit are up.
NODES=$(kubectl get nodes -l "cloud.google.com/gke-accelerator=nvidia-h100-mega-80gb" \
  -o jsonpath='{.items[*].metadata.name}')
for node in $NODES; do
  echo "  waiting on $node..."
  for i in $(seq 1 60); do
    gpus=$(kubectl get node "$node" -o jsonpath='{.status.allocatable.nvidia\.com/gpu}' 2>/dev/null || echo 0)
    [ "${gpus:-0}" = "8" ] && { echo "    8 GPUs allocatable"; break; }
    sleep 10
  done
done

echo "=== [8/8] Smoke check ==="
kubectl get pods -n nvsnap-system
echo
echo "=== Cluster ${CLUSTER} ready. Kubeconfig: ${KUBECONFIG_PATH} ==="
echo "Next: KUBECONFIG=${KUBECONFIG_PATH} ./scripts/test-e2e.sh vllm-small"
