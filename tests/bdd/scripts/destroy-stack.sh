#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Destructive stack cleanup for the BDD suite. Uninstalls every
# stack-owned helm release and deletes every stack-owned namespace
# on a retained k3d cluster, then clears stack-generated artifacts:
# root-level handoff yaml and Helmfile --output-dir render trees under
# deploy/stacks/self-managed/out/ and
# deploy/stacks/nvcf-compute-plane/out/, plus generated compute
# registration values under
# deploy/stacks/nvcf-compute-plane/registration/. The k3d cluster
# itself is left running so a subsequent install does not pay the
# cluster boot cost.
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
# switching. Only documented NotFound and a truly unreachable kube
# context (missing context or connection refused) are skipped.
# Permission errors, timeouts, API failures, missing binaries, and
# finalizer-patch failures abort cleanup.
#
# Usage:
#   tests/bdd/scripts/destroy-stack.sh single [CLUSTER_NAME=ncp-local]
#   tests/bdd/scripts/destroy-stack.sh multi
#
# Multi-cluster mode discovers every ncp-local-compute-* cluster
# via `k3d cluster list -o json | jq` and cleans each (worker layer
# first to satisfy CR-finalizer ordering), then cleans
# k3d-ncp-local-cp. Control-plane cleanup also applies the worker
# release and namespace allow-lists: multi-cluster feature Backgrounds
# create nvca-operator (and its pull secret) on k3d-ncp-local-cp, not
# only on compute contexts.
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
# builds the /finalize subresource patch with jq. kubectl and helm
# must also be present; a missing binary is not an unreachable
# cluster and must not be treated as a successful no-op.
command -v jq >/dev/null 2>&1 || {
  echo "destroy-stack.sh requires jq; install it and retry." >&2
  exit 1
}
command -v kubectl >/dev/null 2>&1 || {
  echo "destroy-stack.sh requires kubectl; install it and retry." >&2
  exit 1
}
command -v helm >/dev/null 2>&1 || {
  echo "destroy-stack.sh requires helm; install it and retry." >&2
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
STACK_REGISTRATION_DIR="$REPO_ROOT/deploy/stacks/nvcf-compute-plane/registration"

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
  "default-monitors:monitoring"
  "otel-collector:monitoring"
  "opentelemetry-operator:monitoring"
  "victoria-metrics:monitoring"
  "prometheus-operator-crds:monitoring"
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
  monitoring
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

# Stack-owned resources that live in topology-owned namespaces. Helm removes
# the Certificate, but cert-manager intentionally leaves its generated Secret
# without an owner reference. Delete it explicitly so a reinstalled OpenBao
# hierarchy cannot inherit a certificate signed by the previous root CA.
STACK_RESOURCES_CP=(
  "secret:llm-request-router-grpc-tls:envoy-gateway-system"
)

# --- Helpers ---

# Capture kubectl stdout+stderr in _kubectl_out and return kubectl's
# exit code. Used so callers can classify NotFound / unreachable
# without treating every non-zero as a skip.
kubectl_capture() {
  local rc
  set +e
  _kubectl_out=$(kubectl "$@" 2>&1)
  rc=$?
  set -e
  return "$rc"
}

# Missing context or connection refused. Timeouts, Forbidden,
# Unauthorized, and missing binaries are not unreachable.
kubectl_is_unreachable() {
  local msg="$1"
  case "$msg" in
    *"does not exist"*|*"connection refused"*|*"was refused"*|*"no such host"*|*"no route to host"*)
      return 0
      ;;
  esac
  return 1
}

kubectl_is_not_found() {
  local msg="$1"
  case "$msg" in
    *"(NotFound)"*|*" not found"*)
      return 0
      ;;
  esac
  return 1
}

kubectl_is_missing_type() {
  local msg="$1"
  case "$msg" in
    *"doesn't have a resource type"*|*"could not find the requested resource"*)
      return 0
      ;;
  esac
  return 1
}

# Return 0 if the API answers, 1 if the context or cluster is absent.
# Any other cluster-info failure prints the error and aborts.
cluster_is_reachable() {
  local ctx="$1"
  if kubectl_capture --context "$ctx" cluster-info; then
    return 0
  fi
  if kubectl_is_unreachable "$_kubectl_out"; then
    return 1
  fi
  printf '%s\n' "$_kubectl_out" >&2
  echo "cluster-info failed for $ctx" >&2
  exit 1
}

namespace_exists() {
  local ctx="$1"
  local ns="$2"
  if kubectl_capture --context "$ctx" get namespace "$ns"; then
    return 0
  fi
  if kubectl_is_not_found "$_kubectl_out"; then
    return 1
  fi
  printf '%s\n' "$_kubectl_out" >&2
  echo "get namespace $ns failed on $ctx" >&2
  exit 1
}

cr_available() {
  local ctx="$1"
  local ns="$2"
  local cr="$3"
  if kubectl_capture --context "$ctx" -n "$ns" get "$cr"; then
    return 0
  fi
  if kubectl_is_missing_type "$_kubectl_out" || kubectl_is_not_found "$_kubectl_out"; then
    return 1
  fi
  printf '%s\n' "$_kubectl_out" >&2
  echo "get $cr in $ns failed on $ctx" >&2
  exit 1
}

patch_cr_finalizers() {
  local ctx="$1"
  local ns="$2"
  local cr="$3"
  local names obj
  names=$(kubectl --context "$ctx" -n "$ns" get "$cr" -o name)
  while IFS= read -r obj; do
    [[ -z "$obj" ]] && continue
    if kubectl_capture --context "$ctx" -n "$ns" patch "$obj" \
        --type=merge -p '{"metadata":{"finalizers":[]}}'; then
      continue
    fi
    if kubectl_is_not_found "$_kubectl_out"; then
      continue
    fi
    printf '%s\n' "$_kubectl_out" >&2
    echo "patch $obj in $ns failed on $ctx" >&2
    return 1
  done <<< "$names"
}

delete_stack_crs() {
  local ctx="$1"
  shift
  local namespaces=("$@")
  for ns in "${namespaces[@]}"; do
    if ! namespace_exists "$ctx" "$ns"; then
      continue
    fi
    for cr in "${STACK_CRS_WORKER[@]}"; do
      # Missing CRD or NotFound is a skip. Other get failures abort.
      if ! cr_available "$ctx" "$ns" "$cr"; then
        continue
      fi
      # Clear finalizers BEFORE delete: the nvca-operator controller
      # may be unresponsive mid-uninstall and unable to remove its
      # own finalizers, which causes the CR delete and the
      # subsequent namespace delete to hang. The stack is being
      # destroyed, so finalizer-driven reconciliation is moot.
      # NotFound on an individual object is fine; other patch
      # failures abort so cleanup cannot report success with CRs
      # still blocked.
      patch_cr_finalizers "$ctx" "$ns" "$cr"
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
# NotFound means the namespace already went away. Other failures abort.
force_clear_namespace_finalizers() {
  local ctx="$1"
  local ns="$2"
  local json
  if ! json=$(kubectl --context "$ctx" get namespace "$ns" -o json 2>&1); then
    if kubectl_is_not_found "$json"; then
      return 0
    fi
    printf '%s\n' "$json" >&2
    echo "get namespace $ns -o json failed on $ctx" >&2
    return 1
  fi
  printf '%s' "$json" | jq '.spec.finalizers = []' | \
    kubectl --context "$ctx" replace --raw "/api/v1/namespaces/$ns/finalize" -f - \
      >/dev/null
}

force_delete_namespace_pods() {
  local ctx="$1"
  local ns="$2"
  local pods pod
  if ! pods=$(kubectl --context "$ctx" -n "$ns" get pods -o name 2>&1); then
    if kubectl_is_not_found "$pods"; then
      return 0
    fi
    printf '%s\n' "$pods" >&2
    echo "get pods in namespace $ns failed on $ctx" >&2
    return 1
  fi
  while IFS= read -r pod; do
    [[ -z "$pod" ]] && continue
    echo "  force-delete $pod in $ns"
    kubectl --context "$ctx" -n "$ns" delete "$pod" \
      --force --grace-period=0 --wait=false --ignore-not-found
  done <<< "$pods"
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

delete_stack_resources() {
  local ctx="$1"
  shift
  local resources=("$@")
  for entry in "${resources[@]}"; do
    local kind="${entry%%:*}"
    local rest="${entry#*:}"
    local name="${rest%%:*}"
    local ns="${rest#*:}"
    echo "  delete $kind $name in $ns"
    kubectl --context "$ctx" -n "$ns" delete "$kind" "$name" \
      --ignore-not-found --wait --timeout=60s
  done
}

delete_stack_namespaces() {
  local ctx="$1"
  shift
  local namespaces=("$@")
  for ns in "${namespaces[@]}"; do
    echo "  delete namespace $ns"
    # Polite delete with a bounded wait. Disposable worker and gateway
    # pods can inherit a longer termination grace period than this local
    # cleanup budget, so force-delete only the pods that remain. If the
    # namespace is still stuck, clear its finalizers as a last resort.
    if ! kubectl --context "$ctx" delete namespace "$ns" \
        --ignore-not-found --wait --timeout=60s; then
      force_delete_namespace_pods "$ctx" "$ns"
      if ! kubectl --context "$ctx" wait --for=delete "namespace/$ns" \
          --timeout=60s; then
        echo "  force-clear finalizers on $ns (stuck Terminating)"
        force_clear_namespace_finalizers "$ctx" "$ns"
      fi
    fi
  done
}

# clean_stack_out removes stack-generated handoff files. It is the
# explicit-path equivalent of each stack's `make clean` for helmfile
# render trees, plus compute registration values that `make clean`
# does not cover. Root-level non-yaml notes in out/ are left in place.
clean_stack_out() {
  local dir sub
  local restore_nullglob=0
  if ! shopt -q nullglob; then
    shopt -s nullglob
    restore_nullglob=1
  fi

  for dir in "${STACK_OUT_DIRS[@]}"; do
    echo "  clean $dir"
    if [[ -d "$dir" ]]; then
      rm -f "$dir"/*.yaml
      for sub in "$dir"/*/; do
        rm -rf "$sub"
      done
    fi
  done

  echo "  clean $STACK_REGISTRATION_DIR"
  if [[ -d "$STACK_REGISTRATION_DIR" ]]; then
    rm -f "$STACK_REGISTRATION_DIR"/*.yaml
  fi

  if [[ "$restore_nullglob" -eq 1 ]]; then
    shopt -u nullglob
  fi
}


# --- Mode dispatch ---

if [[ "$mode" == "single" ]]; then
  ctx="k3d-$CLUSTER_NAME"
  if cluster_is_reachable "$ctx"; then
    delete_stack_crs "$ctx" "nvca-operator"
    uninstall_stack_releases "$ctx" \
      "${STACK_RELEASES_WORKER[@]}" \
      "${STACK_RELEASES_CP[@]}"
    delete_stack_resources "$ctx" "${STACK_RESOURCES_CP[@]}"
    delete_stack_namespaces "$ctx" \
      "${STACK_NAMESPACES_WORKER[@]}" \
      "${STACK_NAMESPACES_CP[@]}"
  else
    echo "Context $ctx unreachable; skipping cluster resources."
  fi
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
  if cluster_is_reachable "$ctx"; then
    delete_stack_crs "$ctx" "nvca-operator"
    uninstall_stack_releases "$ctx" "${STACK_RELEASES_WORKER[@]}"
    delete_stack_namespaces "$ctx" "${STACK_NAMESPACES_WORKER[@]}"
  else
    echo "Context $ctx unreachable; skipping."
    continue
  fi
done

if k3d cluster get ncp-local-cp >/dev/null 2>&1; then
  echo ">>> Cleaning control-plane cluster k3d-ncp-local-cp"
  ctx="k3d-ncp-local-cp"
  if cluster_is_reachable "$ctx"; then
    # Worker allow-lists apply on the CP cluster too: multi-cluster
    # Backgrounds create nvca-operator (namespace + pull secret, and
    # sometimes the helm release) on k3d-ncp-local-cp. Compute clusters
    # were cleaned above. Helm --ignore-not-found and kubectl
    # --ignore-not-found keep this idempotent when the worker stack was
    # never installed on the CP.
    delete_stack_crs "$ctx" "nvca-operator"
    uninstall_stack_releases "$ctx" \
      "${STACK_RELEASES_WORKER[@]}" \
      "${STACK_RELEASES_CP[@]}"
    delete_stack_resources "$ctx" "${STACK_RESOURCES_CP[@]}"
    delete_stack_namespaces "$ctx" \
      "${STACK_NAMESPACES_WORKER[@]}" \
      "${STACK_NAMESPACES_CP[@]}"
  else
    echo "Context $ctx unreachable; skipping."
  fi
fi

clean_stack_out
