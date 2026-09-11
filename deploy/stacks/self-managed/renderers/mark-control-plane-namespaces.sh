#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

owner_label="nvcf.nvidia.com/control-plane-owner"
stack_annotation="nvcf.nvidia.com/control-plane-stack"
chart_version_annotation="nvcf.nvidia.com/control-plane-chart-version"
nvca_operator_version_annotation="nvcf.nvidia.com/nvca-operator-version"

owner=""
primary_namespace=""
stack=""
chart_version=""
nvca_operator_version=""
namespaces=()
shared_namespaces=()
kubectl_args=()

usage() {
  echo "usage: mark-control-plane-namespaces.sh --owner <owner> --primary-namespace <namespace> --stack <stack> --chart-version <version> --nvca-operator-version <version> [--namespace <namespace>]... [--shared-namespace <namespace>]... [--kubeconfig <path>] [--context <name>]" >&2
  exit 2
}

fail() {
  echo "mark-control-plane-namespaces: $*" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --owner)
      [[ $# -ge 2 ]] || usage
      owner="$2"
      shift 2
      ;;
    --primary-namespace)
      [[ $# -ge 2 ]] || usage
      primary_namespace="$2"
      shift 2
      ;;
    --stack)
      [[ $# -ge 2 ]] || usage
      stack="$2"
      shift 2
      ;;
    --chart-version)
      [[ $# -ge 2 ]] || usage
      chart_version="$2"
      shift 2
      ;;
    --nvca-operator-version)
      [[ $# -ge 2 ]] || usage
      nvca_operator_version="$2"
      shift 2
      ;;
    --namespace)
      [[ $# -ge 2 ]] || usage
      namespaces+=("$2")
      shift 2
      ;;
    --shared-namespace)
      [[ $# -ge 2 ]] || usage
      shared_namespaces+=("$2")
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

[[ -n "$owner" ]] || usage
[[ -n "$primary_namespace" ]] || usage
[[ -n "$stack" ]] || usage
[[ -n "$chart_version" ]] || usage
[[ -n "$nvca_operator_version" ]] || usage

if [[ "$owner" == "shared" || ${#owner} -gt 32 || ! "$owner" =~ ^(default|[a-z0-9]([-a-z0-9]*[a-z0-9])?)$ ]]; then
  fail "owner must be default or a lowercase DNS label up to 32 characters other than shared, got $owner"
fi

owner_jsonpath="{.metadata.labels.nvcf\\.nvidia\\.com/control-plane-owner}"

mark_namespace() {
  local namespace="$1"
  local expected_owner="$2"
  local mark_primary="$3"
  local actual_owner

  if ! kubectl "${kubectl_args[@]}" get namespace "$namespace" >/dev/null 2>&1; then
    return 0
  fi

  actual_owner="$(
    kubectl "${kubectl_args[@]}" get namespace "$namespace" -o "jsonpath=$owner_jsonpath" 2>/dev/null || true
  )"
  if [[ -n "$actual_owner" && "$actual_owner" != "$expected_owner" ]]; then
    fail "refusing to mark namespace $namespace: $owner_label=$actual_owner, expected $expected_owner"
  fi

  kubectl "${kubectl_args[@]}" label namespace "$namespace" "$owner_label=$expected_owner" --overwrite >/dev/null

  if [[ "$mark_primary" == "true" ]]; then
    kubectl "${kubectl_args[@]}" annotate namespace "$namespace" \
      "$stack_annotation=$stack" \
      "$chart_version_annotation=$chart_version" \
      "$nvca_operator_version_annotation=$nvca_operator_version" \
      --overwrite >/dev/null
  fi
}

mark_namespace "$primary_namespace" "$owner" true

for namespace in "${namespaces[@]}"; do
  [[ "$namespace" != "$primary_namespace" ]] || continue
  mark_namespace "$namespace" "$owner" false
done

for namespace in "${shared_namespaces[@]}"; do
  mark_namespace "$namespace" "shared" false
done
