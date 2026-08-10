#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

readonly SCRIPT_NAME="$(basename "$0")"
readonly MIN_HELM_VERSION="3.12.0"
readonly HELMFILE_MAJOR="1"
readonly HELMFILE_MINOR="1"

ok=0
warn=0
fail=0
skip_cluster_checks=false
kube_context=""
kubectl_args=()
summary_rows=()

usage() {
  cat <<EOF
NVCF prerequisite checker

Checks whether the local machine and the selected Kubernetes cluster have the
tools and cluster-level components expected before installing or validating a
self-managed NVCF environment.

Usage:
  ${SCRIPT_NAME} [options]

Options:
  --context <name>         Use a specific kubeconfig context for cluster checks.
  --skip-cluster-checks    Check only local tools and skip Kubernetes queries.
  -h, --help               Show this help message and exit.

What this checks:
  - Local tools: kubectl, helm, helmfile, helm-diff, ngc
  - Cluster access through kubectl
  - Container runtime on the first node
  - CNI detection for common Calico installations
  - MetalLB, MetalLB IP ranges, and common alternative load balancers
  - NVIDIA Network Operator and NicClusterPolicy
  - NVIDIA GPU Operator, ClusterPolicy, and GPU node capacity

Examples:
  ${SCRIPT_NAME}
  ${SCRIPT_NAME} --context my-cluster
  ${SCRIPT_NAME} --skip-cluster-checks

Exit codes:
  0  All required checks passed.
  1  One or more required checks failed.
  2  Invalid command-line arguments.
EOF
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --context)
        if [[ $# -lt 2 || -z "${2:-}" ]]; then
          echo "error: --context requires a value" >&2
          return 2
        fi
        kube_context="$2"
        shift 2
        ;;
      --skip-cluster-checks)
        skip_cluster_checks=true
        shift
        ;;
      -h | --help)
        usage
        exit 0
        ;;
      *)
        echo "error: unknown option: $1" >&2
        echo "Run '${SCRIPT_NAME} --help' for usage." >&2
        return 2
        ;;
    esac
  done

  if [[ -n "$kube_context" ]]; then
    kubectl_args=(--context "$kube_context")
  fi
}

print_header() {
  echo
  echo "============================================================"
  echo "  NVCF prerequisite check"
  echo "============================================================"
  if [[ -n "$kube_context" ]]; then
    echo "  Kubernetes context: $kube_context"
    echo "------------------------------------------------------------"
  fi
  echo
}

record_check() {
  local label="$1"
  local result="$2"
  local status="$3"
  local icon

  case "$status" in
    pass)
      icon="PASS"
      ok=$((ok + 1))
      ;;
    warn)
      icon="WARN"
      warn=$((warn + 1))
      ;;
    fail)
      icon="FAIL"
      fail=$((fail + 1))
      ;;
    *)
      echo "error: invalid status '$status' for check '$label'" >&2
      return 2
      ;;
  esac

  printf "  %-4s  %-24s %s\n" "$icon" "$label" "$result"
  summary_rows+=("${icon}|${label}|${result}")
}

kubectl_cmd() {
  if [[ "${#kubectl_args[@]}" -gt 0 ]]; then
    kubectl "${kubectl_args[@]}" "$@"
  else
    kubectl "$@"
  fi
}

run_check() {
  local check_name="$1"

  "$check_name" || true
}

version_at_least() {
  local actual="${1#v}"
  local minimum="${2#v}"
  local actual_major actual_minor actual_patch
  local min_major min_minor min_patch

  IFS=. read -r actual_major actual_minor actual_patch <<<"$actual"
  IFS=. read -r min_major min_minor min_patch <<<"$minimum"

  actual_major="${actual_major:-0}"
  actual_minor="${actual_minor:-0}"
  actual_patch="${actual_patch:-0}"
  min_major="${min_major:-0}"
  min_minor="${min_minor:-0}"
  min_patch="${min_patch:-0}"

  [[ "$actual_major" =~ ^[0-9]+$ ]] || return 1
  [[ "$actual_minor" =~ ^[0-9]+$ ]] || return 1
  [[ "$actual_patch" =~ ^[0-9]+$ ]] || return 1
  [[ "$min_major" =~ ^[0-9]+$ ]] || return 1
  [[ "$min_minor" =~ ^[0-9]+$ ]] || return 1
  [[ "$min_patch" =~ ^[0-9]+$ ]] || return 1

  if ((actual_major != min_major)); then
    ((actual_major > min_major))
    return
  fi
  if ((actual_minor != min_minor)); then
    ((actual_minor > min_minor))
    return
  fi
  ((actual_patch >= min_patch))
}

version_major_minor() {
  local version="${1#v}"
  local major minor patch

  IFS=. read -r major minor patch <<<"$version"
  printf "%s.%s\n" "${major:-0}" "${minor:-0}"
}

join_by() {
  local delimiter="$1"
  shift
  local first=true
  local item

  for item in "$@"; do
    if [[ "$first" == true ]]; then
      printf "%s" "$item"
      first=false
    else
      printf "%s%s" "$delimiter" "$item"
    fi
  done
}

check_kubectl() {
  local version

  if ! command -v kubectl >/dev/null 2>&1; then
    record_check "kubectl" "not found" "fail"
    return
  fi

  version=$(
    kubectl version --client -o json 2>/dev/null \
      | sed -n 's/.*"gitVersion": *"\([^"]*\)".*/\1/p' \
      | head -n 1 \
      || true
  )
  record_check "kubectl" "${version:-unknown}" "pass"
}

check_cluster_connectivity() {
  local endpoint

  if kubectl_cmd cluster-info >/dev/null 2>&1; then
    endpoint=$(
      kubectl_cmd cluster-info 2>/dev/null \
        | head -n 1 \
        | sed 's/\x1b\[[0-9;]*m//g'
    )
    record_check "cluster connectivity" "$endpoint" "pass"
  else
    record_check "cluster connectivity" "cannot reach cluster; check KUBECONFIG, context, or VPN" "fail"
  fi
}

check_helm() {
  local version

  if ! command -v helm >/dev/null 2>&1; then
    record_check "helm" "not found" "fail"
    return
  fi

  version=$(helm version --short 2>/dev/null | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -n 1 || true)
  if [[ -z "$version" ]]; then
    record_check "helm" "version unknown; need >= ${MIN_HELM_VERSION}" "fail"
  elif version_at_least "$version" "$MIN_HELM_VERSION"; then
    record_check "helm" "$version" "pass"
  else
    record_check "helm" "$version; need >= ${MIN_HELM_VERSION}" "fail"
  fi
}

check_helmfile() {
  local version major_minor

  if ! command -v helmfile >/dev/null 2>&1; then
    record_check "helmfile" "not found" "fail"
    return
  fi

  version=$(helmfile version 2>/dev/null | grep -oE 'v?[0-9]+\.[0-9]+\.[0-9]+' | head -n 1 || true)
  major_minor=$(version_major_minor "${version:-0.0.0}")
  if [[ -z "$version" ]]; then
    record_check "helmfile" "version unknown; want ${HELMFILE_MAJOR}.${HELMFILE_MINOR}.x" "warn"
  elif [[ "$major_minor" == "${HELMFILE_MAJOR}.${HELMFILE_MINOR}" ]]; then
    record_check "helmfile" "$version" "pass"
  else
    record_check "helmfile" "$version; want ${HELMFILE_MAJOR}.${HELMFILE_MINOR}.x" "warn"
  fi
}

check_helm_diff() {
  local version

  if ! command -v helm >/dev/null 2>&1; then
    record_check "helm-diff" "not checked because helm is not found" "fail"
    return
  fi

  if helm plugin list 2>/dev/null | awk '$1 == "diff" {found = 1} END {exit !found}'; then
    version=$(helm plugin list 2>/dev/null | awk '$1 == "diff" {print $2; exit}')
    record_check "helm-diff" "${version:-installed}" "pass"
  else
    record_check "helm-diff" "not installed; run: helm plugin install https://github.com/databus23/helm-diff" "fail"
  fi
}

check_ngc() {
  local version

  if ! command -v ngc >/dev/null 2>&1; then
    record_check "ngc-cli" "not found; install from https://org.ngc.nvidia.com/setup/installers/cli" "fail"
    return
  fi

  version=$(ngc version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n 1 || true)
  record_check "ngc-cli" "v${version:-unknown}" "pass"
}

check_container_runtime() {
  local runtime

  runtime=$(kubectl_cmd get nodes -o jsonpath='{.items[0].status.nodeInfo.containerRuntimeVersion}' 2>/dev/null || true)
  if [[ -n "$runtime" ]]; then
    record_check "container runtime" "$runtime" "pass"
  else
    record_check "container runtime" "could not detect" "warn"
  fi
}

pod_counts() {
  local namespace="$1"

  kubectl_cmd get pods -n "$namespace" --no-headers 2>/dev/null \
    | awk '$3 == "Running" {running += 1} {total += 1} END {printf "%d %d", running + 0, total + 0}'
}

metallb_ip_ranges() {
  local ranges

  ranges=$(
    kubectl_cmd get ipaddresspools.metallb.io -n metallb-system \
      -o jsonpath='{range .items[*]}{range .spec.addresses[*]}{.}{" "}{end}{end}' 2>/dev/null \
      | sed 's/[[:space:]]*$//'
  )
  if [[ -n "$ranges" ]]; then
    printf "%s\n" "$ranges"
    return
  fi

  kubectl_cmd get ipaddresspools.metallb.io -n metallb-system \
    --no-headers 2>/dev/null \
    | awk '{$1 = $2 = $3 = ""; sub(/^[[:space:]]+/, ""); gsub(/[][]|"/, ""); print}' \
    | paste -sd ' ' -
}

check_cni() {
  local found=()

  if kubectl_cmd get namespace calico-system >/dev/null 2>&1; then
    found+=("calico-system namespace")
  fi
  if kubectl_cmd get namespace tigera-operator >/dev/null 2>&1; then
    found+=("tigera-operator namespace")
  fi
  if kubectl_cmd get pods -A --no-headers 2>/dev/null | awk '$2 ~ /^calico-node/ {found = 1} END {exit !found}'; then
    found+=("calico-node pods")
  fi

  if [[ "${#found[@]}" -gt 0 ]]; then
    record_check "CNI" "Calico detected ($(join_by ', ' "${found[@]}"))" "pass"
  else
    record_check "CNI" "Calico not detected; cluster may use another CNI" "warn"
  fi
}

check_metallb() {
  local running total ranges

  if ! kubectl_cmd get namespace metallb-system >/dev/null 2>&1; then
    record_check "MetalLB" "metallb-system namespace not found" "fail"
    record_check "MetalLB IP ranges" "not checked because MetalLB is absent" "warn"
    return
  fi

  read -r running total < <(pod_counts metallb-system)
  if [[ "$running" -gt 0 ]]; then
    record_check "MetalLB" "${running}/${total} pods Running" "pass"
  else
    record_check "MetalLB" "${running}/${total} pods Running" "fail"
  fi

  ranges=$(metallb_ip_ranges || true)
  if [[ -n "$ranges" ]]; then
    record_check "MetalLB IP ranges" "$ranges" "pass"
  else
    record_check "MetalLB IP ranges" "no IPAddressPool ranges found" "warn"
  fi
}

check_other_load_balancers() {
  local found_lbs=()

  if kubectl_cmd get svc -A --no-headers 2>/dev/null \
    | awk '$2 ~ /ingress-nginx/ && $3 == "LoadBalancer" {found = 1} END {exit !found}'; then
    found_lbs+=("nginx-ingress")
  fi

  if kubectl_cmd get ns traefik >/dev/null 2>&1; then
    found_lbs+=("traefik")
  fi
  if kubectl_cmd get ns haproxy-controller >/dev/null 2>&1; then
    found_lbs+=("haproxy")
  fi
  if kubectl_cmd get ds -A --no-headers 2>/dev/null | grep -qi 'kube-vip'; then
    found_lbs+=("kube-vip")
  fi
  if kubectl_cmd get svc -A --no-headers 2>/dev/null \
    | awk '$5 !~ /^<none>$|^$|^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/ && $5 ~ /\./ {found = 1} END {exit !found}'; then
    found_lbs+=("cloud-lb(external)")
  fi

  if [[ "${#found_lbs[@]}" -gt 0 ]]; then
    record_check "other load balancers" "$(join_by ', ' "${found_lbs[@]}")" "warn"
  else
    record_check "other load balancers" "none detected besides MetalLB" "pass"
  fi
}

check_network_operator() {
  local running total chart detail

  if ! kubectl_cmd get namespace network-operator >/dev/null 2>&1; then
    record_check "network-operator" "namespace not found; not installed" "warn"
    record_check "NicClusterPolicy" "not checked because network-operator is absent" "warn"
    return
  fi

  read -r running total < <(pod_counts network-operator)
  chart=$(helm list -n network-operator ${kube_context:+--kube-context "$kube_context"} 2>/dev/null | awk 'NR > 1 {print $9; exit}' || true)
  detail="${running}/${total} pods Running"
  if [[ -n "$chart" ]]; then
    detail="${detail}; chart ${chart}"
  fi

  if [[ "$running" -gt 0 ]]; then
    record_check "network-operator" "$detail" "pass"
  else
    record_check "network-operator" "$detail" "fail"
  fi

  if kubectl_cmd get nicclusterpolicy --no-headers 2>/dev/null | awk 'NF > 0 {found = 1} END {exit !found}'; then
    record_check "NicClusterPolicy" "present" "pass"
  else
    record_check "NicClusterPolicy" "not found" "warn"
  fi
}

check_gpu_operator() {
  local running total chart detail cluster_policy gpu_nodes

  if ! kubectl_cmd get namespace gpu-operator >/dev/null 2>&1; then
    record_check "gpu-operator" "namespace not found; not installed" "warn"
    record_check "ClusterPolicy" "not checked because gpu-operator is absent" "warn"
    record_check "GPU nodes" "not checked because gpu-operator is absent" "warn"
    return
  fi

  read -r running total < <(pod_counts gpu-operator)
  chart=$(helm list -n gpu-operator ${kube_context:+--kube-context "$kube_context"} 2>/dev/null | awk 'NR > 1 {print $9; exit}' || true)
  detail="${running}/${total} pods Running"
  if [[ -n "$chart" ]]; then
    detail="${detail}; chart ${chart}"
  fi

  if [[ "$running" -gt 0 ]]; then
    record_check "gpu-operator" "$detail" "pass"
  else
    record_check "gpu-operator" "$detail" "fail"
  fi

  cluster_policy=$(kubectl_cmd get clusterpolicy --no-headers 2>/dev/null | awk 'NF > 0 {print $1; exit}' || true)
  if [[ -n "$cluster_policy" ]]; then
    record_check "ClusterPolicy" "$cluster_policy" "pass"
  else
    record_check "ClusterPolicy" "not found" "warn"
  fi

  gpu_nodes=$(
    kubectl_cmd get nodes \
      -o custom-columns='NAME:.metadata.name,GPU:.status.capacity.nvidia\.com/gpu' \
      --no-headers 2>/dev/null \
      | awk '$2 != "<none>" && $2 != "0" {printf "%s%s(%sgpu)", sep, $1, $2; sep = ", "}' \
      || true
  )
  if [[ -n "$gpu_nodes" ]]; then
    record_check "GPU nodes" "$gpu_nodes" "pass"
  else
    record_check "GPU nodes" "no nvidia.com/gpu capacity on any node" "warn"
  fi
}

print_summary() {
  local row icon label detail

  echo
  echo "============================================================"
  echo "  Summary: ${ok} passed, ${warn} warned, ${fail} failed"
  echo "============================================================"

  for row in "${summary_rows[@]}"; do
    IFS='|' read -r icon label detail <<<"$row"
    printf "  %-4s  %-24s %s\n" "$icon" "$label" "$detail"
  done

  echo "============================================================"
  echo
}

run_cluster_checks() {
  if [[ "$skip_cluster_checks" == true ]]; then
    record_check "cluster checks" "skipped by --skip-cluster-checks" "warn"
    return
  fi

  if ! command -v kubectl >/dev/null 2>&1; then
    record_check "cluster checks" "skipped because kubectl is not found" "fail"
    return
  fi

  run_check check_cluster_connectivity
  run_check check_container_runtime
  run_check check_cni
  run_check check_metallb
  run_check check_other_load_balancers
  run_check check_network_operator
  run_check check_gpu_operator
}

main() {
  parse_args "$@"
  print_header

  check_kubectl
  check_helm
  check_helmfile
  check_helm_diff
  check_ngc
  run_cluster_checks
  print_summary

  [[ "$fail" -eq 0 ]]
}

main "$@"