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

chart_root="$(cd "$(dirname "$0")/.." && pwd)"
repo_root="$(cd "$chart_root/../../.." && pwd)"
policy="$repo_root/docs/overview/samples/configs/envoy-api-connection-idle.yaml"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

yq -e '.apiVersion == "gateway.envoyproxy.io/v1alpha1" and .kind == "BackendTrafficPolicy" and (.spec.targetRefs | length) == 1 and .spec.targetRefs[0].group == "gateway.networking.k8s.io" and .spec.targetRefs[0].kind == "HTTPRoute" and .spec.retry == null and .spec.timeout.http.requestTimeout == null' "$policy" >/dev/null

# The standalone policy must target the API route without adding a chart policy.
policy_namespace="$(yq '.metadata.namespace' "$policy")"
route_name="$(yq '.nvcfGatewayRoutes.routes.nvcfApi.name' "$chart_root/chart/values.yaml")"
test "$(yq '.spec.targetRefs[0].name' "$policy")" = "$route_name"
helm template ingress "$chart_root/chart" \
  --set "nvcfGatewayRoutes.gateways.shared.namespace=$policy_namespace" > "$work_dir/rendered.yaml"
API_IDLE_ROUTE_NAME="$route_name" yq ea -e '[select(.kind == "HTTPRoute" and .metadata.name == strenv(API_IDLE_ROUTE_NAME))] | length == 1' "$work_dir/rendered.yaml" >/dev/null
API_IDLE_ROUTE_NAME="$route_name" yq ea -e '[select(.kind == "BackendTrafficPolicy") | .spec.targetRefs[] | select(.kind == "HTTPRoute" and .name == strenv(API_IDLE_ROUTE_NAME))] | length == 0' "$work_dir/rendered.yaml" >/dev/null

# Preserve a safety margin against the backend default; derive both values.
proxy_timeout="$(yq '.spec.timeout.http.connectionIdleTimeout' "$policy")"
server_timeout="$(yq '.server.tomcat.keep-alive-timeout' "$repo_root/src/control-plane-services/cloud-functions/nvcf-service/src/main/resources/application.yaml")"
case "$proxy_timeout:$server_timeout" in
  *s:*s) test "${proxy_timeout%s}" -gt 0 && test "${proxy_timeout%s}" -lt "${server_timeout%s}" ;;
  *) echo "update timeout comparison for non-second durations" >&2; exit 1 ;;
esac

echo "Standalone Envoy API connection idle example checks passed."
