#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

ROOT="${NVSNAP_LOCAL_ROOT:-/tmp/nvsnap-local}"
LIB_DIR="${ROOT}/nvsnap-lib"
RUN_DIR="${ROOT}/run"
CHECKPOINT_DIR="${ROOT}/checkpoints"
BUNDLE_DIR="${ROOT}/criu-bundle"
AGENT_BIN="${ROOT}/nvsnap-agent"

IMAGE="${NVSNAP_LOCAL_IMAGE:-nvsnap-uvloop-test:local}"
SRC_CONTAINER="${NVSNAP_LOCAL_CONTAINER:-nvsnap-uvloop-src}"
RESTORE_CONTAINER="${NVSNAP_LOCAL_RESTORE_CONTAINER:-nvsnap-uvloop-restore}"

AGENT_PORT="${NVSNAP_AGENT_PORT:-8081}"
CONTAINERD_SOCKET="${NVSNAP_CONTAINERD_SOCKET:-/run/containerd/containerd.sock}"
DOCKER_HOST_DEFAULT="unix:///var/run/docker.sock"
export DOCKER_HOST="${NVSNAP_DOCKER_HOST:-$DOCKER_HOST_DEFAULT}"

usage() {
  cat <<EOF
Usage: $0 <command>

Commands:
  build         Build uvloop test image + intercept lib
  start         Start uvloop test container
  agent         Build + start local agent
  checkpoint    Trigger checkpoint (cleans checkpoints first)
  restore       Restore latest checkpoint into new container
  logs          Show container logs
  cleanup       Stop container + agent + remove local dirs

Env overrides:
  NVSNAP_LOCAL_ROOT       (default: /tmp/nvsnap-local)
  NVSNAP_LOCAL_IMAGE      (default: nvsnap-uvloop-test:local)
  NVSNAP_LOCAL_CONTAINER  (default: nvsnap-uvloop-src)
  NVSNAP_AGENT_PORT       (default: 8081)
  NVSNAP_CONTAINERD_SOCKET (default: /run/containerd/containerd.sock)
  NVSNAP_CONTAINERD_NAMESPACE (default: auto-detect)
EOF
  exit 1
}

ensure_dirs() {
  mkdir -p "${LIB_DIR}" "${RUN_DIR}" "${CHECKPOINT_DIR}"
}

detect_namespace() {
  local ns="${NVSNAP_CONTAINERD_NAMESPACE:-}"
  if [[ -n "${ns}" ]]; then
    echo "${ns}"
    return
  fi
  if command -v ctr >/dev/null 2>&1; then
    if ctr namespaces list 2>/dev/null | awk '{print $1}' | grep -q "^moby$"; then
      echo "moby"
      return
    fi
    if ctr namespaces list 2>/dev/null | awk '{print $1}' | grep -q "^default$"; then
      echo "default"
      return
    fi
  fi
  echo "k8s.io"
}

build_intercept() {
  echo "Building intercept library (ubuntu:22.04 toolchain)..."
  "${REPO_ROOT}/scripts/build-intercept-lib-local.sh" "${LIB_DIR}"
}

build_image() {
  echo "Building uvloop test image..."
  docker build -f "${REPO_ROOT}/tests/uvloop-mp-test/Dockerfile.pip" -t "${IMAGE}" "${REPO_ROOT}"
}

cmd_build() {
  ensure_dirs
  build_intercept
  build_image
  echo "Build complete."
}

cmd_start() {
  ensure_dirs
  rm -f "${LIB_DIR}/.restored" "${LIB_DIR}/.force_uvloop_fork" "${LIB_DIR}/.debug_io_uring" "${LIB_DIR}/uvloop_loops."*".json" "${LIB_DIR}/uvloop_loops.json" 2>/dev/null || true
  rm -f "${RUN_DIR}/.restored" "${RUN_DIR}/.force_uvloop_fork" "${RUN_DIR}/.debug_io_uring" "${RUN_DIR}/uvloop_loops."*".json" "${RUN_DIR}/uvloop_loops.json" 2>/dev/null || true
  docker rm -f "${SRC_CONTAINER}" >/dev/null 2>&1 || true
  echo "Starting uvloop test container..."
  docker run -d --name "${SRC_CONTAINER}" --privileged \
    -p 8000:8000 \
    -e LD_PRELOAD=/nvsnap-lib/libnvsnap_intercept.so \
    -e NVSNAP_QUIESCE_SIGNALS=1 \
    -e NVSNAP_QUIESCE_METADATA_ONLY=1 \
    -v "${LIB_DIR}:/nvsnap-lib" \
    -v "${RUN_DIR}:/var/run/nvsnap" \
    "${IMAGE}" >/dev/null
  echo "Container started: ${SRC_CONTAINER}"
}

cmd_agent() {
  ensure_dirs
  if [[ ! -S "${CONTAINERD_SOCKET}" ]]; then
    echo "ERROR: containerd socket not found at ${CONTAINERD_SOCKET}"
    exit 1
  fi

  local ns
  ns="$(detect_namespace)"
  echo "Using containerd namespace: ${ns}"

  echo "Building agent..."
  (cd "${REPO_ROOT}" && go build -o "${AGENT_BIN}" ./cmd/agent)

  if [[ -f "${ROOT}/agent.pid" ]]; then
    kill "$(cat "${ROOT}/agent.pid")" >/dev/null 2>&1 || true
  fi

  echo "Starting agent on :${AGENT_PORT}..."
  "${AGENT_BIN}" \
    --listen ":${AGENT_PORT}" \
    --log-level debug \
    --containerd-socket "${CONTAINERD_SOCKET}" \
    --containerd-namespace "${ns}" \
    --checkpoint-dir "${CHECKPOINT_DIR}" \
    --criu-path "${CRIU_PATH:-/usr/local/sbin/criu}" \
    --cuda-checkpoint-path "${CUDA_CHECKPOINT_PATH:-/usr/local/bin/cuda-checkpoint}" \
    > "${ROOT}/agent.log" 2>&1 &

  echo $! > "${ROOT}/agent.pid"
  sleep 1
  if ! kill -0 "$(cat "${ROOT}/agent.pid")" >/dev/null 2>&1; then
    echo "Agent failed to start. Log:"
    tail -n 20 "${ROOT}/agent.log" || true
    exit 1
  fi
  echo "Agent started (pid $(cat "${ROOT}/agent.pid"))"
}

cmd_checkpoint() {
  ensure_dirs
  echo "Cleaning checkpoints in ${CHECKPOINT_DIR}..."
  if ! rm -rf "${CHECKPOINT_DIR:?}/"* 2>/dev/null; then
    printf '%s\n' "0mMurug@" | sudo -S rm -rf "${CHECKPOINT_DIR:?}/"* || true
  fi

  local container_id
  container_id="$(docker inspect -f '{{.Id}}' "${SRC_CONTAINER}")"

  local container_name
  container_name="$(docker inspect -f '{{.Name}}' "${SRC_CONTAINER}" | sed 's,^/,,')"
  echo "Triggering checkpoint for container ${container_id:0:12} (${container_name})..."
  curl -s -X POST "http://127.0.0.1:${AGENT_PORT}/v1/checkpoint" \
    -H "Content-Type: application/json" \
    -d "{\"namespace\":\"local\",\"containerName\":\"${container_name}\",\"containerId\":\"${container_id}\"}"
  echo ""
}

cmd_logs() {
  docker logs --tail 200 "${SRC_CONTAINER}"
}

latest_checkpoint_id() {
  if [[ -n "${NVSNAP_CHECKPOINT_ID:-}" ]]; then
    echo "${NVSNAP_CHECKPOINT_ID}"
    return
  fi
  local latest
  latest="$(ls -1dt "${CHECKPOINT_DIR}/"* 2>/dev/null | head -n1 || true)"
  if [[ -z "${latest}" ]]; then
    echo ""
    return
  fi
  basename "${latest}"
}

cmd_restore() {
  ensure_dirs
  local checkpoint_id
  checkpoint_id="$(latest_checkpoint_id)"
  if [[ -z "${checkpoint_id}" ]]; then
    echo "ERROR: no checkpoints found in ${CHECKPOINT_DIR}"
    exit 1
  fi

  docker rm -f "${RESTORE_CONTAINER}" >/dev/null 2>&1 || true
  # Stop source container to free port 8000.
  docker rm -f "${SRC_CONTAINER}" >/dev/null 2>&1 || true

  echo "Restoring checkpoint ${checkpoint_id} into ${RESTORE_CONTAINER}..."
  local force_fork="${NVSNAP_FORCE_UVLOOP_FORK:-1}"
  docker run -d --name "${RESTORE_CONTAINER}" --privileged \
    -p 8000:8000 \
    -e CRIU_BUNDLE_PATH=/nvsnap \
    -e CHECKPOINT_PATH=/checkpoints \
    -e CHECKPOINT_ID="${checkpoint_id}" \
    -e NVSNAP_FORCE_UVLOOP_FORK="${force_fork}" \
    -e NVSNAP_LOG_FILE=/var/run/nvsnap/nvsnap.log \
    -e NVSNAP_LOG_LEVEL=1 \
    -e NVSNAP_DEBUG_IO_URING=1 \
    -v "${REPO_ROOT}/bin/criu-bundle:/nvsnap:ro" \
    -v "${CHECKPOINT_DIR}:/checkpoints" \
    -v "${LIB_DIR}:/nvsnap-lib" \
    -v "${RUN_DIR}:/var/run/nvsnap" \
    "${IMAGE}" /nvsnap/restore-entrypoint >/dev/null

  echo "Restore container started: ${RESTORE_CONTAINER}"
}

cmd_cleanup() {
  if [[ -f "${ROOT}/agent.pid" ]]; then
    kill "$(cat "${ROOT}/agent.pid")" >/dev/null 2>&1 || true
    rm -f "${ROOT}/agent.pid"
  fi
  docker rm -f "${SRC_CONTAINER}" >/dev/null 2>&1 || true
  docker rm -f "${RESTORE_CONTAINER}" >/dev/null 2>&1 || true
  rm -rf "${ROOT}"
  echo "Cleaned ${ROOT}"
}

case "${1:-}" in
  build) cmd_build ;;
  start) cmd_start ;;
  agent) cmd_agent ;;
  checkpoint) cmd_checkpoint ;;
  restore) cmd_restore ;;
  logs) cmd_logs ;;
  cleanup) cmd_cleanup ;;
  *) usage ;;
esac
