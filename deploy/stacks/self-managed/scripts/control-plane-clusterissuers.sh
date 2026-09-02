#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

if [[ "$#" -ne 2 || "$1" != cleanup || -z "$2" ]]; then
  echo "Usage: $0 cleanup <control-plane-id>" >&2
  exit 2
fi

control_plane_id="$2"
owner_label="nvcf.nvidia.com/control-plane-id"
owner_jsonpath='{.metadata.labels.nvcf\.nvidia\.com/control-plane-id}'

# Helm retains managed ClusterIssuers, including prior names left by an
# upgrade. Select every issuer explicitly owned by this plane, then re-check
# both the label and name immediately before deleting to fail closed on stale
# list results or ownership changes.
resources="$(kubectl get clusterissuers \
  -l "${owner_label}=${control_plane_id}" \
  -o name)"
while IFS= read -r resource; do
  [[ -n "$resource" ]] || continue
  issuer="${resource##*/}"
  if [[ "$issuer" != "${control_plane_id}-"* ]]; then
    echo "Error: refusing to delete managed ClusterIssuer outside control plane ${control_plane_id}: ${issuer}" >&2
    exit 1
  fi

  owner="$(kubectl get clusterissuer "$issuer" -o "jsonpath=${owner_jsonpath}")"
  if [[ "$owner" != "$control_plane_id" ]]; then
    echo "Error: refusing to delete ClusterIssuer ${issuer}: expected owner ${control_plane_id}, found ${owner:-unset}." >&2
    exit 1
  fi
  kubectl delete clusterissuer "$issuer" --wait=true
done <<<"$resources"
