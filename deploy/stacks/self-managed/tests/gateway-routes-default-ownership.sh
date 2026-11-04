#!/usr/bin/env bash
# Verify that the stack supplies topology and opt-ins while the gateway-routes
# chart owns listener defaults. Also verify that secure routing derives the
# certificate's primary DNS identity from the advertised HTTPS endpoint.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
gateway_chart="$(cd "$stack_dir/../../helm/gateway-routes/chart" && pwd)"
work_dir="$(mktemp -d)"
default_values="$work_dir/default-values.yaml"
override_values="$work_dir/override-values.yaml"
default_manifest="$work_dir/default-manifest.yaml"
override_manifest="$work_dir/override-manifest.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "gateway-routes-default-ownership: $*" >&2
  exit 1
}

helmfile_common=(
  --file helmfile.d/02-core.yaml.gotmpl
  --environment default
  --state-values-set ingress.gatewayApi.enabled=true
  --state-values-set ingress.gatewayApi.controllerNamespace=gateway
  --state-values-set ingress.gatewayApi.routes.grpcWorker.enabled=true
  --state-values-set ingress.gatewayApi.routes.nats.enabled=true
  --state-values-set ingress.gatewayApi.routes.llmWorker.enabled=true
  --state-values-set ingress.gatewayApi.routes.llmWorker.backend.namespace=nvcf
  --state-values-set-string global.workerEndpoints.llmRequestRouterAddress=https://router.example.invalid:50071
  --state-values-set-string addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress=https://router.example.invalid:50071
  --state-values-set-string addons.llm.requestRouter.backendRouter.pylonReverseTunnelDialAddress=router.example.invalid:50072
  --state-values-set addons.llm.requestRouter.grpcTls.enabled=true
  --state-values-set-string addons.llm.requestRouter.grpcTls.issuerRef.name=test-issuer
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=gateway
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=gateway
  --state-values-set ingress.gatewayApi.gateways.nats.name=nats-gw
  --state-values-set ingress.gatewayApi.gateways.nats.namespace=gateway
  --state-values-set ingress.gatewayApi.gateways.llmGrpc.name=llm-grpc-gw
  --state-values-set ingress.gatewayApi.gateways.llmGrpc.namespace=gateway
  --state-values-set ingress.gatewayApi.gateways.llmQuic.name=llm-quic-gw
  --state-values-set ingress.gatewayApi.gateways.llmQuic.namespace=gateway
  --selector name=ingress
)

render_values() {
  local output="$1"
  shift
  (
    cd "$stack_dir"
    HELMFILE_ENV=base helmfile "${helmfile_common[@]}" "$@" \
      write-values --output-file-template "$output" >/dev/null
  )
  test -s "$output" || fail "helmfile wrote no ingress values"
}

assert_value() {
  local file="$1" expression="$2" expected="$3"
  local actual
  actual="$(yq ea -r "$expression" "$file")"
  test "$actual" = "$expected" ||
    fail "$expression = ${actual:-missing}; want $expected"
}

assert_unset() {
  local file="$1" expression="$2"
  yq -e "$expression == null" "$file" >/dev/null 2>&1 ||
    fail "$expression should be omitted so the chart default applies"
}

assert_resource_field() {
  local file="$1" kind="$2" name="$3" expression="$4" expected="$5"
  assert_value "$file" \
    "select(.kind == \"$kind\" and .metadata.name == \"$name\") | $expression" \
    "$expected"
}

render_values "$default_values"
assert_unset "$default_values" '.nvcfGatewayRoutes.gateways.nats.listenerName'
assert_unset "$default_values" '.nvcfGatewayRoutes.gateways.llmGrpc.listenerName'
assert_unset "$default_values" '.nvcfGatewayRoutes.gateways.llmQuic.listenerName'
assert_unset "$default_values" '.nvcfGatewayRoutes.routes.grpcWorker.listenerName'
assert_value "$default_values" '.llmRequestRouter.grpcTls.dnsNames | length' '1'
assert_value "$default_values" '.llmRequestRouter.grpcTls.dnsNames[0]' 'router.example.invalid'

helm template nvcf-gateway-routes "$gateway_chart" \
  --namespace gateway --values "$default_values" >"$default_manifest"
assert_resource_field "$default_manifest" TCPRoute grpc-worker '.spec.parentRefs[0].sectionName' worker-tcp
assert_resource_field "$default_manifest" TCPRoute nats '.spec.parentRefs[0].sectionName' nats
assert_resource_field "$default_manifest" GRPCRoute llm-worker-grpc '.spec.parentRefs[0].sectionName' llm-grpc
assert_resource_field "$default_manifest" UDPRoute llm-worker-quic '.spec.parentRefs[0].sectionName' llm-quic
assert_resource_field "$default_manifest" Certificate llm-request-router-grpc-tls '.spec.dnsNames[0]' router.example.invalid

render_values "$override_values" \
  --state-values-set-string ingress.gatewayApi.gateways.nats.listenerName=custom-nats \
  --state-values-set-string ingress.gatewayApi.gateways.llmGrpc.listenerName=custom-llm-grpc \
  --state-values-set-string ingress.gatewayApi.gateways.llmQuic.listenerName=custom-llm-quic \
  --state-values-set-string ingress.gatewayApi.routes.grpcWorker.listenerName=custom-worker \
  --state-values-set-string 'addons.llm.requestRouter.grpcTls.dnsNames[0]=regional.example.invalid'

assert_value "$override_values" '.nvcfGatewayRoutes.gateways.nats.listenerName' custom-nats
assert_value "$override_values" '.nvcfGatewayRoutes.gateways.llmGrpc.listenerName' custom-llm-grpc
assert_value "$override_values" '.nvcfGatewayRoutes.gateways.llmQuic.listenerName' custom-llm-quic
assert_value "$override_values" '.nvcfGatewayRoutes.routes.grpcWorker.listenerName' custom-worker
assert_value "$override_values" '.llmRequestRouter.grpcTls.dnsNames | length' '2'
assert_value "$override_values" '.llmRequestRouter.grpcTls.dnsNames[0]' router.example.invalid
assert_value "$override_values" '.llmRequestRouter.grpcTls.dnsNames[1]' regional.example.invalid

helm template nvcf-gateway-routes "$gateway_chart" \
  --namespace gateway --values "$override_values" >"$override_manifest"
assert_resource_field "$override_manifest" TCPRoute grpc-worker '.spec.parentRefs[0].sectionName' custom-worker
assert_resource_field "$override_manifest" TCPRoute nats '.spec.parentRefs[0].sectionName' custom-nats
assert_resource_field "$override_manifest" GRPCRoute llm-worker-grpc '.spec.parentRefs[0].sectionName' custom-llm-grpc
assert_resource_field "$override_manifest" UDPRoute llm-worker-quic '.spec.parentRefs[0].sectionName' custom-llm-quic

echo "gateway-routes-default-ownership: all checks passed"
