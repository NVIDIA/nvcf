#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="nats-placement-tags-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
values_file="$work_dir/nats-values.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "nats-placement-tags: $*" >&2
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

tags="$(yq -r '.nats.config.merge.server_tags | join(",")' "$values_file")"
[[ "$tags" == "dc:ncp,aws-region:ncp" ]] ||
  fail "expected dc:ncp,aws-region:ncp, got ${tags:-<none>}"

cat >>"$environment_file" <<'EOF'
  config:
    merge:
      server_tags:
        - dc:custom
        - aws-region:custom
EOF

write_values

tags="$(yq -r '.nats.config.merge.server_tags | join(",")' "$values_file")"
[[ "$tags" == "dc:custom,aws-region:custom" ]] ||
  fail "expected environment tag override, got ${tags:-<none>}"

echo "nats-placement-tags: OK"
