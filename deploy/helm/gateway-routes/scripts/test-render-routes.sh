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
trap 'rm -f "$default_render" "$enabled_render" "$annotated_render" "$disabled_render"' EXIT

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

assert_resource_count "$default_render" HTTPRoute sis gateway 1
assert_resource_field "$default_render" HTTPRoute sis gateway '.metadata.labels."app.kubernetes.io/component"' sis-route
assert_resource_field "$default_render" HTTPRoute sis gateway '.spec.hostnames[0]' sis.localhost
assert_resource_field "$default_render" HTTPRoute sis gateway '.spec.rules[0].backendRefs[0].name' api
assert_resource_field "$default_render" HTTPRoute sis gateway '.spec.rules[0].backendRefs[0].namespace' sis
assert_resource_field "$default_render" HTTPRoute sis gateway '.spec.rules[0].backendRefs[0].port' 8080

assert_resource_count "$default_render" HTTPRoute reval gateway 1
assert_resource_field "$default_render" HTTPRoute reval gateway '.metadata.labels."app.kubernetes.io/component"' reval-route
assert_resource_field "$default_render" HTTPRoute reval gateway '.spec.hostnames[0]' reval.localhost
assert_resource_field "$default_render" HTTPRoute reval gateway '.spec.rules[0].backendRefs[0].name' reval
assert_resource_field "$default_render" HTTPRoute reval gateway '.spec.rules[0].backendRefs[0].namespace' nvcf
assert_resource_field "$default_render" HTTPRoute reval gateway '.spec.rules[0].backendRefs[0].port' 8080

helm template nvcf-gateway-routes "$repo_root/chart" \
  --set nvcfGatewayRoutes.routes.reval.enabled=false \
  > "$disabled_render"

assert_resource_count "$disabled_render" HTTPRoute reval gateway 0

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
assert_resource_count "$default_render" ReferenceGrant allow-tcproute-to-nats nats-system 0

helm template nvcf-gateway-routes "$repo_root/chart" \
  --set nvcfGatewayRoutes.routes.llmInvocation.enabled=true \
  --set nvcfGatewayRoutes.routes.nvcfApi.grpc.enabled=true \
  --set nvcfGatewayRoutes.routes.nvctApi.grpc.enabled=true \
  --set nvcfGatewayRoutes.routes.grpcWorker.enabled=true \
  --set nvcfGatewayRoutes.routes.nats.enabled=true \
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

assert_resource_count "$enabled_render" ReferenceGrant allow-tcproute-to-nats nats-system 1
assert_resource_field "$enabled_render" ReferenceGrant allow-tcproute-to-nats nats-system '.spec.from[0].kind' TCPRoute
assert_resource_field "$enabled_render" ReferenceGrant allow-tcproute-to-nats nats-system '.spec.from[0].namespace' gateway
assert_resource_field "$enabled_render" ReferenceGrant allow-tcproute-to-nats nats-system '.spec.to[0].kind' Service

helm template nvcf-gateway-routes "$repo_root/chart" \
  --set nvcfGatewayRoutes.routes.nats.enabled=true \
  --set 'nvcfGatewayRoutes.routes.nats.routeAnnotations.example\.com/nats-route=true' \
  > "$annotated_render"

assert_resource_field "$annotated_render" TCPRoute nats gateway '.metadata.annotations."example.com/nats-route"' true

echo "Gateway route render checks passed."
