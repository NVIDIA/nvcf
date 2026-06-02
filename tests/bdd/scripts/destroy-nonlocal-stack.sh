#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Destructive stack cleanup for retained non-local Kubernetes clusters.
# This mirrors tests/bdd/scripts/destroy-stack.sh, but requires explicit
# kube contexts instead of discovering k3d clusters.
#
# Usage:
#   tests/bdd/scripts/destroy-nonlocal-stack.sh \
#     --control-plane-context <ctx> \
#     --compute-context <ctx> [--compute-context <ctx> ...]
#
# Optional:
#   --dry-run     Print commands without executing them.
#   --clean-out   Remove deploy/stacks/self-managed/out/*.yaml.
#
# Stack cleanup may only delete stack-owned resources. Releases and
# namespaces are explicit allow-lists. The script does not delete EKS
# clusters, nodes, CSI driver resources, fake GPU operator resources,
# cert-manager, or CRDs.
#
# Gateway lifecycle (EKS BDD-owned): the single-cluster EKS Helmfile
# feature installs the envoy-gateway controller, GatewayClass, Gateway,
# and the envoy-gateway / envoy-gateway-system namespaces itself as
# part of @gateway-setup, so this script tears those back down when
# --control-plane-context is set. This is intentionally different from
# tests/bdd/scripts/destroy-stack.sh (the k3d sibling), which leaves
# envoy-gateway in place because the local topology owns it. Do not
# carry this change into destroy-stack.sh.

set -euo pipefail

CONTROL_PLANE_CONTEXT=""
COMPUTE_CONTEXTS=()
DRY_RUN=0
CLEAN_OUT=0

usage() {
  sed -n '7,19p' "$0" >&2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --control-plane-context)
      CONTROL_PLANE_CONTEXT="${2:-}"
      shift 2
      ;;
    --compute-context)
      COMPUTE_CONTEXTS+=("${2:-}")
      shift 2
      ;;
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    --clean-out)
      CLEAN_OUT=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage
      exit 2
      ;;
  esac
done

if [[ -z "$CONTROL_PLANE_CONTEXT" && "${#COMPUTE_CONTEXTS[@]}" -eq 0 ]]; then
  echo "at least one --control-plane-context or --compute-context is required" >&2
  usage
  exit 2
fi

if [[ -n "${BDD_REPO_ROOT:-}" ]]; then
  REPO_ROOT="$BDD_REPO_ROOT"
else
  REPO_ROOT="$(git rev-parse --show-toplevel)"
fi
STACK_OUT_DIR="$REPO_ROOT/deploy/stacks/self-managed/out"

# Keep these allow-lists in sync with destroy-stack.sh, EXCEPT for
# the envoy-gateway entries: those are nonlocal-only because EKS BDD
# owns gateway installation (see header for rationale).
STACK_RELEASES_CP=(
  "nats:nats-system"
  "openbao-server:vault-system"
  "cassandra:cassandra-system"
  "api-keys:api-keys"
  "admin-issuer-proxy:api-keys"
  "sis:sis"
  "api:nvcf"
  "nvct-api:nvcf"
  "invocation-service:nvcf"
  "grpc-proxy:nvcf"
  "ess-api:ess"
  "notary-service:nvcf"
  "reval:nvcf"
  "nats-auth-callout-service:nats-system"
  "ingress:envoy-gateway-system"
  "llm-request-router:nvcf"
  "llm-api-gateway:nvcf"
  "eg:envoy-gateway-system"
)

STACK_RELEASES_WORKER=(
  "nvca-operator:nvca-operator"
)

STACK_NAMESPACES_CP=(
  nats-system
  cassandra-system
  vault-system
  api-keys
  sis
  ess
  nvcf
  envoy-gateway
  envoy-gateway-system
)

STACK_NAMESPACES_WORKER=(
  nvca-operator
  nvca-system
  nvcf-backend
)

STACK_CRS_WORKER=(
  "nvcfbackend:nvcfbackends.nvcf.nvidia.io"
)

run() {
  printf '+'
  printf ' %q' "$@"
  printf '\n'
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return 0
  fi
  "$@"
}

context_reachable() {
  local ctx="$1"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return 0
  fi
  kubectl --context "$ctx" cluster-info >/dev/null 2>&1
}

namespace_exists() {
  local ctx="$1"
  local ns="$2"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return 0
  fi
  kubectl --context "$ctx" get namespace "$ns" >/dev/null 2>&1
}

crd_exists() {
  local ctx="$1"
  local crd="$2"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return 0
  fi
  kubectl --context "$ctx" get crd "$crd" >/dev/null 2>&1
}

delete_stack_crs() {
  local ctx="$1"
  shift
  local namespaces=("$@")
  for ns in "${namespaces[@]}"; do
    if ! namespace_exists "$ctx" "$ns"; then
      continue
    fi
    for entry in "${STACK_CRS_WORKER[@]}"; do
      local kind="${entry%:*}"
      local crd="${entry#*:}"
      if ! crd_exists "$ctx" "$crd"; then
        continue
      fi
      # Null finalizers FIRST so the delete does not hang waiting for
      # the operator's reconciler (which the next step is about to
      # uninstall) to process them. Tolerate empty CR lists.
      if [[ "$DRY_RUN" -eq 0 ]]; then
        local names
        names=$(kubectl --context "$ctx" -n "$ns" get "$kind" \
          -o name 2>/dev/null || true)
        if [[ -n "$names" ]]; then
          echo "  null finalizers on $kind in $ns"
          while IFS= read -r ref; do
            [[ -z "$ref" ]] && continue
            run kubectl --context "$ctx" -n "$ns" patch "$ref" \
              --type=merge -p '{"metadata":{"finalizers":null}}' \
              || true
          done <<<"$names"
        fi
      fi
      echo "  delete $kind in $ns"
      run kubectl --context "$ctx" -n "$ns" delete "$kind" --all \
        --ignore-not-found --timeout=60s
    done
  done
}

# A release that is stuck in pending-install, pending-upgrade, or
# uninstalling status causes `helm uninstall --wait` to fail (or hang)
# because its post-install hooks never completed. Helm 3 stores release
# state as a kube secret named sh.helm.release.v1.<name>.v<rev>. Deleting
# those secrets directly tells helm the release does not exist. This is
# the same approach helm's own troubleshooting docs recommend for
# transitional-state releases.
purge_stuck_helm_releases() {
  local ctx="$1"
  shift
  local releases=("$@")
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return 0
  fi
  local status
  for entry in "${releases[@]}"; do
    local name="${entry%:*}"
    local ns="${entry#*:}"
    if ! namespace_exists "$ctx" "$ns"; then
      continue
    fi
    # `helm status` fails non-zero when the release does not exist; allow
    # that with `|| true` so `set -e` does not abort the cleanup. Restrict
    # the JSON parse to the top-level "status" field by anchoring on the
    # leading brace context (helm chart metadata also contains a "status"
    # key under .info that we are not after).
    status=$(helm --kube-context "$ctx" status "$name" -n "$ns" \
      -o json 2>/dev/null \
      | sed -n 's/^[[:space:]]*"status":[[:space:]]*"\([^"]*\)".*/\1/p' \
      | head -n1 || true)
    case "$status" in
      pending-install|pending-upgrade|pending-rollback|uninstalling)
        echo "  purge stuck $name ($status) in $ns"
        run kubectl --context "$ctx" -n "$ns" delete secret \
          -l "owner=helm,name=$name" --ignore-not-found
        ;;
    esac
  done
}

uninstall_stack_releases() {
  local ctx="$1"
  shift
  local releases=("$@")
  for entry in "${releases[@]}"; do
    local name="${entry%:*}"
    local ns="${entry#*:}"
    echo "  uninstall $name in $ns"
    # Tolerate helm uninstall non-zero exit: the actual release-secret
    # removal usually succeeds within the timeout even when --wait
    # reports `context deadline exceeded` waiting for resource churn
    # (e.g. operator-managed CRs draining). The follow-up namespace
    # deletion + verify_clean catches anything that genuinely survived.
    run helm --kube-context "$ctx" uninstall "$name" -n "$ns" \
      --ignore-not-found --no-hooks --wait --timeout 2m || true
  done
}

# Known chart workaround: the nvca-operator chart writes a ConfigMap
# named nvca-operator-shutdown-sentinel with a finalizer the operator
# clears at shutdown. If the operator is force-uninstalled (helm
# uninstall --no-hooks) or otherwise dies before clearing it, the
# finalizer pins the CM and the namespace cannot terminate. Null any
# matching CM finalizers before deleting namespaces.
clear_sentinel_finalizers() {
  local ctx="$1"
  shift
  local namespaces=("$@")
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return 0
  fi
  for ns in "${namespaces[@]}"; do
    if ! namespace_exists "$ctx" "$ns"; then
      continue
    fi
    local cms
    cms=$(kubectl --context "$ctx" -n "$ns" get cm \
      -o name 2>/dev/null | grep -E "shutdown-sentinel" || true)
    if [[ -n "$cms" ]]; then
      echo "  null finalizers on sentinel CMs in $ns"
      while IFS= read -r ref; do
        [[ -z "$ref" ]] && continue
        run kubectl --context "$ctx" -n "$ns" patch "$ref" \
          --type=merge -p '{"metadata":{"finalizers":null}}' || true
      done <<<"$cms"
    fi
  done
}

delete_stack_namespaces() {
  local ctx="$1"
  shift
  local namespaces=("$@")
  clear_sentinel_finalizers "$ctx" "${namespaces[@]}"
  for ns in "${namespaces[@]}"; do
    echo "  delete namespace $ns"
    run kubectl --context "$ctx" delete namespace "$ns" \
      --ignore-not-found --wait --timeout=120s
  done
}

# delete_gateway_first removes the EKS BDD-owned Gateway BEFORE the
# helm uninstall of the envoy-gateway controller. Order matters: the
# controller's finalizers drain the AWS NLB on Gateway delete, so if
# the controller is gone first the Gateway sticks in deletion and the
# namespace hangs.
#
# The Gateway references the chart-provisioned `eg` GatewayClass, so
# no separate GatewayClass cleanup is needed: the helm uninstall of
# the `eg` release removes the GatewayClass alongside the controller.
delete_gateway_first() {
  local ctx="$1"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return 0
  fi
  if ! context_reachable "$ctx"; then
    return 0
  fi
  if namespace_exists "$ctx" envoy-gateway && \
     kubectl --context "$ctx" get gateway nvcf-gateway -n envoy-gateway >/dev/null 2>&1; then
    echo "  delete gateway nvcf-gateway in envoy-gateway"
    run kubectl --context "$ctx" delete gateway nvcf-gateway \
      -n envoy-gateway --ignore-not-found --wait --timeout=300s || true
  fi
}

clean_stack_out() {
  if [[ "$CLEAN_OUT" -ne 1 ]]; then
    return 0
  fi
  echo "  clean $STACK_OUT_DIR"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    echo "+ rm -f $STACK_OUT_DIR/*.yaml"
    return 0
  fi
  shopt -s nullglob
  local files=("$STACK_OUT_DIR"/*.yaml)
  if [[ "${#files[@]}" -gt 0 ]]; then
    run rm -f "${files[@]}"
  fi
}

verify_clean() {
  local ctx="$1"
  shift
  local releases=("$@")
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return 0
  fi
  local leftover=()
  for entry in "${releases[@]}"; do
    local name="${entry%:*}"
    local ns="${entry#*:}"
    if helm --kube-context "$ctx" status "$name" -n "$ns" \
        >/dev/null 2>&1; then
      leftover+=("$name@$ns")
    fi
  done
  if [[ "${#leftover[@]}" -gt 0 ]]; then
    echo "FAIL: leftover releases on $ctx: ${leftover[*]}" >&2
    return 1
  fi
}

for ctx in "${COMPUTE_CONTEXTS[@]}"; do
  if [[ -z "$ctx" ]]; then
    echo "empty --compute-context value" >&2
    exit 2
  fi
  echo ">>> Cleaning compute stack on $ctx"
  if ! context_reachable "$ctx"; then
    echo "Context $ctx unreachable; skipping."
    continue
  fi
  delete_stack_crs "$ctx" "${STACK_NAMESPACES_WORKER[@]}"
  # Clear sentinel CM finalizers BEFORE uninstall: helm uninstall --wait
  # blocks on those finalizers too, not just namespace termination.
  clear_sentinel_finalizers "$ctx" "${STACK_NAMESPACES_WORKER[@]}"
  purge_stuck_helm_releases "$ctx" "${STACK_RELEASES_WORKER[@]}"
  uninstall_stack_releases "$ctx" "${STACK_RELEASES_WORKER[@]}"
  delete_stack_namespaces "$ctx" "${STACK_NAMESPACES_WORKER[@]}"
  verify_clean "$ctx" "${STACK_RELEASES_WORKER[@]}"
done

if [[ -n "$CONTROL_PLANE_CONTEXT" ]]; then
  echo ">>> Cleaning control-plane stack on $CONTROL_PLANE_CONTEXT"
  if context_reachable "$CONTROL_PLANE_CONTEXT"; then
    delete_gateway_first "$CONTROL_PLANE_CONTEXT"
    purge_stuck_helm_releases "$CONTROL_PLANE_CONTEXT" "${STACK_RELEASES_CP[@]}"
    uninstall_stack_releases "$CONTROL_PLANE_CONTEXT" "${STACK_RELEASES_CP[@]}"
    delete_stack_namespaces "$CONTROL_PLANE_CONTEXT" "${STACK_NAMESPACES_CP[@]}"
    verify_clean "$CONTROL_PLANE_CONTEXT" "${STACK_RELEASES_CP[@]}"
  else
    echo "Context $CONTROL_PLANE_CONTEXT unreachable; skipping."
  fi
fi

clean_stack_out
