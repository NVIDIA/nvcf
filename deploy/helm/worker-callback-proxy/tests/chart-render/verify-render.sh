#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Usage:
#   bash tests/chart-render/verify-render.sh

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHART="${ROOT_DIR}/worker-callback-proxy"
VALUES="${ROOT_DIR}/../../../tools/ci/helm-validate-values/worker-callback-proxy.yaml"

render() {
  helm template worker-callback-proxy "${CHART}" --namespace nvcf -f "${VALUES}" "$@"
}

fail() {
  echo "FAILED: $*" >&2
  exit 1
}

assert_contains() {
  grep -F -q -- "$2" <<<"$1" || fail "expected render to contain: $2"
}

assert_kind_count() {
  local rendered="$1" kind="$2" expected="$3" actual
  actual="$(grep -c "^kind: ${kind}$" <<<"${rendered}" || true)"
  [[ "${actual}" -eq "${expected}" ]] || fail "expected ${expected} ${kind}, found ${actual}"
}

assert_render_fails() {
  local expected="$1" err
  shift
  if err="$(render "$@" 2>&1)"; then
    fail "expected render to fail with: ${expected}"
  fi
  grep -F -q -- "${expected}" <<<"${err}" || fail "expected error '${expected}', got: ${err}"
}

rendered="$(render)"
assert_kind_count "${rendered}" Deployment 1
assert_kind_count "${rendered}" Service 1
assert_kind_count "${rendered}" ConfigMap 1
assert_kind_count "${rendered}" Certificate 2
assert_kind_count "${rendered}" PodDisruptionBudget 1
assert_contains "${rendered}" '- "*.grpc-worker.example.com"'
assert_contains "${rendered}" '- "*.grpc-headless.nvcf.svc.cluster.local"'
assert_contains "${rendered}" 'regex: '"'"'^\d+-\d+-\d+-\d+\.invocation\.nvcf\.svc\.cluster\.local:8080$'"'"
assert_contains "${rendered}" 'domain ~= "grpc-worker.example.com"'
assert_contains "${rendered}" 'drain_listeners?graceful'
assert_contains "${rendered}" '- gradual'
assert_contains "${rendered}" 'readOnlyRootFilesystem: true'
assert_contains "${rendered}" 'socket_address: { protocol: TCP, address: "127.0.0.1", port_value: 9901 }'
assert_contains "${rendered}" '- match: { path: "/ready" }'
assert_contains "${rendered}" 'port: health'

rendered="$(render --set-string workerCallbackProxy.tls.frontend.existingSecret=worker-tls)"
assert_kind_count "${rendered}" Certificate 1
assert_contains "${rendered}" 'secretName: worker-tls'

rendered="$(render --set workerCallbackProxy.replicaCount=1)"
assert_kind_count "${rendered}" PodDisruptionBudget 0

rendered="$(render --set workerCallbackProxy.grpcProxy.enabled=false)"
assert_kind_count "${rendered}" Certificate 0

assert_render_fails "workerCallbackProxy.workerDomain is required" \
  --set-string workerCallbackProxy.workerDomain=
assert_render_fails "must be a lowercase DNS name" \
  --set-string workerCallbackProxy.workerDomain=Worker
assert_render_fails "enable grpcProxy, invocation, or both" \
  --set workerCallbackProxy.grpcProxy.enabled=false \
  --set workerCallbackProxy.invocation.enabled=false

echo "ok: worker-callback-proxy render checks passed"
