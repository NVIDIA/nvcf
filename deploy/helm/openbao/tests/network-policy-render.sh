#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -eu

script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
chart_dir=$(CDPATH= cd -- "${script_dir}/../helm" && pwd)
repo_root=$(CDPATH= cd -- "${script_dir}/../../../.." && pwd)
ci_values="${repo_root}/tools/ci/helm-validate-values/openbao.yaml"
tmpdir=$(mktemp -d)

cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT

fail() {
  echo "network-policy-render: $*" >&2
  exit 1
}

render() {
  helm template openbao-server "$chart_dir" \
    --namespace vault-system \
    --values "$ci_values" \
    "$@"
}

query() {
  expression=$1
  manifest=$2
  yq -rN "$expression" "$manifest"
}

query_all() {
  expression=$1
  manifest=$2
  yq eval-all -rN "$expression" "$manifest"
}

policy_query() {
  expression=$1
  manifest=$2
  yq -N 'select(.kind == "NetworkPolicy" and .metadata.name == "openbao-server")' "$manifest" |
    yq -rN "$expression"
}

assert_equal() {
  expected=$1
  actual=$2
  description=$3

  if [ "$actual" != "$expected" ]; then
    fail "${description}: expected ${expected}, got ${actual}"
  fi
}

default_manifest="${tmpdir}/default.yaml"
render >"$default_manifest"

policy_count=$(query_all '[select(.kind == "NetworkPolicy" and .metadata.name == "openbao-server")] | length' "$default_manifest")
assert_equal 0 "$policy_count" "default NetworkPolicy count"

render --set openbao.networkPolicy.enabled=true >"$default_manifest"
policy_count=$(query_all '[select(.kind == "NetworkPolicy" and .metadata.name == "openbao-server")] | length' "$default_manifest")
assert_equal 1 "$policy_count" "enabled NetworkPolicy count"

policy_namespace=$(policy_query '.metadata.namespace' "$default_manifest")
assert_equal vault-system "$policy_namespace" "default policy namespace"

selected_name=$(policy_query '.spec.podSelector.matchLabels."app.kubernetes.io/name"' "$default_manifest")
selected_instance=$(policy_query '.spec.podSelector.matchLabels."app.kubernetes.io/instance"' "$default_manifest")
selected_component=$(policy_query '.spec.podSelector.matchLabels.component' "$default_manifest")
assert_equal openbao "$selected_name" "selected application"
assert_equal openbao-server "$selected_instance" "selected release"
assert_equal server "$selected_component" "selected component"

server_name=$(query 'select(.kind == "StatefulSet" and .metadata.name == "openbao-server") | .spec.template.metadata.labels."app.kubernetes.io/name"' "$default_manifest")
server_instance=$(query 'select(.kind == "StatefulSet" and .metadata.name == "openbao-server") | .spec.template.metadata.labels."app.kubernetes.io/instance"' "$default_manifest")
server_component=$(query 'select(.kind == "StatefulSet" and .metadata.name == "openbao-server") | .spec.template.metadata.labels.component' "$default_manifest")
assert_equal "$selected_name" "$server_name" "selected StatefulSet application"
assert_equal "$selected_instance" "$server_instance" "selected StatefulSet release"
assert_equal "$selected_component" "$server_component" "selected StatefulSet component"

policy_type_count=$(policy_query '.spec.policyTypes | length' "$default_manifest")
policy_type=$(policy_query '.spec.policyTypes[0]' "$default_manifest")
egress=$(policy_query '.spec.egress' "$default_manifest")
assert_equal 1 "$policy_type_count" "policyTypes count"
assert_equal Ingress "$policy_type" "policy type"
assert_equal null "$egress" "egress policy"

empty_peer_count=$(policy_query '[.spec.ingress[].from[] | select(. == {} or .podSelector == {} or .namespaceSelector == {})] | length' "$default_manifest")
namespace_only_peer_count=$(policy_query '[.spec.ingress[].from[] | select(has("namespaceSelector") and (has("podSelector") | not))] | length' "$default_manifest")
assert_equal 0 "$empty_peer_count" "empty ingress peer count"
assert_equal 0 "$namespace_only_peer_count" "namespace-only ingress peer count"

server_peer_count=$(policy_query '[.spec.ingress[] | select(.from[]?.podSelector.matchLabels."app.kubernetes.io/name" == "openbao" and .from[]?.podSelector.matchLabels."app.kubernetes.io/instance" == "openbao-server" and .from[]?.podSelector.matchLabels.component == "server") | select(([.ports[].port] | contains([8200, 8201])))] | length' "$default_manifest")
assert_equal 1 "$server_peer_count" "OpenBao peer rule count"

bootstrap_peer_count=$(policy_query '[.spec.ingress[].from[] | select(.podSelector.matchLabels."app.kubernetes.io/instance" == "openbao-server" and .podSelector.matchLabels."app.kubernetes.io/component" == "openbao-bootstrap")] | length' "$default_manifest")
migration_peer_count=$(policy_query '[.spec.ingress[].from[] | select(.podSelector.matchLabels."app.kubernetes.io/component" == "openbao-migrations")] | length' "$default_manifest")
assert_equal 1 "$bootstrap_peer_count" "OpenBao bootstrap peer count"
assert_equal 1 "$migration_peer_count" "OpenBao migration peer count"

cert_manager_peer_count=$(policy_query '[.spec.ingress[].from[] | select(.namespaceSelector.matchLabels."kubernetes.io/metadata.name" == "cert-manager" and .podSelector.matchLabels."app.kubernetes.io/name" == "cert-manager" and .podSelector.matchLabels."app.kubernetes.io/instance" == "cert-manager" and .podSelector.matchLabels."app.kubernetes.io/component" == "controller")] | length' "$default_manifest")
nvcf_peer_count=$(policy_query '[.spec.ingress[].from[] | select(.namespaceSelector.matchLabels."kubernetes.io/metadata.name" == "nvcf" and .podSelector.matchExpressions[0].key == "app.kubernetes.io/instance" and .podSelector.matchExpressions[0].operator == "In")] | length' "$default_manifest")
nvcf_instances=$(policy_query '.spec.ingress[].from[] | select(.namespaceSelector.matchLabels."kubernetes.io/metadata.name" == "nvcf") | .podSelector.matchExpressions[0].values | sort | join(",")' "$default_manifest")
api_keys_peer_count=$(policy_query '[.spec.ingress[].from[] | select(.namespaceSelector.matchLabels."kubernetes.io/metadata.name" == "api-keys" and .podSelector.matchExpressions[0].key == "app.kubernetes.io/instance" and .podSelector.matchExpressions[0].operator == "In")] | length' "$default_manifest")
api_keys_instances=$(policy_query '.spec.ingress[].from[] | select(.namespaceSelector.matchLabels."kubernetes.io/metadata.name" == "api-keys") | .podSelector.matchExpressions[0].values | sort | join(",")' "$default_manifest")
ess_peer_count=$(policy_query '[.spec.ingress[].from[] | select(.namespaceSelector.matchLabels."kubernetes.io/metadata.name" == "ess" and .podSelector.matchLabels."app.kubernetes.io/instance" == "ess-api")] | length' "$default_manifest")
nvcf_ui_peer_count=$(policy_query '[.spec.ingress[].from[] | select(.namespaceSelector.matchLabels."kubernetes.io/metadata.name" == "nvcf-ui" and .podSelector.matchLabels."app.kubernetes.io/instance" == "nvcf-ui")] | length' "$default_manifest")
assert_equal 1 "$cert_manager_peer_count" "cert-manager peer count"
assert_equal 1 "$nvcf_peer_count" "nvcf peer count"
assert_equal 'api,function-autoscaler,grpc-proxy,invocation-service,llm-api-gateway,llm-request-router,notary-service,nvct-api,ratelimiter' "$nvcf_instances" "nvcf client instances"
assert_equal 1 "$api_keys_peer_count" "api-keys peer count"
assert_equal 'admin-issuer-proxy,api-keys' "$api_keys_instances" "api-keys client instances"
assert_equal 1 "$ess_peer_count" "ess peer count"
assert_equal 1 "$nvcf_ui_peer_count" "nvcf-ui peer count"

non_api_client_port_count=$(policy_query '[.spec.ingress[1:][]?.ports[] | select(.protocol != "TCP" or .port != 8200)] | length' "$default_manifest")
assert_equal 0 "$non_api_client_port_count" "non-API client port count"

bootstrap_label=$(query 'select(.kind == "Job" and .metadata.name == "openbao-server-initialize-cluster") | .spec.template.metadata.labels."app.kubernetes.io/component"' "$default_manifest")
migrations_label=$(query 'select(.kind == "Job" and .metadata.name == "openbao-server-migrations") | .spec.template.metadata.labels."app.kubernetes.io/component"' "$default_manifest")
bootstrap_hook=$(query 'select(.kind == "Job" and .metadata.name == "openbao-server-initialize-cluster") | .metadata.annotations."helm.sh/hook"' "$default_manifest")
bootstrap_weight=$(query 'select(.kind == "Job" and .metadata.name == "openbao-server-initialize-cluster") | .metadata.annotations."helm.sh/hook-weight"' "$default_manifest")
migrations_hook=$(query 'select(.kind == "Job" and .metadata.name == "openbao-server-migrations") | .metadata.annotations."helm.sh/hook"' "$default_manifest")
migrations_weight=$(query 'select(.kind == "Job" and .metadata.name == "openbao-server-migrations") | .metadata.annotations."helm.sh/hook-weight"' "$default_manifest")
assert_equal openbao-bootstrap "$bootstrap_label" "bootstrap pod label"
assert_equal openbao-migrations "$migrations_label" "migrations pod label"
assert_equal post-install "$bootstrap_hook" "bootstrap hook phase"
assert_equal -5 "$bootstrap_weight" "bootstrap hook weight"
assert_equal post-install,post-upgrade "$migrations_hook" "migrations hook phases"
assert_equal 1 "$migrations_weight" "migrations hook weight"

disabled_manifest="${tmpdir}/disabled.yaml"
render --set openbao.networkPolicy.enabled=false >"$disabled_manifest"
disabled_count=$(query_all '[select(.kind == "NetworkPolicy" and .metadata.name == "openbao-server")] | length' "$disabled_manifest")
assert_equal 0 "$disabled_count" "disabled NetworkPolicy count"

no_clients_manifest="${tmpdir}/no-clients.yaml"
render \
  --set openbao.networkPolicy.enabled=true \
  --set openbao.networkPolicy.clients.certManager.enabled=false \
  --set openbao.networkPolicy.clients.apiKeys.enabled=false \
  --set openbao.networkPolicy.clients.ess.enabled=false \
  --set openbao.networkPolicy.clients.nvcf.enabled=false \
  --set openbao.networkPolicy.clients.nvcfUi.enabled=false \
  >"$no_clients_manifest"
no_clients_ingress_count=$(policy_query '.spec.ingress | length' "$no_clients_manifest")
empty_from_count=$(policy_query '[.spec.ingress[] | select((has("from") | not) or .from == null or (.from | length) == 0)] | length' "$no_clients_manifest")
assert_equal 2 "$no_clients_ingress_count" "ingress rule count with all API clients disabled"
assert_equal 0 "$empty_from_count" "empty ingress source count with all API clients disabled"

invalid_client_manifest="${tmpdir}/invalid-client.yaml"
invalid_client_error="${tmpdir}/invalid-client.err"
if render \
  --set openbao.networkPolicy.enabled=true \
  --set openbao.networkPolicy.clients.custom.enabled=true \
  --set-string openbao.networkPolicy.clients.custom.namespace=custom-client \
  >"$invalid_client_manifest" 2>"$invalid_client_error"; then
  fail "enabled client without a pod selector rendered successfully"
fi
if ! grep -q 'openbao.networkPolicy.clients.custom.podSelector is required when the client is enabled' "$invalid_client_error"; then
  fail "enabled client without a pod selector did not report the expected error"
fi

empty_match_labels_values="${tmpdir}/empty-match-labels.yaml"
empty_match_expressions_values="${tmpdir}/empty-match-expressions.yaml"
cat >"$empty_match_labels_values" <<'EOF'
openbao:
  networkPolicy:
    clients:
      custom:
        enabled: true
        namespace: custom-client
        podSelector:
          matchLabels: {}
EOF
cat >"$empty_match_expressions_values" <<'EOF'
openbao:
  networkPolicy:
    clients:
      custom:
        enabled: true
        namespace: custom-client
        podSelector:
          matchExpressions: []
EOF
for broad_client_values in "$empty_match_labels_values" "$empty_match_expressions_values"; do
  broad_client_error="${broad_client_values}.err"
  if render --set openbao.networkPolicy.enabled=true --values "$broad_client_values" >/dev/null 2>"$broad_client_error"; then
    fail "enabled client with an empty nested pod selector rendered successfully"
  fi
  if ! grep -q 'openbao.networkPolicy.clients.custom.podSelector must contain at least one matchLabels or matchExpressions entry' "$broad_client_error"; then
    fail "enabled client with an empty nested pod selector did not report the expected error"
  fi
done

override_manifest="${tmpdir}/override.yaml"
render \
  --set openbao.networkPolicy.enabled=true \
  --set-string openbao.namespace=security-openbao \
  --set-string openbao.networkPolicy.clients.certManager.namespace=security-cert-manager \
  --set-string openbao.networkPolicy.clients.nvcf.namespace=control-plane \
  >"$override_manifest"

override_policy_namespace=$(policy_query '.metadata.namespace' "$override_manifest")
override_cert_manager_count=$(policy_query '[.spec.ingress[].from[] | select(.namespaceSelector.matchLabels."kubernetes.io/metadata.name" == "security-cert-manager" and .podSelector.matchLabels."app.kubernetes.io/component" == "controller")] | length' "$override_manifest")
override_nvcf_count=$(policy_query '[.spec.ingress[].from[] | select(.namespaceSelector.matchLabels."kubernetes.io/metadata.name" == "control-plane" and (.podSelector.matchExpressions[]? | select(.key == "app.kubernetes.io/instance")))] | length' "$override_manifest")
assert_equal security-openbao "$override_policy_namespace" "overridden policy namespace"
assert_equal 1 "$override_cert_manager_count" "overridden cert-manager namespace"
assert_equal 1 "$override_nvcf_count" "overridden nvcf namespace"

global_namespace_manifest="${tmpdir}/global-namespace.yaml"
render \
  --set openbao.networkPolicy.enabled=true \
  --set-string openbao.namespace=ignored-openbao \
  --set-string openbao.global.namespace=global-openbao \
  >"$global_namespace_manifest"
global_policy_namespace=$(policy_query '.metadata.namespace' "$global_namespace_manifest")
assert_equal global-openbao "$global_policy_namespace" "global namespace precedence"

echo "network-policy-render: all checks passed"
