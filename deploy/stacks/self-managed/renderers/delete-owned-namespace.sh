#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

owner_label="nvcf.nvidia.com/control-plane-owner"
owner=""
namespace=""
kubectl_args=()

usage() {
  echo "usage: delete-owned-namespace.sh --owner <owner> --namespace <namespace> [--kubeconfig <path>] [--context <name>]" >&2
  exit 2
}

fail() {
  echo "delete-owned-namespace: $*" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --owner)
      [[ $# -ge 2 ]] || usage
      owner="$2"
      shift 2
      ;;
    --namespace)
      [[ $# -ge 2 ]] || usage
      namespace="$2"
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
[[ -n "$namespace" ]] || usage

if ! kubectl "${kubectl_args[@]}" get namespace "$namespace" >/dev/null 2>&1; then
  exit 0
fi

jsonpath="{.metadata.labels.nvcf\\.nvidia\\.com/control-plane-owner}"
actual_owner="$(
  kubectl "${kubectl_args[@]}" get namespace "$namespace" -o "jsonpath=$jsonpath" 2>/dev/null || true
)"

if [[ -z "$actual_owner" ]]; then
  if [[ "$owner" != "default" ]]; then
    fail "refusing to delete namespace $namespace: missing $owner_label label for owner $owner"
  fi
elif [[ "$actual_owner" != "$owner" ]]; then
  fail "refusing to delete namespace $namespace: $owner_label=$actual_owner, current owner is $owner"
fi

kubectl "${kubectl_args[@]}" delete namespace "$namespace" --wait=true
