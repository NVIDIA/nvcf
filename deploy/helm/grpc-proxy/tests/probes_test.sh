#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Liveness must not use the client-port /health, which checks NATS. A shared
# dependency outage would otherwise restart every grpc-proxy pod.

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$root/grpc-proxy"
values="$root/../../../tools/ci/helm-validate-values/grpc-proxy.yaml"

fail() {
  echo "probes_test.sh: $*" >&2
  exit 1
}

assert_probes() {
  local mode="$1" kind="$2"
  shift 2
  local rendered actual
  rendered="$(helm template grpc-proxy "$chart" -f "$values" "$@" \
    --set-string grpcproxy.image.registry=registry.example.test \
    --set-string grpcproxy.image.repository=nvcf-grpc-proxy)"
  actual="$(yq ea -r "select(.kind == \"$kind\") | .spec.template.spec.containers[0]
    | .livenessProbe.httpGet.port + \" \" + .livenessProbe.httpGet.path
      + \" | \" + .readinessProbe.httpGet.port + \" \" + .readinessProbe.httpGet.path" - <<<"$rendered")"
  [[ "$actual" == "metrics /health | client /health" ]] ||
    fail "$mode: expected liveness 'metrics /health' and readiness 'client /health', got '$actual'"
}

assert_probes deployment Deployment
assert_probes daemonset DaemonSet -f "$root/values.daemonset.yaml"

echo "probes_test.sh: liveness uses the admin port, readiness uses the client port"
