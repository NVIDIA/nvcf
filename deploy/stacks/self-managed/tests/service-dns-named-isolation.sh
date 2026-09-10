#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
stack_dir="$(cd "$script_dir/.." && pwd)"
repo_root="$(cd "$stack_dir/../../.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
values_dir="$work_dir/values"
manifest_dir="$work_dir/manifests"
owner="plane-a"
openbao_name="${owner}-openbao"
openbao_url="http://${openbao_name}.${owner}-vault-system.svc.cluster.local:8200"
openbao_addr="${openbao_name}.${owner}-vault-system.svc.cluster.local:8200"
openbao_host="${openbao_name}.${owner}-vault-system.svc.cluster.local"
openbao_initialize_service_account="${openbao_name}-initialize-cluster"
openbao_root_token_secret="${openbao_name}-root-token"
api_grpc_url="http://api.${owner}-nvcf.svc.cluster.local:9090"
api_grpc_addr="api.${owner}-nvcf.svc.cluster.local:9090"
nats_url="nats://nats.${owner}-nats-system.svc.cluster.local:4222"
api_keys_metadata_url="http://api-keys.${owner}-api-keys.svc.cluster.local:8080/v1/services"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "service-dns-named-isolation: $*" >&2
  exit 1
}

command -v helm >/dev/null 2>&1 || fail "helm is required"
command -v helmfile >/dev/null 2>&1 || fail "helmfile is required"
command -v python3 >/dev/null 2>&1 || fail "python3 is required"
command -v yq >/dev/null 2>&1 || fail "yq is required"

mkdir -p "$values_dir" "$manifest_dir"
cp -R "$stack_dir" "$test_stack_dir"
printf '{}\n' >"$test_stack_dir/secrets/base-secrets.yaml"

# Named mode is intentionally still blocked in the source tree while other
# object-level leak classes are fixed. This test unlocks only the temp copy so
# it can inspect the values produced by this subphase.
perl -0pi -e 's/\$namedControlPlaneObjectIdentitiesReady := false/\$namedControlPlaneObjectIdentitiesReady := true/g' \
  "$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
  "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
  "$test_stack_dir/helmfile.d/03-observability.yaml.gotmpl" \
  "$test_stack_dir/global.yaml.gotmpl"

run_helmfile() {
  HELMFILE_ENV=base \
    NVCF_CONTROL_PLANE_OWNER="$owner" \
    NVCF_ALPHA_NAMED_CONTROL_PLANE=true \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=gateway \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gateway \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=gateway \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gateway \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=gateway \
      --state-values-set addons.llm.enabled=true \
      --state-values-set addons.llm.pki.enabled=true \
      --state-values-set addons.llm.pki.allowedDomains=cluster.local \
      --state-values-set addons.lls.enabled=true \
      --state-values-set addons.lls.hmacRotation.image.tag=0.16.3 \
      --state-values-set observability.profile=control \
      --state-values-set stateMetrics.enabled=true \
      "$@"
}

render_release_values() {
  local state_file="$1"
  local release_name="$2"
  local output_file="$3"

  if ! run_helmfile \
    --file "$test_stack_dir/helmfile.d/$state_file" \
    --selector "name=$release_name" \
    write-values \
    --output-file-template "$output_file" >/dev/null; then
    fail "helmfile could not render values for $release_name"
  fi
  test -s "$output_file" ||
    fail "helmfile wrote no values for $release_name"
}

render_chart() {
  local release_name="$1"
  local chart_path="$2"
  local namespace="$3"
  local values_file="$4"
  local manifest_file="$5"

  helm template "$release_name" "$repo_root/$chart_path" \
    --namespace "$namespace" \
    --values "$values_file" >"$manifest_file"
}

assert_value() {
  local file="$1"
  local expression="$2"
  local expected="$3"
  local actual

  actual="$(yq -r "$expression" "$file")"
  test "$actual" = "$expected" ||
    fail "expected $expression in $(basename "$file") to be $expected, got $actual"
}

assert_render_contains() {
  local file="$1"
  local expression="$2"
  local expected="$3"
  local actual

  actual="$(yq -r "$expression" "$file")"
  grep -Fq -- "$expected" <<<"$actual" ||
    fail "expected rendered $(basename "$file") field $expression to contain $expected"
}

render_service() {
  local state_file="$1"
  local release_name="$2"
  local chart_path="$3"
  local namespace="$4"
  local values_file="$values_dir/$release_name-values.yaml"
  local manifest_file="$manifest_dir/$release_name.yaml"

  render_release_values "$state_file" "$release_name" "$values_file"
  render_chart "$release_name" "$chart_path" "$namespace" "$values_file" "$manifest_file"
}

render_service "02-core.yaml.gotmpl" "api-keys" "deploy/helm/api-keys-colocated/api-keys" "${owner}-api-keys"
render_service "02-core.yaml.gotmpl" "api" "deploy/helm/cloud-functions/nvcf-api" "${owner}-nvcf"
render_service "02-core.yaml.gotmpl" "nvct-api" "deploy/helm/cloud-tasks/nvct-api" "${owner}-nvcf"
render_service "02-core.yaml.gotmpl" "invocation-service" "deploy/helm/http-invocation/nvcf-invocation-service" "${owner}-nvcf"
render_service "02-core.yaml.gotmpl" "grpc-proxy" "deploy/helm/grpc-proxy/grpc-proxy" "${owner}-nvcf"
render_service "02-core.yaml.gotmpl" "ratelimiter" "deploy/helm/ratelimiter/nvcf-ratelimiter" "${owner}-nvcf"
render_service "02-core.yaml.gotmpl" "ess-api" "deploy/helm/encrypted-secret-store/ess-api" "${owner}-ess"
render_service "02-core.yaml.gotmpl" "notary-service" "deploy/helm/notary/nvcf-notary-service" "${owner}-nvcf"
render_service "02-core.yaml.gotmpl" "sis" "deploy/helm/icms/icms-api" "${owner}-sis"
render_service "02-core.yaml.gotmpl" "admin-issuer-proxy" "deploy/helm/admin-token-issuer-proxy/chart" "${owner}-api-keys"
render_service "02-core.yaml.gotmpl" "llm-request-router" "deploy/helm/llm-request-router/llm-request-router" "${owner}-nvcf"
render_service "02-core.yaml.gotmpl" "llm-api-gateway" "deploy/helm/llm-api-gateway/llm-api-gateway" "${owner}-nvcf"
render_service "03-observability.yaml.gotmpl" "function-autoscaler" "deploy/helm/function-autoscaler" "${owner}-nvcf"

assert_value "$values_dir/api-values.yaml" '.api.accountBootstrap.openbaoServiceAddress' "$openbao_addr"
assert_value "$values_dir/api-values.yaml" '.api.accountBootstrap.openbaoAudience' "$openbao_url"
assert_value "$values_dir/api-values.yaml" '.api.volumes[0].projected.sources[0].serviceAccountToken.audience' "$openbao_url"
assert_render_contains "$manifest_dir/api.yaml" \
  'select(.kind == "ConfigMap" and .metadata.name == "nvcf-api-account-bootstrap-script") | .data."account-bootstrap.sh"' \
  "OPENBAO_SERVICE_ADDR=\"$openbao_addr\""

render_release_values "01-dependencies.yaml.gotmpl" "nvcf-pki" "$values_dir/nvcf-pki-values.yaml"
assert_value "$values_dir/nvcf-pki-values.yaml" '.clusterIssuer.server' "$openbao_url"
assert_value "$values_dir/nvcf-pki-values.yaml" '.clusterIssuer.auth.serviceAccount.audience' "$openbao_url"

assert_value "$values_dir/admin-issuer-proxy-values.yaml" '.adminIssuerProxy.config.vaultAddr' "$openbao_url"
assert_value "$values_dir/admin-issuer-proxy-values.yaml" '.adminIssuerProxy.config.vaultAudience' "$openbao_url"
assert_value "$values_dir/admin-issuer-proxy-values.yaml" '.adminIssuerProxy.config.serviceMetadataURL' "$api_keys_metadata_url"
assert_value "$values_dir/invocation-service-values.yaml" '.invocation.env.NATS_PROPERTIES__NATS_ADDRESS' "$nats_url"
assert_value "$values_dir/invocation-service-values.yaml" '.invocation.env.NVCF_API_ADDRESS' "$api_grpc_url"
assert_value "$values_dir/invocation-service-values.yaml" '.invocation.env.REGIONAL_NVCF_API_GRPC_ADDRESS' "$api_grpc_url"
assert_value "$values_dir/grpc-proxy-values.yaml" '.grpcproxy.env.NVCF_FQDN_GRPC' "$api_grpc_url"
assert_value "$values_dir/ratelimiter-values.yaml" '.rateLimiter.env.NVCF_API_URL' "$api_grpc_url"
assert_value "$values_dir/ratelimiter-values.yaml" '.rateLimiter.env.OAUTH2_JWKS_URL' "$openbao_url/v1/services/ratelimiter-api/jwt/jwks"
assert_value "$values_dir/llm-api-gateway-values.yaml" '.llmApiGateway.config.requestRouterUrl' "http://llm-request-router.${owner}-nvcf.svc.cluster.local:8000"
assert_value "$values_dir/llm-api-gateway-values.yaml" '.llmApiGateway.config.nvcfGrpcAddr' "$api_grpc_addr"
assert_value "$values_dir/llm-request-router-values.yaml" '.llmRequestRouter.auth.workerAuthEndpoint' "$api_grpc_url"
assert_value "$values_dir/llm-request-router-values.yaml" '.llmRequestRouter.pki.baoService' "$openbao_host"
assert_value "$values_dir/llm-request-router-values.yaml" '.llmRequestRouter.pki.serviceAccountName' "$openbao_initialize_service_account"
assert_value "$values_dir/llm-request-router-values.yaml" '.llmRequestRouter.pki.rootTokenSecretName' "$openbao_root_token_secret"
assert_value "$values_dir/llm-request-router-values.yaml" '.llmRequestRouter.certificate.dnsNames[0]' "llm-request-router.${owner}-nvcf.svc.cluster.local"
assert_value "$values_dir/llm-request-router-values.yaml" '.llmRequestRouter.certificate.dnsNames[1]' "*.llm-request-router-headless.${owner}-nvcf.svc.cluster.local"
assert_value "$manifest_dir/llm-request-router.yaml" 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.serviceAccountName' "$openbao_initialize_service_account"
assert_value "$manifest_dir/llm-request-router.yaml" 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.containers[0].env[] | select(.name == "BAO_SERVICE") | .value' "$openbao_host"
assert_value "$manifest_dir/llm-request-router.yaml" 'select(.kind == "Job" and .metadata.name == "addons-llm-migrations") | .spec.template.spec.volumes[] | select(.name == "root-token") | .secret.secretName' "$openbao_root_token_secret"
assert_value "$values_dir/sis-values.yaml" '.sis.lls.namespace' "${owner}-vault-system"
assert_value "$values_dir/sis-values.yaml" '.sis.lls.hmacRotation.baoService' "$openbao_host"
assert_value "$values_dir/sis-values.yaml" '.sis.lls.hmacRotation.serviceAccountName' "$openbao_initialize_service_account"
assert_value "$values_dir/sis-values.yaml" '.sis.lls.hmacRotation.rootTokenSecretName' "$openbao_root_token_secret"
assert_value "$manifest_dir/sis.yaml" 'select(.kind == "Job" and .metadata.name == "addons-lls-migrations") | .metadata.namespace' "${owner}-vault-system"
assert_value "$manifest_dir/sis.yaml" 'select(.kind == "Job" and .metadata.name == "addons-lls-migrations") | .spec.template.spec.serviceAccountName' "$openbao_initialize_service_account"
assert_value "$manifest_dir/sis.yaml" 'select(.kind == "Job" and .metadata.name == "addons-lls-migrations") | .spec.template.spec.containers[0].env[] | select(.name == "BAO_SERVICE") | .value' "$openbao_host"
assert_value "$manifest_dir/sis.yaml" 'select(.kind == "Job" and .metadata.name == "addons-lls-migrations") | .spec.template.spec.volumes[] | select(.name == "root-token") | .secret.secretName' "$openbao_root_token_secret"
assert_value "$manifest_dir/sis.yaml" 'select(.kind == "CronJob" and .metadata.name == "addons-lls-turn-hmac-rotation") | .spec.jobTemplate.spec.template.spec.serviceAccountName' "$openbao_initialize_service_account"
assert_value "$manifest_dir/sis.yaml" 'select(.kind == "CronJob" and .metadata.name == "addons-lls-turn-hmac-rotation") | .spec.jobTemplate.spec.template.spec.containers[0].env[] | select(.name == "BAO_SERVICE") | .value' "$openbao_host"
assert_value "$manifest_dir/sis.yaml" 'select(.kind == "CronJob" and .metadata.name == "addons-lls-turn-hmac-rotation") | .spec.jobTemplate.spec.template.spec.volumes[] | select(.name == "root-token") | .secret.secretName' "$openbao_root_token_secret"
assert_value "$values_dir/function-autoscaler-values.yaml" '.functionautoscaler.volumes[0].projected.sources[0].serviceAccountToken.audience' "$openbao_url"

python3 "$repo_root/deploy/stacks/tests/verify-named-control-plane-render-isolation.py" \
  --render "$owner=$manifest_dir" >/dev/null

echo "service-dns-named-isolation: all checks passed"
