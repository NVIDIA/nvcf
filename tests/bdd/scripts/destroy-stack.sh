#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Destructive stack cleanup for the BDD suite. Uninstalls every
# stack-owned helm release and deletes every stack-owned namespace
# on a retained k3d cluster, then clears stack handoff artifacts
# from deploy/stacks/self-managed/out/ and
# deploy/stacks/nvcf-compute-plane/out/. The k3d cluster itself is left
# running so a subsequent install does not pay the cluster boot cost.
#
# Governing rule (see tests/bdd/PLAN_DESTRUCTIVE_CLEANUP.md and
# tests/bdd/AGENTS.md): topology cleanup may delete topology
# resources, stack cleanup may only delete stack-owned resources
# and stack artifacts. Releases and namespaces are explicit
# allow-lists below; do not introduce blanket `helm list`-based
# uninstall or namespace deletion that would catch topology
# infrastructure (the `eg` Gateway API controller, the
# envoy-gateway-system namespace itself, cert-manager).
#
# Every kubectl and helm call carries an explicit --context /
# --kube-context flag; no global `kubectl config use-context`
# switching. Errors other than NotFound propagate (the recipes use
# --ignore-not-found for that single case).
#
# Usage:
#   tests/bdd/scripts/destroy-stack.sh single [CLUSTER_NAME=ncp-local]
#   tests/bdd/scripts/destroy-stack.sh multi
#
# Multi-cluster mode discovers every ncp-local-compute-* cluster
# via `k3d cluster list -o json | jq` and cleans each (worker layer
# first to satisfy CR-finalizer ordering), then cleans
# k3d-ncp-local-cp.
#
# Repo root resolution: $BDD_REPO_ROOT if set, otherwise
# `git rev-parse --show-toplevel`. The script changes directory to
# the repo root before touching stack out directories.

set -euo pipefail

mode="${1:-}"
case "$mode" in
  single|multi) ;;
  *)
    echo "usage: $0 single|multi" >&2
    exit 2
    ;;
esac

# Both modes need jq: the force-clear path in delete_stack_namespaces
# builds the /finalize subresource patch with jq.
command -v jq >/dev/null 2>&1 || {
  echo "destroy-stack.sh requires jq; install it and retry." >&2
  exit 1
}

if [[ -n "${BDD_REPO_ROOT:-}" ]]; then
  REPO_ROOT="$BDD_REPO_ROOT"
else
  REPO_ROOT="$(git rev-parse --show-toplevel)"
fi
STACK_OUT_DIRS=(
  "$REPO_ROOT/deploy/stacks/self-managed/out"
  "$REPO_ROOT/deploy/stacks/nvcf-compute-plane/out"
)

CLUSTER_NAME="${CLUSTER_NAME:-ncp-local}"

# --- Allow-lists (single source of truth) ---

# Stack-owned helm releases as name:namespace pairs. Update whenever
# helmfile.d adds or removes a release. The `ingress` release is on
# this list but envoy-gateway-system is NOT on STACK_NAMESPACES_CP,
# by design: stack uninstalls ingress in place while leaving the
# namespace and topology-owned eg controller untouched. cert-manager
# is intentionally absent for the same reason: helmfile installs it,
# but it is topology infrastructure that other workloads (and other
# helm charts) depend on via CRDs and ClusterIssuer objects. A
# stack-cleanup run leaves cert-manager in place; the next helmfile
# install reconciles to it.
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
)

STACK_RELEASES_WORKER=(
  "nvca-operator:nvca-operator"
)

# Stack-owned namespaces. Excludes envoy-gateway-system and
# cert-manager because those are shared with topology infrastructure.
STACK_NAMESPACES_CP=(
  nats-system
  cassandra-system
  vault-system
  api-keys
  sis
  ess
  nvcf
)

STACK_NAMESPACES_WORKER=(
  nvca-operator
  nvca-system
  nvcf-backend
)

# Namespaced custom resources to delete BEFORE helm uninstall so
# finalizer-bearing CRs do not block namespace termination. Extend
# this list when a future install path introduces another blocking
# CR.
STACK_CRS_WORKER=(
  nvcfbackend
)

# --- Helpers ---

delete_stack_crs() {
  local ctx="$1"
  shift
  local namespaces=("$@")
  for ns in "${namespaces[@]}"; do
    if ! kubectl --context "$ctx" get namespace "$ns" >/dev/null 2>&1; then
      continue
    fi
    for cr in "${STACK_CRS_WORKER[@]}"; do
      # If the CRD is absent (a prior topology destroy wiped it, or
      # a partial install never registered it), there is nothing to
      # clean. Skip the finalizer patch AND the delete call -- both
      # `kubectl get <crd>` and `kubectl delete <crd> --ignore-not-
      # found` exit non-zero on "resource type does not exist"
      # (--ignore-not-found covers missing instances, not missing
      # types), and `set -o pipefail` would otherwise abort the
      # whole script.
      if ! kubectl --context "$ctx" -n "$ns" get "$cr" >/dev/null 2>&1; then
        continue
      fi
      # Clear finalizers BEFORE delete: the nvca-operator controller
      # may be unresponsive mid-uninstall and unable to remove its
      # own finalizers, which causes the CR delete and the
      # subsequent namespace delete to hang. The stack is being
      # destroyed, so finalizer-driven reconciliation is moot.
      kubectl --context "$ctx" -n "$ns" get "$cr" -o name 2>/dev/null | \
        while read -r obj; do
          kubectl --context "$ctx" -n "$ns" patch "$obj" \
            --type=merge -p '{"metadata":{"finalizers":[]}}' \
            >/dev/null 2>&1 || true
        done
      echo "  delete $cr in $ns"
      kubectl --context "$ctx" -n "$ns" delete "$cr" --all \
        --ignore-not-found --timeout=60s
    done
  done
}

# force_clear_namespace_finalizers handles the stuck-Terminating
# namespace case: even after delete_stack_crs + helm uninstall, a
# namespace's own spec.finalizers can hold it in Terminating. nvca-
# operator and nvcf-backend are the typical offenders. This clears
# those via the /finalize subresource so namespace teardown completes.
force_clear_namespace_finalizers() {
  local ctx="$1"
  local ns="$2"
  kubectl --context "$ctx" get namespace "$ns" -o json 2>/dev/null | \
    jq '.spec.finalizers = []' | \
    kubectl --context "$ctx" replace --raw "/api/v1/namespaces/$ns/finalize" -f - \
      >/dev/null 2>&1 || true
}

uninstall_stack_releases() {
  local ctx="$1"
  shift
  local releases=("$@")
  for entry in "${releases[@]}"; do
    local name="${entry%:*}"
    local ns="${entry#*:}"
    echo "  uninstall $name in $ns"
    helm --kube-context "$ctx" uninstall "$name" -n "$ns" \
      --ignore-not-found --wait --timeout 2m
  done
}

delete_stack_namespaces() {
  local ctx="$1"
  shift
  local namespaces=("$@")
  for ns in "${namespaces[@]}"; do
    echo "  delete namespace $ns"
    # Polite delete with a bounded wait. If the namespace is stuck
    # Terminating (orphan finalizers on nvca-operator / nvcf-backend
    # after the controller is gone), drop into the force-clear path
    # rather than hang the BDD cleanup.
    if ! kubectl --context "$ctx" delete namespace "$ns" \
        --ignore-not-found --wait --timeout=60s 2>/dev/null; then
      echo "  force-clear finalizers on $ns (stuck Terminating)"
      force_clear_namespace_finalizers "$ctx" "$ns"
    fi
  done
}

clean_stack_out() {
  local dir
  for dir in "${STACK_OUT_DIRS[@]}"; do
    echo "  clean $dir"
    rm -f "$dir"/*.yaml
  done
}


# --- Mode dispatch ---

if [[ "$mode" == "single" ]]; then
  ctx="k3d-$CLUSTER_NAME"
  if ! kubectl --context "$ctx" cluster-info >/dev/null 2>&1; then
    echo "Context $ctx unreachable; nothing to clean."
    exit 0
  fi
  delete_stack_crs "$ctx" "nvca-operator"
  uninstall_stack_releases "$ctx" \
    "${STACK_RELEASES_WORKER[@]}" \
    "${STACK_RELEASES_CP[@]}"
  delete_stack_namespaces "$ctx" \
    "${STACK_NAMESPACES_WORKER[@]}" \
    "${STACK_NAMESPACES_CP[@]}"
  clean_stack_out
  exit 0
fi

# multi
# Worker layer first per CR-finalizer ordering: CRs on the cp can
# wait on worker controllers to clear them. Discover every
# ncp-local-compute-* cluster on the host.
COMPUTES=()
while IFS= read -r name; do
  [[ -n "$name" ]] && COMPUTES+=("$name")
done < <(
  k3d cluster list -o json |
    jq -r '.[] | select(.name|startswith("ncp-local-compute-")) | .name'
)

for name in "${COMPUTES[@]:-}"; do
  [[ -z "$name" ]] && continue
  ctx="k3d-$name"
  echo ">>> Cleaning compute cluster $ctx"
  if ! kubectl --context "$ctx" cluster-info >/dev/null 2>&1; then
    echo "Context $ctx unreachable; skipping."
    continue
  fi
  delete_stack_crs "$ctx" "nvca-operator"
  uninstall_stack_releases "$ctx" "${STACK_RELEASES_WORKER[@]}"
  delete_stack_namespaces "$ctx" "${STACK_NAMESPACES_WORKER[@]}"
done

if k3d cluster get ncp-local-cp >/dev/null 2>&1; then
  echo ">>> Cleaning control-plane cluster k3d-ncp-local-cp"
  ctx="k3d-ncp-local-cp"
  if kubectl --context "$ctx" cluster-info >/dev/null 2>&1; then
    uninstall_stack_releases "$ctx" "${STACK_RELEASES_CP[@]}"
    delete_stack_namespaces "$ctx" "${STACK_NAMESPACES_CP[@]}"
  else
    echo "Context $ctx unreachable; skipping."
  fi
fi

clean_stack_out
