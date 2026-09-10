#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -eu

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
default_render="$(mktemp)"
enabled_render="$(mktemp)"
annotated_render="$(mktemp)"
disabled_render="$(mktemp)"
hostname_conflict_error="$(mktemp)"
named_render="$(mktemp)"
trap 'rm -f "$default_render" "$enabled_render" "$annotated_render" "$disabled_render" "$hostname_conflict_error" "$named_render"' EXIT

if ! command -v yq >/dev/null 2>&1; then
  echo "yq is required for render tests" >&2
  exit 1
fi

assert_yq_eq() {
  file="$1"
  expr="$2"
  expected="$3"
  actual="$(yq ea -r "$expr" "$file")"
  if [ "$actual" != "$expected" ]; then
    echo "expected yq expression to equal '$expected', got '$actual': $expr" >&2
    exit 1
  fi
}

assert_resource_count() {
  file="$1"
  kind="$2"
  name="$3"
  namespace="$4"
  expected="$5"
  assert_yq_eq "$file" "[select(.kind == \"$kind\" and .metadata.name == \"$name\" and .metadata.namespace == \"$namespace\")] | length" "$expected"
}

assert_resource_field() {
  file="$1"
  kind="$2"
  name="$3"
  namespace="$4"
  field="$5"
  expected="$6"
  assert_yq_eq "$file" "select(.kind == \"$kind\" and .metadata.name == \"$name\" and .metadata.namespace == \"$namespace\") | $field" "$expected"
}

assert_yq_eq "$repo_root/chart/values.yaml" '.nvcfGatewayRoutes.routes.grpc | has("hostnames")' false
assert_yq_eq "$repo_root/chart/values.yaml" '.nvcfGatewayRoutes.routes.grpcWorker | has("hostnames")' false
assert_yq_eq "$repo_root/chart/values.yaml" '.nvcfGatewayRoutes.routes.nats | has("hostnames")' false

helm template nvcf-gateway-routes "$repo_root/chart" > "$default_render"

# Admission guard for chart-owned HTTPRoute hostnames.
assert_yq_eq "$default_render" '[select(.kind == "ValidatingAdmissionPolicy")] | length' 1
assert_yq_eq "$default_render" '[select(.kind == "ValidatingAdmissionPolicyBinding")] | length' 1
assert_yq_eq "$default_render" 'select(.kind == "ValidatingAdmissionPolicy") | .spec.failurePolicy' Fail
assert_yq_eq "$default_render" 'select(.kind == "ValidatingAdmissionPolicy") | .spec.matchConstraints.resourceRules[0].resources[0]' httproutes
assert_yq_eq "$default_render" 'select(.kind == "ValidatingAdmissionPolicyBinding") | .spec.validationActions[0]' Deny

reserved_hostnames="$(yq ea -r 'select(.kind == "ValidatingAdmissionPolicy") | .spec.variables[] | select(.name == "reservedHostnames") | .expression' "$default_render")"
case "$reserved_hostnames" in
  *'"api.localhost": "gateway/nvcf-api"'*'"events.localhost": "gateway/event-ledger"'*) ;;
  *)
    echo "admission policy does not reserve every enabled HTTPRoute hostname: $reserved_hostnames" >&2
    exit 1
    ;;
esac

# Default-enabled HTTPRoutes.
assert_resource_count "$default_render" HTTPRoute nvcf-api gateway 1
assert_resource_field "$default_render" HTTPRoute nvcf-api gateway '.metadata.labels."app.kubernetes.io/component"' nvcf-api-route
assert_resource_field "$default_render" HTTPRoute nvcf-api gateway '.spec.parentRefs[0].name' gateway
assert_resource_field "$default_render" HTTPRoute nvcf-api gateway '.spec.parentRefs[0].namespace' gateway
assert_resource_field "$default_render" HTTPRoute nvcf-api gateway '.spec.parentRefs[0].sectionName' http
assert_resource_field "$default_render" HTTPRoute nvcf-api gateway '.spec.hostnames[0]' api.localhost
assert_resource_field "$default_render" HTTPRoute nvcf-api gateway '.spec.rules[0].backendRefs[0].name' api
assert_resource_field "$default_render" HTTPRoute nvcf-api gateway '.spec.rules[0].backendRefs[0].namespace' nvcf
assert_resource_field "$default_render" HTTPRoute nvcf-api gateway '.spec.rules[0].backendRefs[0].port' 8080

assert_resource_count "$default_render" HTTPRoute nvct-api gateway 1
assert_resource_field "$default_render" HTTPRoute nvct-api gateway '.spec.hostnames[0]' tasks.localhost
assert_resource_field "$default_render" HTTPRoute nvct-api gateway '.spec.rules[0].backendRefs[0].name' nvct-api
assert_resource_field "$default_render" HTTPRoute nvct-api gateway '.spec.rules[0].backendRefs[0].namespace' nvcf
assert_resource_field "$default_render" HTTPRoute nvct-api gateway '.spec.rules[0].backendRefs[0].port' 8080

assert_resource_count "$default_render" HTTPRoute api-keys gateway 1
assert_resource_field "$default_render" HTTPRoute api-keys gateway '.metadata.labels."app.kubernetes.io/component"' api-keys-route
assert_resource_field "$default_render" HTTPRoute api-keys gateway '.spec.hostnames[0]' api-keys.localhost
assert_resource_field "$default_render" HTTPRoute api-keys gateway '.spec.rules[0].backendRefs[0].name' api-keys
assert_resource_field "$default_render" HTTPRoute api-keys gateway '.spec.rules[0].backendRefs[0].namespace' api-keys
assert_resource_field "$default_render" HTTPRoute api-keys gateway '.spec.rules[0].backendRefs[0].port' 8080

assert_resource_count "$default_render" HTTPRoute invocation-service gateway 1
assert_resource_field "$default_render" HTTPRoute invocation-service gateway '.metadata.labels."app.kubernetes.io/component"' invocation-service-route
assert_resource_field "$default_render" HTTPRoute invocation-service gateway '.spec.hostnames[0]' '*.invocation.localhost'
assert_resource_field "$default_render" HTTPRoute invocation-service gateway '.spec.hostnames[1]' invocation.localhost
assert_resource_field "$default_render" HTTPRoute invocation-service gateway '.spec.rules[0].backendRefs[0].name' invocation
assert_resource_field "$default_render" HTTPRoute invocation-service gateway '.spec.rules[0].backendRefs[0].namespace' nvcf
assert_resource_field "$default_render" HTTPRoute invocation-service gateway '.spec.rules[0].backendRefs[0].port' 8080

assert_resource_count "$default_render" HTTPRoute llm-api-gateway gateway 1
assert_resource_field "$default_render" HTTPRoute llm-api-gateway gateway '.metadata.labels."app.kubernetes.io/component"' llm-api-gateway-route
assert_resource_field "$default_render" HTTPRoute llm-api-gateway gateway '.spec.hostnames[0]' llm.localhost
assert_resource_field "$default_render" HTTPRoute llm-api-gateway gateway '.spec.rules[0].backendRefs[0].name' llm-api-gateway
assert_resource_field "$default_render" HTTPRoute llm-api-gateway gateway '.spec.rules[0].backendRefs[0].namespace' nvcf
assert_resource_field "$default_render" HTTPRoute llm-api-gateway gateway '.spec.rules[0].backendRefs[0].port' 8080
# A request timeout truncates long-lived SSE responses even when the backend
# terminates them correctly. The zero duration disables the route-level request
# deadline while still allowing a caller disconnect to close the downstream request.
assert_resource_field "$default_render" HTTPRoute llm-api-gateway gateway '.spec.rules[0].timeouts.request' 0s

assert_resource_count "$default_render" HTTPRoute sis gateway 1
assert_resource_field "$default_render" HTTPRoute sis gateway '.metadata.labels."app.kubernetes.io/component"' sis-route
assert_resource_field "$default_render" HTTPRoute sis gateway '.spec.hostnames[0]' sis.localhost
assert_resource_field "$default_render" HTTPRoute sis gateway '.spec.rules[0].backendRefs[0].name' api
assert_resource_field "$default_render" HTTPRoute sis gateway '.spec.rules[0].backendRefs[0].namespace' sis
assert_resource_field "$default_render" HTTPRoute sis gateway '.spec.rules[0].backendRefs[0].port' 8080

assert_resource_count "$default_render" HTTPRoute event-ledger gateway 1
assert_resource_field "$default_render" HTTPRoute event-ledger gateway '.metadata.labels."app.kubernetes.io/component"' event-ledger-route
assert_resource_field "$default_render" HTTPRoute event-ledger gateway '.spec.hostnames[0]' events.localhost
assert_resource_field "$default_render" HTTPRoute event-ledger gateway '.spec.rules[0].matches[0].path.type' PathPrefix
assert_resource_field "$default_render" HTTPRoute event-ledger gateway '.spec.rules[0].matches[0].path.value' /
assert_resource_field "$default_render" HTTPRoute event-ledger gateway '.spec.rules[0].backendRefs[0].name' event-ledger
assert_resource_field "$default_render" HTTPRoute event-ledger gateway '.spec.rules[0].backendRefs[0].namespace' nvcf
assert_resource_field "$default_render" HTTPRoute event-ledger gateway '.spec.rules[0].backendRefs[0].port' 8080

assert_resource_count "$default_render" HTTPRoute reval gateway 1
assert_resource_field "$default_render" HTTPRoute reval gateway '.metadata.labels."app.kubernetes.io/component"' reval-route
assert_resource_field "$default_render" HTTPRoute reval gateway '.spec.hostnames[0]' reval.localhost
assert_resource_field "$default_render" HTTPRoute reval gateway '.spec.rules[0].backendRefs[0].name' reval
assert_resource_field "$default_render" HTTPRoute reval gateway '.spec.rules[0].backendRefs[0].namespace' nvcf
assert_resource_field "$default_render" HTTPRoute reval gateway '.spec.rules[0].backendRefs[0].port' 8080

helm template nvcf-gateway-routes "$repo_root/chart" \
  --set nvcfGatewayRoutes.routes.reval.enabled=false \
  --set nvcfGatewayRoutes.routes.eventLedger.enabled=false \
  --set nvcfGatewayRoutes.hostnameConflictPolicy.enabled=false \
  > "$disabled_render"

assert_resource_count "$disabled_render" HTTPRoute reval gateway 0
assert_resource_count "$disabled_render" HTTPRoute event-ledger gateway 0
assert_yq_eq "$disabled_render" '[select(.kind == "ValidatingAdmissionPolicy")] | length' 0
assert_yq_eq "$disabled_render" '[select(.kind == "ValidatingAdmissionPolicyBinding")] | length' 0

if helm template nvcf-gateway-routes "$repo_root/chart" \
  --set-string 'nvcfGatewayRoutes.routes.eventLedger.hostnames[0]=api.localhost' \
  >/dev/null 2>"$hostname_conflict_error"; then
  echo "expected duplicate HTTPRoute hostname render to fail" >&2
  exit 1
fi
grep -Fq 'routes event-ledger and nvcf-api both use hostname "api.localhost"' "$hostname_conflict_error"

# Default-enabled TCPRoute.
assert_resource_count "$default_render" TCPRoute grpc gateway 1
assert_resource_field "$default_render" TCPRoute grpc gateway '.metadata.labels."app.kubernetes.io/component"' grpc-route
assert_resource_field "$default_render" TCPRoute grpc gateway '.spec.parentRefs[0].name' tcp-gateway
assert_resource_field "$default_render" TCPRoute grpc gateway '.spec.parentRefs[0].namespace' gateway
assert_resource_field "$default_render" TCPRoute grpc gateway '.spec.parentRefs[0].sectionName' tcp
assert_resource_field "$default_render" TCPRoute grpc gateway '.spec.hostnames' null
assert_resource_field "$default_render" TCPRoute grpc gateway '.spec.rules[0].backendRefs[0].name' grpc
assert_resource_field "$default_render" TCPRoute grpc gateway '.spec.rules[0].backendRefs[0].namespace' nvcf
assert_resource_field "$default_render" TCPRoute grpc gateway '.spec.rules[0].backendRefs[0].port' 10081

# Cross-namespace grants for default routes.
assert_resource_count "$default_render" ReferenceGrant allow-routes-to-nvcf nvcf 1
assert_resource_field "$default_render" ReferenceGrant allow-routes-to-nvcf nvcf '.spec.from[0].kind' HTTPRoute
assert_resource_field "$default_render" ReferenceGrant allow-routes-to-nvcf nvcf '.spec.from[0].namespace' gateway
assert_resource_field "$default_render" ReferenceGrant allow-routes-to-nvcf nvcf '.spec.from[1].kind' TCPRoute
assert_resource_field "$default_render" ReferenceGrant allow-routes-to-nvcf nvcf '.spec.from[1].namespace' gateway
assert_resource_field "$default_render" ReferenceGrant allow-routes-to-nvcf nvcf '.spec.from[2].kind' GRPCRoute
assert_resource_field "$default_render" ReferenceGrant allow-routes-to-nvcf nvcf '.spec.from[2].namespace' gateway
assert_resource_field "$default_render" ReferenceGrant allow-routes-to-nvcf nvcf '.spec.to[0].kind' Service

assert_resource_count "$default_render" ReferenceGrant allow-httproute-to-api-keys api-keys 1
assert_resource_field "$default_render" ReferenceGrant allow-httproute-to-api-keys api-keys '.spec.from[0].kind' HTTPRoute
assert_resource_field "$default_render" ReferenceGrant allow-httproute-to-api-keys api-keys '.spec.from[0].namespace' gateway
assert_resource_field "$default_render" ReferenceGrant allow-httproute-to-api-keys api-keys '.spec.to[0].kind' Service

assert_resource_count "$default_render" ReferenceGrant allow-httproute-to-sis sis 1
assert_resource_field "$default_render" ReferenceGrant allow-httproute-to-sis sis '.spec.from[0].kind' HTTPRoute
assert_resource_field "$default_render" ReferenceGrant allow-httproute-to-sis sis '.spec.from[0].namespace' gateway
assert_resource_field "$default_render" ReferenceGrant allow-httproute-to-sis sis '.spec.to[0].kind' Service

# Routes disabled by default stay absent unless explicitly enabled.
assert_resource_count "$default_render" HTTPRoute llm-invocation gateway 0
assert_resource_count "$default_render" GRPCRoute nvcf-api-grpc gateway 0
assert_resource_count "$default_render" GRPCRoute nvct-api-grpc gateway 0
assert_resource_count "$default_render" TCPRoute grpc-worker gateway 0
assert_resource_count "$default_render" TCPRoute nats gateway 0
assert_resource_count "$default_render" TCPRoute llm-worker-grpc gateway 0
assert_resource_count "$default_render" UDPRoute llm-worker-quic gateway 0
assert_resource_count "$default_render" ReferenceGrant allow-llm-worker-routes nvcf 0
assert_resource_count "$default_render" ReferenceGrant allow-tcproute-to-nats nats-system 0

helm template nvcf-gateway-routes "$repo_root/chart" \
  --set nvcfGatewayRoutes.routes.llmInvocation.enabled=true \
  --set nvcfGatewayRoutes.routes.nvcfApi.grpc.enabled=true \
  --set nvcfGatewayRoutes.routes.nvctApi.grpc.enabled=true \
  --set nvcfGatewayRoutes.routes.grpcWorker.enabled=true \
  --set nvcfGatewayRoutes.routes.nats.enabled=true \
  --set nvcfGatewayRoutes.routes.llmWorker.enabled=true \
  --set nvcfGatewayRoutes.routes.llmWorker.backend.namespace=nvcf \
  --set llmRequestRouter.grpcTls.allowInsecureHttp=true \
  --set nvcfGatewayRoutes.gateways.nats.name=nats-gateway \
  --set nvcfGatewayRoutes.gateways.nats.namespace=gateway \
  --set nvcfGatewayRoutes.gateways.nats.listenerName=nats \
  > "$enabled_render"

assert_resource_count "$enabled_render" HTTPRoute llm-invocation gateway 1
assert_resource_field "$enabled_render" HTTPRoute llm-invocation gateway '.metadata.labels."app.kubernetes.io/component"' llm-invocation-route
assert_resource_field "$enabled_render" HTTPRoute llm-invocation gateway '.spec.hostnames[0]' llm.invocation.localhost
assert_resource_field "$enabled_render" HTTPRoute llm-invocation gateway '.spec.rules[0].backendRefs[0].name' llm-api-gateway
assert_resource_field "$enabled_render" HTTPRoute llm-invocation gateway '.spec.rules[0].backendRefs[0].namespace' nvcf
assert_resource_field "$enabled_render" HTTPRoute llm-invocation gateway '.spec.rules[0].backendRefs[0].port' 8080

assert_resource_count "$enabled_render" GRPCRoute nvcf-api-grpc gateway 1
assert_resource_field "$enabled_render" GRPCRoute nvcf-api-grpc gateway '.metadata.labels."app.kubernetes.io/component"' nvcf-api-grpc-route
assert_resource_field "$enabled_render" GRPCRoute nvcf-api-grpc gateway '.spec.parentRefs[0].name' gateway
assert_resource_field "$enabled_render" GRPCRoute nvcf-api-grpc gateway '.spec.parentRefs[0].namespace' gateway
assert_resource_field "$enabled_render" GRPCRoute nvcf-api-grpc gateway '.spec.parentRefs[0].sectionName' http
assert_resource_field "$enabled_render" GRPCRoute nvcf-api-grpc gateway '.spec.hostnames[0]' api.localhost
assert_resource_field "$enabled_render" GRPCRoute nvcf-api-grpc gateway '.spec.rules[0].backendRefs[0].name' api
assert_resource_field "$enabled_render" GRPCRoute nvcf-api-grpc gateway '.spec.rules[0].backendRefs[0].namespace' nvcf
assert_resource_field "$enabled_render" GRPCRoute nvcf-api-grpc gateway '.spec.rules[0].backendRefs[0].port' 9090

assert_resource_count "$enabled_render" GRPCRoute nvct-api-grpc gateway 1
assert_resource_field "$enabled_render" GRPCRoute nvct-api-grpc gateway '.metadata.labels."app.kubernetes.io/component"' nvct-api-grpc-route
assert_resource_field "$enabled_render" GRPCRoute nvct-api-grpc gateway '.spec.parentRefs[0].name' gateway
assert_resource_field "$enabled_render" GRPCRoute nvct-api-grpc gateway '.spec.parentRefs[0].namespace' gateway
assert_resource_field "$enabled_render" GRPCRoute nvct-api-grpc gateway '.spec.parentRefs[0].sectionName' http
assert_resource_field "$enabled_render" GRPCRoute nvct-api-grpc gateway '.spec.hostnames[0]' tasks.localhost
assert_resource_field "$enabled_render" GRPCRoute nvct-api-grpc gateway '.spec.rules[0].backendRefs[0].name' nvct-api
assert_resource_field "$enabled_render" GRPCRoute nvct-api-grpc gateway '.spec.rules[0].backendRefs[0].namespace' nvcf
assert_resource_field "$enabled_render" GRPCRoute nvct-api-grpc gateway '.spec.rules[0].backendRefs[0].port' 9090

assert_resource_count "$enabled_render" TCPRoute grpc-worker gateway 1
assert_resource_field "$enabled_render" TCPRoute grpc-worker gateway '.metadata.labels."app.kubernetes.io/component"' grpc-worker-route
assert_resource_field "$enabled_render" TCPRoute grpc-worker gateway '.spec.parentRefs[0].name' tcp-gateway
assert_resource_field "$enabled_render" TCPRoute grpc-worker gateway '.spec.parentRefs[0].namespace' gateway
assert_resource_field "$enabled_render" TCPRoute grpc-worker gateway '.spec.parentRefs[0].sectionName' worker-tcp
assert_resource_field "$enabled_render" TCPRoute grpc-worker gateway '.spec.rules[0].backendRefs[0].name' grpc
assert_resource_field "$enabled_render" TCPRoute grpc-worker gateway '.spec.rules[0].backendRefs[0].namespace' nvcf
assert_resource_field "$enabled_render" TCPRoute grpc-worker gateway '.spec.rules[0].backendRefs[0].port' 10086
assert_resource_field "$enabled_render" TCPRoute grpc-worker gateway '.spec.hostnames' null

assert_resource_count "$enabled_render" TCPRoute nats gateway 1
assert_resource_field "$enabled_render" TCPRoute nats gateway '.metadata.labels."app.kubernetes.io/component"' nats-route
assert_resource_field "$enabled_render" TCPRoute nats gateway '.spec.parentRefs[0].name' nats-gateway
assert_resource_field "$enabled_render" TCPRoute nats gateway '.spec.parentRefs[0].namespace' gateway
assert_resource_field "$enabled_render" TCPRoute nats gateway '.spec.parentRefs[0].sectionName' nats
assert_resource_field "$enabled_render" TCPRoute nats gateway '.spec.rules[0].backendRefs[0].name' nats
assert_resource_field "$enabled_render" TCPRoute nats gateway '.spec.rules[0].backendRefs[0].namespace' nats-system
assert_resource_field "$enabled_render" TCPRoute nats gateway '.spec.rules[0].backendRefs[0].port' 4222
assert_resource_field "$enabled_render" TCPRoute nats gateway '.spec.hostnames' null
assert_resource_field "$enabled_render" TCPRoute nats gateway '.metadata.annotations' null

assert_resource_count "$enabled_render" TCPRoute llm-worker-grpc gateway 1
assert_resource_field "$enabled_render" TCPRoute llm-worker-grpc gateway '.metadata.labels."app.kubernetes.io/component"' llm-worker-grpc-route
assert_resource_field "$enabled_render" TCPRoute llm-worker-grpc gateway '.spec.parentRefs[0].name' llm-grpc-gateway
assert_resource_field "$enabled_render" TCPRoute llm-worker-grpc gateway '.spec.parentRefs[0].namespace' gateway
assert_resource_field "$enabled_render" TCPRoute llm-worker-grpc gateway '.spec.parentRefs[0].sectionName' llm-grpc
assert_resource_field "$enabled_render" TCPRoute llm-worker-grpc gateway '.spec.rules[0].backendRefs[0].name' llm-request-router-backend-router
assert_resource_field "$enabled_render" TCPRoute llm-worker-grpc gateway '.spec.rules[0].backendRefs[0].namespace' nvcf
assert_resource_field "$enabled_render" TCPRoute llm-worker-grpc gateway '.spec.rules[0].backendRefs[0].port' 50071

assert_resource_count "$enabled_render" UDPRoute llm-worker-quic gateway 1
assert_resource_field "$enabled_render" UDPRoute llm-worker-quic gateway '.metadata.labels."app.kubernetes.io/component"' llm-worker-quic-route
assert_resource_field "$enabled_render" UDPRoute llm-worker-quic gateway '.spec.parentRefs[0].name' llm-quic-gateway
assert_resource_field "$enabled_render" UDPRoute llm-worker-quic gateway '.spec.parentRefs[0].namespace' gateway
assert_resource_field "$enabled_render" UDPRoute llm-worker-quic gateway '.spec.parentRefs[0].sectionName' llm-quic
assert_resource_field "$enabled_render" UDPRoute llm-worker-quic gateway '.spec.rules[0].backendRefs[0].name' llm-request-router-backend-router
assert_resource_field "$enabled_render" UDPRoute llm-worker-quic gateway '.spec.rules[0].backendRefs[0].namespace' nvcf
assert_resource_field "$enabled_render" UDPRoute llm-worker-quic gateway '.spec.rules[0].backendRefs[0].port' 50072

assert_resource_count "$enabled_render" ReferenceGrant allow-llm-worker-routes nvcf 1
assert_resource_field "$enabled_render" ReferenceGrant allow-llm-worker-routes nvcf '.spec.from[0].kind' TCPRoute
assert_resource_field "$enabled_render" ReferenceGrant allow-llm-worker-routes nvcf '.spec.from[0].namespace' gateway
assert_resource_field "$enabled_render" ReferenceGrant allow-llm-worker-routes nvcf '.spec.from[1].kind' UDPRoute
assert_resource_field "$enabled_render" ReferenceGrant allow-llm-worker-routes nvcf '.spec.from[1].namespace' gateway
assert_resource_field "$enabled_render" ReferenceGrant allow-llm-worker-routes nvcf '.spec.to[0].kind' Service
assert_resource_field "$enabled_render" ReferenceGrant allow-llm-worker-routes nvcf '.spec.to[0].name' llm-request-router-backend-router

assert_resource_count "$enabled_render" ReferenceGrant allow-tcproute-to-nats nats-system 1
assert_resource_field "$enabled_render" ReferenceGrant allow-tcproute-to-nats nats-system '.spec.from[0].kind' TCPRoute
assert_resource_field "$enabled_render" ReferenceGrant allow-tcproute-to-nats nats-system '.spec.from[0].namespace' gateway
assert_resource_field "$enabled_render" ReferenceGrant allow-tcproute-to-nats nats-system '.spec.to[0].kind' Service

helm template nvcf-gateway-routes "$repo_root/chart" \
  --set nvcfGatewayRoutes.routes.nats.enabled=true \
  --set 'nvcfGatewayRoutes.routes.nats.routeAnnotations.example\.com/nats-route=true' \
  > "$annotated_render"

assert_resource_field "$annotated_render" TCPRoute nats gateway '.metadata.annotations."example.com/nats-route"' true

helm template nvcf-gateway-routes "$repo_root/chart" \
  --set nvcfGatewayRoutes.routes.nvcfApi.name=plane-a-nvcf-api \
  --set nvcfGatewayRoutes.routes.nvcfApi.backend.namespace=plane-a-nvcf \
  --set nvcfGatewayRoutes.routes.nvcfApi.grpc.enabled=true \
  --set nvcfGatewayRoutes.routes.nvcfApi.grpc.name=plane-a-nvcf-api-grpc \
  --set nvcfGatewayRoutes.routes.nvcfApi.grpc.backend.namespace=plane-a-nvcf \
  --set nvcfGatewayRoutes.routes.nvctApi.name=plane-a-nvct-api \
  --set nvcfGatewayRoutes.routes.nvctApi.backend.namespace=plane-a-nvcf \
  --set nvcfGatewayRoutes.routes.nvctApi.grpc.enabled=true \
  --set nvcfGatewayRoutes.routes.nvctApi.grpc.name=plane-a-nvct-api-grpc \
  --set nvcfGatewayRoutes.routes.nvctApi.grpc.backend.namespace=plane-a-nvcf \
  --set nvcfGatewayRoutes.routes.apiKeys.name=plane-a-api-keys \
  --set nvcfGatewayRoutes.routes.apiKeys.backend.namespace=plane-a-api-keys \
  --set nvcfGatewayRoutes.routes.invocation.name=plane-a-invocation-service \
  --set nvcfGatewayRoutes.routes.invocation.backend.namespace=plane-a-nvcf \
  --set nvcfGatewayRoutes.routes.llmApiGateway.name=plane-a-llm-api-gateway \
  --set nvcfGatewayRoutes.routes.llmApiGateway.backend.namespace=plane-a-nvcf \
  --set nvcfGatewayRoutes.routes.llmInvocation.enabled=true \
  --set nvcfGatewayRoutes.routes.llmInvocation.name=plane-a-llm-invocation \
  --set nvcfGatewayRoutes.routes.llmInvocation.backend.namespace=plane-a-nvcf \
  --set nvcfGatewayRoutes.routes.vanityGateway.enabled=true \
  --set nvcfGatewayRoutes.routes.vanityGateway.name=plane-a-vanity-gateway \
  --set nvcfGatewayRoutes.routes.vanityGateway.backend.namespace=plane-a-nvcf \
  --set nvcfGatewayRoutes.routes.reval.name=plane-a-reval \
  --set nvcfGatewayRoutes.routes.reval.backend.namespace=plane-a-nvcf \
  --set nvcfGatewayRoutes.routes.nvcfUi.enabled=true \
  --set nvcfGatewayRoutes.routes.nvcfUi.name=plane-a-nvcf-ui \
  --set nvcfGatewayRoutes.routes.nvcfUi.backend.namespace=plane-a-nvcf-ui \
  --set nvcfGatewayRoutes.routes.sis.name=plane-a-sis \
  --set nvcfGatewayRoutes.routes.sis.backend.namespace=plane-a-sis \
  --set nvcfGatewayRoutes.routes.eventLedger.name=plane-a-event-ledger \
  --set nvcfGatewayRoutes.routes.eventLedger.backend.namespace=plane-a-nvcf \
  --set nvcfGatewayRoutes.routes.grpc.name=plane-a-grpc \
  --set nvcfGatewayRoutes.routes.grpc.backend.namespace=plane-a-nvcf \
  --set nvcfGatewayRoutes.routes.grpcWorker.enabled=true \
  --set nvcfGatewayRoutes.routes.grpcWorker.name=plane-a-grpc-worker \
  --set nvcfGatewayRoutes.routes.grpcWorker.backend.namespace=plane-a-nvcf \
  --set nvcfGatewayRoutes.routes.nats.enabled=true \
  --set nvcfGatewayRoutes.routes.nats.name=plane-a-nats \
  --set nvcfGatewayRoutes.routes.nats.backend.namespace=plane-a-nats-system \
  --set nvcfGatewayRoutes.gateways.nats.name=nats-gateway \
  --set nvcfGatewayRoutes.gateways.nats.namespace=gateway \
  --set nvcfGatewayRoutes.routes.llmWorker.enabled=true \
  --set nvcfGatewayRoutes.routes.llmWorker.name=plane-a-llm-worker \
  --set nvcfGatewayRoutes.routes.llmWorker.backend.namespace=plane-a-nvcf \
  --set llmRequestRouter.grpcTls.enabled=true \
  --set llmRequestRouter.grpcTls.mode=certManager \
  --set llmRequestRouter.grpcTls.secretName=plane-a-llm-grpc-tls \
  --set llmRequestRouter.grpcTls.dnsNames[0]=llm-grpc.example.invalid \
  --set llmRequestRouter.grpcTls.issuerRef.name=plane-a-nvcf-openbao-pki \
  > "$named_render"

assert_resource_count "$named_render" HTTPRoute plane-a-nvcf-api gateway 1
assert_resource_field "$named_render" HTTPRoute plane-a-nvcf-api gateway '.spec.rules[0].backendRefs[0].namespace' plane-a-nvcf
assert_resource_count "$named_render" GRPCRoute plane-a-nvcf-api-grpc gateway 1
assert_resource_field "$named_render" GRPCRoute plane-a-nvcf-api-grpc gateway '.spec.rules[0].backendRefs[0].namespace' plane-a-nvcf
assert_resource_count "$named_render" HTTPRoute plane-a-nvct-api gateway 1
assert_resource_field "$named_render" HTTPRoute plane-a-nvct-api gateway '.spec.rules[0].backendRefs[0].namespace' plane-a-nvcf
assert_resource_count "$named_render" GRPCRoute plane-a-nvct-api-grpc gateway 1
assert_resource_count "$named_render" HTTPRoute plane-a-api-keys gateway 1
assert_resource_field "$named_render" HTTPRoute plane-a-api-keys gateway '.spec.rules[0].backendRefs[0].namespace' plane-a-api-keys
assert_resource_count "$named_render" HTTPRoute plane-a-invocation-service gateway 1
assert_resource_count "$named_render" HTTPRoute plane-a-llm-api-gateway gateway 1
assert_resource_count "$named_render" HTTPRoute plane-a-llm-invocation gateway 1
assert_resource_count "$named_render" HTTPRoute plane-a-vanity-gateway gateway 1
assert_resource_count "$named_render" HTTPRoute plane-a-reval gateway 1
assert_resource_count "$named_render" HTTPRoute plane-a-nvcf-ui gateway 1
assert_resource_count "$named_render" HTTPRoute plane-a-sis gateway 1
assert_resource_count "$named_render" HTTPRoute plane-a-event-ledger gateway 1
assert_resource_count "$named_render" TCPRoute plane-a-grpc gateway 1
assert_resource_count "$named_render" TCPRoute plane-a-grpc-worker gateway 1
assert_resource_count "$named_render" TCPRoute plane-a-nats gateway 1
assert_resource_count "$named_render" GRPCRoute plane-a-llm-worker-grpc gateway 1
assert_resource_count "$named_render" UDPRoute plane-a-llm-worker-quic gateway 1
assert_resource_count "$named_render" BackendTrafficPolicy plane-a-llm-worker-grpc-streams gateway 1
assert_resource_field "$named_render" BackendTrafficPolicy plane-a-llm-worker-grpc-streams gateway '.spec.targetRefs[0].name' plane-a-llm-worker-grpc

assert_resource_count "$named_render" ReferenceGrant allow-routes-to-plane-a-nvcf plane-a-nvcf 1
assert_resource_count "$named_render" ReferenceGrant allow-httproute-to-plane-a-api-keys plane-a-api-keys 1
assert_resource_count "$named_render" ReferenceGrant allow-httproute-to-plane-a-sis plane-a-sis 1
assert_resource_count "$named_render" ReferenceGrant allow-httproute-to-plane-a-nvcf-ui plane-a-nvcf-ui 1
assert_resource_count "$named_render" ReferenceGrant allow-tcproute-to-plane-a-nats plane-a-nats-system 1
assert_resource_count "$named_render" ReferenceGrant allow-plane-a-llm-worker-routes plane-a-nvcf 1

echo "Gateway route render checks passed."
