#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../llm-request-router" && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT

fail() {
  echo "load-balancer-render: $*" >&2
  exit 1
}

render() {
  local manifest="$1"
  shift
  helm template llm-request-router "${chart_dir}" \
    --namespace nvcf \
    --set llmRequestRouter.image.repository=stargate \
    "$@" >"${manifest}"
}

router_args() {
  local manifest="$1"
  yq ea -r \
    'select((.kind == "Deployment" or .kind == "StatefulSet") and .metadata.name == "llm-request-router") | .spec.template.spec.containers[0].args[]' \
    "${manifest}"
}

default_manifest="${work_dir}/default.yaml"
render "${default_manifest}"

default_config_maps="$(yq ea -r '[select(.kind == "ConfigMap" and .metadata.name == "llm-request-router-lb")] | length' "${default_manifest}")"
test "${default_config_maps}" = "0" || fail "default render created an embedded load-balancer ConfigMap"
if router_args "${default_manifest}" | grep -q -- '^--lb-config-path='; then
  fail "default render overrode Stargate's built-in load-balancer policy"
fi

embedded_values="${work_dir}/embedded-values.yaml"
cat >"${embedded_values}" <<'EOF'
llmRequestRouter:
  loadBalancer:
    config: |
      {
        "default": "random",
        "request_algorithms": {
          "round-robin": "round-robin"
        }
      }
EOF

embedded_manifest="${work_dir}/embedded.yaml"
render "${embedded_manifest}" --values "${embedded_values}"
embedded_config="$(yq ea -r 'select(.kind == "ConfigMap" and .metadata.name == "llm-request-router-lb") | .data."lb-config.json"' "${embedded_manifest}")"
printf '%s' "${embedded_config}" | jq -e \
  '.default == "random" and .request_algorithms == {"round-robin":"round-robin"}' \
  >/dev/null || fail "explicit embedded load-balancer config did not round-trip semantically"
router_args "${embedded_manifest}" | grep -qx -- '--lb-config-path=/etc/llm-request-router/lb-config.json' ||
  fail "explicit embedded config did not mount the chart-managed path"

path_manifest="${work_dir}/path.yaml"
render "${path_manifest}" --set-string llmRequestRouter.loadBalancer.configPath=/etc/stargate/lb.json
path_config_maps="$(yq ea -r '[select(.kind == "ConfigMap" and .metadata.name == "llm-request-router-lb")] | length' "${path_manifest}")"
test "${path_config_maps}" = "0" || fail "configPath-only render created an embedded ConfigMap"
router_args "${path_manifest}" | grep -qx -- '--lb-config-path=/etc/stargate/lb.json' ||
  fail "explicit load-balancer configPath was not forwarded"

echo "load-balancer-render: all checks passed"
