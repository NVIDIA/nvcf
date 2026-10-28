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
# Phase 1: one cluster (the hub), gds-server from the chart with the memory
# store and one replica, driven over REST through a port-forward, pool agent
# registrations included.
# Phase 2: the hub plus clusters a and b on one Docker network, with distinct
# pod and service CIDRs and static routes between the node containers, and
# gds-mcs-controller from its chart in a and b: export, import, pools and
# their label-derived agents, a controller restart and unexport.
# Phase 3: Stargate (llm-request-router chart) exporting itself from a, two
# replicas in direct router mode by default, and a Pylon in b that finds them
# only through GDS, registers with each pod once and is the PYLON agent of its
# pool.
#
# The script is compatible with bash 3.2 (the macOS default). It talks to its
# clusters through its own kubeconfig files in the work directory, so it never
# reads, writes or switches the default kubeconfig or its current context. It
# only creates k3d clusters and Docker networks whose names start with
# gds-e2e, records them in the work directory, and with E2E_CLEANUP=1 deletes
# only the recorded ones.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
MANIFESTS="${SCRIPT_DIR}/manifests"

# Phases to run, separated by spaces or commas.
E2E_PHASES="${E2E_PHASES:-1 2 3}"
# The hub cluster: gds-server for every phase. Phases 2 and 3 add
# <E2E_CLUSTER>-a and <E2E_CLUSTER>-b on the Docker network <E2E_CLUSTER>-net.
E2E_CLUSTER="${E2E_CLUSTER:-gds-e2e}"
E2E_CLUSTER_A="${E2E_CLUSTER_A:-${E2E_CLUSTER}-a}"
E2E_CLUSTER_B="${E2E_CLUSTER_B:-${E2E_CLUSTER}-b}"
E2E_NETWORK="${E2E_NETWORK:-${E2E_CLUSTER}-net}"
E2E_CREATE_CLUSTERS="${E2E_CREATE_CLUSTERS:-1}"
E2E_CROSS_CLUSTER_ROUTES="${E2E_CROSS_CLUSTER_ROUTES:-1}"
E2E_CLEANUP="${E2E_CLEANUP:-0}"
E2E_CLEANUP_ONLY="${E2E_CLEANUP_ONLY:-0}"
E2E_DEPLOY_ONLY="${E2E_DEPLOY_ONLY:-0}"
E2E_DRY_RUN="${E2E_DRY_RUN:-0}"
E2E_BUILD_ONLY="${E2E_BUILD_ONLY:-0}"
E2E_SKIP_BUILD="${E2E_SKIP_BUILD:-0}"
E2E_TIMEOUT="${E2E_TIMEOUT:-60}"
E2E_HELM_TIMEOUT="${E2E_HELM_TIMEOUT:-5m}"
E2E_NAMESPACE="${E2E_NAMESPACE:-global-discovery}"
E2E_CONTROLLER_NAMESPACE="${E2E_CONTROLLER_NAMESPACE:-gds-system}"
E2E_DEMO_NAMESPACE="${E2E_DEMO_NAMESPACE:-gds-e2e-demo}"
E2E_ROUTER_NAMESPACE="${E2E_ROUTER_NAMESPACE:-gds-e2e-router}"
E2E_PYLON_NAMESPACE="${E2E_PYLON_NAMESPACE:-gds-e2e-pylon}"
E2E_REGION="${E2E_REGION:-e2e-local}"
E2E_CLUSTERSET="${E2E_CLUSTERSET:-e2e}"
E2E_MCS_CLUSTERSET="${E2E_MCS_CLUSTERSET:-e2e-mcs}"
E2E_CLUSTER_ID_A="${E2E_CLUSTER_ID_A:-a}"
E2E_CLUSTER_ID_B="${E2E_CLUSTER_ID_B:-b}"
E2E_POD_CIDR_A="${E2E_POD_CIDR_A:-10.44.0.0/16}"
E2E_SERVICE_CIDR_A="${E2E_SERVICE_CIDR_A:-10.45.0.0/16}"
E2E_POD_CIDR_B="${E2E_POD_CIDR_B:-10.46.0.0/16}"
E2E_SERVICE_CIDR_B="${E2E_SERVICE_CIDR_B:-10.47.0.0/16}"
E2E_HTTP_LOCAL_PORT="${E2E_HTTP_LOCAL_PORT:-28080}"
E2E_ADMIN_LOCAL_PORT="${E2E_ADMIN_LOCAL_PORT:-29091}"
E2E_ROUTER_LOCAL_PORT="${E2E_ROUTER_LOCAL_PORT:-28000}"
# The phase 3 router in a: direct (two Stargate replicas of a Deployment in
# direct router mode; the Pylon dials each pod IP) or single (one replica;
# the reverse tunnel dials E2E_ROUTER_HOST).
E2E_ROUTER_MODE="${E2E_ROUTER_MODE:-direct}"
# With E2E_ROUTER_MODE=single, the name Stargate in a advertises for the
# reverse tunnel. Pylons of other clusters must resolve it (design 4.5); the
# Pylon of phase 3 maps it to the Stargate pod IP with a hostAliases entry.
E2E_ROUTER_HOST="${E2E_ROUTER_HOST:-llm-router.${E2E_CLUSTER_ID_A:-a}.gds-e2e.test}"
E2E_IMAGE_REPOSITORY="${E2E_IMAGE_REPOSITORY:-gds-e2e/gds-server}"
E2E_CONTROLLER_IMAGE_REPOSITORY="${E2E_CONTROLLER_IMAGE_REPOSITORY:-gds-e2e/gds-mcs-controller}"
E2E_STARGATE_IMAGE_REPOSITORY="${E2E_STARGATE_IMAGE_REPOSITORY:-gds-e2e/stargate}"
E2E_PYLON_IMAGE_REPOSITORY="${E2E_PYLON_IMAGE_REPOSITORY:-gds-e2e/pylon}"
E2E_BUSYBOX_IMAGE="${E2E_BUSYBOX_IMAGE:-busybox:1.37}"
E2E_SAMPLE_IMAGE="${E2E_SAMPLE_IMAGE:-openai-compatible-sample:e2e}"
E2E_WORK_DIR="${E2E_WORK_DIR:-${TMPDIR:-/tmp}/global-discovery-k3d-e2e}"
E2E_WORK_DIR="${E2E_WORK_DIR%/}"
# Where the images come from. local builds them with Bazel from this
# checkout and imports them into k3d. No published image exists yet.
IMAGE_SOURCE="${IMAGE_SOURCE:-local}"
BAZEL="${BAZEL:-bazel}"
BAZEL_FLAGS="${BAZEL_FLAGS:-}"

readonly SERVER_RELEASE="global-discovery"
readonly SERVER_CHART="${REPO_ROOT}/deploy/helm/global-discovery/global-discovery"
readonly SERVER_SELECTOR="app.kubernetes.io/instance=${SERVER_RELEASE}"
readonly CONTROLLER_RELEASE="gds-mcs-controller"
readonly CONTROLLER_CHART="${REPO_ROOT}/deploy/helm/global-discovery-mcs-controller/global-discovery-mcs-controller"
readonly CONTROLLER_SELECTOR="app.kubernetes.io/instance=${CONTROLLER_RELEASE}"
readonly ROUTER_RELEASE="llm-request-router"
readonly ROUTER_CHART="${REPO_ROOT}/deploy/helm/llm-request-router/llm-request-router"
readonly ROUTER_SELECTOR="app.kubernetes.io/instance=${ROUTER_RELEASE}"
# The chart's headless Service, which names the Stargate pods in direct mode
# (manifests/router-direct-values.yaml).
readonly ROUTER_HEADLESS="llm-request-router-headless"
readonly SAMPLE_DIR="${REPO_ROOT}/examples/function-samples/openai-compatible-sample"
readonly SERVER_TARGET="//src/control-plane-services/global-discovery:image"
readonly CONTROLLER_TARGET="//src/control-plane-services/global-discovery:controller_image"
readonly STARGATE_TARGET="//src/libraries/rust/stargate/crates/stargate:image"
readonly PYLON_TARGET="//src/libraries/rust/stargate/crates/pylon:image"
readonly LEADER_LEASE="gds-mcs-controller.gds.nvidia.com"
readonly CREATED_CLUSTERS_FILE="${E2E_WORK_DIR}/created-clusters"
readonly CREATED_NETWORKS_FILE="${E2E_WORK_DIR}/created-networks"
readonly CONNECTED_NETWORKS_FILE="${E2E_WORK_DIR}/connected-networks"
readonly ROUTES_FILE="${E2E_WORK_DIR}/routes"
readonly CURL_ERR="${E2E_WORK_DIR}/curl.err"
readonly PF_LOG="${E2E_WORK_DIR}/port-forward.log"

REST_URL="http://127.0.0.1:${E2E_HTTP_LOCAL_PORT}"
ADMIN_URL="http://127.0.0.1:${E2E_ADMIN_LOCAL_PORT}"
CS_PATH="/v1/clustersets/${E2E_CLUSTERSET}"
PF_PID=""
ROUTER_PF_PID=""
REST_STATUS=""
REST_BODY=""
# Image tags of this run, e2e-<image id>, set by build_images.
IMAGE_TAG=""
CONTROLLER_IMAGE_TAG=""
STARGATE_IMAGE_TAG=""
PYLON_IMAGE_TAG=""
# Set by the phase 2 setup: the GDS gRPC URL the other clusters dial, and
# whether pod IPs are routed between a and b.
GDS_GRPC_URL=""
ROUTES_OK=0
HUB_READY=0
MCS_READY=0

RESULTS=()
PASS_COUNT=0
FAIL_COUNT=0
SKIP_COUNT=0
LAST_OBSERVED=""
ABORTED=0
# Remaining step names, recorded as skipped when a required step fails.
REMAINING_STEPS=""
# Phases after the running one, recorded as skipped on an abort.
LATER_PHASES=""

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
  log "SKIP  $1"
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

# kc_exec CLUSTER ARGS...: kc that replaces the shell with kubectl, for
# background jobs: after "kc_exec ... &", $! is kubectl's PID, so killing it
# stops kubectl instead of leaving it running without its subshell.
kc_exec() {
  local cluster="$1"
  shift
  exec kubectl --kubeconfig "$(kubeconfig_of "${cluster}")" --context "$(context_of "${cluster}")" "$@"
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

check_network_name() {
  case "$1" in
    gds-e2e-*) ;;
    *) die "Docker network name '$1' must start with gds-e2e-; run.sh never touches other networks" ;;
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

# node_containers CLUSTER: the Docker containers of the cluster's k3s nodes.
node_containers() {
  docker ps --filter "label=k3d.cluster=$1" --format '{{.Names}} {{.Label "k3d.role"}}' 2>/dev/null |
    awk '$2 == "server" || $2 == "agent" {print $1}' | sort
}

# node_ip CONTAINER: the container's address on E2E_NETWORK.
node_ip() {
  docker inspect -f "{{with index .NetworkSettings.Networks \"${E2E_NETWORK}\"}}{{.IPAddress}}{{end}}" "$1" 2>/dev/null
}

# ensure_network: the Docker network the phase 2 clusters share. Created and
# recorded when missing; k3d then uses it without owning it.
ensure_network() {
  check_network_name "${E2E_NETWORK}"
  if docker network inspect "${E2E_NETWORK}" >/dev/null 2>&1; then
    return 0
  fi
  [ "${E2E_CREATE_CLUSTERS}" = "1" ] || die "Docker network ${E2E_NETWORK} does not exist; set E2E_CREATE_CLUSTERS=1"
  log "creating Docker network ${E2E_NETWORK}"
  printf '%s\n' "${E2E_NETWORK}" >>"${CREATED_NETWORKS_FILE}"
  docker network create "${E2E_NETWORK}" >/dev/null || die "docker network create ${E2E_NETWORK} failed"
}

# attach_network CLUSTER: connects the node containers of a reused cluster to
# E2E_NETWORK, so the other clusters reach its NodePorts. Recorded for
# cleanup.
attach_network() {
  local container
  for container in $(node_containers "$1"); do
    [ -n "$(node_ip "${container}")" ] && continue
    log "connecting ${container} to Docker network ${E2E_NETWORK}"
    docker network connect "${E2E_NETWORK}" "${container}" || return 1
    printf '%s %s\n' "${E2E_NETWORK}" "${container}" >>"${CONNECTED_NETWORKS_FILE}"
  done
}

# ensure_cluster NAME [K3D CREATE ARGS...]: reuses k3d cluster NAME or, with
# E2E_CREATE_CLUSTERS=1, creates it on E2E_NETWORK without touching the
# default kubeconfig. Then writes the cluster's kubeconfig into the work
# directory and waits for a Ready node. Returns 1 with LAST_OBSERVED set when
# the cluster is unusable.
ensure_cluster() {
  local name="$1"
  shift
  check_cluster_name "${name}"
  ensure_network
  if cluster_exists "${name}"; then
    log "reusing k3d cluster ${name}"
    if ! attach_network "${name}"; then
      LAST_OBSERVED="cannot connect the nodes of ${name} to Docker network ${E2E_NETWORK}"
      return 1
    fi
  else
    [ "${E2E_CREATE_CLUSTERS}" = "1" ] ||
      die "k3d cluster ${name} does not exist; create it with 'make cluster' or set E2E_CREATE_CLUSTERS=1"
    log "creating k3d cluster ${name}"
    # Recorded before the create, so a half-created cluster is cleaned up too.
    printf '%s\n' "${name}" >>"${CREATED_CLUSTERS_FILE}"
    if ! k3d cluster create "${name}" \
      --servers 1 --agents 0 --no-lb \
      --network "${E2E_NETWORK}" \
      --k3s-arg '--disable=traefik@server:*' \
      --kubeconfig-update-default=false \
      --kubeconfig-switch-context=false \
      --wait --timeout 180s \
      ${1+"$@"} >"${E2E_WORK_DIR}/k3d-create-${name}.log" 2>&1; then
      LAST_OBSERVED="k3d cluster create ${name} failed: $(tail -n 3 "${E2E_WORK_DIR}/k3d-create-${name}.log" | decolor | tr '\n' ' ') $(node_start_hint "${name}")"
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
      # k3d cluster delete rewrites the kubeconfig it loads; point it at a
      # scratch file so the default kubeconfig stays untouched.
      KUBECONFIG="${E2E_WORK_DIR}/k3d-delete-kubeconfig" k3d cluster delete "${name}" \
        >"${E2E_WORK_DIR}/k3d-delete-${name}.log" 2>&1 || log "k3d cluster delete ${name} failed; see ${E2E_WORK_DIR}/k3d-delete-${name}.log"
    fi
  done <"${CREATED_CLUSTERS_FILE}"
  rm -f "${CREATED_CLUSTERS_FILE}"
}

# delete_created_networks: disconnects the reused node containers run.sh
# connected, then deletes the networks it created once nothing uses them.
delete_created_networks() {
  local network container
  if [ -f "${CONNECTED_NETWORKS_FILE}" ]; then
    while read -r network container; do
      [ -n "${container}" ] || continue
      check_network_name "${network}"
      docker network disconnect "${network}" "${container}" >/dev/null 2>&1 || true
    done <"${CONNECTED_NETWORKS_FILE}"
    rm -f "${CONNECTED_NETWORKS_FILE}"
  fi
  [ -f "${CREATED_NETWORKS_FILE}" ] || return 0
  while IFS= read -r network; do
    [ -n "${network}" ] || continue
    check_network_name "${network}"
    if docker network inspect "${network}" >/dev/null 2>&1; then
      if [ "$(docker network inspect -f '{{len .Containers}}' "${network}" 2>/dev/null)" = "0" ]; then
        log "deleting Docker network ${network} (created by run.sh)"
        docker network rm "${network}" >/dev/null || true
      else
        log "Docker network ${network} still has containers; not deleted"
      fi
    fi
  done <"${CREATED_NETWORKS_FILE}"
  rm -f "${CREATED_NETWORKS_FILE}"
}

# route_pod_cidrs FROM TO: on every node container of cluster FROM, a static
# route to the pod CIDR of every node of cluster TO through that node's
# address on E2E_NETWORK. Recorded, so cleanup removes them from reused
# clusters.
route_pod_cidrs() {
  local from="$1" to="$2" target src cidr via
  for target in $(node_containers "${to}"); do
    cidr="$(kc "${to}" get node "${target}" -o jsonpath='{.spec.podCIDR}' 2>/dev/null)" || cidr=""
    via="$(node_ip "${target}")"
    if [ -z "${cidr}" ] || [ -z "${via}" ]; then
      LAST_OBSERVED="node ${target} of ${to} has no pod CIDR or no address on ${E2E_NETWORK}"
      return 1
    fi
    for src in $(node_containers "${from}"); do
      if ! docker exec "${src}" ip route replace "${cidr}" via "${via}" 2>"${E2E_WORK_DIR}/route.err"; then
        LAST_OBSERVED="ip route replace ${cidr} via ${via} in ${src} failed: $(tr '\n' ' ' <"${E2E_WORK_DIR}/route.err")"
        return 1
      fi
      printf '%s %s\n' "${src}" "${cidr}" >>"${ROUTES_FILE}"
    done
  done
}

# remove_routes: deletes the routes run.sh added to node containers that
# still exist.
remove_routes() {
  local container cidr
  [ -f "${ROUTES_FILE}" ] || return 0
  while read -r container cidr; do
    [ -n "${cidr}" ] || continue
    docker exec "${container}" ip route del "${cidr}" >/dev/null 2>&1 || true
  done <"${ROUTES_FILE}"
  rm -f "${ROUTES_FILE}"
}

# ---------------------------------------------------------------------------
# Images

# load_bazel_image TARGET REPOSITORY ENTRYPOINT: builds TARGET.tar with Bazel
# (unless E2E_SKIP_BUILD=1), loads it into Docker as REPOSITORY:e2e-<image
# id> and REPOSITORY:e2e-latest, checks that its entrypoint is ENTRYPOINT,
# and prints the e2e-<image id> tag on stdout (logs go to stderr). The tarball names the shared tag of the
# Bazel load target (<package>:latest); run.sh rewrites it before loading, so
# concurrent builds of other images in the same package cannot swap them.
# With E2E_SKIP_BUILD=1 it reuses REPOSITORY:e2e-latest.
load_bazel_image() {
  local target="$1" repo="$2" entrypoint="$3" id tarball dir got
  if [ "${E2E_SKIP_BUILD}" = "1" ]; then
    id="$(docker image inspect --format '{{.Id}}' "${repo}:e2e-latest" 2>/dev/null)" ||
      die "image ${repo}:e2e-latest is not in Docker; run without E2E_SKIP_BUILD=1 or with E2E_BUILD_ONLY=1 first"
  else
    log "bazel build ${target}.tar" >&2
    # BAZEL_FLAGS is a list of flags.
    # shellcheck disable=SC2086
    (cd "${REPO_ROOT}" && "${BAZEL}" build ${BAZEL_FLAGS} "${target}.tar" >"${E2E_WORK_DIR}/bazel-build.log" 2>&1) ||
      { tail -n 30 "${E2E_WORK_DIR}/bazel-build.log" >&2; die "bazel build ${target}.tar failed; log ${E2E_WORK_DIR}/bazel-build.log"; }
    # shellcheck disable=SC2086
    tarball="$(cd "${REPO_ROOT}" && "${BAZEL}" cquery ${BAZEL_FLAGS} --output=files "${target}.tar" 2>/dev/null | tail -n 1)"
    if [ -z "${tarball}" ] || [ ! -f "${REPO_ROOT}/${tarball}" ]; then
      die "bazel cquery found no tarball for ${target}.tar"
    fi
    dir="${E2E_WORK_DIR}/image-load"
    chmod -R u+w "${dir}" 2>/dev/null || true
    rm -rf "${dir}"
    mkdir -p "${dir}/root"
    tar -xf "${REPO_ROOT}/${tarball}" -C "${dir}/root"
    chmod -R u+w "${dir}"
    jq --arg tag "${repo}:e2e-loading" '.[0].RepoTags = [$tag]' "${dir}/root/manifest.json" >"${dir}/manifest.json" ||
      die "${tarball} has no Docker manifest.json"
    mv "${dir}/manifest.json" "${dir}/root/manifest.json"
    (cd "${dir}/root" && tar -cf ../image.tar manifest.json blobs)
    docker load -i "${dir}/image.tar" >/dev/null || die "docker load of ${tarball} failed"
    rm -rf "${dir}"
    id="$(docker image inspect --format '{{.Id}}' "${repo}:e2e-loading")" || die "docker load did not tag ${repo}:e2e-loading"
    docker tag "${repo}:e2e-loading" "${repo}:e2e-latest"
    docker rmi "${repo}:e2e-loading" >/dev/null 2>&1 || true
  fi
  got="$(docker image inspect --format '{{join .Config.Entrypoint " "}}' "${id}" 2>/dev/null)" || got=""
  case " ${got} " in
    *" ${entrypoint} "*) ;;
    *) die "image ${repo} (${id}) runs '${got}', expected ${entrypoint}" ;;
  esac
  id="${id#sha256:}"
  local tag
  tag="e2e-$(printf '%s' "${id}" | cut -c1-12)"
  docker tag "${id}" "${repo}:${tag}"
  log "image ${repo}:${tag}" >&2
  printf '%s' "${tag}"
}

# build_images PHASES...: builds and loads the images the phases need, once.
build_images() {
  case "${IMAGE_SOURCE}" in
    local) ;;
    *) die "IMAGE_SOURCE must be local, got '${IMAGE_SOURCE}'; no published images exist yet" ;;
  esac
  local phase need_mcs=0 need_llm=0
  for phase in "$@"; do
    case "${phase}" in
      2) need_mcs=1 ;;
      3) need_mcs=1 need_llm=1 ;;
    esac
  done
  [ -n "${IMAGE_TAG}" ] || IMAGE_TAG="$(load_bazel_image "${SERVER_TARGET}" "${E2E_IMAGE_REPOSITORY}" /usr/bin/gds-server)"
  if [ "${need_mcs}" = "1" ]; then
    [ -n "${CONTROLLER_IMAGE_TAG}" ] ||
      CONTROLLER_IMAGE_TAG="$(load_bazel_image "${CONTROLLER_TARGET}" "${E2E_CONTROLLER_IMAGE_REPOSITORY}" /usr/bin/gds-mcs-controller)"
    ensure_docker_image "${E2E_BUSYBOX_IMAGE}"
  fi
  if [ "${need_llm}" = "1" ]; then
    [ -n "${STARGATE_IMAGE_TAG}" ] ||
      STARGATE_IMAGE_TAG="$(load_bazel_image "${STARGATE_TARGET}" "${E2E_STARGATE_IMAGE_REPOSITORY}" /usr/local/bin/stargate)"
    [ -n "${PYLON_IMAGE_TAG}" ] ||
      PYLON_IMAGE_TAG="$(load_bazel_image "${PYLON_TARGET}" "${E2E_PYLON_IMAGE_REPOSITORY}" /usr/local/bin/pylon)"
    if ! docker image inspect "${E2E_SAMPLE_IMAGE}" >/dev/null 2>&1; then
      log "docker build -t ${E2E_SAMPLE_IMAGE} ${SAMPLE_DIR}"
      docker build -q -t "${E2E_SAMPLE_IMAGE}" "${SAMPLE_DIR}" >/dev/null || die "docker build of ${E2E_SAMPLE_IMAGE} failed"
    fi
  fi
}

# ensure_docker_image IMAGE: pulls a third-party image when Docker lacks it.
ensure_docker_image() {
  docker image inspect "$1" >/dev/null 2>&1 && return 0
  log "docker pull $1"
  docker pull -q "$1" >/dev/null || die "docker pull $1 failed"
}

# cri_ref IMAGE: the reference containerd in the k3s node stores IMAGE under.
cri_ref() {
  case "$1" in
    */*)
      case "${1%%/*}" in
        *.* | *:* | localhost) printf '%s' "$1" ;;
        *) printf 'docker.io/%s' "$1" ;;
      esac
      ;;
    *) printf 'docker.io/library/%s' "$1" ;;
  esac
}

# image_in_nodes CLUSTER IMAGE: every node of the cluster has the image.
image_in_nodes() {
  local node
  for node in $(node_containers "$1"); do
    docker exec "${node}" crictl inspecti "$(cri_ref "$2")" >/dev/null 2>&1 || return 1
  done
}

# import_image CLUSTER IMAGE...: imports images into k3d cluster CLUSTER one
# at a time and checks that every node has them; k3d reports success even
# when the import into a node fails. Dies when one is missing: the charts
# run these images with imagePullPolicy Never.
import_image() {
  local cluster="$1" image
  shift
  for image in "$@"; do
    k3d image import -c "${cluster}" "${image}" >"${E2E_WORK_DIR}/k3d-import.log" 2>&1 || true
    image_in_nodes "${cluster}" "${image}" ||
      die "k3d image import of ${image} into ${cluster} failed: $(grep -m1 -i 'erro' "${E2E_WORK_DIR}/k3d-import.log" | decolor)"
  done
}

# import_or_pull CLUSTER IMAGE...: like import_image for third-party images
# with imagePullPolicy IfNotPresent. A multi-platform image pulled into a
# containerd-backed Docker cannot always be imported (missing platform
# blobs); the cluster then pulls it from its registry.
import_or_pull() {
  local cluster="$1" image
  shift
  for image in "$@"; do
    k3d image import -c "${cluster}" "${image}" >"${E2E_WORK_DIR}/k3d-import.log" 2>&1 || true
    if ! image_in_nodes "${cluster}" "${image}"; then
      log "${image} could not be imported into ${cluster} ($(grep -m1 -i 'erro' "${E2E_WORK_DIR}/k3d-import.log" | decolor | cut -c1-160)); the cluster pulls it"
    fi
  done
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
# Port-forward to the gds-server of the hub

start_port_forward() {
  local cluster="$1"
  if [ -n "${PF_PID}" ] && kill -0 "${PF_PID}" 2>/dev/null; then
    return 0
  fi
  if curl -s -o /dev/null --max-time 2 "${ADMIN_URL}/healthz"; then
    printf 'port 127.0.0.1:%s already answers: another port-forward or run uses it; set E2E_HTTP_LOCAL_PORT and E2E_ADMIN_LOCAL_PORT\n' \
      "${E2E_ADMIN_LOCAL_PORT}" >>"${PF_LOG}"
    return 1
  fi
  kc_exec "${cluster}" -n "${E2E_NAMESPACE}" port-forward --address 127.0.0.1 "svc/${SERVER_RELEASE}" \
    "${E2E_HTTP_LOCAL_PORT}:8080" "${E2E_ADMIN_LOCAL_PORT}:9091" >>"${PF_LOG}" 2>&1 &
  PF_PID=$!
  local i=0
  while [ "${i}" -lt 30 ]; do
    if curl -s -o /dev/null --max-time 2 "${ADMIN_URL}/healthz" && kill -0 "${PF_PID}" 2>/dev/null; then
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

# dump_namespace CLUSTER NAMESPACE SELECTOR: pods, recent events and logs.
dump_namespace() {
  local cluster="$1" ns="$2" selector="$3"
  kc "${cluster}" --request-timeout=10s get namespace "${ns}" >/dev/null 2>&1 || return 0
  section "Pods in ${ns} (${cluster})"
  kc "${cluster}" --request-timeout=10s -n "${ns}" get pods -o wide 2>&1 || true
  section "Events in ${ns} (${cluster}, last 20)"
  kc "${cluster}" --request-timeout=10s -n "${ns}" get events --sort-by=.lastTimestamp 2>&1 | tail -n 20 || true
  if [ -n "${selector}" ]; then
    section "Logs of ${selector} in ${ns} (${cluster}, tail 80)"
    kc "${cluster}" --request-timeout=10s -n "${ns}" logs -l "${selector}" --tail=80 --all-containers --prefix 2>&1 | decolor || true
  fi
}

dump_diagnostics() {
  local cluster
  if [ -s "$(kubeconfig_of "${E2E_CLUSTER}")" ]; then
    dump_namespace "${E2E_CLUSTER}" "${E2E_NAMESPACE}" "${SERVER_SELECTOR}"
  fi
  for cluster in "${E2E_CLUSTER_A}" "${E2E_CLUSTER_B}"; do
    [ -s "$(kubeconfig_of "${cluster}")" ] || continue
    dump_namespace "${cluster}" "${E2E_CONTROLLER_NAMESPACE}" "${CONTROLLER_SELECTOR}"
    if kc "${cluster}" --request-timeout=10s get namespace "${E2E_DEMO_NAMESPACE}" >/dev/null 2>&1; then
      section "MCS objects in ${E2E_DEMO_NAMESPACE} (${cluster})"
      kc "${cluster}" --request-timeout=10s -n "${E2E_DEMO_NAMESPACE}" get serviceexports,serviceimports,services,endpointslices -o wide 2>&1 || true
    fi
    dump_namespace "${cluster}" "${E2E_ROUTER_NAMESPACE}" "${ROUTER_SELECTOR}"
    dump_namespace "${cluster}" "${E2E_PYLON_NAMESPACE}" "app=gds-e2e-pylon"
  done
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

# abort: a required step failed. Records the remaining steps and phases as
# skipped, dumps diagnostics and exits. E2E_CLEANUP=1 still cleans up.
abort() {
  ABORTED=1
  local s p
  local IFS='|'
  for s in ${REMAINING_STEPS}; do
    [ -n "${s}" ] && skip "${s}"
  done
  IFS=' '
  for p in ${LATER_PHASES}; do
    skip "phase ${p} (not run)"
  done
  dump_diagnostics
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
  stop_router_port_forward
}
trap on_exit EXIT

# ---------------------------------------------------------------------------
# Setup

preflight() {
  local tool
  for tool in k3d docker kubectl helm curl jq tar; do
    command -v "${tool}" >/dev/null 2>&1 || die "${tool} is required"
  done
  if [ "${IMAGE_SOURCE}" = "local" ] && [ "${E2E_SKIP_BUILD}" != "1" ]; then
    command -v "${BAZEL}" >/dev/null 2>&1 || die "${BAZEL} is required to build the images; or set E2E_SKIP_BUILD=1"
  fi
  docker info >/dev/null 2>&1 || die "Docker is not reachable"
  mkdir -p "${E2E_WORK_DIR}"
  chmod 700 "${E2E_WORK_DIR}"
  : >"${PF_LOG}"
  : >"${CURL_ERR}"
  log "work dir ${E2E_WORK_DIR}, phases ${E2E_PHASES}"
}

# server_args: the helm arguments of the gds-server release. The Service is a
# NodePort, which the controllers of phase 2 dial over the Docker network.
server_args() {
  printf '%s\n' \
    --namespace "${E2E_NAMESPACE}" \
    --values "${MANIFESTS}/gds-server-values.yaml" \
    --set-string "image.repository=${E2E_IMAGE_REPOSITORY}" \
    --set-string "image.tag=${IMAGE_TAG}" \
    --set-string "image.pullPolicy=Never" \
    --set-string "region=${E2E_REGION}" \
    --set-string "service.type=NodePort"
}

# install_server CLUSTER: installs or upgrades the gds-server release and
# waits until its Deployment is ready.
install_server() {
  local cluster="$1" args=() line
  while IFS= read -r line; do
    args+=("${line}")
  done < <(server_args)
  hm "${cluster}" upgrade --install "${SERVER_RELEASE}" "${SERVER_CHART}" "${args[@]}" \
    --create-namespace --wait --timeout "${E2E_HELM_TIMEOUT}" >"${E2E_WORK_DIR}/helm-server.log" 2>&1
}

# setup_hub: the hub cluster with gds-server and the REST port-forward, once
# per run. Every failure aborts.
setup_hub() {
  local cluster="${E2E_CLUSTER}"
  [ "${HUB_READY}" = "1" ] && return 0
  if ensure_cluster "${cluster}"; then
    pass "k3d cluster ${cluster} (hub) has a Ready node; kubeconfig $(kubeconfig_of "${cluster}")"
  else
    fail "k3d cluster ${cluster}" "${LAST_OBSERVED}"
    abort
  fi
  import_image "${cluster}" "${E2E_IMAGE_REPOSITORY}:${IMAGE_TAG}"
  pass "image ${E2E_IMAGE_REPOSITORY}:${IMAGE_TAG} imported into ${cluster}"
  local existed=0
  hm "${cluster}" status "${SERVER_RELEASE}" --namespace "${E2E_NAMESPACE}" >/dev/null 2>&1 && existed=1
  if install_server "${cluster}"; then
    pass "helm upgrade --install ${SERVER_RELEASE}: Deployment ready"
  else
    fail "helm upgrade --install ${SERVER_RELEASE}" "$(tail -n 5 "${E2E_WORK_DIR}/helm-server.log" | tr '\n' ' ')"
    abort
  fi
  # A release from an earlier run keeps that run's registry in memory; a
  # restart starts every phase from an empty store.
  if [ "${existed}" = "1" ]; then
    if kc "${cluster}" -n "${E2E_NAMESPACE}" rollout restart "deploy/${SERVER_RELEASE}" >/dev/null &&
      kc "${cluster}" -n "${E2E_NAMESPACE}" rollout status "deploy/${SERVER_RELEASE}" --timeout=120s >/dev/null 2>&1; then
      pass "gds-server restarted with an empty memory store (release from an earlier run)"
    else
      fail "restart of gds-server from an earlier run"
      abort
    fi
  fi
  if ! start_port_forward "${cluster}"; then
    fail "port-forward to svc/${SERVER_RELEASE}" "$(tail -n 5 "${PF_LOG}" | tr '\n' ' ')"
    abort
  fi
  pass "port-forward REST 127.0.0.1:${E2E_HTTP_LOCAL_PORT}, admin 127.0.0.1:${E2E_ADMIN_LOCAL_PORT}"
  HUB_READY=1
}

# ensure_kubeconfig CLUSTER: the work directory kubeconfig of a cluster that
# exists, for cleanup of an earlier run.
ensure_kubeconfig() {
  [ -s "$(kubeconfig_of "$1")" ] && return 0
  k3d kubeconfig get "$1" >"$(kubeconfig_of "$1")" 2>/dev/null || return 1
  chmod 600 "$(kubeconfig_of "$1")"
}

# delete_namespace CLUSTER NAMESPACE: deletes a namespace and waits. When the
# ServiceExport finalizer holds it (the controller or GDS is gone), removes
# the finalizers and waits again.
delete_namespace() {
  local cluster="$1" ns="$2" se
  kc "${cluster}" get namespace "${ns}" >/dev/null 2>&1 || return 0
  log "deleting namespace ${ns} in ${cluster}"
  kc "${cluster}" delete namespace "${ns}" --wait=true --timeout=120s >/dev/null 2>&1 && return 0
  for se in $(kc "${cluster}" -n "${ns}" get serviceexports -o name 2>/dev/null); do
    kc "${cluster}" -n "${ns}" patch "${se}" --type=merge -p '{"metadata":{"finalizers":null}}' >/dev/null 2>&1 || true
  done
  kc "${cluster}" wait --for=delete "namespace/${ns}" --timeout=60s >/dev/null 2>&1 || true
}

# cleanup_member CLUSTER: removes what phases 2 and 3 installed in a member
# cluster run.sh did not create. The workloads go first, while the controller
# and gds-server still run, so the ServiceExport finalizers can deregister.
# The CRDs stay (helm.sh/resource-policy: keep).
cleanup_member() {
  local cluster="$1" ns
  check_cluster_name "${cluster}"
  cluster_exists "${cluster}" || return 0
  cluster_created_here "${cluster}" && return 0
  ensure_kubeconfig "${cluster}" || return 0
  for ns in "${E2E_PYLON_NAMESPACE}" "${E2E_ROUTER_NAMESPACE}" "${E2E_DEMO_NAMESPACE}"; do
    delete_namespace "${cluster}" "${ns}"
  done
  hm "${cluster}" uninstall "${CONTROLLER_RELEASE}" --namespace "${E2E_CONTROLLER_NAMESPACE}" --wait >/dev/null 2>&1 || true
  delete_namespace "${cluster}" "${E2E_CONTROLLER_NAMESPACE}"
}

cleanup_hub() {
  local cluster="${E2E_CLUSTER}"
  check_cluster_name "${cluster}"
  cluster_exists "${cluster}" || return 0
  if cluster_created_here "${cluster}"; then
    log "k3d cluster ${cluster} was created by run.sh; deleting it removes the release"
    return 0
  fi
  ensure_kubeconfig "${cluster}" || return 0
  hm "${cluster}" uninstall "${SERVER_RELEASE}" --namespace "${E2E_NAMESPACE}" --wait >/dev/null 2>&1 || true
  delete_namespace "${cluster}" "${E2E_NAMESPACE}"
}

cleanup() {
  section "Cleanup"
  stop_port_forward
  cleanup_member "${E2E_CLUSTER_A}"
  cleanup_member "${E2E_CLUSTER_B}"
  cleanup_hub
  remove_routes
  delete_created_clusters
  delete_created_networks
  rm -f "${E2E_WORK_DIR}"/kubeconfig-* "${E2E_WORK_DIR}/k3d-delete-kubeconfig" "${E2E_WORK_DIR}/rest-body.json"
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

# clusterset_absent: phase 1 starts without its clusterset; phase 2 may have
# created its own on the same server.
clusterset_absent() {
  rest_expect GET /v1/clustersets "" 200 "all(.clustersets[]?; .clusterset != \"${E2E_CLUSTERSET}\")"
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

# POOL_SUMMARY: a jq filter that summarises one pool and its agents as
#   "<state> relay=<kvRelayLive> ready=<readyEndpointCount> ingress=<readyIngressCount> |"
# followed by one entry per agent (sorted by role):
#   " <role>:<source>:<Service or ->:ready=<ready endpoints>:regs=<active registrations>(<agent ids>):<live|dead>"
# Fields the response omits count as their defaults.
readonly POOL_SUMMARY='(.state | sub("POOL_STATE_"; "")) + " relay=" + ((.kvRelayLive // false) | tostring)
  + " ready=" + ((.readyEndpointCount // 0) | tostring) + " ingress=" + ((.readyIngressCount // 0) | tostring) + " |"
  + ([.agents[]? | " " + (.role | sub("FRONTEND_ROLE_"; "")) + ":" + (.source | sub("AGENT_SOURCE_"; ""))
      + ":" + (.service.name // "-") + ":ready=" + ((.readyEndpointCount // 0) | tostring)
      + ":regs=" + ((.activeRegistrationCount // 0) | tostring) + "(" + ([.registrations[]?.agentId] | join("+")) + ")"
      + ":" + (if .live then "live" else "dead" end)] | join(""))'

# pool_is POOL_ID EXPECTED: GetPool, summarised with POOL_SUMMARY.
pool_is() {
  rest GET "${CS_PATH}/pools/$1"
  local got
  got="$(rest_json ".pool | ${POOL_SUMMARY}" 2>/dev/null)" || got="<not JSON>"
  LAST_OBSERVED="HTTP ${REST_STATUS} ${got}"
  [ "${REST_STATUS}" = "200" ] && [ "${got}" = "$2" ]
}

# pool_named_is QUERY NAME EXPECTED: pool NAME of ListPools?QUERY,
# summarised with POOL_SUMMARY, or "absent".
pool_named_is() {
  rest GET "${CS_PATH}/pools?$1"
  local got
  got="$(rest_json "[.pools[]? | select(.name == \"$2\") | ${POOL_SUMMARY}] | first // \"absent\"" 2>/dev/null)" || got="<not JSON>"
  LAST_OBSERVED="HTTP ${REST_STATUS} $2: ${got}"
  [ "${REST_STATUS}" = "200" ] && [ "${got}" = "$3" ]
}

# Pool agents: one registration per process, keyed (pool, role, agent_id),
# each with its own lease epoch. Set by register_agent.
AGENT_POOL_ID=""
AGENT_EPOCH=""
RELAY_1_EPOCH=""
RELAY_ONLY_ID=""
PYLON_ONLY_ID=""

# register_agent CLUSTER POOL ROLE AGENT_ID POOL_STATE [ATTRIBUTES]:
# RegisterPoolAgent with a 60s lease. The registration comes back with an
# ACTIVE lease and an epoch, and the pool is in POOL_STATE. Sets
# AGENT_POOL_ID and AGENT_EPOCH.
register_agent() {
  local attrs=""
  [ -z "${6:-}" ] || attrs=",\"attributes\":$6"
  rest_expect POST "${CS_PATH}/pool-agents" \
    "{\"cluster\":\"$1\",\"poolName\":\"$2\",\"agent\":{\"role\":\"$3\",\"agentId\":\"$4\"${attrs}},\"leaseTtl\":\"60s\"}" \
    200 "(.poolId | length > 0) and .registration.agentId == \"$4\" and .registration.lease.state == \"LEASE_STATE_ACTIVE\" and (.registration.lease.epoch | length > 0) and .poolState == \"$5\"" \
    '{poolId, poolState, registration: (.registration | {agentId, lease})}' || return 1
  AGENT_POOL_ID="$(rest_json .poolId)"
  AGENT_EPOCH="$(rest_json .registration.lease.epoch)"
}

# register_blue_relay AGENT_ID: a KV_RELAY registration joins the API pool
# blue under blue's pool id. Blue's ingress is the frontend URL of its API
# registration, so its pool lease keeps it ACTIVE whatever agents it has.
register_blue_relay() {
  register_agent cluster-a blue FRONTEND_ROLE_KV_RELAY "$1" POOL_STATE_ACTIVE || return 1
  LAST_OBSERVED="${LAST_OBSERVED}; pool id of blue ${POOL_ID}"
  [ "${AGENT_POOL_ID}" = "${POOL_ID}" ]
}

# heartbeat_agent POOL_ID ROLE AGENT_ID EPOCH: HeartbeatPoolAgent renews the
# registration's lease under the same epoch.
heartbeat_agent() {
  rest_expect POST "${CS_PATH}/pools/$1/agents/$2:heartbeat" "{\"agentId\":\"$3\",\"epoch\":\"$4\"}" \
    200 ".lease.state == \"LEASE_STATE_ACTIVE\" and .lease.epoch == \"$4\"" '{lease, poolState}'
}

# agent_heartbeat_rejected POOL_ID ROLE AGENT_ID EPOCH STATUS REASON
agent_heartbeat_rejected() {
  rest_expect POST "${CS_PATH}/pools/$1/agents/$2:heartbeat" "{\"agentId\":\"$3\",\"epoch\":\"$4\"}" \
    "$5" "any(.details[]?; .reason == \"$6\" and .domain == \"gds.nvidia.com\")" '{code, message}'
}

# deregister_agent POOL_ID ROLE AGENT_ID: DeregisterPoolAgent; agent_id is a
# query parameter over REST.
deregister_agent() {
  rest_expect DELETE "${CS_PATH}/pools/$1/agents/$2?agent_id=$3" "" 200 '. == {}'
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
  local T="${E2E_TIMEOUT}"
  CS_PATH="/v1/clustersets/${E2E_CLUSTERSET}"
  if [ "${E2E_DEPLOY_ONLY}" = "1" ]; then
    REMAINING_STEPS="phase 1 setup|phase 1 server|"
  else
    REMAINING_STEPS="phase 1 setup|phase 1 server|phase 1 clusters|phase 1 pools|phase 1 pool agents|phase 1 service export and import|phase 1 pools watch|phase 1 heartbeats and leases|phase 1 deregister|"
  fi

  section "Phase 1 setup: k3d cluster ${E2E_CLUSTER} (hub), gds-server with the memory store"
  setup_hub
  step_done

  section "Phase 1: server"
  expect "admin /readyz is ok" "${T}" admin_says_ok /readyz || abort
  expect "admin /healthz is ok" "${T}" admin_says_ok /healthz || true
  expect "/metrics has nvcf_gds_build_info" "${T}" metrics_have nvcf_gds_build_info || true
  expect "GET /v1/serverinfo reports region ${E2E_REGION}" "${T}" serverinfo_ok || true
  expect "GET /v1/clustersets has no clusterset ${E2E_CLUSTERSET}" "${T}" clusterset_absent || true
  step_done

  if [ "${E2E_DEPLOY_ONLY}" = "1" ]; then
    return 0
  fi

  section "Phase 1: clusters"
  once "PUT cluster-a registers it with an ACTIVE lease" register_cluster cluster-a 300s || abort
  once "PUT cluster-b registers it with an ACTIVE lease" register_cluster cluster-b 300s || abort
  expect "GET /v1/clustersets lists ${E2E_CLUSTERSET} with 2 active clusters" "${T}" clusterset_lists 2 || true
  step_done

  section "Phase 1: pools"
  once "POST pool blue registers it (REGISTERED, with a lease)" register_pool || abort
  once "pool heartbeat with the lease epoch turns blue ACTIVE" heartbeat_pool || true
  expect "GET pools?model=llama-3&region=${E2E_REGION} lists blue" "${T}" \
    pools_are "model=llama-3&region=${E2E_REGION}" "cluster-a/blue=POOL_STATE_ACTIVE" || true
  expect "GET pools?model=mistral-7b lists nothing" "${T}" pools_are "model=mistral-7b" "" || true
  expect "GET pools?region=elsewhere lists nothing" "${T}" pools_are "region=elsewhere" "" || true
  step_done

  # Agent registrations of blue are removed again at the end of this
  # section, so blue is an API-only pool for the checks that follow.
  section "Phase 1: pool agents"
  if once "RegisterPoolAgent relay-1 (KV_RELAY) on blue: lease ACTIVE, blue's pool id, pool stays ACTIVE (static ingress)" \
    register_blue_relay relay-1; then
    RELAY_1_EPOCH="${AGENT_EPOCH}"
    expect "GET pool blue: agent KV_RELAY from the API, 1 active registration, kvRelayLive" "${T}" \
      pool_is "${POOL_ID}" "ACTIVE relay=true ready=0 ingress=0 | KV_RELAY:API:-:ready=0:regs=1(relay-1):live" || true
    once "relay-1 heartbeat with a stale epoch is 409 LEASE_EPOCH_MISMATCH" \
      agent_heartbeat_rejected "${POOL_ID}" FRONTEND_ROLE_KV_RELAY relay-1 1 409 LEASE_EPOCH_MISMATCH || true
    once "relay-1 heartbeat with its epoch renews the lease" \
      heartbeat_agent "${POOL_ID}" FRONTEND_ROLE_KV_RELAY relay-1 "${RELAY_1_EPOCH}" || true
    once "RegisterPoolAgent relay-2 (KV_RELAY) on blue: its own ACTIVE lease" register_blue_relay relay-2 || true
    expect "GET pool blue: 2 active KV_RELAY registrations, relay-1 and relay-2" "${T}" \
      pool_is "${POOL_ID}" "ACTIVE relay=true ready=0 ingress=0 | KV_RELAY:API:-:ready=0:regs=2(relay-1+relay-2):live" || true
    once "relay-1 heartbeat with its epoch still succeeds after relay-2 registered (no fencing)" \
      heartbeat_agent "${POOL_ID}" FRONTEND_ROLE_KV_RELAY relay-1 "${RELAY_1_EPOCH}" || true
    once "DELETE agents/FRONTEND_ROLE_KV_RELAY?agent_id=relay-1 deregisters relay-1" \
      deregister_agent "${POOL_ID}" FRONTEND_ROLE_KV_RELAY relay-1 || true
    expect "GET pool blue: 1 active KV_RELAY registration, relay-2" "${T}" \
      pool_is "${POOL_ID}" "ACTIVE relay=true ready=0 ingress=0 | KV_RELAY:API:-:ready=0:regs=1(relay-2):live" || true
    once "DELETE agents/FRONTEND_ROLE_KV_RELAY?agent_id=relay-2 deregisters relay-2" \
      deregister_agent "${POOL_ID}" FRONTEND_ROLE_KV_RELAY relay-2 || true
    expect "GET pool blue: no agents, still ACTIVE through its pool lease" "${T}" \
      pool_is "${POOL_ID}" "ACTIVE relay=false ready=0 ingress=0 |" || true
  fi
  if once "RegisterPoolAgent creates pool relay-only through a KV_RELAY agent (REGISTERED)" \
    register_agent cluster-a relay-only FRONTEND_ROLE_KV_RELAY relay-only-0 POOL_STATE_REGISTERED; then
    RELAY_ONLY_ID="${AGENT_POOL_ID}"
  fi
  if once "RegisterPoolAgent creates pool pylon-only through a PYLON agent (ACTIVE)" \
    register_agent cluster-a pylon-only FRONTEND_ROLE_PYLON pylon-0 POOL_STATE_ACTIVE '{"models":"llama-3"}'; then
    PYLON_ONLY_ID="${AGENT_POOL_ID}"
  fi
  expect "ListPools lists relay-only REGISTERED with kvRelayLive: a relay alone is not routable" "${T}" \
    pool_named_is "cluster=cluster-a" relay-only "REGISTERED relay=true ready=0 ingress=0 | KV_RELAY:API:-:ready=0:regs=1(relay-only-0):live" || true
  expect "ListPools lists pylon-only ACTIVE with readyIngressCount 0 and its PYLON agent live" "${T}" \
    pool_named_is "cluster=cluster-a" pylon-only "ACTIVE relay=false ready=0 ingress=0 | PYLON:API:-:ready=0:regs=1(pylon-0):live" || true
  if [ -n "${RELAY_ONLY_ID}" ]; then
    once "DeregisterPoolAgent relay-only-0" deregister_agent "${RELAY_ONLY_ID}" FRONTEND_ROLE_KV_RELAY relay-only-0 || true
  fi
  if [ -n "${PYLON_ONLY_ID}" ]; then
    once "DeregisterPoolAgent pylon-0" deregister_agent "${PYLON_ONLY_ID}" FRONTEND_ROLE_PYLON pylon-0 || true
  fi
  expect "pools relay-only and pylon-only are gone with their last registration" "${T}" \
    pools_are "cluster=cluster-a" "cluster-a/blue=POOL_STATE_ACTIVE" || true
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
# Phase 2: clusters a and b with gds-mcs-controller, the hub with gds-server.
# Kubernetes objects are read as JSON and fed to jq through here-strings.

# kjson CLUSTER ARGS...: kubectl get ... -o json, or empty when it fails.
kjson() {
  local cluster="$1"
  shift
  kc "${cluster}" --request-timeout=10s get "$@" -o json 2>/dev/null || true
}

# render_manifest FILE: a manifest with the run's placeholders replaced. The
# lines between "# router-mode single: begin" and "end" stay only with
# E2E_ROUTER_MODE=single.
render_manifest() {
  local router_mode='/# router-mode single: begin/,/# router-mode single: end/d'
  if [ "${E2E_ROUTER_MODE}" = "single" ]; then
    router_mode='/# router-mode single: /d'
  fi
  sed -e "${router_mode}" \
    -e "s|__BUSYBOX_IMAGE__|${E2E_BUSYBOX_IMAGE}|g" \
    -e "s|__SAMPLE_IMAGE__|${E2E_SAMPLE_IMAGE}|g" \
    -e "s|__PYLON_IMAGE__|${E2E_PYLON_IMAGE_REPOSITORY}:${PYLON_IMAGE_TAG}|g" \
    -e "s|__GDS_URL__|${GDS_GRPC_URL}|g" \
    -e "s|__CLUSTERSET__|${E2E_MCS_CLUSTERSET}|g" \
    -e "s|__CLUSTER_ID__|${E2E_CLUSTER_ID_B}|g" \
    -e "s|__ROUTER_IP__|${ROUTER_IP}|g" \
    -e "s|__ROUTER_HOST__|${E2E_ROUTER_HOST}|g" \
    "$1"
}

# controller_args CLUSTER_ID: the helm arguments of the controller release.
controller_args() {
  printf '%s\n' \
    --namespace "${E2E_CONTROLLER_NAMESPACE}" \
    --values "${MANIFESTS}/gds-mcs-controller-values.yaml" \
    --set-string "image.repository=${E2E_CONTROLLER_IMAGE_REPOSITORY}" \
    --set-string "image.tag=${CONTROLLER_IMAGE_TAG}" \
    --set-string "image.pullPolicy=Never" \
    --set-string "gds.urls[0]=${GDS_GRPC_URL}" \
    --set-string "clusterset.id=${E2E_MCS_CLUSTERSET}" \
    --set-string "cluster.id=$1"
}

# install_controller CLUSTER CLUSTER_ID: helm --wait returns when both
# replicas are ready: connected to GDS and, on the leader, registered.
install_controller() {
  local cluster="$1" args=() line
  while IFS= read -r line; do
    args+=("${line}")
  done < <(controller_args "$2")
  hm "${cluster}" upgrade --install "${CONTROLLER_RELEASE}" "${CONTROLLER_CHART}" "${args[@]}" \
    --create-namespace --wait --timeout "${E2E_HELM_TIMEOUT}" >"${E2E_WORK_DIR}/helm-controller-${cluster}.log" 2>&1
}

# hub_grpc_url: GDS_GRPC_URL, the hub node's address on E2E_NETWORK and the
# gRPC NodePort of gds-server.
hub_grpc_url() {
  local container ip port
  container="$(awk 'NR == 1' <<<"$(node_containers "${E2E_CLUSTER}")")"
  ip="$(node_ip "${container}")"
  port="$(kc "${E2E_CLUSTER}" -n "${E2E_NAMESPACE}" get svc "${SERVER_RELEASE}" \
    -o jsonpath='{.spec.ports[?(@.name=="grpc")].nodePort}' 2>/dev/null)" || port=""
  LAST_OBSERVED="hub node ${container:-none} address ${ip:-none}, gRPC NodePort ${port:-none}"
  [ -n "${ip}" ] && [ -n "${port}" ] || return 1
  GDS_GRPC_URL="http://${ip}:${port}"
}

pod_cidrs() {  # pod_cidrs CLUSTER: the podCIDR of every node
  kc "$1" get nodes -o jsonpath='{range .items[*]}{.spec.podCIDR}{"\n"}{end}' 2>/dev/null | sort
}

# pod_cidrs_assigned CLUSTER: every node has its podCIDR; a node can turn
# Ready a moment before node IPAM assigns it.
pod_cidrs_assigned() {
  local got
  got="$(kc "$1" get nodes -o jsonpath='{range .items[*]}{.metadata.name}={.spec.podCIDR}{" "}{end}' 2>/dev/null)" || got=""
  LAST_OBSERVED="pod CIDRs of $1: ${got:-none}"
  [ -n "${got}" ] && ! grep -qE '=( |$)' <<<"${got}"
}

# setup_routes: static routes between the pod CIDRs of a and b, unless
# E2E_CROSS_CLUSTER_ROUTES=0 or they cannot be added. Without them the data
# path checks are skipped and only the control plane objects are asserted.
setup_routes() {
  local a="${E2E_CLUSTER_A}" b="${E2E_CLUSTER_B}" overlap
  ROUTES_OK=0
  if [ "${E2E_CROSS_CLUSTER_ROUTES}" != "1" ]; then
    skip "static routes between ${a} and ${b}: E2E_CROSS_CLUSTER_ROUTES=0; the data path checks (curl through the derived ClusterIP, Pylon registration) are skipped"
    return 0
  fi
  if ! poll 60 pod_cidrs_assigned "${a}" || ! poll 60 pod_cidrs_assigned "${b}"; then
    skip "static routes between ${a} and ${b}: ${LAST_OBSERVED}; the data path checks are skipped"
    return 0
  fi
  overlap="$(comm -12 <(pod_cidrs "${a}") <(pod_cidrs "${b}") | tr '\n' ' ')"
  if [ -n "${overlap}" ]; then
    skip "static routes between ${a} and ${b}: the clusters share pod CIDRs (${overlap}); recreate them with distinct --cluster-cidr. The data path checks are skipped"
    return 0
  fi
  if route_pod_cidrs "${a}" "${b}" && route_pod_cidrs "${b}" "${a}"; then
    ROUTES_OK=1
    pass "static routes between the pod CIDRs of ${a} ($(pod_cidrs "${a}" | tr '\n' ' ')) and ${b} ($(pod_cidrs "${b}" | tr '\n' ' '))"
  else
    skip "static routes between ${a} and ${b} could not be added (${LAST_OBSERVED}); the data path checks are skipped, the control plane objects are still asserted"
  fi
}

# setup_members: clusters a and b, routes, images and the controller in each,
# once per run. Every failure aborts.
setup_members() {
  local cluster id
  [ "${MCS_READY}" = "1" ] && return 0
  if ensure_cluster "${E2E_CLUSTER_A}" \
    --k3s-arg "--cluster-cidr=${E2E_POD_CIDR_A}@server:*" \
    --k3s-arg "--service-cidr=${E2E_SERVICE_CIDR_A}@server:*"; then
    pass "k3d cluster ${E2E_CLUSTER_A} (pods ${E2E_POD_CIDR_A}, services ${E2E_SERVICE_CIDR_A}) has a Ready node"
  else
    fail "k3d cluster ${E2E_CLUSTER_A}" "${LAST_OBSERVED}"
    abort
  fi
  if ensure_cluster "${E2E_CLUSTER_B}" \
    --k3s-arg "--cluster-cidr=${E2E_POD_CIDR_B}@server:*" \
    --k3s-arg "--service-cidr=${E2E_SERVICE_CIDR_B}@server:*"; then
    pass "k3d cluster ${E2E_CLUSTER_B} (pods ${E2E_POD_CIDR_B}, services ${E2E_SERVICE_CIDR_B}) has a Ready node"
  else
    fail "k3d cluster ${E2E_CLUSTER_B}" "${LAST_OBSERVED}"
    abort
  fi
  setup_routes
  once "gds-server gRPC NodePort reachable address on ${E2E_NETWORK}" hub_grpc_url || abort
  log "GDS URL of the controllers: ${GDS_GRPC_URL}"
  for cluster in "${E2E_CLUSTER_A}" "${E2E_CLUSTER_B}"; do
    import_image "${cluster}" "${E2E_CONTROLLER_IMAGE_REPOSITORY}:${CONTROLLER_IMAGE_TAG}"
    import_or_pull "${cluster}" "${E2E_BUSYBOX_IMAGE}"
  done
  pass "image ${E2E_CONTROLLER_IMAGE_REPOSITORY}:${CONTROLLER_IMAGE_TAG} imported into ${E2E_CLUSTER_A} and ${E2E_CLUSTER_B}"
  for cluster in "${E2E_CLUSTER_A}" "${E2E_CLUSTER_B}"; do
    id="${E2E_CLUSTER_ID_A}"
    [ "${cluster}" = "${E2E_CLUSTER_B}" ] && id="${E2E_CLUSTER_ID_B}"
    if install_controller "${cluster}" "${id}"; then
      pass "helm upgrade --install ${CONTROLLER_RELEASE} in ${cluster} (cluster id ${id}): 2 replicas ready"
    else
      fail "helm upgrade --install ${CONTROLLER_RELEASE} in ${cluster}" "$(tail -n 5 "${E2E_WORK_DIR}/helm-controller-${cluster}.log" | tr '\n' ' ')"
      abort
    fi
  done
  MCS_READY=1
}

# leader_held CLUSTER: the leader election Lease names a running controller
# pod.
leader_held() {
  local holder pods
  holder="$(kc "$1" -n "${E2E_CONTROLLER_NAMESPACE}" get lease "${LEADER_LEASE}" -o jsonpath='{.spec.holderIdentity}' 2>/dev/null)" || holder=""
  pods="$(kc "$1" -n "${E2E_CONTROLLER_NAMESPACE}" get pods -l "${CONTROLLER_SELECTOR}" \
    --field-selector=status.phase=Running -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null)" || pods=""
  LAST_OBSERVED="holder ${holder:-none}; running pods $(tr '\n' ' ' <<<"${pods}")"
  [ -n "${holder}" ] || return 1
  # controller-runtime names the holder <pod>_<uuid>.
  grep -qx -- "${holder%%_*}" <<<"${pods}"
}

# apply_demo: the echo workload in a, the client in b, the demo namespace in
# both (namespace sameness).
apply_demo() {
  local cluster
  for cluster in "${E2E_CLUSTER_A}" "${E2E_CLUSTER_B}"; do
    kc "${cluster}" create namespace "${E2E_DEMO_NAMESPACE}" --dry-run=client -o yaml | kc "${cluster}" apply -f - >/dev/null || return 1
  done
  render_manifest "${MANIFESTS}/mcs-demo.yaml" | kc "${E2E_CLUSTER_A}" -n "${E2E_DEMO_NAMESPACE}" apply -f - >/dev/null || return 1
  render_manifest "${MANIFESTS}/mcs-client.yaml" | kc "${E2E_CLUSTER_B}" -n "${E2E_DEMO_NAMESPACE}" apply -f - >/dev/null || return 1
  kc "${E2E_CLUSTER_A}" -n "${E2E_DEMO_NAMESPACE}" rollout status deploy/echo --timeout=120s >/dev/null 2>&1 ||
    { LAST_OBSERVED="deploy/echo in ${E2E_CLUSTER_A} not ready"; return 1; }
  kc "${E2E_CLUSTER_B}" -n "${E2E_DEMO_NAMESPACE}" wait --for=condition=Ready pod/client --timeout=120s >/dev/null 2>&1 ||
    { LAST_OBSERVED="pod/client in ${E2E_CLUSTER_B} not ready"; return 1; }
}

scale_echo() {  # scale_echo REPLICAS
  kc "${E2E_CLUSTER_A}" -n "${E2E_DEMO_NAMESPACE}" scale deploy/echo --replicas="$1" >/dev/null 2>"${E2E_WORK_DIR}/kubectl.err" ||
    { LAST_OBSERVED="$(cat "${E2E_WORK_DIR}/kubectl.err")"; return 1; }
}

label_service() {  # label_service CLUSTER NAME LABEL...
  local cluster="$1" name="$2"
  shift 2
  kc "${cluster}" -n "${E2E_DEMO_NAMESPACE}" label service "${name}" "$@" --overwrite >/dev/null 2>"${E2E_WORK_DIR}/kubectl.err" ||
    { LAST_OBSERVED="$(cat "${E2E_WORK_DIR}/kubectl.err")"; return 1; }
}

# serviceexport_is CLUSTER NAME EXPECTED: "<managed-by>|Valid=<s>|Ready=<s>
# [|pool-id=<id>]" of the ServiceExport.
serviceexport_is() {
  local got
  got="$(jq -r '"\(.metadata.labels["gds.nvidia.com/managed-by"] // "-")|Valid=\([.status.conditions[]? | select(.type == "Valid") | .status][0] // "-")|Ready=\([.status.conditions[]? | select(.type == "Ready") | .status][0] // "-")" + (if .metadata.labels["gds.nvidia.com/pool-id"] then "|pool-id=" + .metadata.labels["gds.nvidia.com/pool-id"] else "" end)' \
    <<<"$(kjson "$1" -n "${E2E_DEMO_NAMESPACE}" serviceexport "$2")" 2>/dev/null)" || got=""
  LAST_OBSERVED="ServiceExport ${2}: ${got:-absent}"
  [ "${got}" = "$3" ]
}

object_absent() {  # object_absent CLUSTER NAMESPACE KIND/NAME
  if kc "$1" --request-timeout=10s -n "$2" get "$3" >/dev/null 2>&1; then
    LAST_OBSERVED="$3 still exists in $1"
    return 1
  fi
  LAST_OBSERVED="$3 absent"
}

object_present() {  # object_present CLUSTER NAMESPACE KIND/NAME
  kc "$1" --request-timeout=10s -n "$2" get "$3" >/dev/null 2>&1 && return 0
  LAST_OBSERVED="$3 absent in $1"
  return 1
}

# Set by serviceimport_ok: the derived Service, its ClusterIP and the
# ServiceImport UID in b.
DERIVED_NAME=""
DERIVED_IP=""
IMPORT_UID=""

# serviceimport_ok CLUSTER NAME: a ClusterSetIP ServiceImport whose spec.ips
# is the ClusterIP of its derived Service.
serviceimport_ok() {
  local cluster="$1" name="$2" si svc_ip type
  si="$(kjson "${cluster}" -n "${E2E_DEMO_NAMESPACE}" serviceimport "${name}")"
  [ -n "${si}" ] || si='{}'
  DERIVED_NAME="$(jq -r '.metadata.annotations["multicluster.kubernetes.io/derived-service"] // ""' <<<"${si}")"
  DERIVED_IP="$(jq -r '.spec.ips[0] // ""' <<<"${si}")"
  IMPORT_UID="$(jq -r '.metadata.uid // ""' <<<"${si}")"
  type="$(jq -r '.spec.type // ""' <<<"${si}")"
  svc_ip=""
  if [ -n "${DERIVED_NAME}" ]; then
    svc_ip="$(kc "${cluster}" -n "${E2E_DEMO_NAMESPACE}" get service "${DERIVED_NAME}" -o jsonpath='{.spec.clusterIP}' 2>/dev/null)" || svc_ip=""
  fi
  LAST_OBSERVED="ServiceImport ${name}: type ${type:-none}, ips ${DERIVED_IP:-none}, derived ${DERIVED_NAME:-none} ClusterIP ${svc_ip:-none}"
  [ "${type}" = "ClusterSetIP" ] && [ -n "${DERIVED_IP}" ] && [ "${DERIVED_IP}" = "${svc_ip}" ]
}

# ready_pod_ips CLUSTER: sorted IPs of the ready echo pods.
ready_pod_ips() {
  jq -r '[.items[] | select(any(.status.conditions[]?; .type == "Ready" and .status == "True")) | .status.podIP] | sort | join(",")' \
    <<<"$(kjson "$1" -n "${E2E_DEMO_NAMESPACE}" pods -l app=echo)" 2>/dev/null
}

# slices_match CLUSTER NAME SOURCE_CLUSTER SOURCE_ID COUNT: the imported
# EndpointSlices of NAME from SOURCE_ID hold COUNT ready endpoints, the ready
# echo pod IPs of SOURCE_CLUSTER.
slices_match() {
  local got want
  got="$(jq -r '[.items[].endpoints[]? | select(.conditions.ready != false) | .addresses[0]] | sort | join(",")' \
    <<<"$(kjson "$1" -n "${E2E_DEMO_NAMESPACE}" endpointslices \
      -l "multicluster.kubernetes.io/service-name=$2,multicluster.kubernetes.io/source-cluster=$4")" 2>/dev/null)" || got=""
  want="$(ready_pod_ips "$3")"
  LAST_OBSERVED="slices from $4: ${got:-none}; ready pods in $3: ${want:-none}"
  [ -n "${got}" ] && [ "${got}" = "${want}" ] && [ "$(awk -F, '{print NF}' <<<"${got}")" = "$5" ]
}

# gds_import_ready NAME EXPECTED: "<cluster>:<ready count> ..." of the
# participating clusters of the GDS import.
gds_import_ready() {
  rest GET "${CS_PATH}/imports/${E2E_DEMO_NAMESPACE}/$1?includeEndpoints=true"
  local got
  # $i and $c are jq variables.
  # shellcheck disable=SC2016
  got="$(rest_json '.serviceImport as $i | [$i.clusters[]? | select(.participating) | .cluster as $c
      | $c + ":" + ([$i.endpoints[]? | select(.cluster == $c) | .endpoints[]? | select(.conditions.ready)] | length | tostring)] | join(" ")' 2>/dev/null)" || got="<not JSON>"
  LAST_OBSERVED="HTTP ${REST_STATUS} ${got}"
  [ "${REST_STATUS}" = "200" ] && [ "${got}" = "$2" ]
}

gds_import_absent() {  # gds_import_absent NAME
  rest GET "${CS_PATH}/imports/${E2E_DEMO_NAMESPACE}/$1"
  observe '{code, message}'
  [ "${REST_STATUS}" = "404" ]
}

# curl_derived: a request from the client pod in b to the derived ClusterIP
# answers with the name of an echo pod in a.
curl_derived() {
  local got
  got="$(kc "${E2E_CLUSTER_B}" -n "${E2E_DEMO_NAMESPACE}" exec client -- wget -qO- -T 5 "http://${DERIVED_IP}:8080/" 2>&1)" || got="failed: ${got}"
  LAST_OBSERVED="wget http://${DERIVED_IP}:8080/: $(head -c 200 <<<"${got}")"
  grep -q '^echo-' <<<"${got}"
}

POOL_DEMO_ID=""
POOL_API_ID=""

pool_id_of() {  # pool_id_of NAME: from ListPools of cluster a
  rest GET "${CS_PATH}/pools?cluster=${E2E_CLUSTER_ID_A}"
  rest_json "[.pools[]? | select(.name == \"$1\") | .poolId][0] // \"\""
}

# demo_pool_frontend: pool demo names Service frontend as its OPENAI frontend.
demo_pool_frontend() {
  rest_expect GET "${CS_PATH}/pools?model=demo-model" "" 200 \
    'any(.pools[]?; .name == "demo" and any(.frontends[]?; .role == "FRONTEND_ROLE_OPENAI" and .service.name == "frontend"))' \
    '[.pools[]? | {name, state, frontends}]'
}

drain_demo_pool() {
  POOL_DEMO_ID="$(pool_id_of demo)"
  [ -n "${POOL_DEMO_ID}" ] || { LAST_OBSERVED="pool demo not listed"; return 1; }
  rest_expect PATCH "${CS_PATH}/pools/${POOL_DEMO_ID}" \
    '{"pool":{"desiredState":"POOL_STATE_DRAINING"},"updateMask":"desiredState"}' \
    200 '.pool.desiredState == "POOL_STATE_DRAINING" and .pool.state == "POOL_STATE_DRAINING"' \
    '.pool | {poolId, desiredState, state}'
}

register_api_pool() {
  rest_expect POST "${CS_PATH}/pools" \
    "{\"pool\":{\"name\":\"api\",\"location\":{\"cluster\":\"${E2E_CLUSTER_ID_A}\",\"region\":\"${E2E_REGION}\"},\"models\":[{\"modelId\":\"api-model\"}],\"services\":[{\"namespace\":\"${E2E_DEMO_NAMESPACE}\",\"name\":\"api-svc\",\"role\":\"FRONTEND_ROLE_OPENAI\"}]},\"leaseTtl\":\"300s\"}" \
    200 '.pool.name == "api" and (.pool.poolId | length > 0)' '.pool | {poolId, name, state, services}' || return 1
  POOL_API_ID="$(rest_json .pool.poolId)"
}

deregister_api_pool() {
  rest_expect DELETE "${CS_PATH}/pools/${POOL_API_ID}" "" 200 '. == {}'
}

restart_controllers() {
  local cluster
  for cluster in "${E2E_CLUSTER_A}" "${E2E_CLUSTER_B}"; do
    kc "${cluster}" -n "${E2E_CONTROLLER_NAMESPACE}" rollout restart "deploy/${CONTROLLER_RELEASE}" >/dev/null || return 1
  done
  for cluster in "${E2E_CLUSTER_A}" "${E2E_CLUSTER_B}"; do
    kc "${cluster}" -n "${E2E_CONTROLLER_NAMESPACE}" rollout status "deploy/${CONTROLLER_RELEASE}" --timeout=180s >/dev/null 2>&1 ||
      { LAST_OBSERVED="rollout of ${CONTROLLER_RELEASE} in ${cluster} did not finish"; return 1; }
  done
}

# import_kept UID IP: the ServiceImport echo in b still has UID and IP.
import_kept() {
  local uid="$1" ip="$2"
  serviceimport_ok "${E2E_CLUSTER_B}" echo || return 1
  LAST_OBSERVED="uid ${IMPORT_UID} (was ${uid}), ip ${DERIVED_IP} (was ${ip})"
  [ "${IMPORT_UID}" = "${uid}" ] && [ "${DERIVED_IP}" = "${ip}" ]
}

# derived_gone CLUSTER: no ServiceImport echo, derived Service or slice of it.
derived_gone() {
  local slices
  object_absent "$1" "${E2E_DEMO_NAMESPACE}" serviceimport/echo || return 1
  if [ -n "${DERIVED_NAME}" ]; then
    object_absent "$1" "${E2E_DEMO_NAMESPACE}" "service/${DERIVED_NAME}" || return 1
  fi
  slices="$(kc "$1" -n "${E2E_DEMO_NAMESPACE}" get endpointslices -l multicluster.kubernetes.io/service-name=echo -o name 2>/dev/null)" || slices=""
  LAST_OBSERVED="imported slices left: ${slices:-none}"
  [ -z "${slices}" ]
}

run_phase_2() {
  local T="${E2E_TIMEOUT}" a="${E2E_CLUSTER_A}" b="${E2E_CLUSTER_B}" ida="${E2E_CLUSTER_ID_A}" idb="${E2E_CLUSTER_ID_B}"
  local uid ip
  CS_PATH="/v1/clustersets/${E2E_MCS_CLUSTERSET}"
  if [ "${E2E_DEPLOY_ONLY}" = "1" ]; then
    REMAINING_STEPS="phase 2 setup|phase 2 controllers|"
  else
    REMAINING_STEPS="phase 2 setup|phase 2 controllers|phase 2 export and import|phase 2 pool agents from labels|phase 2 scale down|phase 2 labelled pool|phase 2 API-registered pool|phase 2 controller restart|phase 2 unexport|"
  fi

  section "Phase 2 setup: clusters ${a} and ${b} with gds-mcs-controller, gds-server in ${E2E_CLUSTER}"
  setup_hub
  setup_members
  step_done

  section "Phase 2: controllers"
  expect "GDS lists clusters ${ida} and ${idb} in clusterset ${E2E_MCS_CLUSTERSET}" "${T}" clusters_are "" "${ida},${idb}" || abort
  expect "cluster ${ida} holds an ACTIVE lease" "${T}" cluster_lease_is "${ida}" LEASE_STATE_ACTIVE || true
  expect "cluster ${idb} holds an ACTIVE lease" "${T}" cluster_lease_is "${idb}" LEASE_STATE_ACTIVE || true
  expect "Lease ${LEADER_LEASE} in ${a} is held by a running controller pod" "${T}" leader_held "${a}" || true
  expect "Lease ${LEADER_LEASE} in ${b} is held by a running controller pod" "${T}" leader_held "${b}" || true
  step_done

  if [ "${E2E_DEPLOY_ONLY}" = "1" ]; then
    return 0
  fi

  section "Phase 2: export and import"
  once "echo (2 replicas) and Services in ${a}, client pod in ${b}, namespace ${E2E_DEMO_NAMESPACE} in both" apply_demo || abort
  once "label Service echo in ${a} gds.nvidia.com/export=true" label_service "${a}" echo gds.nvidia.com/export=true || abort
  expect "ServiceExport echo in ${a}: created by the controller, Valid=True, Ready=True" "${T}" \
    serviceexport_is "${a}" echo "gds-mcs-controller|Valid=True|Ready=True" || true
  expect "GDS import echo: cluster ${ida} with 2 ready endpoints" "${T}" gds_import_ready echo "${ida}:2" || true
  expect "ServiceImport echo in ${a} too: the exporting cluster imports its own export" "${T}" serviceimport_ok "${a}" echo || true
  expect "ServiceImport echo in ${b}: ClusterSetIP, spec.ips = the derived Service's ClusterIP" "${T}" serviceimport_ok "${b}" echo || abort
  expect "EndpointSlices in ${b} labelled source-cluster=${ida}: the 2 ready echo pod IPs of ${a}" "${T}" \
    slices_match "${b}" echo "${a}" "${ida}" 2 || true
  if [ "${ROUTES_OK}" = "1" ]; then
    expect "a pod in ${b} reaches echo in ${a} through the derived ClusterIP" "${T}" curl_derived || true
  else
    skip "a pod in ${b} reaches echo in ${a} through the derived ClusterIP: no routes between the pod CIDRs"
  fi
  step_done

  # Before the scale down: both echo replicas back every Service here.
  section "Phase 2: pool agents from labels"
  expect "ServiceExport echo-pylon in ${a} (role pylon, pool demo): Valid=True, Ready=True" "${T}" \
    serviceexport_is "${a}" echo-pylon "gds-mcs-controller|Valid=True|Ready=True" || true
  expect "pool demo: OPENAI agent frontend and PYLON agent echo-pylon from labels, 2 ready each, readyIngressCount 4" "${T}" \
    pool_named_is "cluster=${ida}" demo \
    "ACTIVE relay=false ready=2 ingress=4 | OPENAI:LABEL:frontend:ready=2:regs=0():live PYLON:LABEL:echo-pylon:ready=2:regs=0():live" || true
  expect "pool demo with a PYLON agent still names Service frontend as its OPENAI frontend" "${T}" demo_pool_frontend || true
  expect "ServiceExport echo-combined in ${a} (roles pool-frontend,pylon, pool combo): Valid=True, Ready=True" "${T}" \
    serviceexport_is "${a}" echo-combined "gds-mcs-controller|Valid=True|Ready=True" || true
  expect "pool combo: OPENAI and PYLON agents on Service echo-combined, its 2 ready endpoints counted once" "${T}" \
    pool_named_is "cluster=${ida}" combo \
    "ACTIVE relay=false ready=2 ingress=2 | OPENAI:LABEL:echo-combined:ready=2:regs=0():live PYLON:LABEL:echo-combined:ready=2:regs=0():live" || true
  step_done

  section "Phase 2: scale down"
  once "scale echo in ${a} to 1 replica" scale_echo 1 || true
  expect "EndpointSlices in ${b} from ${ida} drop to the 1 remaining pod" "${T}" slices_match "${b}" echo "${a}" "${ida}" 1 || true
  expect "GDS import echo: cluster ${ida} with 1 ready endpoint" "${T}" gds_import_ready echo "${ida}:1" || true
  step_done

  section "Phase 2: labelled pool"
  expect "labelled Service frontend in ${a} appears as pool demo, ACTIVE, in ListPools" "${T}" \
    pools_are "model=demo-model" "${ida}/demo=POOL_STATE_ACTIVE" || true
  expect "pool demo names Service frontend as its OPENAI frontend" "${T}" demo_pool_frontend || true
  once "UpdatePool overlay sets pool demo DRAINING" drain_demo_pool || true
  expect "ListPools reports pool demo DRAINING" "${T}" pools_are "model=demo-model" "${ida}/demo=POOL_STATE_DRAINING" || true
  expect "draining pool demo keeps ServiceExport frontend in ${a}" "${T}" \
    serviceexport_is "${a}" frontend "gds-mcs-controller|Valid=True|Ready=True" || true
  expect "draining pool demo keeps ServiceImport frontend in ${b}" "${T}" object_present "${b}" "${E2E_DEMO_NAMESPACE}" serviceimport/frontend || true
  step_done

  section "Phase 2: API-registered pool"
  if once "RegisterPool api in ${ida} with services[] = ${E2E_DEMO_NAMESPACE}/api-svc" register_api_pool; then
    expect "ServiceExport api-svc in ${a}: created for the pool, labelled with its pool id" "${T}" \
      serviceexport_is "${a}" api-svc "gds-mcs-controller|Valid=True|Ready=True|pool-id=${POOL_API_ID}" || true
    expect "ServiceImport api-svc appears in ${b}" "${T}" object_present "${b}" "${E2E_DEMO_NAMESPACE}" serviceimport/api-svc || true
    once "DeregisterPool api" deregister_api_pool || true
    expect "ServiceExport api-svc in ${a} is removed with the pool" "${T}" object_absent "${a}" "${E2E_DEMO_NAMESPACE}" serviceexport/api-svc || true
    expect "ServiceImport api-svc in ${b} is removed" "${T}" object_absent "${b}" "${E2E_DEMO_NAMESPACE}" serviceimport/api-svc || true
  fi
  step_done

  section "Phase 2: controller restart"
  serviceimport_ok "${b}" echo || true
  uid="${IMPORT_UID}"
  ip="${DERIVED_IP}"
  once "rollout restart of the controllers in ${a} and ${b}" restart_controllers || true
  expect "ServiceImport echo in ${b} keeps its UID and ClusterIP" "${T}" import_kept "${uid}" "${ip}" || true
  expect "EndpointSlices in ${b} still hold the ready echo pod of ${a}" "${T}" slices_match "${b}" echo "${a}" "${ida}" 1 || true
  expect "ServiceExport echo in ${a} stays Valid and Ready" "${T}" serviceexport_is "${a}" echo "gds-mcs-controller|Valid=True|Ready=True" || true
  expect "a restarted controller pod holds the Lease in ${a}" "${T}" leader_held "${a}" || true
  expect "cluster ${ida} lease is ACTIVE after the restart" "${T}" cluster_lease_is "${ida}" LEASE_STATE_ACTIVE || true
  if [ "${ROUTES_OK}" = "1" ]; then
    expect "a pod in ${b} still reaches echo through the derived ClusterIP" "${T}" curl_derived || true
  fi
  step_done

  section "Phase 2: unexport"
  once "remove the label gds.nvidia.com/export from Service echo in ${a}" label_service "${a}" echo gds.nvidia.com/export- || true
  expect "ServiceExport echo in ${a} is deleted" "${T}" object_absent "${a}" "${E2E_DEMO_NAMESPACE}" serviceexport/echo || true
  expect "GDS no longer has an import echo" "${T}" gds_import_absent echo || true
  expect "ServiceImport echo, its derived Service and its slices are deleted in ${b}" "${T}" derived_gone "${b}" || true
  step_done
}

# ---------------------------------------------------------------------------
# Phase 3: Stargate in a exports itself through the controller; a Pylon in b
# finds it only through GDS and registers with it. E2E_ROUTER_MODE=direct
# runs two Stargate replicas in direct router mode, single runs one.

# router_replicas: the number of Stargate replicas of E2E_ROUTER_MODE.
router_replicas() {
  if [ "${E2E_ROUTER_MODE}" = "direct" ]; then
    printf '2'
  else
    printf '1'
  fi
}

# router_hostname_template: the hostname template GDS reports for the router,
# as the chart exports it ({namespace} resolved).
router_hostname_template() {
  if [ "${E2E_ROUTER_MODE}" = "direct" ]; then
    printf '{pod_name}.%s.%s.svc.cluster.local' "${ROUTER_HEADLESS}" "${E2E_ROUTER_NAMESPACE}"
  else
    printf '%s' "${E2E_ROUTER_HOST}"
  fi
}

# router_authority POD IP: the gRPC authority Stargate advertises for a pod,
# which Pylon's router metrics are labelled with: the pod's hostname in direct
# mode, its pod IP with one replica (self-only discovery).
router_authority() {
  if [ "${E2E_ROUTER_MODE}" = "direct" ]; then
    printf '%s.%s.%s.svc.cluster.local:50071' "$1" "${ROUTER_HEADLESS}" "${E2E_ROUTER_NAMESPACE}"
  else
    printf '%s:50071' "$2"
  fi
}

router_args() {
  printf '%s\n' \
    --namespace "${E2E_ROUTER_NAMESPACE}" \
    --values "${MANIFESTS}/router-values.yaml"
  if [ "${E2E_ROUTER_MODE}" = "direct" ]; then
    printf '%s\n' --values "${MANIFESTS}/router-direct-values.yaml"
  else
    printf '%s\n' --set-string "llmRequestRouter.kubernetes.advertisedHostnameTemplate=${E2E_ROUTER_HOST}"
  fi
  printf '%s\n' \
    --set-string "llmRequestRouter.image.repository=${E2E_STARGATE_IMAGE_REPOSITORY}" \
    --set-string "llmRequestRouter.image.tag=${STARGATE_IMAGE_TAG}" \
    --set-string "llmRequestRouter.globalDiscovery.urls[0]=${GDS_GRPC_URL}" \
    --set-string "llmRequestRouter.globalDiscovery.clustersetId=${E2E_MCS_CLUSTERSET}" \
    --set-string "llmRequestRouter.globalDiscovery.clusterId=${E2E_CLUSTER_ID_A}"
}

install_router() {
  local args=() line
  while IFS= read -r line; do
    args+=("${line}")
  done < <(router_args)
  hm "${E2E_CLUSTER_A}" upgrade --install "${ROUTER_RELEASE}" "${ROUTER_CHART}" "${args[@]}" \
    --create-namespace --wait --timeout "${E2E_HELM_TIMEOUT}" >"${E2E_WORK_DIR}/helm-router.log" 2>&1
}

# ROUTER_PODS: "<pod name> <pod IP>" lines of the ready Stargate pods in a.
# ROUTER_IP: the first pod IP, for the hosts entry of E2E_ROUTER_MODE=single.
ROUTER_PODS=""
ROUTER_IP=""
PYLON_POD=""

# router_pods_ready: exactly router_replicas Stargate pods in a are ready;
# sets ROUTER_PODS and ROUTER_IP.
router_pods_ready() {
  local pods count=0
  pods="$(jq -r '.items[] | select(.metadata.deletionTimestamp == null) | select(any(.status.conditions[]?; .type == "Ready" and .status == "True")) | "\(.metadata.name) \(.status.podIP)"' \
    <<<"$(kjson "${E2E_CLUSTER_A}" -n "${E2E_ROUTER_NAMESPACE}" pods -l "${ROUTER_SELECTOR}")" 2>/dev/null)" || pods=""
  if [ -n "${pods}" ]; then
    count="$(awk 'END { print NR }' <<<"${pods}")"
  fi
  ROUTER_PODS="${pods}"
  ROUTER_IP="$(awk 'NR == 1 { print $2 }' <<<"${pods}")"
  LAST_OBSERVED="ready Stargate pods: $(tr '\n' ' ' <<<"${pods:-none}")"
  [ "${count}" = "$(router_replicas)" ]
}

# router_pods_json: ROUTER_PODS as a JSON array of {name, ip}.
router_pods_json() {
  jq -cnR '[inputs | select(length > 0) | split(" ") | {name: .[0], ip: .[1]}]' <<<"${ROUTER_PODS}"
}

router_export_ok() {
  local got
  got="$(jq -r '"\(.metadata.labels["gds.nvidia.com/managed-by"] // "-")|Valid=\([.status.conditions[]? | select(.type == "Valid") | .status][0] // "-")|Ready=\([.status.conditions[]? | select(.type == "Ready") | .status][0] // "-")"' \
    <<<"$(kjson "${E2E_CLUSTER_A}" -n "${E2E_ROUTER_NAMESPACE}" serviceexport "${ROUTER_RELEASE}")" 2>/dev/null)" || got=""
  LAST_OBSERVED="ServiceExport ${ROUTER_RELEASE}: ${got:-absent}"
  [ "${got}" = "gds-mcs-controller|Valid=True|Ready=True" ]
}

# gds_router_ok: ListRouters has the router of cluster a in direct mode with
# the chart's hostname template, dial scheme http, the gRPC port, the QUIC
# port named by the chart's gds.nvidia.com/quic-port-name annotation and the
# Service ports, and exactly the ready Stargate pods as ready replicas, each
# with the pod name as its identity.
gds_router_ok() {
  local pods
  pods="$(router_pods_json)" || return 1
  rest_expect GET "${CS_PATH}/routers" "" 200 \
    "${pods} as \$pods | any(.routers[]?; . as \$r | .cluster == \"${E2E_CLUSTER_ID_A}\" and .namespace == \"${E2E_ROUTER_NAMESPACE}\" and .name == \"${ROUTER_RELEASE}\" and .routerMode == \"ROUTER_MODE_DIRECT\" and .hostnameTemplate == \"$(router_hostname_template)\" and .dialScheme == \"http\" and .grpcPort == 50071 and .quicPort == 50072 and any(.ports[]?; .name == \"grpc\" and .port == 50071) and any(.ports[]?; .name == \"quic\" and .port == 50072 and .protocol == \"PROTOCOL_UDP\") and ([.replicas[]? | select(.ready)] | length) == (\$pods | length) and all(\$pods[]; . as \$p | any(\$r.replicas[]?; .address == \$p.ip and .ready and .identity == \$p.name)))" \
    '[.routers[]? | {cluster, namespace, name, routerMode, hostnameTemplate, dialScheme, grpcPort, quicPort, ports: [.ports[]? | "\(.name)/\(.port)/\(.protocol)"], replicas: [.replicas[]? | "\(.address)/\(.identity // "-")/ready=\(.ready // false)"]}]'
}

# gds_pylon_pool_ok: pool e2e-pylon of cluster b is ACTIVE with one PYLON
# agent from both sources: the exported Service pylon (1 ready endpoint) and
# the Pylon's own registration under its inference server id.
gds_pylon_pool_ok() {
  pool_named_is "cluster=${E2E_CLUSTER_ID_B}" e2e-pylon \
    "ACTIVE relay=false ready=0 ingress=1 | PYLON:BOTH:pylon:ready=1:regs=1(${E2E_CLUSTER_ID_B}.gds-e2e.pylon.${PYLON_POD}):live"
}

# pylon_agent_models_ok: the Pylon's registration lists its served model.
pylon_agent_models_ok() {
  rest_expect GET "${CS_PATH}/pools?cluster=${E2E_CLUSTER_ID_B}" "" 200 \
    'any(.pools[]?; .name == "e2e-pylon" and any(.agents[]?; .role == "FRONTEND_ROLE_PYLON" and any(.registrations[]?; .attributes.models == "test-model")))' \
    '[.pools[]? | select(.name == "e2e-pylon") | .agents[]? | {role, registrations: [.registrations[]? | {agentId, attributes}]}]'
}

apply_pylon() {
  local cluster="${E2E_CLUSTER_B}"
  kc "${cluster}" create namespace "${E2E_PYLON_NAMESPACE}" --dry-run=client -o yaml | kc "${cluster}" apply -f - >/dev/null || return 1
  render_manifest "${MANIFESTS}/pylon.yaml" | kc "${cluster}" -n "${E2E_PYLON_NAMESPACE}" apply -f - >/dev/null || return 1
  kc "${cluster}" -n "${E2E_PYLON_NAMESPACE}" rollout status deploy/pylon --timeout=180s >/dev/null 2>&1 ||
    { LAST_OBSERVED="deploy/pylon in ${cluster} did not roll out"; return 1; }
  # A pod of an earlier rollout can still be Running while it terminates.
  PYLON_POD="$(jq -r '[.items[] | select(.metadata.deletionTimestamp == null and .status.phase == "Running") | .metadata.name][0] // ""' \
    <<<"$(kjson "${cluster}" -n "${E2E_PYLON_NAMESPACE}" pods -l app=gds-e2e-pylon)" 2>/dev/null)" || PYLON_POD=""
  LAST_OBSERVED="pylon pod ${PYLON_POD:-none}"
  [ -n "${PYLON_POD}" ]
}

pylon_log_has() {  # pylon_log_has TEXT
  local logs
  logs="$(kc "${E2E_CLUSTER_B}" -n "${E2E_PYLON_NAMESPACE}" logs "${PYLON_POD}" -c pylon --tail=-1 2>/dev/null | decolor)" || logs=""
  LAST_OBSERVED="$(grep -F -m1 -- "$1" <<<"${logs}" || tail -n 2 <<<"${logs}" | tr '\n' ' ')"
  grep -qF -- "$1" <<<"${logs}"
}

# pylon_gauges NAME: the Pylon gauge pylon_NAME is 1 for the authority of
# every ready Stargate pod of a (router_authority) and for no other router.
# A pod listed under a second name would show up as another router. Read
# through the API server's pod proxy.
pylon_gauges() {
  local metrics series name ip
  metrics="$(kc "${E2E_CLUSTER_B}" get --raw "/api/v1/namespaces/${E2E_PYLON_NAMESPACE}/pods/${PYLON_POD}:9089/proxy/metrics" 2>/dev/null)" || metrics=""
  series="$(grep "^pylon_$1{" <<<"${metrics}" || true)"
  LAST_OBSERVED="$(tr '\n' ' ' <<<"${series:-no pylon_$1 series}")"
  [ "$(grep -c ' 1$' <<<"${series}" || true)" = "$(router_replicas)" ] || return 1
  while read -r name ip; do
    [ -n "${name}" ] || continue
    grep -qxF "pylon_$1{router=\"$(router_authority "${name}" "${ip}")\"} 1" <<<"${series}" || return 1
  done <<<"${ROUTER_PODS}"
}

# The texts of a rejected duplicate registration: the Pylon logs the gRPC
# status (AlreadyExists, "is already registered"), Stargate its warning.
readonly DUPLICATE_REGISTRATION='AlreadyExists|already registered|duplicate inference_server_id'

# no_duplicate_registrations WINDOW: waits WINDOW seconds, then neither the
# Pylon log nor the log of any Stargate pod of a has a rejected duplicate
# registration in that window. A Pylon that saw a pod under two names would
# retry the second registration every second. LAST_OBSERVED also counts the
# Pylon's lines since it started.
no_duplicate_registrations() {
  local window="$1" pylon_recent pylon_all stargate="" name ip n_recent n_all n_stargate
  sleep "${window}"
  pylon_recent="$(kc "${E2E_CLUSTER_B}" -n "${E2E_PYLON_NAMESPACE}" logs "${PYLON_POD}" -c pylon --since="${window}s" 2>/dev/null | decolor)" || pylon_recent=""
  pylon_all="$(kc "${E2E_CLUSTER_B}" -n "${E2E_PYLON_NAMESPACE}" logs "${PYLON_POD}" -c pylon --tail=-1 2>/dev/null | decolor)" || pylon_all=""
  while read -r name ip; do
    [ -n "${name}" ] || continue
    stargate="${stargate}$(kc "${E2E_CLUSTER_A}" -n "${E2E_ROUTER_NAMESPACE}" logs "${name}" --since="${window}s" 2>/dev/null | decolor)" || true
  done <<<"${ROUTER_PODS}"
  n_recent="$(grep -cE "${DUPLICATE_REGISTRATION}" <<<"${pylon_recent}" || true)"
  n_all="$(grep -cE "${DUPLICATE_REGISTRATION}" <<<"${pylon_all}" || true)"
  n_stargate="$(grep -cE "${DUPLICATE_REGISTRATION}" <<<"${stargate}" || true)"
  LAST_OBSERVED="duplicate registration lines: Pylon last ${window}s ${n_recent}, Pylon since start ${n_all}, Stargate last ${window}s ${n_stargate}"
  if [ "${n_recent}" != "0" ]; then
    LAST_OBSERVED="${LAST_OBSERVED}; $(grep -m1 -E "${DUPLICATE_REGISTRATION}" <<<"${pylon_recent}" | head -c 300)"
  fi
  [ "${n_recent}" = "0" ] && [ "${n_stargate}" = "0" ]
}

# router_has_pylon POD: Stargate pod POD in a lists model test-model with a
# registered and healthy server of cluster b. Read through the API server's
# pod proxy.
router_has_pylon() {
  local got
  got="$(kc "${E2E_CLUSTER_A}" get --raw "/api/v1/namespaces/${E2E_ROUTER_NAMESPACE}/pods/$1:8000/proxy/v1/models" 2>&1)" || got="failed: ${got}"
  LAST_OBSERVED="GET /v1/models: $(head -c 300 <<<"${got}")"
  jq -e --arg c "${E2E_CLUSTER_ID_B}" \
    'any(.models[]?; .model_id == "test-model" and any(.clusters[]?; .cluster_id == $c and .registered_servers >= 1 and .healthy_servers >= 1))' \
    <<<"${got}" >/dev/null 2>&1
}

# start_router_port_forward TARGET: port-forwards E2E_ROUTER_LOCAL_PORT to
# port 8000 of TARGET (pod/<name>) in a.
start_router_port_forward() {
  local i=0
  if curl -s -o /dev/null --max-time 2 "http://127.0.0.1:${E2E_ROUTER_LOCAL_PORT}/"; then
    LAST_OBSERVED="127.0.0.1:${E2E_ROUTER_LOCAL_PORT} is already in use; set E2E_ROUTER_LOCAL_PORT"
    return 1
  fi
  kc_exec "${E2E_CLUSTER_A}" -n "${E2E_ROUTER_NAMESPACE}" port-forward --address 127.0.0.1 "$1" \
    "${E2E_ROUTER_LOCAL_PORT}:8000" >>"${PF_LOG}" 2>&1 &
  ROUTER_PF_PID=$!
  while [ "${i}" -lt 30 ]; do
    if curl -s -o /dev/null --max-time 2 "http://127.0.0.1:${E2E_ROUTER_LOCAL_PORT}/v1/models" && kill -0 "${ROUTER_PF_PID}" 2>/dev/null; then
      return 0
    fi
    kill -0 "${ROUTER_PF_PID}" 2>/dev/null || break
    sleep 1
    i=$((i + 1))
  done
  LAST_OBSERVED="$(tail -n 3 "${PF_LOG}" | tr '\n' ' ')"
  return 1
}

stop_router_port_forward() {
  if [ -n "${ROUTER_PF_PID}" ]; then
    kill "${ROUTER_PF_PID}" 2>/dev/null || true
    wait "${ROUTER_PF_PID}" 2>/dev/null || true
    ROUTER_PF_PID=""
  fi
}

# chat_through_router: a streamed chat completion sent to the port-forwarded
# Stargate pod in a is answered by the sample backend behind the Pylon in b,
# over the reverse tunnel. Stargate needs the x-request-id, x-model and
# x-input-tokens headers the LLM gateway sets; the sample backend serves
# streams only.
chat_through_router() {
  local body="${E2E_WORK_DIR}/chat.json" got
  printf '%s' '{"model":"test-model","messages":[{"role":"user","content":"hello from gds-e2e"}],"max_tokens":8,"stream":true}' >"${body}"
  got="$(curl -sS -i -N --max-time 20 -X POST "http://127.0.0.1:${E2E_ROUTER_LOCAL_PORT}/v1/chat/completions" \
    -H 'Content-Type: application/json' -H 'x-request-id: gds-e2e-phase-3' -H 'x-model: test-model' \
    -H 'x-input-tokens: 8' --data-binary @"${body}" 2>&1 | tr -d '\r')" || got="failed: ${got}"
  LAST_OBSERVED="$(head -c 400 <<<"${got}" | tr '\n' ' ')"
  grep -qix "x-stargate-cluster-id: ${E2E_CLUSTER_ID_B}" <<<"${got}" &&
    grep -q 'chat.completion.chunk' <<<"${got}" && grep -qF 'data: [DONE]' <<<"${got}"
}

run_phase_3() {
  local T="${E2E_TIMEOUT}" a="${E2E_CLUSTER_A}" b="${E2E_CLUSTER_B}" ida="${E2E_CLUSTER_ID_A}"
  local replicas pods name ip i names=() ips=()
  replicas="$(router_replicas)"
  CS_PATH="/v1/clustersets/${E2E_MCS_CLUSTERSET}"
  if [ "${E2E_DEPLOY_ONLY}" = "1" ]; then
    REMAINING_STEPS="phase 3 setup|phase 3 router export|phase 3 pylon|"
  else
    REMAINING_STEPS="phase 3 setup|phase 3 router export|phase 3 pylon|phase 3 registration|"
  fi

  section "Phase 3 setup: Stargate in ${a} (E2E_ROUTER_MODE=${E2E_ROUTER_MODE}), Pylon in ${b}"
  setup_hub
  setup_members
  import_image "${a}" "${E2E_STARGATE_IMAGE_REPOSITORY}:${STARGATE_IMAGE_TAG}"
  import_image "${b}" "${E2E_PYLON_IMAGE_REPOSITORY}:${PYLON_IMAGE_TAG}" "${E2E_SAMPLE_IMAGE}"
  pass "images ${E2E_STARGATE_IMAGE_REPOSITORY}:${STARGATE_IMAGE_TAG} into ${a}; ${E2E_PYLON_IMAGE_REPOSITORY}:${PYLON_IMAGE_TAG} and ${E2E_SAMPLE_IMAGE} into ${b}"
  if install_router; then
    if [ "${E2E_ROUTER_MODE}" = "direct" ]; then
      pass "helm upgrade --install ${ROUTER_RELEASE} in ${a} (${replicas} replicas, direct router mode: no backend router, globalDiscovery and export enabled): ready"
    else
      pass "helm upgrade --install ${ROUTER_RELEASE} in ${a} (one replica, globalDiscovery and export enabled): ready"
    fi
  else
    fail "helm upgrade --install ${ROUTER_RELEASE} in ${a}" "$(tail -n 5 "${E2E_WORK_DIR}/helm-router.log" | tr '\n' ' ')"
    abort
  fi
  step_done

  section "Phase 3: router export"
  expect "${replicas} Stargate pod(s) in ${a} are ready" "${T}" router_pods_ready || abort
  while read -r name ip; do
    if [ -n "${name}" ]; then
      names+=("${name}")
      ips+=("${ip}")
    fi
  done <<<"${ROUTER_PODS}"
  pods="${names[*]}"
  expect "ServiceExport ${ROUTER_RELEASE} in ${a} (role router): created by the controller, Valid and Ready" "${T}" router_export_ok || true
  expect "GDS ListRouters: cluster ${ida}, routerMode ROUTER_MODE_DIRECT, hostnameTemplate $(router_hostname_template), dial scheme http, gRPC port 50071, QUIC port 50072, ports grpc and quic, ${replicas} ready replica(s) whose identities are the pods ${pods}" "${T}" gds_router_ok || abort
  step_done

  section "Phase 3: pylon"
  once "Pylon with --gds-url, --gds-pool-name and no --stargate-address, its Service pylon, and the sample backend, in ${b}" apply_pylon || abort
  expect "Pylon in ${b} learns the router seeds from GDS (log: GDS Stargate seeds changed)" "${T}" pylon_log_has "GDS Stargate seeds changed" || true
  # GDS only: these hold without routes between the pod CIDRs.
  expect "GDS pool e2e-pylon of cluster ${E2E_CLUSTER_ID_B}: ACTIVE, PYLON agent from the Service pylon and the Pylon's registration (BOTH)" "${T}" \
    gds_pylon_pool_ok || true
  expect "GDS pool e2e-pylon: the Pylon's registration lists models test-model" "${T}" pylon_agent_models_ok || true
  step_done

  if [ "${E2E_DEPLOY_ONLY}" = "1" ]; then
    return 0
  fi

  section "Phase 3: registration"
  if [ "${ROUTES_OK}" != "1" ]; then
    skip "Pylon in ${b} registers with Stargate in ${a}: no routes between the pod CIDRs, so the seeds on port 50071 of ${a}'s pod IPs are unreachable"
    step_done
    return 0
  fi
  expect "Pylon registration streams are connected to each of the ${replicas} Stargate pod(s), each under one name (pylon_registration_stream_connected)" 90 pylon_gauges registration_stream_connected || true
  expect "Pylon reverse tunnels are connected to each of the ${replicas} Stargate pod(s) (pylon_reverse_tunnel_connected)" 90 pylon_gauges reverse_tunnel_connected || true
  once "no rejected duplicate registration in the Pylon and Stargate logs over 20 s (${DUPLICATE_REGISTRATION})" no_duplicate_registrations 20 || true
  for i in "${!names[@]}"; do
    name="${names[${i}]}"
    expect "Stargate pod ${name} (${ips[${i}]}) lists test-model with a registered, healthy server of cluster ${E2E_CLUSTER_ID_B}" "${T}" router_has_pylon "${name}" || true
    if start_router_port_forward "pod/${name}"; then
      expect "a streamed chat completion sent to Stargate pod ${name} is answered by the backend behind the Pylon in ${b}" "${T}" chat_through_router || true
    else
      fail "port-forward to pod/${name} in ${a}" "${LAST_OBSERVED}"
    fi
    stop_router_port_forward
  done
  step_done
}

# ---------------------------------------------------------------------------
# Phases

phase_list() {
  printf '%s' "${E2E_PHASES}" | tr ',' ' '
}

run_phases() {
  local phase
  # shellcheck disable=SC2046
  set -- $(phase_list)
  while [ $# -gt 0 ]; do
    phase="$1"
    shift
    LATER_PHASES="$*"
    case "${phase}" in
      1) run_phase_1 ;;
      2) run_phase_2 ;;
      3) run_phase_3 ;;
    esac
  done
  LATER_PHASES=""
}

check_phases() {
  local phase
  [ -n "$(phase_list | tr -d ' ')" ] || die "E2E_PHASES is empty"
  for phase in $(phase_list); do
    case "${phase}" in
      1 | 2 | 3) ;;
      *) die "unknown phase '${phase}'; available: 1, 2, 3" ;;
    esac
  done
  case "${E2E_ROUTER_MODE}" in
    direct | single) ;;
    *) die "E2E_ROUTER_MODE must be direct or single, got '${E2E_ROUTER_MODE}'" ;;
  esac
}

# ---------------------------------------------------------------------------
# Dry run

# print_args LABEL ARGS...: one helm command line.
print_args() {
  printf '  %s' "$1"
  shift
  printf ' %q' "$@"
  printf '\n'
}

# E2E_DRY_RUN=1: renders the charts of the selected phases with their values
# and prints the commands a run would use. It touches no cluster and builds
# nothing.
dry_run() {
  command -v helm >/dev/null 2>&1 || die "helm is required"
  mkdir -p "${E2E_WORK_DIR}"
  chmod 700 "${E2E_WORK_DIR}"
  section "Dry run (E2E_DRY_RUN=1): no cluster access"
  IMAGE_TAG="e2e-dryrun"
  CONTROLLER_IMAGE_TAG="e2e-dryrun"
  STARGATE_IMAGE_TAG="e2e-dryrun"
  PYLON_IMAGE_TAG="e2e-dryrun"
  GDS_GRPC_URL="http://hub-node:30090"
  ROUTER_IP="10.44.0.10"
  local args=() line phase mcs=0 llm=0 manifest
  for phase in $(phase_list); do
    case "${phase}" in
      2) mcs=1 ;;
      3) mcs=1 llm=1 ;;
    esac
  done

  manifest="${E2E_WORK_DIR}/dry-run-${SERVER_RELEASE}.yaml"
  while IFS= read -r line; do
    args+=("${line}")
  done < <(server_args)
  helm template "${SERVER_RELEASE}" "${SERVER_CHART}" "${args[@]}" >"${manifest}" ||
    die "helm template ${SERVER_RELEASE} failed"
  printf 'Rendered %s\n' "${manifest}"
  printf '\nA run with these settings, phases %s:\n' "$(phase_list)"
  printf '  docker network create %s (when missing)\n' "${E2E_NETWORK}"
  printf '  k3d cluster create %s --network %s ... (only when it does not exist and E2E_CREATE_CLUSTERS=1)\n' "${E2E_CLUSTER}" "${E2E_NETWORK}"
  printf '  (cd %s && %s build %s %s.tar), then docker load as %s:e2e-<image id>\n' "${REPO_ROOT}" "${BAZEL}" "${BAZEL_FLAGS}" "${SERVER_TARGET}" "${E2E_IMAGE_REPOSITORY}"
  printf '  k3d image import -c %s %s:e2e-<image id>\n' "${E2E_CLUSTER}" "${E2E_IMAGE_REPOSITORY}"
  print_args "helm upgrade --install ${SERVER_RELEASE} ${SERVER_CHART}" "${args[@]}" --create-namespace --wait
  if [ "${mcs}" = "1" ]; then
    args=()
    while IFS= read -r line; do
      args+=("${line}")
    done < <(controller_args "${E2E_CLUSTER_ID_A}")
    manifest="${E2E_WORK_DIR}/dry-run-${CONTROLLER_RELEASE}.yaml"
    helm template "${CONTROLLER_RELEASE}" "${CONTROLLER_CHART}" "${args[@]}" >"${manifest}" ||
      die "helm template ${CONTROLLER_RELEASE} failed"
    printf 'Rendered %s\n' "${manifest}"
    printf '  k3d cluster create %s --network %s --k3s-arg --cluster-cidr=%s@server:* --k3s-arg --service-cidr=%s@server:*\n' \
      "${E2E_CLUSTER_A}" "${E2E_NETWORK}" "${E2E_POD_CIDR_A}" "${E2E_SERVICE_CIDR_A}"
    printf '  k3d cluster create %s --network %s --k3s-arg --cluster-cidr=%s@server:* --k3s-arg --service-cidr=%s@server:*\n' \
      "${E2E_CLUSTER_B}" "${E2E_NETWORK}" "${E2E_POD_CIDR_B}" "${E2E_SERVICE_CIDR_B}"
    if [ "${E2E_CROSS_CLUSTER_ROUTES}" = "1" ]; then
      printf '  docker exec k3d-%s-server-0 ip route replace <pod CIDR of each node of %s> via <its address on %s>, and back\n' \
        "${E2E_CLUSTER_A}" "${E2E_CLUSTER_B}" "${E2E_NETWORK}"
    fi
    printf '  (cd %s && %s build %s %s.tar), then docker load as %s:e2e-<image id>\n' "${REPO_ROOT}" "${BAZEL}" "${BAZEL_FLAGS}" "${CONTROLLER_TARGET}" "${E2E_CONTROLLER_IMAGE_REPOSITORY}"
    print_args "helm upgrade --install ${CONTROLLER_RELEASE} ${CONTROLLER_CHART}" "${args[@]}" --create-namespace --wait
    printf '  (and the same in %s with cluster.id=%s; gds.urls[0] is the hub node address and gRPC NodePort)\n' "${E2E_CLUSTER_B}" "${E2E_CLUSTER_ID_B}"
  fi
  if [ "${llm}" = "1" ]; then
    args=()
    while IFS= read -r line; do
      args+=("${line}")
    done < <(router_args)
    manifest="${E2E_WORK_DIR}/dry-run-${ROUTER_RELEASE}.yaml"
    helm template "${ROUTER_RELEASE}" "${ROUTER_CHART}" "${args[@]}" >"${manifest}" ||
      die "helm template ${ROUTER_RELEASE} failed"
    printf 'Rendered %s (E2E_ROUTER_MODE=%s, %s replica(s), hostname template %s)\n' "${manifest}" "${E2E_ROUTER_MODE}" "$(router_replicas)" "$(router_hostname_template)"
    print_args "helm upgrade --install ${ROUTER_RELEASE} ${ROUTER_CHART}" "${args[@]}" --create-namespace --wait
    manifest="${E2E_WORK_DIR}/dry-run-pylon.yaml"
    render_manifest "${MANIFESTS}/pylon.yaml" >"${manifest}"
    printf 'Rendered %s\n' "${manifest}"
    printf '  kubectl -n %s apply -f %s   (in %s)\n' "${E2E_PYLON_NAMESPACE}" "${manifest}" "${E2E_CLUSTER_B}"
  fi
  printf '\nImages and arguments in the rendered manifests:\n'
  grep -hE -- '^ *image: |^ *- "?--' "${E2E_WORK_DIR}"/dry-run-*.yaml || true
}

# ---------------------------------------------------------------------------
# Main

print_access() {
  section "Deployed"
  cat <<EOF
gds-server stays installed in k3d cluster ${E2E_CLUSTER}, namespace ${E2E_NAMESPACE}.

  kubectl --kubeconfig $(kubeconfig_of "${E2E_CLUSTER}") --context $(context_of "${E2E_CLUSTER}") \\
    -n ${E2E_NAMESPACE} port-forward svc/${SERVER_RELEASE} ${E2E_HTTP_LOCAL_PORT}:8080 ${E2E_ADMIN_LOCAL_PORT}:9091

  curl -s ${ADMIN_URL}/readyz
  curl -s ${REST_URL}/v1/clustersets
  curl -s ${REST_URL}/v1/clustersets/${E2E_MCS_CLUSTERSET}/routers
  curl -sN ${REST_URL}${CS_PATH}/pools:watch
EOF
  if [ "${MCS_READY}" = "1" ]; then
    cat <<EOF

gds-mcs-controller runs in ${E2E_CLUSTER_A} and ${E2E_CLUSTER_B}, namespace ${E2E_CONTROLLER_NAMESPACE}:

  kubectl --kubeconfig $(kubeconfig_of "${E2E_CLUSTER_A}") --context $(context_of "${E2E_CLUSTER_A}") get serviceexports,serviceimports -A
  kubectl --kubeconfig $(kubeconfig_of "${E2E_CLUSTER_B}") --context $(context_of "${E2E_CLUSTER_B}") get serviceexports,serviceimports -A
EOF
  fi
  cat <<EOF

Remove everything with E2E_CLEANUP_ONLY=1 $0 and the same E2E_CLUSTER and
E2E_WORK_DIR (deletes only clusters and networks run.sh created).
EOF
}

main() {
  check_phases
  if [ "${E2E_DRY_RUN}" = "1" ]; then
    dry_run
    return
  fi
  if [ "${E2E_DEPLOY_ONLY}" = "1" ] && [ "${E2E_CLEANUP}" = "1" ]; then
    die "E2E_DEPLOY_ONLY=1 leaves everything installed and cannot be combined with E2E_CLEANUP=1"
  fi
  check_cluster_name "${E2E_CLUSTER}"
  check_cluster_name "${E2E_CLUSTER_A}"
  check_cluster_name "${E2E_CLUSTER_B}"
  check_network_name "${E2E_NETWORK}"
  if [ "${E2E_CLEANUP_ONLY}" = "1" ]; then
    mkdir -p "${E2E_WORK_DIR}"
    cleanup
    return
  fi
  preflight
  # shellcheck disable=SC2046
  build_images $(phase_list)
  if [ "${E2E_BUILD_ONLY}" = "1" ]; then
    log "images built and loaded into Docker (E2E_BUILD_ONLY=1); run with E2E_SKIP_BUILD=1 to use them"
    return
  fi
  run_phases

  if [ "${FAIL_COUNT}" -gt 0 ]; then
    dump_diagnostics
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
