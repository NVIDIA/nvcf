#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="nats-auth-callout-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "nats-auth-callout-wiring: $*" >&2
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
custom_values_file="$work_dir/nats-auth-callout-custom-values.yaml"

write_values() {
  local output_file="$1"
  shift

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
    "$@" \
    --selector name=nats-auth-callout-service \
    write-values \
    --output-file-template "$output_file" >/dev/null ||
    fail "helmfile could not render the NATS auth-callout values"
}

write_values "$values_file"
write_values "$custom_values_file" \
  --state-values-set nats.authCallout.secretName=custom-auth-callout-nkeys

expected_repository="registry.example.com/example/nvcf/nvcf-nats-auth-callout-service"
actual_repository="$(yq -r '.image.repository // ""' "$values_file")"
test "$actual_repository" = "$expected_repository" ||
  fail "root image.repository is $actual_repository, expected $expected_repository"

actual_nats_url="$(yq -r '.serviceConfig.service.nats_url // ""' "$values_file")"
expected_nats_url="nats://nats.nats-system.svc.cluster.local:4222"
test "$actual_nats_url" = "$expected_nats_url" ||
  fail "NATS URL is $actual_nats_url, expected $expected_nats_url"

seed_secret_name="$(yq -r '.extraEnvs[] | select(.name == "NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY__SEED") | .valueFrom.secretKeyRef.name' "$values_file")"
test "$seed_secret_name" = "nats-auth-callout-nkeys" ||
  fail "default NKey seed Secret is $seed_secret_name, expected nats-auth-callout-nkeys"

signature_secret_name="$(yq -r '.extraEnvs[] | select(.name == "NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY__SIGNATURE") | .valueFrom.secretKeyRef.name' "$values_file")"
test "$signature_secret_name" = "nats-auth-callout-nkeys" ||
  fail "default NKey signature Secret is $signature_secret_name, expected nats-auth-callout-nkeys"

custom_seed_secret_name="$(yq -r '.extraEnvs[] | select(.name == "NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY__SEED") | .valueFrom.secretKeyRef.name' "$custom_values_file")"
test "$custom_seed_secret_name" = "custom-auth-callout-nkeys" ||
  fail "custom NKey seed Secret is $custom_seed_secret_name, expected custom-auth-callout-nkeys"

custom_signature_secret_name="$(yq -r '.extraEnvs[] | select(.name == "NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY__SIGNATURE") | .valueFrom.secretKeyRef.name' "$custom_values_file")"
test "$custom_signature_secret_name" = "custom-auth-callout-nkeys" ||
  fail "custom NKey signature Secret is $custom_signature_secret_name, expected custom-auth-callout-nkeys"

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

config_manifest_file="$work_dir/nats-auth-callout-config-manifest.yaml"
helm template nats-auth-callout-service \
  "$stack_dir/../../helm/nats-auth-callout" \
  --namespace nats-system \
  --values "$values_file" \
  --show-only templates/configmap.yaml >"$config_manifest_file" ||
  fail "NATS auth-callout ConfigMap did not render"

rendered_nats_url="$(yq -r '.data."config.yaml" | from_yaml | .service.nats_url // ""' "$config_manifest_file")"
test "$rendered_nats_url" = "$expected_nats_url" ||
  fail "rendered NATS URL is $rendered_nats_url, expected $expected_nats_url"

rendered_seed_key="$(yq -r '.spec.template.spec.containers[0].env[] | select(.name == "NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY__SEED") | .valueFrom.secretKeyRef.key' "$manifest_file")"
test "$rendered_seed_key" = "nkey_seed" ||
  fail "rendered NKey seed key is $rendered_seed_key, expected nkey_seed"

rendered_signature_key="$(yq -r '.spec.template.spec.containers[0].env[] | select(.name == "NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY__SIGNATURE") | .valueFrom.secretKeyRef.key' "$manifest_file")"
test "$rendered_signature_key" = "nkey_signature" ||
  fail "rendered NKey signature key is $rendered_signature_key, expected nkey_signature"

plugin_secret="$(yq -r '.spec.template.spec.volumes[] | select(.name == "plugin-config") | .secret.secretName' "$manifest_file")"
test "$plugin_secret" = "nats-nkeys" ||
  fail "plugin configuration Secret is $plugin_secret, expected nats-nkeys"

plugin_config_flag="$(yq -r '.spec.template.spec.containers[0].args[] | select(. == "--secrets-file")' "$manifest_file")"
test "$plugin_config_flag" = "--secrets-file" ||
  fail "plugin configuration flag is ${plugin_config_flag:-<none>}"

plugin_config_arg="$(yq -r '.spec.template.spec.containers[0].args[] | select(. == "/etc/plugin-config/plugin.yaml")' "$manifest_file")"
test "$plugin_config_arg" = "/etc/plugin-config/plugin.yaml" ||
  fail "plugin configuration path is ${plugin_config_arg:-<none>}"

echo "nats-auth-callout-wiring: ok"
