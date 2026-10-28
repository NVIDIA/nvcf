#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

set -euo pipefail

# Run the real Makefile in an isolated directory. Commands resolved through
# FAKE_BIN are test doubles, so this test never creates or changes a real cluster.
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_ROOT="$(mktemp -d)"
FAKE_BIN="${TEST_ROOT}/bin"
CALL_LOG="${TEST_ROOT}/calls.log"

cleanup() {
  rm -rf "${TEST_ROOT}"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_contains() {
  local expected="$1"
  local file="$2"
  local label="$3"

  if ! grep -Fqx "${expected}" "${file}"; then
    echo "--- ${file}" >&2
    cat "${file}" >&2
    fail "${label}: missing '${expected}'"
  fi
}

# Match a substring in captured Make output. Use assert_contains above when the
# expected value must occupy a complete call-log line.
assert_output_contains() {
  local expected="$1"
  local file="$2"
  local label="$3"

  if ! grep -Fq "${expected}" "${file}"; then
    cat "${file}" >&2
    fail "${label}: missing '${expected}'"
  fi
}

# Confirm that a failed lifecycle step did not allow a later command to run.
assert_log_excludes() {
  local unexpected="$1"
  local label="$2"

  if grep -Fq "${unexpected}" "${CALL_LOG}"; then
    cat "${CALL_LOG}" >&2
    fail "${label}: unexpectedly found '${unexpected}'"
  fi
}

# Run the copied Makefile quietly in the sandbox. "$@" forwards the target and
# every variable assignment supplied by the test scenario.
run_make() {
  make --no-print-directory -s -C "${TEST_ROOT}" "$@"
}

# The copied Makefile is the unit under test. The dummy Docker config satisfies
# the start target's existence check without using real registry credentials.
mkdir -p "${FAKE_BIN}" "${TEST_ROOT}/secrets"
cp "${ROOT_DIR}/Makefile" "${TEST_ROOT}/Makefile"
printf '{}\n' >"${TEST_ROOT}/secrets/docker-config.json"
: >"${CALL_LOG}"

# Create a controllable k3d test double. The quoted heredoc defers variable
# expansion until the fake command runs. K3D_*_EXIT values simulate cluster
# state and failures, while K3D_CALL_LOG records commands and inputs.
cat >"${FAKE_BIN}/k3d" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case "$*" in
  cluster\ get\ *)
    # Exit 0 means the cluster exists; a nonzero value means it is absent.
    printf 'k3d %s\n' "$*" >>"${K3D_CALL_LOG}"
    exit "${K3D_GET_EXIT:-0}"
    ;;
  cluster\ create\ *)
    # Record the create command and every value consumed by the k3d config.
    {
      printf 'k3d %s\n' "$*"
      printf 'K3D_CLUSTER_NAME=%s\n' "${K3D_CLUSTER_NAME-}"
      printf 'CONTROL_PLANE_HTTP_PORT=%s\n' "${CONTROL_PLANE_HTTP_PORT-}"
      printf 'CONTROL_PLANE_HTTPS_PORT=%s\n' "${CONTROL_PLANE_HTTPS_PORT-}"
      printf 'CONTROL_PLANE_GRPC_PORT=%s\n' "${CONTROL_PLANE_GRPC_PORT-}"
      printf 'CONTROL_PLANE_GRPC_PROXY_PORT=%s\n' "${CONTROL_PLANE_GRPC_PROXY_PORT-}"
      printf 'CONTROL_PLANE_GRPC_WORKER_PORT=%s\n' "${CONTROL_PLANE_GRPC_WORKER_PORT-}"
      printf 'CONTROL_PLANE_LLM_GRPC_PORT=%s\n' "${CONTROL_PLANE_LLM_GRPC_PORT-}"
      printf 'CONTROL_PLANE_LLM_QUIC_PORT=%s\n' "${CONTROL_PLANE_LLM_QUIC_PORT-}"
      printf 'CONTROL_PLANE_NATS_PORT=%s\n' "${CONTROL_PLANE_NATS_PORT-}"
    } >>"${K3D_CALL_LOG}"
    exit "${K3D_CREATE_EXIT:-0}"
    ;;
  cluster\ start\ *)
    # A nonzero value simulates k3d failing to start an existing cluster.
    printf 'k3d %s\n' "$*" >>"${K3D_CALL_LOG}"
    exit "${K3D_START_EXIT:-0}"
    ;;
  kubeconfig\ merge\ *)
    printf 'k3d %s\n' "$*" >>"${K3D_CALL_LOG}"
    ;;
  *)
    echo "unexpected k3d command: $*" >&2
    exit 64
    ;;
esac
EOF

# Provide only the kubectl operations used by ensure-context. Unexpected
# operations fail the test instead of being silently accepted.
cat >"${FAKE_BIN}/kubectl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf 'kubectl %s\n' "$*" >>"${K3D_CALL_LOG}"
case "$*" in
  "config current-context")
    printf '%s\n' "${KUBECTL_CURRENT_CONTEXT-}"
    ;;
  config\ use-context\ *)
    ;;
  *)
    echo "unexpected kubectl command: $*" >&2
    exit 64
    ;;
esac
EOF

# Put the test doubles first in PATH before invoking the copied Makefile.
chmod +x "${FAKE_BIN}/k3d" "${FAKE_BIN}/kubectl"
export PATH="${FAKE_BIN}:${PATH}"
export K3D_CALL_LOG="${CALL_LOG}"

# Scenario 1: the cluster is absent and creation succeeds. Verify that
# ensure-cluster invokes k3d create with the selected name, config, and ports.
export K3D_GET_EXIT=1
export K3D_CREATE_EXIT=0
run_make ensure-cluster \
  CLUSTER_NAME=lifecycle-create \
  K3D_CONFIG_FILE=lifecycle-config.yaml \
  CONTROL_PLANE_HTTP_PORT=18080 \
  CONTROL_PLANE_HTTPS_PORT=18443 \
  CONTROL_PLANE_GRPC_PORT=19090 \
  CONTROL_PLANE_GRPC_PROXY_PORT=20081 \
  CONTROL_PLANE_GRPC_WORKER_PORT=20086 \
  CONTROL_PLANE_LLM_GRPC_PORT=25071 \
  CONTROL_PLANE_LLM_QUIC_PORT=25072 \
  CONTROL_PLANE_NATS_PORT=14222 >/dev/null

assert_contains "k3d cluster create lifecycle-create --config lifecycle-config.yaml" "${CALL_LOG}" "cluster create command"
assert_contains "K3D_CLUSTER_NAME=lifecycle-create" "${CALL_LOG}" "cluster name environment"
assert_contains "CONTROL_PLANE_HTTP_PORT=18080" "${CALL_LOG}" "HTTP port environment"
assert_contains "CONTROL_PLANE_HTTPS_PORT=18443" "${CALL_LOG}" "HTTPS port environment"
assert_contains "CONTROL_PLANE_GRPC_PORT=19090" "${CALL_LOG}" "gRPC port environment"
assert_contains "CONTROL_PLANE_GRPC_PROXY_PORT=20081" "${CALL_LOG}" "gRPC proxy port environment"
assert_contains "CONTROL_PLANE_GRPC_WORKER_PORT=20086" "${CALL_LOG}" "gRPC worker port environment"
assert_contains "CONTROL_PLANE_LLM_GRPC_PORT=25071" "${CALL_LOG}" "LLM gRPC port environment"
assert_contains "CONTROL_PLANE_LLM_QUIC_PORT=25072" "${CALL_LOG}" "LLM QUIC port environment"
assert_contains "CONTROL_PLANE_NATS_PORT=14222" "${CALL_LOG}" "NATS port environment"

# Scenario 2: the cluster is absent and k3d create fails. Verify that
# ensure-cluster returns nonzero and reports the failed cluster name.
: >"${CALL_LOG}"
create_failure_output="${TEST_ROOT}/create-failure.out"
export K3D_CREATE_EXIT=23
if run_make ensure-cluster CLUSTER_NAME=lifecycle-create-failure >"${create_failure_output}" 2>&1; then
  cat "${create_failure_output}" >&2
  fail "ensure-cluster should fail when k3d cluster create fails"
fi
assert_output_contains \
  "ERROR Failed to create k3d cluster lifecycle-create-failure." \
  "${create_failure_output}" \
  "ensure-cluster create failure"

# Scenario 3: start needs to create the cluster, but creation fails. Verify that
# start propagates the failure without attempting startup or context selection.
: >"${CALL_LOG}"
start_create_failure_output="${TEST_ROOT}/start-create-failure.out"
if run_make start CLUSTER_NAME=lifecycle-start-create-failure >"${start_create_failure_output}" 2>&1; then
  cat "${start_create_failure_output}" >&2
  fail "start should fail when cluster creation fails"
fi
assert_log_excludes "k3d cluster start" "start after create failure"
assert_log_excludes "kubectl config current-context" "context check after create failure"

# Scenario 4: the cluster already exists, but k3d start fails. Verify that start
# returns nonzero and stops before ensure-context.
: >"${CALL_LOG}"
start_failure_output="${TEST_ROOT}/start-failure.out"
export K3D_GET_EXIT=0
export K3D_START_EXIT=24
if run_make start CLUSTER_NAME=lifecycle-start-failure >"${start_failure_output}" 2>&1; then
  cat "${start_failure_output}" >&2
  fail "start should fail when k3d cluster start fails"
fi
assert_output_contains \
  "ERROR Failed to start k3d cluster lifecycle-start-failure." \
  "${start_failure_output}" \
  "cluster startup failure"
assert_log_excludes "kubectl config current-context" "context check after startup failure"

# Scenario 5: the cluster exists, starts successfully, and already has the
# expected kubectl context. Verify that the success path reaches ensure-context.
: >"${CALL_LOG}"
start_success_output="${TEST_ROOT}/start-success.out"
export K3D_START_EXIT=0
export KUBECTL_CURRENT_CONTEXT=k3d-lifecycle-start-success
run_make start CLUSTER_NAME=lifecycle-start-success >"${start_success_output}" 2>&1
assert_contains "k3d cluster start lifecycle-start-success" "${CALL_LOG}" "cluster startup command"
assert_contains "kubectl config current-context" "${CALL_LOG}" "ensure-context invocation"
assert_output_contains \
  "OK kubectl context is already set to k3d-lifecycle-start-success" \
  "${start_success_output}" \
  "successful context verification"

echo "PASS: cluster lifecycle Makefile tests"
