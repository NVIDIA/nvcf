#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
router_chart_path="$(cd "$stack_dir/../../helm/llm-request-router/llm-request-router" && pwd)"
gateway_chart_path="$(cd "$stack_dir/../../helm/gateway-routes/chart" && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="llm-router-split-cluster-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "llm-router-split-cluster: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

printf '%s\n' \
  'global:' \
  '  workerEndpoints:' \
  '    llmRequestRouterAddress: https://llm-grpc.example.com:50071' \
  'addons:' \
  '  llm:' \
  '    enabled: true' \
  '    pki:' \
  '      enabled: true' \
  '      allowedDomains: cluster.local' \
  '      dnsNames:' \
  '        - llm-request-router.nvcf.svc.cluster.local' \
  '        - "*.llm-request-router-headless.nvcf.svc.cluster.local"' \
  '    requestRouter:' \
  "      chartPath: $router_chart_path" \
  '      grpcTls:' \
  '        enabled: true' \
  '        mode: certManager' \
  '        secretName: llm-grpc-tls' \
  '        dnsNames:' \
  '          - llm-grpc.example.com' \
  '        issuerRef:' \
  '          kind: ClusterIssuer' \
  '          name: nvcf-openbao-pki' \
  '      backendRouter:' \
  '        pylonGrpcDialAddress: https://llm-grpc.example.com:50071' \
  '        pylonReverseTunnelDialAddress: llm-quic.example.com:50072' \
  'ingress:' \
  '  gatewayApi:' \
  "    chartPath: $gateway_chart_path" \
  '    controllerNamespace: envoy-gateway-system' \
  '    routes:' \
  '      llmWorker:' \
  '        enabled: true' \
  '        backend:' \
  '          namespace: nvcf' \
  '    gateways:' \
  '      shared:' \
  '        name: shared-gw' \
  '        namespace: envoy-gateway-system' \
  '      grpc:' \
  '        name: grpc-gw' \
  '        namespace: envoy-gateway-system' \
  '      llmGrpc:' \
  '        name: llm-grpc-gw' \
  '        namespace: envoy-gateway-system' \
  '        listenerName: llm-grpc' \
  '      llmQuic:' \
  '        name: llm-quic-gw' \
  '        namespace: envoy-gateway-system' \
  '        listenerName: llm-quic' \
  >"$environment_file"

HELMFILE_ENV="$environment_name" \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
    --environment default \
    --selector name=ingress \
    write-values \
    --output-file-template "$work_dir/ingress-values.yaml"

values_file="$work_dir/ingress-values.yaml"
test -f "$values_file" || fail "ingress values were not rendered"

HELMFILE_ENV="$environment_name" \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
    --environment default \
    --selector name=api \
    write-values \
    --output-file-template "$work_dir/api-values.yaml"

HELMFILE_ENV="$environment_name" \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
    --environment default \
    --selector name=llm-request-router \
    write-values \
    --output-file-template "$work_dir/router-values.yaml"

test -f "$work_dir/api-values.yaml" || fail "API values were not rendered"
test -f "$work_dir/router-values.yaml" || fail "request-router values were not rendered"

assert_file_value() {
  local file="$1"
  local path="$2"
  local expected="$3"
  local actual
  actual="$(yq -r "$path" "$file")"
  test "$actual" = "$expected" ||
    fail "expected $path in $(basename "$file") to be $expected, got $actual"
}

assert_value() {
  local path="$1"
  local expected="$2"
  assert_file_value "$values_file" "$path" "$expected"
}

assert_manifest_value() {
  local file="$1"
  local expression="$2"
  local expected="$3"
  local actual

  actual="$(yq ea -r "$expression" "$file")"
  test "$actual" = "$expected" ||
    fail "expected $expression in $(basename "$file") to be $expected, got $actual"
}

assert_resource_count() {
  local file="$1"
  local kind="$2"
  local name="$3"
  local namespace="$4"
  local expected="$5"
  local expression

  expression="[select(.kind == \"$kind\" and .metadata.name == \"$name\" and .metadata.namespace == \"$namespace\")] | length"
  assert_manifest_value "$file" "$expression" "$expected"
}

assert_resource_field() {
  local file="$1"
  local kind="$2"
  local name="$3"
  local namespace="$4"
  local field="$5"
  local expected="$6"
  local expression

  expression="select(.kind == \"$kind\" and .metadata.name == \"$name\" and .metadata.namespace == \"$namespace\") | $field"
  assert_manifest_value "$file" "$expression" "$expected"
}

assert_value '.nvcfGatewayRoutes.routes.llmWorker.enabled' 'true'
assert_value '.nvcfGatewayRoutes.routes.llmWorker.backend.namespace' 'nvcf'
assert_value '.nvcfGatewayRoutes.gateways.llmGrpc.name' 'llm-grpc-gw'
assert_value '.nvcfGatewayRoutes.gateways.llmGrpc.listenerName' 'llm-grpc'
assert_value '.nvcfGatewayRoutes.gateways.llmQuic.name' 'llm-quic-gw'
assert_value '.nvcfGatewayRoutes.gateways.llmQuic.listenerName' 'llm-quic'
assert_value '.llmRequestRouter.grpcTls.enabled' 'true'
assert_value '.llmRequestRouter.grpcTls.mode' 'certManager'
assert_value '.llmRequestRouter.grpcTls.secretName' 'llm-grpc-tls'
assert_value '.llmRequestRouter.grpcTls.dnsNames[0]' 'llm-grpc.example.com'
assert_value '.llmRequestRouter.grpcTls.issuerRef.name' 'nvcf-openbao-pki'

assert_file_value "$work_dir/api-values.yaml" \
  '.api.remoteConfig.configData.nvcf.llm-request-router.worker-address' \
  'https://llm-grpc.example.com:50071'
assert_file_value "$work_dir/router-values.yaml" \
  '.llmRequestRouter.backendRouter.pylonGrpcDialAddress' \
  'https://llm-grpc.example.com:50071'
assert_file_value "$work_dir/router-values.yaml" \
  '.llmRequestRouter.grpcTls.enabled' \
  'true'
assert_file_value "$work_dir/router-values.yaml" \
  '.llmRequestRouter.backendRouter.pylonReverseTunnelDialAddress' \
  'llm-quic.example.com:50072'
assert_file_value "$work_dir/router-values.yaml" \
  '.llmRequestRouter.certificate.dnsNames[0]' \
  'llm-request-router.nvcf.svc.cluster.local'
assert_file_value "$work_dir/router-values.yaml" \
  '.llmRequestRouter.certificate.dnsNames[1]' \
  '*.llm-request-router-headless.nvcf.svc.cluster.local'
assert_file_value "$work_dir/router-values.yaml" \
  '.llmRequestRouter.certificate.dnsNames[2]' \
  'null'
assert_file_value "$work_dir/router-values.yaml" \
  '.llmRequestRouter.tls.quicInsecure' \
  'false'

helm template llm-request-router "$router_chart_path" \
  --namespace nvcf \
  --values "$work_dir/router-values.yaml" \
  >"$work_dir/router-manifest.yaml"
test -s "$work_dir/router-manifest.yaml" ||
  fail "request-router source chart did not render from generated stack values"

helm template nvcf-gateway-routes "$gateway_chart_path" \
  --namespace envoy-gateway-system \
  --values "$values_file" \
  >"$work_dir/gateway-manifest.yaml"
test -s "$work_dir/gateway-manifest.yaml" ||
  fail "gateway-routes source chart did not render from generated stack values"

gateway_manifest="$work_dir/gateway-manifest.yaml"
gateway_namespace='envoy-gateway-system'

assert_resource_count "$gateway_manifest" GRPCRoute llm-worker-grpc "$gateway_namespace" 1
assert_resource_field "$gateway_manifest" GRPCRoute llm-worker-grpc "$gateway_namespace" \
  '.metadata.labels."app.kubernetes.io/component"' 'llm-worker-grpc-route'
assert_resource_field "$gateway_manifest" GRPCRoute llm-worker-grpc "$gateway_namespace" \
  '.spec.parentRefs[0].name' 'llm-grpc-gw'
assert_resource_field "$gateway_manifest" GRPCRoute llm-worker-grpc "$gateway_namespace" \
  '.spec.parentRefs[0].namespace' "$gateway_namespace"
assert_resource_field "$gateway_manifest" GRPCRoute llm-worker-grpc "$gateway_namespace" \
  '.spec.parentRefs[0].sectionName' 'llm-grpc'
assert_resource_field "$gateway_manifest" GRPCRoute llm-worker-grpc "$gateway_namespace" \
  '.spec.rules[0].backendRefs[0].name' 'llm-request-router-backend-router'
assert_resource_field "$gateway_manifest" GRPCRoute llm-worker-grpc "$gateway_namespace" \
  '.spec.rules[0].backendRefs[0].namespace' 'nvcf'
assert_resource_field "$gateway_manifest" GRPCRoute llm-worker-grpc "$gateway_namespace" \
  '.spec.rules[0].backendRefs[0].port' '50071'

assert_resource_count "$gateway_manifest" Certificate llm-grpc-tls "$gateway_namespace" 1
assert_resource_field "$gateway_manifest" Certificate llm-grpc-tls "$gateway_namespace" \
  '.spec.secretName' 'llm-grpc-tls'
assert_resource_field "$gateway_manifest" Certificate llm-grpc-tls "$gateway_namespace" \
  '.spec.dnsNames[0]' 'llm-grpc.example.com'
assert_resource_field "$gateway_manifest" Certificate llm-grpc-tls "$gateway_namespace" \
  '.spec.issuerRef.kind' 'ClusterIssuer'
assert_resource_field "$gateway_manifest" Certificate llm-grpc-tls "$gateway_namespace" \
  '.spec.issuerRef.name' 'nvcf-openbao-pki'

assert_resource_count "$gateway_manifest" BackendTrafficPolicy llm-worker-grpc-streams "$gateway_namespace" 1
assert_resource_field "$gateway_manifest" BackendTrafficPolicy llm-worker-grpc-streams "$gateway_namespace" \
  '.spec.targetRefs[0].group' 'gateway.networking.k8s.io'
assert_resource_field "$gateway_manifest" BackendTrafficPolicy llm-worker-grpc-streams "$gateway_namespace" \
  '.spec.targetRefs[0].kind' 'GRPCRoute'
assert_resource_field "$gateway_manifest" BackendTrafficPolicy llm-worker-grpc-streams "$gateway_namespace" \
  '.spec.targetRefs[0].name' 'llm-worker-grpc'
assert_resource_field "$gateway_manifest" BackendTrafficPolicy llm-worker-grpc-streams "$gateway_namespace" \
  '.spec.timeout.http.requestTimeout' '0s'

assert_partial_backend_override_rejected() {
  local missing_key="$1"
  local case_name="$2"
  local partial_environment_name="${environment_name}-${case_name}"
  local partial_environment_file="$test_stack_dir/environments/$partial_environment_name.yaml"
  local partial_error="$work_dir/$case_name-error.log"

  cp "$environment_file" "$partial_environment_file"
  printf '{}\n' >"$test_stack_dir/secrets/$partial_environment_name-secrets.yaml"
  yq -i "del(.addons.llm.requestRouter.backendRouter.${missing_key})" \
    "$partial_environment_file"

  if HELMFILE_ENV="$partial_environment_name" \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
      --environment default \
      --selector name=llm-request-router \
      write-values \
      --output-file-template "$work_dir/$case_name-values.yaml" \
      >/dev/null 2>"$partial_error"; then
    echo "llm-router-split-cluster: partial override without $missing_key was accepted" >&2
    return 1
  fi

  grep -Fq \
    'addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress and addons.llm.requestRouter.backendRouter.pylonReverseTunnelDialAddress must either both be set or both be omitted' \
    "$partial_error" || {
      echo "llm-router-split-cluster: partial override without $missing_key returned an unexpected error" >&2
      sed -n '1,80p' "$partial_error" >&2
      return 1
    }
}

partial_override_failures=0
assert_partial_backend_override_rejected \
  'pylonReverseTunnelDialAddress' 'missing-reverse-tunnel' ||
  partial_override_failures=$((partial_override_failures + 1))
assert_partial_backend_override_rejected \
  'pylonGrpcDialAddress' 'missing-grpc' ||
  partial_override_failures=$((partial_override_failures + 1))
test "$partial_override_failures" -eq 0 ||
  fail "$partial_override_failures partial backend-router override case(s) were not rejected"

assert_invalid_external_grpc_config_rejected() {
  local case_name="$1"
  local mutation="$2"
  local expected_error="$3"
  local invalid_environment_name="${environment_name}-${case_name}"
  local invalid_environment_file="$test_stack_dir/environments/$invalid_environment_name.yaml"
  local invalid_error="$work_dir/$case_name-error.log"

  cp "$environment_file" "$invalid_environment_file"
  printf '{}\n' >"$test_stack_dir/secrets/$invalid_environment_name-secrets.yaml"
  yq -i "$mutation" "$invalid_environment_file"

  if HELMFILE_ENV="$invalid_environment_name" \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
      --environment default \
      --selector name=llm-request-router \
      write-values \
      --output-file-template "$work_dir/$case_name-values.yaml" \
      >/dev/null 2>"$invalid_error"; then
    echo "llm-router-split-cluster: $case_name was accepted" >&2
    return 1
  fi

  grep -Fq "$expected_error" "$invalid_error" || {
    echo "llm-router-split-cluster: $case_name returned an unexpected error" >&2
    sed -n '1,80p' "$invalid_error" >&2
    return 1
  }
}

invalid_config_failures=0
assert_invalid_external_grpc_config_rejected \
  'mismatched-grpc-uri' \
  '.global.workerEndpoints.llmRequestRouterAddress = "https://other.example.com:50071"' \
  'global.workerEndpoints.llmRequestRouterAddress and addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress must use the same explicit URI when LLM worker routing is enabled' ||
  invalid_config_failures=$((invalid_config_failures + 1))
assert_invalid_external_grpc_config_rejected \
  'secure-http-uri' \
  '.global.workerEndpoints.llmRequestRouterAddress = "http://llm-grpc.example.com:50071" | .addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress = "http://llm-grpc.example.com:50071"' \
  'secure LLM worker routing requires an explicit https:// global.workerEndpoints.llmRequestRouterAddress' ||
  invalid_config_failures=$((invalid_config_failures + 1))
assert_invalid_external_grpc_config_rejected \
  'secure-scheme-less-uri' \
  '.global.workerEndpoints.llmRequestRouterAddress = "llm-grpc.example.com:50071" | .addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress = "llm-grpc.example.com:50071"' \
  'secure LLM worker routing requires an explicit https:// global.workerEndpoints.llmRequestRouterAddress' ||
  invalid_config_failures=$((invalid_config_failures + 1))
assert_invalid_external_grpc_config_rejected \
  'plaintext-without-opt-in' \
  '.global.workerEndpoints.llmRequestRouterAddress = "http://llm-grpc.example.com:50071" | .addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress = "http://llm-grpc.example.com:50071" | .addons.llm.requestRouter.grpcTls.enabled = false' \
  'addons.llm.requestRouter.grpcTls.allowInsecureHttp must be true for an explicitly plaintext external LLM worker route' ||
  invalid_config_failures=$((invalid_config_failures + 1))
test "$invalid_config_failures" -eq 0 ||
  fail "$invalid_config_failures invalid external gRPC configuration case(s) were not rejected"

disabled_environment_name="${environment_name}-backend-router-disabled"
disabled_environment_file="$test_stack_dir/environments/$disabled_environment_name.yaml"
cp "$environment_file" "$disabled_environment_file"
printf '{}\n' >"$test_stack_dir/secrets/$disabled_environment_name-secrets.yaml"
yq -i \
  '.addons.llm.requestRouter.backendRouter.enabled = false |
   del(.addons.llm.requestRouter.backendRouter.pylonReverseTunnelDialAddress)' \
  "$disabled_environment_file"

HELMFILE_ENV="$disabled_environment_name" \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
    --environment default \
    --selector name=llm-request-router \
    write-values \
    --output-file-template "$work_dir/backend-router-disabled-values.yaml"

assert_file_value "$work_dir/backend-router-disabled-values.yaml" \
  '.llmRequestRouter.backendRouter.enabled' \
  'false'

echo "llm-router-split-cluster: all checks passed"
