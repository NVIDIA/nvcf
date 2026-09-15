#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -e

env=$1

echo "===== Setting up MicroK8s with GPU support and kube-state-metrics ====="

# Check if running as root
if [ "$EUID" -ne 0 ]; then
  echo "Please run this script with sudo or as root"
  exit 1
fi

echo "===== Ensuring snap is installed ====="
apt update
apt install -y snapd

echo "===== Installing MicroK8s ====="
snap install microk8s --classic --channel=1.31/stable

echo "===== Installing Helm ====="
snap install helm --classic

echo "===== Waiting for MicroK8s to be ready ====="
microk8s status --wait-ready

echo "===== Enabling MicroK8s addons (DNS, Storage, GPU, Prometheus) ====="
microk8s enable dns
microk8s enable hostpath-storage
microk8s enable nvidia
microk8s enable prometheus

# https://github.com/NVIDIA/gpu-operator/issues/569#issuecomment-1681907677
microk8s kubectl get clusterpolicy/cluster-policy -o yaml | grep -q 'name: DISABLE_DEV_CHAR_SYMLINK_CREATION' || microk8s kubectl patch clusterpolicy cluster-policy --type=merge -p '{"spec":{"validator":{"driver":{"env":[{"name":"DISABLE_DEV_CHAR_SYMLINK_CREATION","value":"true"}]}}}}'

echo "===== Adding Helm repositories ====="
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo update