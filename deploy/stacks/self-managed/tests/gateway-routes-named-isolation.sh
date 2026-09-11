#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
stack_dir="$(cd "$script_dir/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
values_file="$work_dir/ingress-values.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "gateway-routes-named-isolation: $*" >&2
  exit 1
}

command -v helmfile >/dev/null 2>&1 || fail "helmfile is required"
command -v yq >/dev/null 2>&1 || fail "yq is required"

cp -R "$stack_dir" "$test_stack_dir"

# Named mode is intentionally still blocked in the source tree while other
# object-level leak classes are fixed. This test unlocks only the temp copy so
# it can inspect the gateway values produced by this subphase.
perl -0pi -e 's/\$namedControlPlaneObjectIdentitiesReady := false/\$namedControlPlaneObjectIdentitiesReady := true/g' \
  "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
  "$test_stack_dir/global.yaml.gotmpl"

if ! HELMFILE_ENV=base \
  NVCF_CONTROL_PLANE_OWNER=plane-a \
  NVCF_ALPHA_NAMED_CONTROL_PLANE=true \
  HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
  helmfile \
    --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
    --environment default \
    --state-values-set ingress.gatewayApi.controllerNamespace=gateway \
    --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gateway \
    --state-values-set ingress.gatewayApi.gateways.shared.namespace=gateway \
    --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gateway \
    --state-values-set ingress.gatewayApi.gateways.grpc.namespace=gateway \
    --state-values-set ingress.gatewayApi.routes.nvcfApi.grpc.enabled=true \
    --state-values-set ingress.gatewayApi.routes.nvctApi.grpc.enabled=true \
    --state-values-set addons.llm.enabled=true \
    --state-values-set-string global.workerEndpoints.llmRequestRouterAddress=http://llm-grpc-gw:50071 \
    --state-values-set-string addons.llm.requestRouter.backendRouter.pylonGrpcDialAddress=http://llm-grpc-gw:50071 \
    --state-values-set-string addons.llm.requestRouter.backendRouter.pylonReverseTunnelDialAddress=llm-quic-gw:50072 \
    --state-values-set addons.llm.requestRouter.grpcTls.allowInsecureHttp=true \
    --state-values-set addons.vanityGateway.enabled=true \
    --state-values-set addons.nvcfUi.enabled=true \
    --state-values-set addons.eventLedger.enabled=true \
    --state-values-set ingress.gatewayApi.routes.grpcWorker.enabled=true \
    --state-values-set ingress.gatewayApi.routes.nats.enabled=true \
    --state-values-set ingress.gatewayApi.gateways.nats.name=nats-gateway \
    --state-values-set ingress.gatewayApi.gateways.nats.namespace=gateway \
    --state-values-set ingress.gatewayApi.gateways.nats.listenerName=nats \
    --state-values-set ingress.gatewayApi.routes.llmWorker.enabled=true \
    --state-values-set ingress.gatewayApi.gateways.llmGrpc.name=llm-grpc-gateway \
    --state-values-set ingress.gatewayApi.gateways.llmGrpc.namespace=gateway \
    --state-values-set ingress.gatewayApi.gateways.llmGrpc.listenerName=llm-grpc \
    --state-values-set ingress.gatewayApi.gateways.llmQuic.name=llm-quic-gateway \
    --state-values-set ingress.gatewayApi.gateways.llmQuic.namespace=gateway \
    --state-values-set ingress.gatewayApi.gateways.llmQuic.listenerName=llm-quic \
    --state-values-set ingress.gatewayApi.routes.ess.enabled=true \
    --selector name=ingress \
    write-values \
    --output-file-template "$values_file" >/dev/null; then
  fail "helmfile could not render named gateway values"
fi

assert_value() {
  local expression="$1"
  local expected="$2"
  local actual

  actual="$(yq -r "$expression" "$values_file")"
  test "$actual" = "$expected" ||
    fail "expected $expression to be $expected, got $actual"
}

assert_value '.nvcfGatewayRoutes.routes.nvcfApi.name' plane-a-nvcf-api
assert_value '.nvcfGatewayRoutes.routes.nvcfApi.backend.namespace' plane-a-nvcf
assert_value '.nvcfGatewayRoutes.routes.nvcfApi.grpc.name' plane-a-nvcf-api-grpc
assert_value '.nvcfGatewayRoutes.routes.nvcfApi.grpc.backend.namespace' plane-a-nvcf
assert_value '.nvcfGatewayRoutes.routes.nvctApi.name' plane-a-nvct-api
assert_value '.nvcfGatewayRoutes.routes.nvctApi.backend.namespace' plane-a-nvcf
assert_value '.nvcfGatewayRoutes.routes.nvctApi.grpc.name' plane-a-nvct-api-grpc
assert_value '.nvcfGatewayRoutes.routes.nvctApi.grpc.backend.namespace' plane-a-nvcf
assert_value '.nvcfGatewayRoutes.routes.apiKeys.name' plane-a-api-keys
assert_value '.nvcfGatewayRoutes.routes.apiKeys.backend.namespace' plane-a-api-keys
assert_value '.nvcfGatewayRoutes.routes.invocation.name' plane-a-invocation-service
assert_value '.nvcfGatewayRoutes.routes.invocation.backend.namespace' plane-a-nvcf
assert_value '.nvcfGatewayRoutes.routes.llmApiGateway.name' plane-a-llm-api-gateway
assert_value '.nvcfGatewayRoutes.routes.llmApiGateway.backend.namespace' plane-a-nvcf
assert_value '.nvcfGatewayRoutes.routes.llmInvocation.name' plane-a-llm-invocation
assert_value '.nvcfGatewayRoutes.routes.llmInvocation.backend.namespace' plane-a-nvcf
assert_value '.nvcfGatewayRoutes.routes.vanityGateway.name' plane-a-vanity-gateway
assert_value '.nvcfGatewayRoutes.routes.vanityGateway.backend.namespace' plane-a-nvcf
assert_value '.nvcfGatewayRoutes.routes.reval.name' plane-a-reval
assert_value '.nvcfGatewayRoutes.routes.reval.backend.namespace' plane-a-nvcf
assert_value '.nvcfGatewayRoutes.routes.nvcfUi.name' plane-a-nvcf-ui
assert_value '.nvcfGatewayRoutes.routes.nvcfUi.backend.namespace' plane-a-nvcf-ui
assert_value '.nvcfGatewayRoutes.routes.sis.name' plane-a-sis
assert_value '.nvcfGatewayRoutes.routes.sis.backend.namespace' plane-a-sis
assert_value '.nvcfGatewayRoutes.routes.eventLedger.name' plane-a-event-ledger
assert_value '.nvcfGatewayRoutes.routes.eventLedger.backend.namespace' plane-a-nvcf
assert_value '.nvcfGatewayRoutes.routes.grpc.name' plane-a-grpc
assert_value '.nvcfGatewayRoutes.routes.grpc.backend.namespace' plane-a-nvcf
assert_value '.nvcfGatewayRoutes.routes.grpcWorker.name' plane-a-grpc-worker
assert_value '.nvcfGatewayRoutes.routes.grpcWorker.backend.namespace' plane-a-nvcf
assert_value '.nvcfGatewayRoutes.routes.nats.name' plane-a-nats
assert_value '.nvcfGatewayRoutes.routes.nats.backend.namespace' plane-a-nats-system
assert_value '.nvcfGatewayRoutes.routes.llmWorker.name' plane-a-llm-worker
assert_value '.nvcfGatewayRoutes.routes.llmWorker.backend.namespace' plane-a-nvcf
assert_value '.nvcfGatewayRoutes.routes.ess.name' plane-a-ess
assert_value '.nvcfGatewayRoutes.routes.ess.backend.namespace' plane-a-ess

echo "gateway-routes-named-isolation: all checks passed"
