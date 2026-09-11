#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

owner_label="nvcf.nvidia.com/control-plane-owner"
crd_name="nvcfbackends.nvcf.nvidia.io"
shared_release="nvcf-nvca-crds"
shared_namespace="nvcf-shared"
legacy_release="nvca-operator"
kubectl_args=()

usage() {
  echo "usage: adopt-nvca-crd-ownership.sh [--crd-name <name>] [--shared-release <name>] [--shared-namespace <namespace>] [--legacy-release <name>] [--kubeconfig <path>] [--context <name>]" >&2
  exit 2
}

fail() {
  echo "adopt-nvca-crd-ownership: $*" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --crd-name)
      [[ $# -ge 2 ]] || usage
      crd_name="$2"
      shift 2
      ;;
    --shared-release)
      [[ $# -ge 2 ]] || usage
      shared_release="$2"
      shift 2
      ;;
    --shared-namespace)
      [[ $# -ge 2 ]] || usage
      shared_namespace="$2"
      shift 2
      ;;
    --legacy-release)
      [[ $# -ge 2 ]] || usage
      legacy_release="$2"
      shift 2
      ;;
    --kubeconfig)
      [[ $# -ge 2 ]] || usage
      kubectl_args+=(--kubeconfig "$2")
      shift 2
      ;;
    --context)
      [[ $# -ge 2 ]] || usage
      kubectl_args+=(--context "$2")
      shift 2
      ;;
    *)
      usage
      ;;
  esac
done

[[ "$crd_name" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$ ]] ||
  fail "invalid CRD name: $crd_name"
[[ "$shared_release" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] ||
  fail "invalid shared release name: $shared_release"
[[ "$shared_namespace" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] ||
  fail "invalid shared namespace: $shared_namespace"
[[ "$legacy_release" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] ||
  fail "invalid legacy release name: $legacy_release"

if ! kubectl "${kubectl_args[@]}" get customresourcedefinition "$crd_name" >/dev/null 2>&1; then
  echo ">>> NVCFBackend CRD is not installed yet; $shared_release will create it."
  exit 0
fi

get_jsonpath() {
  local jsonpath="$1"
  kubectl "${kubectl_args[@]}" get customresourcedefinition "$crd_name" \
    -o "jsonpath=$jsonpath" 2>/dev/null || true
}

release_name="$(get_jsonpath "{.metadata.annotations.meta\\.helm\\.sh/release-name}")"
release_namespace="$(get_jsonpath "{.metadata.annotations.meta\\.helm\\.sh/release-namespace}")"
managed_by="$(get_jsonpath "{.metadata.labels.app\\.kubernetes\\.io/managed-by}")"

case "$release_name/$release_namespace/$managed_by" in
  "$shared_release/$shared_namespace/Helm")
    echo ">>> NVCFBackend CRD already belongs to $shared_release in $shared_namespace; ensuring shared metadata is present."
    ;;
  "$legacy_release"/*"/Helm")
    echo ">>> Moving NVCFBackend CRD ownership from $release_name/$release_namespace to $shared_release/$shared_namespace."
    ;;
  "//")
    echo ">>> Adopting unowned NVCFBackend CRD into $shared_release/$shared_namespace."
    ;;
  *)
    fail "refusing to adopt $crd_name: current Helm owner is release=${release_name:-<none>} namespace=${release_namespace:-<none>} managed-by=${managed_by:-<none>}"
    ;;
esac

kubectl "${kubectl_args[@]}" annotate customresourcedefinition "$crd_name" \
  "helm.sh/resource-policy=keep" \
  "meta.helm.sh/release-name=$shared_release" \
  "meta.helm.sh/release-namespace=$shared_namespace" \
  --overwrite >/dev/null

kubectl "${kubectl_args[@]}" label customresourcedefinition "$crd_name" \
  "app.kubernetes.io/managed-by=Helm" \
  "$owner_label=shared" \
  --overwrite >/dev/null

echo ">>> NVCFBackend CRD is ready for shared Helm ownership."
