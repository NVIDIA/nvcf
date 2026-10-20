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
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

render() {
  helm template ingress "$chart_root/chart" "$@" > "$work_dir/rendered.yaml"
}
assert_policy_count() {
  actual="$(yq ea '[select(.kind == "BackendTrafficPolicy" and .metadata.labels."app.kubernetes.io/component" == "nvcf-api-route")] | length' "$work_dir/rendered.yaml")"
  test "$actual" = "$1" || { echo "expected $1 API policies, got $actual" >&2; exit 1; }
}

render
assert_policy_count 0
render --set nvcfGatewayRoutes.routes.nvcfApi.envoyBackendTrafficPolicy.enabled=true
assert_policy_count 1
yq ea -e 'select(.kind == "BackendTrafficPolicy") | .spec.timeout.http.connectionIdleTimeout == "30s" and .spec.targetRefs[0].kind == "HTTPRoute" and .spec.targetRefs[0].name == "nvcf-api" and .spec.retry == null and .spec.timeout.http.requestTimeout == null' "$work_dir/rendered.yaml" >/dev/null

# Preserve a safety margin against the backend default; derive both values.
proxy_timeout="$(yq '.nvcfGatewayRoutes.routes.nvcfApi.envoyBackendTrafficPolicy.connectionIdleTimeout' "$chart_root/chart/values.yaml")"
server_timeout="$(yq '.server.tomcat.keep-alive-timeout' "$repo_root/src/control-plane-services/cloud-functions/nvcf-service/src/main/resources/application.yaml")"
case "$proxy_timeout:$server_timeout" in
  *s:*s) test "${proxy_timeout%s}" -gt 0 && test "${proxy_timeout%s}" -lt "${server_timeout%s}" ;;
  *) echo "update timeout comparison for non-second durations" >&2; exit 1 ;;
esac

render --set nvcfGatewayRoutes.routes.nvcfApi.envoyBackendTrafficPolicy.enabled=true \
  --set nvcfGatewayRoutes.routes.nvcfApi.envoyBackendTrafficPolicy.connectionIdleTimeout=20s \
  --set nvcfGatewayRoutes.routes.nvcfApi.name=custom-api \
  --set nvcfGatewayRoutes.gateways.shared.namespace=custom-gateway
yq ea -e 'select(.kind == "BackendTrafficPolicy") | .metadata.name == "custom-api-connection-idle" and .metadata.namespace == "custom-gateway" and .spec.targetRefs[0].name == "custom-api" and .spec.timeout.http.connectionIdleTimeout == "20s"' "$work_dir/rendered.yaml" >/dev/null

for setting in nvcfGatewayRoutes.enabled=false nvcfGatewayRoutes.routes.nvcfApi.enabled=false; do
  render --set nvcfGatewayRoutes.routes.nvcfApi.envoyBackendTrafficPolicy.enabled=true --set "$setting"
  assert_policy_count 0
done
if helm template ingress "$chart_root/chart" \
  --set nvcfGatewayRoutes.routes.nvcfApi.envoyBackendTrafficPolicy.enabled=true \
  --set-string nvcfGatewayRoutes.routes.nvcfApi.envoyBackendTrafficPolicy.connectionIdleTimeout= \
  >"$work_dir/invalid.yaml" 2>"$work_dir/error"; then
  echo "an enabled policy must require a timeout" >&2
  exit 1
fi
echo "API connection idle policy checks passed."
