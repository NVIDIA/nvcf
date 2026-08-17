#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="$(cd "$stack_dir/../../.." && pwd)"
work_dir="$(mktemp -d)"
test_stacks_dir="$work_dir/stacks"
test_stack_dir="$test_stacks_dir/self-managed"
test_observability_dir="$test_stacks_dir/observability"
environment_name="openbao-network-policy-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
rendered_values="$work_dir/openbao-values.yaml"
full_stack_releases="$work_dir/full-stack-releases.json"
nvcf_ui_manifest="$work_dir/nvcf-ui-manifest.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "openbao-network-policy-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir" "$test_observability_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
cp -R "$stack_dir/../observability"/. "$test_observability_dir"
printf '{}\n' >"$secrets_file"

cat >"$environment_file" <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
openbao:
  networkPolicy:
    enabled: false
    clients:
      certManager:
        namespace: security-cert-manager
      nvcf:
        namespace: control-plane
EOF

cp "$environment_file" "$test_observability_dir/environments/$environment_name.yaml"

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
    --selector name=openbao-server \
    write-values \
    --output-file-template "$rendered_values" \
    >/dev/null

HELMFILE_ENV="$environment_name" \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_stack_dir/helmfile.d" \
    --environment default \
    --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
    --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
    --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
    list \
    --skip-charts \
    --output json \
    >"$full_stack_releases"

enabled=$(yq -r '.openbao.networkPolicy.enabled' "$rendered_values")
cert_manager_namespace=$(yq -r '.openbao.networkPolicy.clients.certManager.namespace' "$rendered_values")
nvcf_namespace=$(yq -r '.openbao.networkPolicy.clients.nvcf.namespace' "$rendered_values")

[[ "$enabled" == "false" ]] || fail "enabled override did not reach the chart values"
[[ "$cert_manager_namespace" == "security-cert-manager" ]] || fail "cert-manager namespace override did not reach the chart values"
[[ "$nvcf_namespace" == "control-plane" ]] || fail "nvcf namespace override did not reach the chart values"

stage_order=$(find "$test_stack_dir/helmfile.d" -maxdepth 1 -type f -name '*.yaml.gotmpl' -exec basename {} \; | LC_ALL=C sort | paste -sd, -)
expected_stage_order='00-observability-infrastructure.yaml.gotmpl,01-dependencies.yaml.gotmpl,02-core.yaml.gotmpl,03-observability.yaml.gotmpl'
[[ "$stage_order" == "$expected_stage_order" ]] || fail "unexpected Helmfile stage order: $stage_order"

dependencies_file="$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl"
core_file="$test_stack_dir/helmfile.d/02-core.yaml.gotmpl"
openbao_release=$(sed -n '/^  - name: openbao-server/,/^  - name: cassandra/p' "$dependencies_file")
grep -q '^    namespace: vault-system$' <<<"$openbao_release" || fail "OpenBao is not installed in vault-system"
grep -q '^    needs:$' <<<"$openbao_release" || fail "OpenBao release has no dependency ordering"
grep -q '^      - nats-system/nats$' <<<"$openbao_release" || fail "OpenBao is not ordered after NATS"
grep -q '^  - name: llm-request-router$' "$core_file" || fail "LLM migration owner is not in the core stage"
grep -q '^  - name: nvcf-ui$' "$core_file" || fail "NVCF UI migration owner is not in the core stage"

openbao_release_count=$(yq -r '[.[] | select(.name == "openbao-server" and .namespace == "vault-system")] | length' "$full_stack_releases")
cert_manager_release_count=$(yq -r '[.[] | select(.name == "cert-manager" and .namespace == "cert-manager")] | length' "$full_stack_releases")
api_client_release_count=$(yq -r '[.[] | select(.name == "api-keys" or .name == "admin-issuer-proxy" or .name == "ess-api" or .name == "api" or .name == "nvct-api" or .name == "invocation-service" or .name == "grpc-proxy" or .name == "notary-service" or .name == "ratelimiter" or .name == "function-autoscaler" or .name == "llm-request-router" or .name == "llm-api-gateway" or .name == "nvcf-ui")] | length' "$full_stack_releases")
[[ "$openbao_release_count" == "1" ]] || fail "full stack does not contain the expected OpenBao release"
[[ "$cert_manager_release_count" == "1" ]] || fail "full stack does not contain the expected cert-manager release"
[[ "$api_client_release_count" == "13" ]] || fail "full stack release inventory does not match the API client selectors"

helm template nvcf-ui "$repo_root/src/uis/nvcf-ui/helm" \
  --namespace nvcf-ui \
  --set-string nvcfUi.fullnameOverride=nvcf-ui \
  >"$nvcf_ui_manifest"
nvcf_ui_migration_namespace=$(yq -rN 'select(.kind == "Job" and .metadata.name == "nvcf-ui-openbao-migrations") | .metadata.namespace' "$nvcf_ui_manifest")
nvcf_ui_migration_instance=$(yq -rN 'select(.kind == "Job" and .metadata.name == "nvcf-ui-openbao-migrations") | .spec.template.metadata.labels."app.kubernetes.io/instance"' "$nvcf_ui_manifest")
nvcf_ui_migration_component=$(yq -rN 'select(.kind == "Job" and .metadata.name == "nvcf-ui-openbao-migrations") | .spec.template.metadata.labels."app.kubernetes.io/component"' "$nvcf_ui_manifest")
[[ "$nvcf_ui_migration_namespace" == "vault-system" ]] || fail "NVCF UI migration Job is not in the OpenBao namespace"
[[ "$nvcf_ui_migration_instance" == "nvcf-ui" ]] || fail "NVCF UI migration Job does not match the allowed release instance"
[[ "$nvcf_ui_migration_component" == "openbao-migrations" ]] || fail "NVCF UI migration Job does not match the allowed migration component"

echo "openbao-network-policy-wiring: all checks passed"
