#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="nats-auth-callout-image-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "nats-auth-callout-image-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"
cat >"$environment_file" <<'YAML'
global:
  image:
    registry: registry.example.com
    repository: example/nvcf
YAML

values_file="$work_dir/nats-auth-callout-values.yaml"
HELMFILE_ENV="$environment_name" \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
    --environment default \
    --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
    --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
    --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
    --selector name=nats-auth-callout-service \
    write-values \
    --output-file-template "$values_file" >/dev/null ||
  fail "helmfile could not render the NATS auth-callout values"

expected_repository="registry.example.com/example/nvcf/nvcf-nats-auth-callout-service"
actual_repository="$(yq -r '.image.repository // ""' "$values_file")"
test "$actual_repository" = "$expected_repository" ||
  fail "root image.repository is $actual_repository, expected $expected_repository"

manifest_file="$work_dir/nats-auth-callout-manifest.yaml"
helm template nats-auth-callout-service \
  "$stack_dir/../../helm/nats-auth-callout" \
  --namespace nats-system \
  --values "$values_file" \
  --show-only templates/deployment.yaml >"$manifest_file" ||
  fail "NATS auth-callout chart did not render"

actual_image="$(yq -r '.spec.template.spec.containers[0].image // ""' "$manifest_file")"
expected_image="$expected_repository:0.8.3"
test "$actual_image" = "$expected_image" ||
  fail "rendered image is $actual_image, expected $expected_image"

echo "nats-auth-callout-image-wiring: ok"
