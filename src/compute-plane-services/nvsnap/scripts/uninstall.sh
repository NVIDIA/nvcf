#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Uninstall everything nvsnap deploys onto a Kubernetes cluster.
#
# Removes (in dependency order):
#   - MutatingWebhookConfiguration nvsnap-rootfs-restore
#   - Capture artifacts: PVCs, Jobs, ConfigMaps with nvsnap.io/short-hash label
#   - Test/source pods commonly created during e2e (vllm-8b, vllm-8b-fresh,
#     sglang-*, trtllm-*, nim-*-rootfs-only) — only if they exist.
#   - DaemonSet, Services, ServiceAccount, Secret in nvsnap-system
#   - cert-manager Issuer + Certificate
#   - ClusterRole + ClusterRoleBinding nvsnap-agent
#   - StorageClass nvsnap-capture
#
# Does NOT remove:
#   - Namespace nvsnap-system (keeps pre-existing auth secrets like hf-token,
#     ngc-api-key, nvsnap-pull-secret intact)
#   - nvsnap-server resources (older API server; out of scope)
#   - Anything prefixed nvsnap-* (separate older system)
#
# Usage:
#   scripts/uninstall.sh [KUBECONFIG]
#   scripts/uninstall.sh                      # uses $KUBECONFIG / default
#   scripts/uninstall.sh /path/to/kubeconfig  # explicit path
#
# Idempotent: every delete uses --ignore-not-found.

set -euo pipefail

KC="${1:-${KUBECONFIG:-}}"
KCFLAG=""
if [[ -n "$KC" ]]; then
  KCFLAG="--kubeconfig=$KC"
fi

NS="${NVSNAP_NAMESPACE:-nvsnap-system}"

run() {
  # shellcheck disable=SC2086
  kubectl $KCFLAG "$@"
}

echo "==> uninstalling nvsnap (kubeconfig=${KC:-default}, ns=$NS)"

echo "==> webhook config"
run delete mutatingwebhookconfiguration nvsnap-rootfs-restore --ignore-not-found

echo "==> capture artifacts (PVCs, Jobs, ConfigMaps with nvsnap.io/short-hash)"
run -n "$NS" delete pvc,job,configmap -l nvsnap.io/short-hash --ignore-not-found --wait=false

echo "==> stale capture writer/reader PVCs (no short-hash label, just kind)"
run -n "$NS" delete pvc,job -l nvsnap.io/kind --ignore-not-found --wait=false

echo "==> test pods commonly created during e2e"
for pod in vllm-8b vllm-8b-fresh \
           sglang-small sglang-small-fresh sglang-8b sglang-8b-fresh \
           trtllm-small trtllm-small-fresh trtllm-8b trtllm-8b-fresh \
           nim-llama-70b-rootfs-only nim-qwen3-32b nim-qwen3-32b-restore; do
  run -n "$NS" delete pod "$pod" --ignore-not-found --wait=false
done

echo "==> agent + webhook deployment objects"
run -n "$NS" delete daemonset nvsnap-agent --ignore-not-found
run -n "$NS" delete service nvsnap-agent nvsnap-webhook --ignore-not-found
run -n "$NS" delete sa nvsnap-agent --ignore-not-found

echo "==> webhook TLS (cert-manager)"
run -n "$NS" delete certificate nvsnap-webhook-tls --ignore-not-found
run -n "$NS" delete issuer nvsnap-webhook-selfsigned --ignore-not-found
run -n "$NS" delete secret nvsnap-webhook-tls --ignore-not-found

echo "==> cluster-scoped RBAC + storage class"
run delete clusterrolebinding nvsnap-agent --ignore-not-found
run delete clusterrole nvsnap-agent --ignore-not-found
run delete storageclass nvsnap-capture --ignore-not-found

# Released PVs (Retain reclaim policy leaves these orphaned). Force-delete
# any whose claimRef points at a nvsnap capture namespace.
echo "==> orphan PVs from Retain'd captures"
run get pv -o jsonpath='{range .items[?(@.spec.claimRef.namespace=="'"$NS"'")]}{.metadata.name}{"\n"}{end}' \
  | grep -v '^$' \
  | while read -r pv; do
      # Only touch PVs whose claimRef name starts with nvsnap-capture-
      cref=$(run get pv "$pv" -o jsonpath='{.spec.claimRef.name}' 2>/dev/null || true)
      case "$cref" in
        nvsnap-capture-*)
          echo "    deleting orphan PV $pv (claim was $cref)"
          # Switch reclaim to Delete so the GCE disk is reclaimed.
          run patch pv "$pv" -p '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}' --type=merge 2>/dev/null || true
          run delete pv "$pv" --ignore-not-found --wait=false
          ;;
      esac
    done

echo "==> done. namespace $NS preserved (auth secrets intact)."
echo
echo "remaining nvsnap-prefixed resources (review manually if anything looks unexpected):"
run -n "$NS" get all,pvc,configmap,issuer,certificate 2>/dev/null | grep -i 'nvsnap' | grep -v nvsnap || echo "  (none in $NS)"
echo
echo "cluster-scoped:"
run get clusterrole,clusterrolebinding,storageclass,mutatingwebhookconfiguration 2>/dev/null \
  | grep -E 'nvsnap-' | grep -v nvsnap || echo "  (none)"
