#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# End-to-end test of the Global Discovery Service on local k3d clusters. See
# README.md for prerequisites, settings and the assertions of each phase.
#
# Phase 1: one cluster, gds-server from the chart with the memory store and one
# replica, driven over REST through a port-forward.
#
# The script is compatible with bash 3.2 (the macOS default). It talks to its
# clusters through its own kubeconfig file in the work directory, so it never
# reads, writes or switches the default kubeconfig or its current context. It
# only creates k3d clusters whose names start with gds-e2e, records them in
# the work directory, and with E2E_CLEANUP=1 deletes only the recorded ones.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
MANIFESTS="${SCRIPT_DIR}/manifests"

# Phases to run, separated by spaces or commas. Only phase 1 exists so far;
# phase 2 (three clusters with the MCS controller) adds run_phase_2.
E2E_PHASES="${E2E_PHASES:-1}"
E2E_CLUSTER="${E2E_CLUSTER:-gds-e2e}"
E2E_CREATE_CLUSTERS="${E2E_CREATE_CLUSTERS:-1}"
E2E_CLEANUP="${E2E_CLEANUP:-0}"
E2E_CLEANUP_ONLY="${E2E_CLEANUP_ONLY:-0}"
E2E_DEPLOY_ONLY="${E2E_DEPLOY_ONLY:-0}"
E2E_DRY_RUN="${E2E_DRY_RUN:-0}"
E2E_SKIP_BUILD="${E2E_SKIP_BUILD:-0}"
E2E_TIMEOUT="${E2E_TIMEOUT:-60}"
E2E_HELM_TIMEOUT="${E2E_HELM_TIMEOUT:-5m}"
E2E_NAMESPACE="${E2E_NAMESPACE:-global-discovery}"
E2E_REGION="${E2E_REGION:-e2e-local}"
E2E_CLUSTERSET="${E2E_CLUSTERSET:-e2e}"
E2E_HTTP_LOCAL_PORT="${E2E_HTTP_LOCAL_PORT:-28080}"
E2E_ADMIN_LOCAL_PORT="${E2E_ADMIN_LOCAL_PORT:-29091}"
E2E_IMAGE_REPOSITORY="${E2E_IMAGE_REPOSITORY:-gds-e2e/gds-server}"
E2E_WORK_DIR="${E2E_WORK_DIR:-${TMPDIR:-/tmp}/global-discovery-k3d-e2e}"
E2E_WORK_DIR="${E2E_WORK_DIR%/}"
# Where the server image comes from. local builds it with Bazel from this
# checkout, tags it by image id and imports it into k3d. No published image
# exists yet.
IMAGE_SOURCE="${IMAGE_SOURCE:-local}"
BAZEL="${BAZEL:-bazel}"
BAZEL_FLAGS="${BAZEL_FLAGS:-}"

readonly SERVER_RELEASE="global-discovery"
readonly SERVER_CHART="${REPO_ROOT}/deploy/helm/global-discovery/global-discovery"
readonly SERVER_SELECTOR="app.kubernetes.io/instance=${SERVER_RELEASE}"
readonly IMAGE_TARGET="//src/control-plane-services/global-discovery:image_load"
# The repository:tag the Bazel image_load target loads into Docker.
readonly BAZEL_IMAGE="src/control-plane-services/global-discovery:latest"
readonly CREATED_CLUSTERS_FILE="${E2E_WORK_DIR}/created-clusters"
readonly CURL_ERR="${E2E_WORK_DIR}/curl.err"
readonly PF_LOG="${E2E_WORK_DIR}/port-forward.log"

REST_URL="http://127.0.0.1:${E2E_HTTP_LOCAL_PORT}"
ADMIN_URL="http://127.0.0.1:${E2E_ADMIN_LOCAL_PORT}"
CS_PATH="/v1/clustersets/${E2E_CLUSTERSET}"
PF_PID=""
IMAGE_TAG=""
REST_STATUS=""
REST_BODY=""

RESULTS=()
PASS_COUNT=0
FAIL_COUNT=0
SKIP_COUNT=0
LAST_OBSERVED=""
ABORTED=0
# Remaining step names, recorded as skipped when a required step fails.
REMAINING_STEPS=""

# ---------------------------------------------------------------------------
# Output helpers

log() {
  printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"
}

section() {
  printf '\n===== %s =====\n' "$*"
}

pass() {
  PASS_COUNT=$((PASS_COUNT + 1))
  RESULTS+=("PASS  $1${2:+ ($2)}")
  log "PASS  $1${2:+ ($2)}"
}

fail() {
  FAIL_COUNT=$((FAIL_COUNT + 1))
  RESULTS+=("FAIL  $1${2:+ ($2)}")
  log "FAIL  $1${2:+ ($2)}"
}

skip() {
  SKIP_COUNT=$((SKIP_COUNT + 1))
  RESULTS+=("SKIP  $1")
}

die() {
  printf 'run.sh: %s\n' "$*" >&2
  exit 2
}

# ---------------------------------------------------------------------------
# Clusters. Every kubectl and helm call names the kubeconfig file and the
# context of one cluster explicitly.

context_of() {
  printf 'k3d-%s' "$1"
}

kubeconfig_of() {
  printf '%s/kubeconfig-%s' "${E2E_WORK_DIR}" "$1"
}

# kc CLUSTER ARGS...: kubectl against CLUSTER.
kc() {
  local cluster="$1"
  shift
  kubectl --kubeconfig "$(kubeconfig_of "${cluster}")" --context "$(context_of "${cluster}")" "$@"
}

# hm CLUSTER ARGS...: helm against CLUSTER.
hm() {
  local cluster="$1"
  shift
  helm --kubeconfig "$(kubeconfig_of "${cluster}")" --kube-context "$(context_of "${cluster}")" "$@"
}

check_cluster_name() {
  case "$1" in
    gds-e2e | gds-e2e-*) ;;
    *) die "cluster name '$1' must be gds-e2e or start with gds-e2e-; run.sh never touches other clusters" ;;
  esac
}

cluster_exists() {
  k3d cluster get "$1" >/dev/null 2>&1
}

cluster_created_here() {
  [ -f "${CREATED_CLUSTERS_FILE}" ] && grep -qx -- "$1" "${CREATED_CLUSTERS_FILE}"
}

nodes_ready() {  # nodes_ready CLUSTER: every node registered and Ready
  local nodes
  nodes="$(kc "$1" get nodes -o jsonpath='{range .items[*]}{.metadata.name}={.status.conditions[?(@.type=="Ready")].status}{" "}{end}' 2>/dev/null)" || nodes=""
  LAST_OBSERVED="nodes: ${nodes:-none registered}"
  [ -n "${nodes}" ] && ! grep -q '=[^T]' <<<"${nodes}"
}

# node_start_hint CLUSTER: why the k3s node did not register, when known.
node_start_hint() {
  if docker exec "k3d-$1-server-0" grep -q 'too many open files' \
    /var/lib/rancher/k3s/agent/containerd/containerd.log 2>/dev/null; then
    printf '%s' "containerd in the k3s node failed with 'too many open files': the Docker host's fs.inotify.max_user_instances is used up, usually by other k3d clusters. Raise it on the Docker host (colima: colima ssh -- sudo sysctl -w fs.inotify.max_user_instances=512) or stop clusters you do not need."
  fi
}

# ensure_cluster NAME [K3D CREATE ARGS...]: reuses k3d cluster NAME or, with
# E2E_CREATE_CLUSTERS=1, creates it without touching the default kubeconfig.
# Then writes the cluster's kubeconfig into the work directory and waits for
# a Ready node. Returns 1 with LAST_OBSERVED set when the cluster is unusable.
ensure_cluster() {
  local name="$1"
  shift
  check_cluster_name "${name}"
  if cluster_exists "${name}"; then
    log "reusing k3d cluster ${name}"
  else
    [ "${E2E_CREATE_CLUSTERS}" = "1" ] ||
      die "k3d cluster ${name} does not exist; create it with 'make cluster' or set E2E_CREATE_CLUSTERS=1"
    log "creating k3d cluster ${name}"
    # Recorded before the create, so a half-created cluster is cleaned up too.
    printf '%s\n' "${name}" >>"${CREATED_CLUSTERS_FILE}"
    if ! k3d cluster create "${name}" \
      --servers 1 --agents 0 --no-lb \
      --k3s-arg '--disable=traefik@server:*' \
      --kubeconfig-update-default=false \
      --kubeconfig-switch-context=false \
      --wait --timeout 180s \
      "$@"; then
      LAST_OBSERVED="k3d cluster create ${name} failed"
      return 1
    fi
  fi
  if ! k3d kubeconfig get "${name}" >"$(kubeconfig_of "${name}")"; then
    LAST_OBSERVED="cannot read the kubeconfig of k3d cluster ${name}"
    return 1
  fi
  chmod 600 "$(kubeconfig_of "${name}")"
  if ! poll 120 nodes_ready "${name}"; then
    LAST_OBSERVED="${LAST_OBSERVED} after 120s. $(node_start_hint "${name}")"
    return 1
  fi
}

# delete_created_clusters: deletes the k3d clusters run.sh created and
# recorded, and nothing else.
delete_created_clusters() {
  local name
  [ -f "${CREATED_CLUSTERS_FILE}" ] || return 0
  while IFS= read -r name; do
    [ -n "${name}" ] || continue
    check_cluster_name "${name}"
    if cluster_exists "${name}"; then
      log "deleting k3d cluster ${name} (created by run.sh)"
      k3d cluster delete "${name}" || true
    fi
  done <"${CREATED_CLUSTERS_FILE}"
  rm -f "${CREATED_CLUSTERS_FILE}"
}

# ---------------------------------------------------------------------------
# Images

# build_image: builds the gds-server image with Bazel unless E2E_SKIP_BUILD=1,
# then tags it as E2E_IMAGE_REPOSITORY:e2e-<image id>, so a rebuilt image gets
# a new tag and helm upgrade rolls the pod.
build_image() {
  case "${IMAGE_SOURCE}" in
    local) ;;
    *) die "IMAGE_SOURCE must be local, got '${IMAGE_SOURCE}'; no published gds-server image exists yet" ;;
  esac
  if [ "${E2E_SKIP_BUILD}" != "1" ]; then
    log "bazel run ${IMAGE_TARGET}"
    # BAZEL_FLAGS is a list of flags.
    # shellcheck disable=SC2086
    (cd "${REPO_ROOT}" && "${BAZEL}" run ${BAZEL_FLAGS} "${IMAGE_TARGET}") || die "bazel run ${IMAGE_TARGET} failed"
  fi
  local id
  id="$(docker image inspect --format '{{.Id}}' "${BAZEL_IMAGE}" 2>/dev/null)" ||
    die "image ${BAZEL_IMAGE} is not in Docker; run without E2E_SKIP_BUILD=1"
  id="${id#sha256:}"
  IMAGE_TAG="e2e-$(printf '%s' "${id}" | cut -c1-12)"
  docker tag "${BAZEL_IMAGE}" "${E2E_IMAGE_REPOSITORY}:${IMAGE_TAG}"
  log "gds-server image ${E2E_IMAGE_REPOSITORY}:${IMAGE_TAG}"
}

# import_image CLUSTER: imports the tagged image into k3d cluster CLUSTER.
import_image() {
  k3d image import -c "$1" "${E2E_IMAGE_REPOSITORY}:${IMAGE_TAG}" >/dev/null ||
    die "k3d image import into $1 failed"
}

# ---------------------------------------------------------------------------
# Polling. A check function returns 0 when satisfied and may set
# LAST_OBSERVED to describe what it saw.

poll() {
  local timeout="$1"
  shift
  local deadline=$((SECONDS + timeout))
  while :; do
    if "$@"; then
      return 0
    fi
    if [ "${SECONDS}" -ge "${deadline}" ]; then
      return 1
    fi
    sleep 2
  done
}

# expect NAME TIMEOUT CHECK [ARGS...]: poll CHECK and record the outcome.
expect() {
  local name="$1" timeout="$2"
  shift 2
  local start="${SECONDS}"
  LAST_OBSERVED=""
  if poll "${timeout}" "$@"; then
    pass "${name}" "$((SECONDS - start))s"
    return 0
  fi
  fail "${name}" "timed out after ${timeout}s; last observed: ${LAST_OBSERVED:-nothing}"
  return 1
}

# once NAME CHECK [ARGS...]: run CHECK once and record the outcome. For steps
# that write, where a retry would change what is checked.
once() {
  local name="$1"
  shift
  LAST_OBSERVED=""
  if "$@"; then
    pass "${name}"
    return 0
  fi
  fail "${name}" "observed: ${LAST_OBSERVED:-nothing}"
  return 1
}

# ---------------------------------------------------------------------------
# REST. Checks feed text to grep, head and awk through here-strings: with
# pipefail, a pipe into a reader that exits early fails with SIGPIPE.

# rest METHOD PATH [BODY]: one REST call against the port-forwarded gateway.
# Sets REST_STATUS (000 when the call failed) and REST_BODY.
rest() {
  local method="$1" path="$2" body="${3:-}"
  local out="${E2E_WORK_DIR}/rest-body.json"
  : >"${out}"
  if [ -n "${body}" ]; then
    REST_STATUS="$(curl -sS -o "${out}" -w '%{http_code}' --max-time 10 -X "${method}" \
      -H 'Content-Type: application/json' --data-binary "${body}" "${REST_URL}${path}" 2>>"${CURL_ERR}")" || REST_STATUS="000"
  else
    REST_STATUS="$(curl -sS -o "${out}" -w '%{http_code}' --max-time 10 -X "${method}" \
      "${REST_URL}${path}" 2>>"${CURL_ERR}")" || REST_STATUS="000"
  fi
  REST_BODY="$(cat "${out}")"
}

# body_matches JQ_FILTER: true when the filter yields true on REST_BODY.
body_matches() {
  printf '%s' "${REST_BODY}" | jq -e "$1" >/dev/null 2>&1
}

# observe JQ_FILTER: LAST_OBSERVED becomes the status and the filter's compact
# output on REST_BODY, or the raw body when it is not JSON.
observe() {
  local shown
  shown="$(jq -c "$1" <<<"${REST_BODY}" 2>/dev/null)" || shown="$(head -c 300 <<<"${REST_BODY}")"
  LAST_OBSERVED="HTTP ${REST_STATUS} ${shown}"
}

# rest_expect METHOD PATH BODY STATUS JQ_FILTER [SHOW_FILTER]: one call whose
# status must be STATUS and whose body must satisfy JQ_FILTER.
rest_expect() {
  local method="$1" path="$2" body="$3" status="$4" filter="$5" show="${6:-.}"
  rest "${method}" "${path}" "${body}"
  observe "${show}"
  [ "${REST_STATUS}" = "${status}" ] && body_matches "${filter}"
}

# rest_json FILTER: the filter's raw output on REST_BODY.
rest_json() {
  printf '%s' "${REST_BODY}" | jq -r "$1"
}

# ---------------------------------------------------------------------------
# Port-forward

start_port_forward() {
  local cluster="$1"
  if [ -n "${PF_PID}" ] && kill -0 "${PF_PID}" 2>/dev/null; then
    return 0
  fi
  kc "${cluster}" -n "${E2E_NAMESPACE}" port-forward --address 127.0.0.1 "svc/${SERVER_RELEASE}" \
    "${E2E_HTTP_LOCAL_PORT}:8080" "${E2E_ADMIN_LOCAL_PORT}:9091" >>"${PF_LOG}" 2>&1 &
  PF_PID=$!
  local i=0
  while [ "${i}" -lt 30 ]; do
    if curl -s -o /dev/null --max-time 2 "${ADMIN_URL}/healthz"; then
      return 0
    fi
    kill -0 "${PF_PID}" 2>/dev/null || break
    sleep 1
    i=$((i + 1))
  done
  return 1
}

stop_port_forward() {
  if [ -n "${PF_PID}" ]; then
    kill "${PF_PID}" 2>/dev/null || true
    wait "${PF_PID}" 2>/dev/null || true
    PF_PID=""
  fi
}

# ---------------------------------------------------------------------------
# Summary and diagnostics

decolor() {
  sed $'s/\033\\[[0-9;]*m//g'
}

dump_diagnostics() {
  local cluster="$1"
  section "Pods in ${E2E_NAMESPACE} (${cluster})"
  kc "${cluster}" --request-timeout=10s -n "${E2E_NAMESPACE}" get pods -o wide 2>&1 || true
  section "Events in ${E2E_NAMESPACE} (last 30)"
  kc "${cluster}" --request-timeout=10s -n "${E2E_NAMESPACE}" get events --sort-by=.lastTimestamp 2>&1 | tail -n 30 || true
  section "gds-server logs (tail 100)"
  kc "${cluster}" --request-timeout=10s -n "${E2E_NAMESPACE}" logs -l "${SERVER_SELECTOR}" --tail=100 --all-containers --prefix 2>&1 | decolor || true
  section "Port-forward log (tail 20)"
  tail -n 20 "${PF_LOG}" 2>/dev/null || true
}

print_summary() {
  local r
  section "SUMMARY"
  for r in ${RESULTS[@]+"${RESULTS[@]}"}; do
    printf '%s\n' "${r}"
  done
  printf -- '---------------------------------------------\n'
  if [ "${FAIL_COUNT}" -eq 0 ] && [ "${ABORTED}" -eq 0 ]; then
    printf 'RESULT: PASS (%d passed, %d failed, %d skipped)\n' "${PASS_COUNT}" "${FAIL_COUNT}" "${SKIP_COUNT}"
  else
    printf 'RESULT: FAIL (%d passed, %d failed, %d skipped)\n' "${PASS_COUNT}" "${FAIL_COUNT}" "${SKIP_COUNT}"
  fi
}

# abort CLUSTER: a required step failed. Records the remaining steps as
# skipped, dumps diagnostics and exits. E2E_CLEANUP=1 still cleans up.
abort() {
  ABORTED=1
  local s
  local IFS='|'
  for s in ${REMAINING_STEPS}; do
    [ -n "${s}" ] && skip "${s}"
  done
  dump_diagnostics "$1"
  if [ "${E2E_CLEANUP}" = "1" ]; then
    cleanup
  fi
  print_summary
  exit 1
}

# step_done: marks the first remaining step as done.
step_done() {
  REMAINING_STEPS="${REMAINING_STEPS#*|}"
}

on_exit() {
  stop_port_forward
}
trap on_exit EXIT

# ---------------------------------------------------------------------------
# Setup

preflight() {
  local tool
  for tool in k3d docker kubectl helm curl jq; do
    command -v "${tool}" >/dev/null 2>&1 || die "${tool} is required"
  done
  if [ "${IMAGE_SOURCE}" = "local" ] && [ "${E2E_SKIP_BUILD}" != "1" ]; then
    command -v "${BAZEL}" >/dev/null 2>&1 || die "${BAZEL} is required to build the image; or set E2E_SKIP_BUILD=1"
  fi
  docker info >/dev/null 2>&1 || die "Docker is not reachable"
  mkdir -p "${E2E_WORK_DIR}"
  chmod 700 "${E2E_WORK_DIR}"
  : >"${PF_LOG}"
  : >"${CURL_ERR}"
  log "work dir ${E2E_WORK_DIR}, phases ${E2E_PHASES}"
}

# server_args: the helm arguments of the gds-server release.
server_args() {
  printf '%s\n' \
    --namespace "${E2E_NAMESPACE}" \
    --values "${MANIFESTS}/gds-server-values.yaml" \
    --set-string "image.repository=${E2E_IMAGE_REPOSITORY}" \
    --set-string "image.tag=${IMAGE_TAG}" \
    --set-string "image.pullPolicy=Never" \
    --set-string "region=${E2E_REGION}"
}

# install_server CLUSTER: installs or upgrades the gds-server release and
# waits until its Deployment is ready.
install_server() {
  local cluster="$1" args=() line
  while IFS= read -r line; do
    args+=("${line}")
  done < <(server_args)
  hm "${cluster}" upgrade --install "${SERVER_RELEASE}" "${SERVER_CHART}" "${args[@]}" \
    --create-namespace --wait --timeout "${E2E_HELM_TIMEOUT}" >/dev/null
}

cleanup() {
  section "Cleanup"
  stop_port_forward
  if cluster_exists "${E2E_CLUSTER}" && ! cluster_created_here "${E2E_CLUSTER}" &&
    [ ! -f "$(kubeconfig_of "${E2E_CLUSTER}")" ]; then
    k3d kubeconfig get "${E2E_CLUSTER}" >"$(kubeconfig_of "${E2E_CLUSTER}")" 2>/dev/null || true
  fi
  if cluster_exists "${E2E_CLUSTER}" && [ -s "$(kubeconfig_of "${E2E_CLUSTER}")" ]; then
    if cluster_created_here "${E2E_CLUSTER}"; then
      log "k3d cluster ${E2E_CLUSTER} was created by run.sh; deleting it removes the release"
    else
      hm "${E2E_CLUSTER}" uninstall "${SERVER_RELEASE}" --namespace "${E2E_NAMESPACE}" --wait || true
      kc "${E2E_CLUSTER}" delete namespace "${E2E_NAMESPACE}" --ignore-not-found --wait=true --timeout=120s || true
    fi
  fi
  delete_created_clusters
  rm -f "${E2E_WORK_DIR}"/kubeconfig-* "${E2E_WORK_DIR}/rest-body.json"
}

# ---------------------------------------------------------------------------
# Phase 1 checks: one cluster, memory store, REST through a port-forward.

admin_says_ok() {  # admin_says_ok PATH
  local got
  got="$(curl -s --max-time 5 "${ADMIN_URL}$1" 2>>"${CURL_ERR}")" || got=""
  LAST_OBSERVED="${got:-no answer}"
  [ "${got}" = "ok" ]
}

metrics_have() {  # metrics_have METRIC
  local got
  got="$(curl -s --max-time 5 "${ADMIN_URL}/metrics" 2>>"${CURL_ERR}")" || got=""
  LAST_OBSERVED="$(grep -m1 -- "^$1" <<<"${got}" || echo "no $1 series")"
  grep -q -- "^$1" <<<"${got}"
}

serverinfo_ok() {
  rest_expect GET /v1/serverinfo "" 200 \
    ".region == \"${E2E_REGION}\" and (.viewEpoch | length > 0)" '{region, version, viewEpoch}'
}

clustersets_empty() {
  rest_expect GET /v1/clustersets "" 200 '(.clustersets // []) | length == 0'
}

# register_cluster CLUSTER TTL: RegisterCluster; the response carries an
# ACTIVE lease with an epoch.
register_cluster() {
  local cluster="$1" ttl="$2"
  rest_expect PUT "${CS_PATH}/clusters/${cluster}" \
    "{\"region\":\"${E2E_REGION}\",\"zones\":[\"${E2E_REGION}-a\"],\"provider\":\"k3d\",\"leaseTtl\":\"${ttl}\"}" \
    200 ".cluster.cluster == \"${cluster}\" and .cluster.lease.state == \"LEASE_STATE_ACTIVE\" and (.cluster.lease.epoch | length > 0)" \
    '.cluster | {cluster, lease}'
}

clusterset_lists() {  # clusterset_lists ACTIVE_CLUSTERS
  rest_expect GET /v1/clustersets "" 200 \
    "any(.clustersets[]?; .clusterset == \"${E2E_CLUSTERSET}\" and .activeClusterCount == $1)"
}

# Pool blue is registered through the API, the way a pool operator or a
# PoolRelay registers.
POOL_ID=""
POOL_EPOCH=""

register_pool() {
  rest_expect POST "${CS_PATH}/pools" \
    "{\"pool\":{\"name\":\"blue\",\"location\":{\"cluster\":\"cluster-a\",\"region\":\"${E2E_REGION}\"},\"hardwareClass\":\"h100\",\"models\":[{\"modelId\":\"llama-3\"}],\"workerCount\":2},\"leaseTtl\":\"300s\"}" \
    200 '.pool.name == "blue" and .pool.state == "POOL_STATE_REGISTERED" and (.pool.poolId | length > 0)' \
    '.pool | {poolId, name, state, lease}' || return 1
  POOL_ID="$(rest_json .pool.poolId)"
  POOL_EPOCH="$(rest_json .pool.lease.epoch)"
}

heartbeat_pool() {
  rest_expect POST "${CS_PATH}/pools/${POOL_ID}:heartbeat" \
    "{\"epoch\":\"${POOL_EPOCH}\",\"readyWorkerCount\":2}" \
    200 '.state == "POOL_STATE_ACTIVE" and .lease.state == "LEASE_STATE_ACTIVE"' '{state, lease}'
}

# pools_are QUERY EXPECTED: the sorted "cluster/name=state" entries of
# ListPools?QUERY equal EXPECTED.
pools_are() {
  rest GET "${CS_PATH}/pools?$1"
  local got
  got="$(rest_json '[.pools[]? | .location.cluster + "/" + .name + "=" + .state] | sort | join(",")' 2>/dev/null)" || got="<not JSON>"
  LAST_OBSERVED="HTTP ${REST_STATUS} pools: ${got:-none}"
  [ "${REST_STATUS}" = "200" ] && [ "${got}" = "$2" ]
}

put_export() {  # put_export CLUSTER
  rest_expect PUT "${CS_PATH}/clusters/$1/exports/llm/frontend" "$(cat "${MANIFESTS}/frontend-export.json")" \
    200 ".export.cluster == \"$1\" and .export.spec.type == \"SERVICE_TYPE_CLUSTER_SET_IP\"" \
    '.export | {cluster, namespace, name, generation}'
}

put_endpoints() {  # put_endpoints CLUSTER ADDRESS...
  local cluster="$1" eps="" address
  shift
  for address in "$@"; do
    eps="${eps}${eps:+,}{\"addresses\":[\"${address}\"],\"conditions\":{\"ready\":true,\"serving\":true},\"zone\":\"${E2E_REGION}-a\"}"
  done
  rest_expect PUT "${CS_PATH}/clusters/${cluster}/exports/llm/frontend/endpoints" "{\"endpoints\":[${eps}]}" \
    200 ".readyEndpointCount == $#" '.'
}

# import_is EXPECTED: the merged ServiceImport llm/frontend, summarised as
# "<ports> | <cluster>:<participating>:<ready addresses> ... | conflict".
import_is() {
  rest GET "${CS_PATH}/imports/llm/frontend?includeEndpoints=true"
  local got
  # $i and $c are jq variables.
  # shellcheck disable=SC2016
  got="$(rest_json '.serviceImport as $i
    | ([$i.ports[]? | "\(.name)/\(.port)"] | join(","))
      + " |"
      + ([$i.clusters[]? as $c
          | " " + $c.cluster + ":" + (if $c.participating then "in" else "out" end) + ":"
            + ([$i.endpoints[]? | select(.cluster == $c.cluster) | .endpoints[]? | select(.conditions.ready) | .addresses[0]] | sort | join("+"))]
         | join(""))
      + " | " + ($i.conflict.reason // "no-conflict")' 2>/dev/null)" || got="<not JSON>"
  LAST_OBSERVED="HTTP ${REST_STATUS} ${got}"
  [ "${REST_STATUS}" = "200" ] && [ "${got}" = "$1" ]
}

cluster_lease_is() {  # cluster_lease_is CLUSTER STATE
  rest_expect GET "${CS_PATH}/clusters/$1" "" 200 ".cluster.lease.state == \"$2\"" '.cluster.lease'
}

clusters_are() {  # clusters_are QUERY EXPECTED: the cluster names of ListClusters
  rest GET "${CS_PATH}/clusters?$1"
  local got
  got="$(rest_json '[.clusters[]?.cluster] | sort | join(",")' 2>/dev/null)" || got="<not JSON>"
  LAST_OBSERVED="HTTP ${REST_STATUS} clusters: ${got:-none}"
  [ "${REST_STATUS}" = "200" ] && [ "${got}" = "$2" ]
}

CLUSTER_EPOCH=""

heartbeat_cluster_renews() {  # heartbeat_cluster_renews CLUSTER
  rest GET "${CS_PATH}/clusters/$1"
  CLUSTER_EPOCH="$(rest_json .cluster.lease.epoch)"
  local before
  before="$(rest_json .cluster.lease.renewedAt)"
  sleep 1
  rest_expect POST "${CS_PATH}/clusters/$1:heartbeat" "{\"epoch\":\"${CLUSTER_EPOCH}\"}" \
    200 ".lease.state == \"LEASE_STATE_ACTIVE\" and .lease.epoch == \"${CLUSTER_EPOCH}\" and .lease.renewedAt > \"${before}\"" \
    '.lease'
}

heartbeat_rejected() {  # heartbeat_rejected CLUSTER EPOCH STATUS REASON
  rest_expect POST "${CS_PATH}/clusters/$1:heartbeat" "{\"epoch\":\"$2\"}" \
    "$3" "any(.details[]?; .reason == \"$4\" and .domain == \"gds.nvidia.com\")" '{code, message}'
}

deregister_cluster() {  # deregister_cluster CLUSTER
  rest_expect DELETE "${CS_PATH}/clusters/$1" "" 200 '. == {}'
}

update_pool_draining() {
  rest_expect PATCH "${CS_PATH}/pools/${POOL_ID}" \
    '{"pool":{"desiredState":"POOL_STATE_DRAINING"},"updateMask":"desiredState"}' \
    200 '.pool.desiredState == "POOL_STATE_DRAINING" and .pool.state == "POOL_STATE_DRAINING"' \
    '.pool | {desiredState, state}'
}

# The pools watch: curl -N for WATCH_SECONDS, each line prefixed with the
# second it arrived at. The DRAINING update is sent while it runs.
readonly WATCH_SECONDS=12
WATCH_FILE="${E2E_WORK_DIR}/pools-watch.ndjson"

record_pools_watch() {
  local start line
  start="$(date +%s)"
  : >"${WATCH_FILE}"
  (
    curl -sN --max-time "${WATCH_SECONDS}" "${REST_URL}${CS_PATH}/pools:watch?model=llama-3" 2>>"${CURL_ERR}" |
      while IFS= read -r line; do
        printf '%s %s\n' "$(($(date +%s) - start))" "${line}"
      done >"${WATCH_FILE}"
  ) &
  local watch_pid=$!
  sleep 2
  once "UpdatePool sets blue DRAINING while the watch runs" update_pool_draining || true
  wait "${watch_pid}" 2>/dev/null || true
  log "watch: $(wc -l <"${WATCH_FILE}" | tr -d ' ') events in ${WATCH_SECONDS}s"
}

# watch_types: one "<second> <type> <cluster>/<name>=<state>" line per event.
watch_types() {
  local second json
  while read -r second json; do
    printf '%s %s\n' "${second}" "$(printf '%s' "${json}" | jq -r '.result | .type + " " + ((.pool.location.cluster // "") + "/" + (.pool.name // "") + "=" + (.pool.state // ""))' 2>/dev/null || echo "invalid-json")"
  done <"${WATCH_FILE}"
}

watch_snapshot_ok() {
  local events first end_line upserts
  events="$(watch_types)"
  first="$(awk 'NR == 1 {print $2}' <<<"${events}")"
  end_line="$(awk '$2 == "EVENT_TYPE_SNAPSHOT_END" && !found {print NR; found = 1}' <<<"${events}")"
  upserts="$(printf '%s\n' "${events}" | awk -v end="${end_line:-0}" 'NR > 1 && NR < end && $2 == "EVENT_TYPE_UPSERT" {print $3}' | sort | tr '\n' ',')"
  LAST_OBSERVED="first ${first:-none}, SNAPSHOT_END at event ${end_line:-none}, snapshot ${upserts:-empty}"
  [ "${first}" = "EVENT_TYPE_SNAPSHOT_START" ] && [ -n "${end_line}" ] &&
    grep -q 'cluster-a/blue=POOL_STATE_ACTIVE' <<<"${upserts}"
}

watch_delta_ok() {
  local events
  events="$(watch_types)"
  LAST_OBSERVED="$(printf '%s\n' "${events}" | awk '$2 != "EVENT_TYPE_BOOKMARK"' | tr '\n' ';')"
  printf '%s\n' "${events}" |
    awk '$2 == "EVENT_TYPE_SNAPSHOT_END" {after = 1; next} after && $2 == "EVENT_TYPE_UPSERT" && $3 == "cluster-a/blue=POOL_STATE_DRAINING" {found = 1} END {exit !found}'
}

watch_bookmark_ok() {  # watch_bookmark_ok MAX_SECONDS
  local events
  events="$(watch_types)"
  LAST_OBSERVED="bookmarks at second(s): $(printf '%s\n' "${events}" | awk '$2 == "EVENT_TYPE_BOOKMARK" {printf "%s ", $1}')"
  printf '%s\n' "${events}" |
    awk -v max="$1" '$2 == "EVENT_TYPE_SNAPSHOT_END" {after = 1; next} after && $2 == "EVENT_TYPE_BOOKMARK" && $1 <= max {found = 1} END {exit !found}'
}

run_phase_1() {
  local cluster="${E2E_CLUSTER}" T="${E2E_TIMEOUT}"
  if [ "${E2E_DEPLOY_ONLY}" = "1" ]; then
    REMAINING_STEPS="phase 1 setup|phase 1 server|"
  else
    REMAINING_STEPS="phase 1 setup|phase 1 server|phase 1 clusters|phase 1 pools|phase 1 service export and import|phase 1 pools watch|phase 1 heartbeats and leases|phase 1 deregister|"
  fi

  section "Phase 1 setup: k3d cluster ${cluster}, gds-server with the memory store"
  if ensure_cluster "${cluster}"; then
    pass "k3d cluster ${cluster} has a Ready node; kubeconfig $(kubeconfig_of "${cluster}")"
  else
    fail "k3d cluster ${cluster}" "${LAST_OBSERVED}"
    abort "${cluster}"
  fi
  build_image
  import_image "${cluster}"
  pass "image ${E2E_IMAGE_REPOSITORY}:${IMAGE_TAG} imported into ${cluster}"
  if install_server "${cluster}"; then
    pass "helm upgrade --install ${SERVER_RELEASE}: Deployment ready"
  else
    fail "helm upgrade --install ${SERVER_RELEASE}"
    abort "${cluster}"
  fi
  if ! start_port_forward "${cluster}"; then
    fail "port-forward to svc/${SERVER_RELEASE}" "$(tail -n 5 "${PF_LOG}" | tr '\n' ' ')"
    abort "${cluster}"
  fi
  pass "port-forward REST 127.0.0.1:${E2E_HTTP_LOCAL_PORT}, admin 127.0.0.1:${E2E_ADMIN_LOCAL_PORT}"
  step_done

  section "Phase 1: server"
  expect "admin /readyz is ok" "${T}" admin_says_ok /readyz || abort "${cluster}"
  expect "admin /healthz is ok" "${T}" admin_says_ok /healthz || true
  expect "/metrics has nvcf_gds_build_info" "${T}" metrics_have nvcf_gds_build_info || true
  expect "GET /v1/serverinfo reports region ${E2E_REGION}" "${T}" serverinfo_ok || true
  expect "GET /v1/clustersets is empty" "${T}" clustersets_empty || true
  step_done

  if [ "${E2E_DEPLOY_ONLY}" = "1" ]; then
    return 0
  fi

  section "Phase 1: clusters"
  once "PUT cluster-a registers it with an ACTIVE lease" register_cluster cluster-a 300s || abort "${cluster}"
  once "PUT cluster-b registers it with an ACTIVE lease" register_cluster cluster-b 300s || abort "${cluster}"
  expect "GET /v1/clustersets lists ${E2E_CLUSTERSET} with 2 active clusters" "${T}" clusterset_lists 2 || true
  step_done

  section "Phase 1: pools"
  once "POST pool blue registers it (REGISTERED, with a lease)" register_pool || abort "${cluster}"
  once "pool heartbeat with the lease epoch turns blue ACTIVE" heartbeat_pool || true
  expect "GET pools?model=llama-3&region=${E2E_REGION} lists blue" "${T}" \
    pools_are "model=llama-3&region=${E2E_REGION}" "cluster-a/blue=POOL_STATE_ACTIVE" || true
  expect "GET pools?model=mistral-7b lists nothing" "${T}" pools_are "model=mistral-7b" "" || true
  expect "GET pools?region=elsewhere lists nothing" "${T}" pools_are "region=elsewhere" "" || true
  step_done

  section "Phase 1: service export and import"
  once "PUT export llm/frontend in cluster-a" put_export cluster-a || true
  once "PUT export llm/frontend in cluster-b" put_export cluster-b || true
  once "PUT endpoints of llm/frontend in cluster-a (2 ready)" put_endpoints cluster-a 10.10.0.5 10.10.0.6 || true
  once "PUT endpoints of llm/frontend in cluster-b (1 ready)" put_endpoints cluster-b 10.20.0.7 || true
  expect "GET import llm/frontend merges both clusters" "${T}" \
    import_is "http/8000 | cluster-a:in:10.10.0.5+10.10.0.6 cluster-b:in:10.20.0.7 | no-conflict" || true
  expect "labelled exports derive pool green in both clusters" "${T}" \
    pools_are "model=llama-3" "cluster-a/blue=POOL_STATE_ACTIVE,cluster-a/green=POOL_STATE_ACTIVE,cluster-b/green=POOL_STATE_ACTIVE" || true
  step_done

  section "Phase 1: pools watch"
  record_pools_watch
  once "pools:watch sends SNAPSHOT_START, the pools, then SNAPSHOT_END" watch_snapshot_ok || true
  once "pools:watch delivers the DRAINING update after the snapshot" watch_delta_ok || true
  once "pools:watch sends a BOOKMARK within 10s" watch_bookmark_ok 10 || true
  step_done

  section "Phase 1: heartbeats and leases"
  once "cluster-a heartbeat with its epoch renews the lease" heartbeat_cluster_renews cluster-a || true
  once "cluster-a heartbeat with a stale epoch is 409 LEASE_EPOCH_MISMATCH" \
    heartbeat_rejected cluster-a 1 409 LEASE_EPOCH_MISMATCH || true
  once "heartbeat of an unregistered cluster is 404 CLUSTER_NOT_REGISTERED" \
    heartbeat_rejected cluster-z 1 404 CLUSTER_NOT_REGISTERED || true
  once "PUT cluster-c registers it with a 10s lease" register_cluster cluster-c 10s || true
  once "PUT export llm/frontend in cluster-c" put_export cluster-c || true
  once "PUT endpoints of llm/frontend in cluster-c (1 ready)" put_endpoints cluster-c 10.30.0.8 || true
  expect "cluster-c endpoints join the import" "${T}" \
    import_is "http/8000 | cluster-a:in:10.10.0.5+10.10.0.6 cluster-b:in:10.20.0.7 cluster-c:in:10.30.0.8 | no-conflict" || true
  log "cluster-c sends no heartbeat; its lease expires after 10s plus the 2s skew"
  expect "cluster-c lease turns EXPIRED without heartbeats" 40 cluster_lease_is cluster-c LEASE_STATE_EXPIRED || true
  expect "expired cluster-c leaves the import, its endpoints hidden" "${T}" \
    import_is "http/8000 | cluster-a:in:10.10.0.5+10.10.0.6 cluster-b:in:10.20.0.7 cluster-c:out: | no-conflict" || true
  expect "pool green of expired cluster-c turns UNHEALTHY" "${T}" \
    pools_are "cluster=cluster-c" "cluster-c/green=POOL_STATE_UNHEALTHY" || true
  expect "ListClusters omits expired cluster-c" "${T}" clusters_are "" "cluster-a,cluster-b" || true
  expect "ListClusters?includeExpired=true keeps cluster-c" "${T}" clusters_are "includeExpired=true" "cluster-a,cluster-b,cluster-c" || true
  step_done

  section "Phase 1: deregister"
  once "DELETE cluster-b deregisters it" deregister_cluster cluster-b || true
  expect "import llm/frontend no longer has cluster-b's endpoints" "${T}" \
    import_is "http/8000 | cluster-a:in:10.10.0.5+10.10.0.6 cluster-c:out: | no-conflict" || true
  expect "pool green of cluster-b is gone from ListPools" "${T}" \
    pools_are "model=llama-3" "cluster-a/blue=POOL_STATE_DRAINING,cluster-a/green=POOL_STATE_ACTIVE,cluster-c/green=POOL_STATE_UNHEALTHY" || true
  expect "ListClusters?includeExpired=true no longer has cluster-b" "${T}" \
    clusters_are "includeExpired=true" "cluster-a,cluster-c" || true
  step_done
}

# ---------------------------------------------------------------------------
# Phase 2 (three clusters with the MCS controller) adds run_phase_2 here and
# to the case in run_phases. It reuses ensure_cluster with per-cluster
# --k3s-arg CIDRs and a shared --network, build_image and import_image.

run_phases() {
  local phase
  for phase in $(printf '%s' "${E2E_PHASES}" | tr ',' ' '); do
    case "${phase}" in
      1) run_phase_1 ;;
      *) die "unknown phase '${phase}'; available: 1" ;;
    esac
  done
}

# ---------------------------------------------------------------------------
# Dry run

# E2E_DRY_RUN=1: renders the chart with the phase 1 values and prints the
# commands a run would use. It touches no cluster and builds nothing.
dry_run() {
  command -v helm >/dev/null 2>&1 || die "helm is required"
  mkdir -p "${E2E_WORK_DIR}"
  chmod 700 "${E2E_WORK_DIR}"
  section "Dry run (E2E_DRY_RUN=1): no cluster access"
  IMAGE_TAG="e2e-dryrun"
  local args=() line manifest="${E2E_WORK_DIR}/dry-run-${SERVER_RELEASE}.yaml"
  while IFS= read -r line; do
    args+=("${line}")
  done < <(server_args)
  helm template "${SERVER_RELEASE}" "${SERVER_CHART}" "${args[@]}" >"${manifest}" ||
    die "helm template ${SERVER_RELEASE} failed"
  printf 'Rendered %s\n\nA run with these settings:\n' "${manifest}"
  printf '  k3d cluster create %s ... (only when it does not exist and E2E_CREATE_CLUSTERS=1)\n' "${E2E_CLUSTER}"
  printf '  (cd %s && %s run %s %s)\n' "${REPO_ROOT}" "${BAZEL}" "${BAZEL_FLAGS}" "${IMAGE_TARGET}"
  printf '  docker tag %s %s:e2e-<image id>\n' "${BAZEL_IMAGE}" "${E2E_IMAGE_REPOSITORY}"
  printf '  k3d image import -c %s %s:e2e-<image id>\n' "${E2E_CLUSTER}" "${E2E_IMAGE_REPOSITORY}"
  printf '  helm upgrade --install %s %s' "${SERVER_RELEASE}" "${SERVER_CHART}"
  printf ' %q' "${args[@]}"
  printf ' --create-namespace --wait\n'
  printf '\nImage and arguments in the rendered Deployment:\n'
  grep -E -- '^ *image: |^ *- "--' "${manifest}" || true
}

# ---------------------------------------------------------------------------
# Main

print_access() {
  section "Deployed"
  cat <<EOF
gds-server stays installed in k3d cluster ${E2E_CLUSTER}, namespace ${E2E_NAMESPACE}.

  export KUBECONFIG=$(kubeconfig_of "${E2E_CLUSTER}")
  kubectl --context $(context_of "${E2E_CLUSTER}") -n ${E2E_NAMESPACE} port-forward svc/${SERVER_RELEASE} ${E2E_HTTP_LOCAL_PORT}:8080 ${E2E_ADMIN_LOCAL_PORT}:9091

  curl -s ${ADMIN_URL}/readyz
  curl -s ${REST_URL}/v1/clustersets
  curl -sN ${REST_URL}${CS_PATH}/pools:watch

Remove everything with E2E_CLEANUP=1 $0 (deletes only clusters run.sh created).
EOF
}

main() {
  if [ "${E2E_DRY_RUN}" = "1" ]; then
    dry_run
    return
  fi
  if [ "${E2E_DEPLOY_ONLY}" = "1" ] && [ "${E2E_CLEANUP}" = "1" ]; then
    die "E2E_DEPLOY_ONLY=1 leaves everything installed and cannot be combined with E2E_CLEANUP=1"
  fi
  check_cluster_name "${E2E_CLUSTER}"
  if [ "${E2E_CLEANUP_ONLY}" = "1" ]; then
    mkdir -p "${E2E_WORK_DIR}"
    cleanup
    return
  fi
  preflight
  run_phases

  if [ "${FAIL_COUNT}" -gt 0 ]; then
    dump_diagnostics "${E2E_CLUSTER}"
  fi
  if [ "${E2E_CLEANUP}" = "1" ]; then
    cleanup
  else
    print_access
  fi
  print_summary
  [ "${FAIL_COUNT}" -eq 0 ]
}

main "$@"
