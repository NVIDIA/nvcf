#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Verifies that nats.container.merge.resources in environments/<env>.yaml
# reaches the NATS release and that nothing is rendered when it is unset.

set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="nats-container-resources-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
values_file="$work_dir/nats-values.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "nats-container-resources: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

write_values() {
  HELMFILE_ENV="$environment_name" \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
      --selector name=nats \
      write-values \
      --output-file-template "$values_file" >/dev/null
}

cat >"$environment_file" <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
nats:
  enabled: true
EOF

write_values

merge="$(yq -r '.nats.container.merge' "$values_file")"
[[ "$merge" == "null" ]] ||
  fail "expected no container merge block, got ${merge:-<none>}"

image_tag="$(yq -r '.nats.container.image.tag // ""' "$values_file")"
[[ -n "$image_tag" ]] ||
  fail "expected a non-empty NATS image tag"

cat >>"$environment_file" <<'EOF'
  container:
    merge:
      resources:
        requests:
          cpu: "2"
          memory: 2Gi
        limits:
          cpu: "2"
EOF

write_values

limits_cpu="$(yq -r '.nats.container.merge.resources.limits.cpu' "$values_file")"
[[ "$limits_cpu" == "2" ]] ||
  fail "expected CPU limit 2, got ${limits_cpu:-<none>}"

requests_memory="$(yq -r '.nats.container.merge.resources.requests.memory' "$values_file")"
[[ "$requests_memory" == "2Gi" ]] ||
  fail "expected memory request 2Gi, got ${requests_memory:-<none>}"

limits_memory="$(yq -r '.nats.container.merge.resources.limits.memory' "$values_file")"
[[ "$limits_memory" == "null" ]] ||
  fail "expected no memory limit, got ${limits_memory:-<none>}"

security_context="$(yq -r '.nats.container.merge.securityContext' "$values_file")"
[[ "$security_context" == "null" ]] ||
  fail "expected no security context, got ${security_context:-<none>}"

cat >"$environment_file" <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
nats:
  enabled: true
  container:
    merge:
      securityContext:
        runAsNonRoot: true
      resources:
        requests:
          cpu: "2"
          memory: 2Gi
        limits:
          cpu: "2"
EOF

write_values

run_as_non_root="$(yq -r '.nats.container.merge.securityContext.runAsNonRoot' "$values_file")"
[[ "$run_as_non_root" == "true" ]] ||
  fail "expected runAsNonRoot true, got ${run_as_non_root:-<none>}"

limits_cpu="$(yq -r '.nats.container.merge.resources.limits.cpu' "$values_file")"
[[ "$limits_cpu" == "2" ]] ||
  fail "expected CPU limit 2 with security context, got ${limits_cpu:-<none>}"

echo "nats-container-resources: OK"
