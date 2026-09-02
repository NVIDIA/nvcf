#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

usage() {
  echo "Usage: $0 <prepare|verify|cleanup> <control-plane-id> <namespace>..." >&2
  exit 2
}

[[ "$#" -ge 3 ]] || usage
action="$1"
control_plane_id="$2"
shift 2

if [[ "$action" != prepare && "$action" != verify && "$action" != cleanup ]]; then
  usage
fi

owner_label="nvcf.nvidia.com/control-plane-id"
owner_jsonpath='{.metadata.labels.nvcf\.nvidia\.com/control-plane-id}'

for namespace in "$@"; do
  if [[ "$namespace" != "${control_plane_id}-"* ]]; then
    echo "Error: refusing to manage namespace outside control plane ${control_plane_id}: ${namespace}" >&2
    exit 1
  fi

  if [[ "$action" == prepare || "$action" == verify ]]; then
    if kubectl get namespace "$namespace" >/dev/null 2>&1; then
      owner="$(kubectl get namespace "$namespace" -o "jsonpath=${owner_jsonpath}")"
      if [[ "$owner" != "$control_plane_id" ]]; then
        echo "Error: namespace $namespace is not owned by control plane $control_plane_id (owner=${owner:-unset})." >&2
        exit 1
      fi
    elif [[ "$action" == prepare ]]; then
      kubectl create namespace "$namespace"
      kubectl label namespace "$namespace" "${owner_label}=${control_plane_id}"
    fi
    continue
  fi

  if ! kubectl get namespace "$namespace" >/dev/null 2>&1; then
    continue
  fi
  owner="$(kubectl get namespace "$namespace" -o "jsonpath=${owner_jsonpath}")"
  if [[ "$owner" != "$control_plane_id" ]]; then
    echo "Skipping namespace $namespace: expected owner $control_plane_id, found ${owner:-unset}."
    continue
  fi
  kubectl delete namespace "$namespace" --wait=true
done
