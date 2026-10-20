#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Run only against a fresh isolated k3d cluster reserved for this test.
set -euo pipefail

: "${KUBECONFIG:?Set KUBECONFIG to the isolated cluster config}"
: "${NVCF_RECEIPT_LIVE_CONTEXT:?Set the isolated k3d context}"
: "${NVCF_RECEIPT_LIVE_ARTIFACTS:?Set a new external artifact directory}"
case "$NVCF_RECEIPT_LIVE_CONTEXT" in
  k3d-nvcf-receipt-*) ;;
  *) echo "Use a dedicated k3d-nvcf-receipt-* context" >&2; exit 1 ;;
esac
mkdir -m 700 "$NVCF_RECEIPT_LIVE_ARTIFACTS"
stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
expected_version="$(tr -d '[:space:]' < "$stack_dir/VERSION")"
expected_image="$(yq -r '.image.registry + "/" + .image.repository + ":" + .image.tag' "$stack_dir/charts/nvcf-upgrade-receipt/values.yaml")"
kubectl_cmd=(kubectl --context "$NVCF_RECEIPT_LIVE_CONTEXT" -n nvcf)
helm_cmd=(helm --kube-context "$NVCF_RECEIPT_LIVE_CONTEXT" -n nvcf)

if [[ "$("${helm_cmd[@]}" list -a -o json | yq -r 'length')" != 0 ]]; then
  echo "The test requires an empty nvcf namespace" >&2
  exit 1
fi
if [[ -n "$("${kubectl_cmd[@]}" get configmap nvcf-upgrade-receipt --ignore-not-found -o name)" ]]; then
  echo "An installed-version receipt already exists; use a fresh cluster" >&2
  exit 1
fi
kubectl --context "$NVCF_RECEIPT_LIVE_CONTEXT" create namespace nvcf --dry-run=client -o yaml |
  kubectl --context "$NVCF_RECEIPT_LIVE_CONTEXT" apply -f -

sync_receipt() {
  (cd "$stack_dir" && HELMFILE_ENV=base helmfile \
    --kube-context "$NVCF_RECEIPT_LIVE_CONTEXT" \
    --file helmfile.d/04-upgrade-receipt.yaml.gotmpl \
    --state-values-set global.image.registry=nvcr.io \
    --state-values-set global.image.repository=nvidia/nvcf \
    sync --wait --wait-for-jobs --timeout 180)
}

check_receipt() {
  local actual image
  actual="$("${kubectl_cmd[@]}" get configmap nvcf-upgrade-receipt -o jsonpath='{.data.installed_stack_version}')"
  [[ "$actual" == "$expected_version" ]] || { echo "Incorrect installed version: $actual" >&2; exit 1; }
  image="$("${kubectl_cmd[@]}" get job upgrade-receipt -o jsonpath='{.spec.template.spec.containers[0].image}')"
  [[ "$image" == "$expected_image" ]] || { echo "Unexpected helper image: $image" >&2; exit 1; }
  "${kubectl_cmd[@]}" wait --for=condition=complete job/upgrade-receipt --timeout=30s
}

sync_receipt
check_receipt
receipt_uid="$("${kubectl_cmd[@]}" get configmap nvcf-upgrade-receipt -o jsonpath='{.metadata.uid}')"
job_uid="$("${kubectl_cmd[@]}" get job upgrade-receipt -o jsonpath='{.metadata.uid}')"
"${kubectl_cmd[@]}" get job,pods,configmap -o json > "$NVCF_RECEIPT_LIVE_ARTIFACTS/install.json"
"${kubectl_cmd[@]}" logs job/upgrade-receipt > "$NVCF_RECEIPT_LIVE_ARTIFACTS/install.log"

# Simulate a receipt from an older version. The post-upgrade hook must update
# the existing object, not only create one on a fresh install.
"${kubectl_cmd[@]}" patch configmap nvcf-upgrade-receipt --type merge \
  -p '{"data":{"installed_stack_version":"0.0.0-test"}}'
sync_receipt
check_receipt
[[ "$("${kubectl_cmd[@]}" get configmap nvcf-upgrade-receipt -o jsonpath='{.metadata.uid}')" == "$receipt_uid" ]]
[[ "$("${kubectl_cmd[@]}" get job upgrade-receipt -o jsonpath='{.metadata.uid}')" != "$job_uid" ]]
"${kubectl_cmd[@]}" get job,pods,configmap -o json > "$NVCF_RECEIPT_LIVE_ARTIFACTS/upgrade.json"
"${kubectl_cmd[@]}" logs job/upgrade-receipt > "$NVCF_RECEIPT_LIVE_ARTIFACTS/upgrade.log"
"${helm_cmd[@]}" list -a -o json > "$NVCF_RECEIPT_LIVE_ARTIFACTS/releases.json"
echo "PASS: receipt install and upgrade hooks; cluster and evidence retained"
