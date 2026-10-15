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

chart_dir=${CHART_DIR:-.}
release_name=${RELEASE_NAME:-nvcf-nats}
namespace=${NAMESPACE:-nats-system}
tmpdir=$(mktemp -d)

cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT

export HELM_CONFIG_HOME=${HELM_CONFIG_HOME:-"$tmpdir/helm-config"}
export HELM_CACHE_HOME=${HELM_CACHE_HOME:-"$tmpdir/helm-cache"}
export HELM_DATA_HOME=${HELM_DATA_HOME:-"$tmpdir/helm-data"}

render() {
  helm template "$release_name" "$chart_dir" \
    --namespace "$namespace" \
    --values "$chart_dir/values.yaml" \
    "$@"
}

assert_contains() {
  file=$1
  pattern=$2

  if ! grep -Fq -- "$pattern" "$file"; then
    echo "expected rendered output to contain: $pattern" >&2
    return 1
  fi
}

assert_not_contains() {
  file=$1
  pattern=$2

  if grep -Fq -- "$pattern" "$file"; then
    echo "expected rendered output not to contain: $pattern" >&2
    return 1
  fi
}

default_render="$tmpdir/default.yaml"
render > "$default_render"
test "$(yq -r 'select(.kind == "StatefulSet") | .spec.template.spec.securityContext.runAsUser' "$default_render")" = 1000
test "$(yq -r 'select(.kind == "StatefulSet") | .spec.template.spec.securityContext.fsGroup' "$default_render")" = 1000
for container in nats reloader; do
  test "$(yq -r "select(.kind == \"StatefulSet\") | .spec.template.spec.containers[] | select(.name == \"$container\") | .securityContext.runAsNonRoot" "$default_render")" = true
  test "$(yq -r "select(.kind == \"StatefulSet\") | .spec.template.spec.containers[] | select(.name == \"$container\") | .securityContext.runAsUser" "$default_render")" = 1000
done
# Environment-supplied identities must override defaults without dropping the
# remaining non-root restrictions from the owning chart.
identity_render="$tmpdir/identity-override.yaml"
render \
  --set nats.container.merge.securityContext.runAsUser=2000 \
  --set nats.container.merge.securityContext.runAsGroup=2000 \
  --set nats.reloader.merge.securityContext.runAsUser=2000 \
  --set nats.reloader.merge.securityContext.runAsGroup=2000 \
  --set nats.podTemplate.merge.spec.securityContext.runAsUser=2000 \
  --set nats.podTemplate.merge.spec.securityContext.runAsGroup=2000 \
  --set nats.podTemplate.merge.spec.securityContext.fsGroup=2000 > "$identity_render"
for field in runAsUser runAsGroup fsGroup; do
  test "$(yq -r "select(.kind == \"StatefulSet\") | .spec.template.spec.securityContext.$field" "$identity_render")" = 2000
done
for container in nats reloader; do
  context="select(.kind == \"StatefulSet\") | .spec.template.spec.containers[] | select(.name == \"$container\") | .securityContext"
  for field in runAsUser runAsGroup; do
    test "$(yq -r "$context.$field" "$identity_render")" = 2000
  done
  test "$(yq -r "$context.runAsNonRoot" "$identity_render")" = true
  test "$(yq -r "$context.allowPrivilegeEscalation" "$identity_render")" = false
  test "$(yq -r "$context.capabilities.drop[]" "$identity_render")" = ALL
done

reloader_image="$(yq -r '.nats.reloader.image.registry + "/" + .nats.reloader.image.repository + ":" + .nats.reloader.image.tag' "$chart_dir/values.yaml")"
assert_contains "$default_render" "image: $reloader_image"
assert_contains "$default_render" "# Source: helm-nvcf-nats/templates/nats-auth-callout-nkeys-secret.yaml"
assert_contains "$default_render" "name: nats-auth-callout-nkeys"
assert_contains "$default_render" "nkey_signature:"
# The shared-worker Secret contains the non-secret public-key mapping consumed
# as a partial service config. Private auth-callout seeds remain separate.
assert_not_contains "$default_render" "secrets.json:"
assert_contains "$default_render" "auth-callout-plugin.yaml:"
plugin_config="$tmpdir/auth-callout-plugin.yaml"
yq -r 'select(.kind == "Secret" and .metadata.name == "nats-nkeys") | .data."auth-callout-plugin.yaml"' "$default_render" | base64 -d > "$plugin_config"
worker_public_key=$(yq -r 'select(.kind == "Secret" and .metadata.name == "nats-nkeys") | .data."user.pub"' "$default_render" | base64 -d)
test "$(yq -r '.service.plugin_configs.nkey.plugin_type' "$plugin_config")" = "nkey"
test "$(yq -r '.service.plugin_configs.nkey.config.nkey_mappings[0].nkey' "$plugin_config")" = "$worker_public_key"
test "$(yq -r '.service.plugin_configs.nkey.config.nkey_mappings[0].account' "$plugin_config")" = "APP"
test "$(yq -r '.service.plugin_configs.sis.plugin_type' "$plugin_config")" = "webhook"
test "$(yq -r '.service.plugin_configs.sis.config.url' "$plugin_config")" = "http://api.sis.svc.cluster.local:8080/v1/nvca/nats-authorize"
test "$(yq -r '.service.account_configs.APP.enabled_plugins | length' "$plugin_config")" = "2"
test "$(yq -r '.service.account_configs.APP.enabled_plugins[0].id' "$plugin_config")" = "sis"
test "$(yq -r '.service.account_configs.APP.enabled_plugins[0].alias' "$plugin_config")" = "oidc"
test "$(yq -r '.service.account_configs.APP.enabled_plugins[1].id' "$plugin_config")" = "nkey"
# Worker token authentication is opt-in: by default nothing routes to the API webhook.
test "$(yq -r '.service.plugin_configs | has("nvcf-api")' "$plugin_config")" = "false"
test "$(yq -r '.service.account_configs | has("Worker")' "$plugin_config")" = "false"
# nkey-bao-access RBAC must grant openbao-migrations read on both the
# shared-worker Secret and the auth-callout Secret so 19_setup_nats-auth-callout.sh
# can mirror seeds into KV.
assert_contains "$default_render" "- nats-nkeys"
assert_contains "$default_render" "- nats-auth-callout-nkeys"
# The post-install hook from the previous design must be gone.
assert_not_contains "$default_render" "nats-auth-callout-secrets-json-hook"
assert_contains "$default_render" '"server_tags": ['
assert_contains "$default_render" '"dc:ncp",'
assert_contains "$default_render" '"aws-region:ncp"'

no_create_render="$tmpdir/no-create.yaml"
render --set nats.authCallout.createNkeySecret=false > "$no_create_render"
assert_not_contains "$no_create_render" "nkey_signature:"
assert_contains "$no_create_render" "name: nats-auth-callout-nkeys"

legacy_no_create_render="$tmpdir/legacy-no-create.yaml"
render --set authCallout.createNkeySecret=false > "$legacy_no_create_render"
assert_not_contains "$legacy_no_create_render" "nkey_signature:"
assert_contains "$legacy_no_create_render" "name: nats-auth-callout-nkeys"

custom_render="$tmpdir/custom-secret-name.yaml"
render --set nats.authCallout.secretName=custom-auth-callout-nkeys > "$custom_render"
assert_contains "$custom_render" "name: custom-auth-callout-nkeys"
assert_contains "$custom_render" "- custom-auth-callout-nkeys"
assert_not_contains "$custom_render" "name: nats-auth-callout-nkeys"

token_render="$tmpdir/worker-token-auth.yaml"
render --set nats.authCallout.workerTokenAuth.enabled=true > "$token_render"
token_plugin_config="$tmpdir/worker-token-auth-plugin.yaml"
yq -r 'select(.kind == "Secret" and .metadata.name == "nats-nkeys") | .data."auth-callout-plugin.yaml"' "$token_render" | base64 -d > "$token_plugin_config"
test "$(yq -r '.service.plugin_configs["nvcf-api"].plugin_type' "$token_plugin_config")" = "webhook"
test "$(yq -r '.service.plugin_configs["nvcf-api"].config.url' "$token_plugin_config")" = "http://api.nvcf.svc.cluster.local:8080/v2/nvcf/webhook/nats-auth"
# Workers ask for APP with plugin webhook, so APP gains the API webhook under that alias.
test "$(yq -r '.service.account_configs | keys | join(",")' "$token_plugin_config")" = "APP"
test "$(yq -r '.service.account_configs.APP.enabled_plugins | length' "$token_plugin_config")" = "3"
test "$(yq -r '.service.account_configs.APP.enabled_plugins[2].id' "$token_plugin_config")" = "nvcf-api"
test "$(yq -r '.service.account_configs.APP.enabled_plugins[2].alias' "$token_plugin_config")" = "webhook"
# NVCA's and the services' plugins stay as they are, in the same order.
for i in 0 1; do
  test "$(yq -o=json -I=0 ".service.account_configs.APP.enabled_plugins[$i]" "$token_plugin_config")" = "$(yq -o=json -I=0 ".service.account_configs.APP.enabled_plugins[$i]" "$plugin_config")"
done
test "$(yq -o=json -I=0 '.service.plugin_configs.sis' "$token_plugin_config")" = "$(yq -o=json -I=0 '.service.plugin_configs.sis' "$plugin_config")"
test "$(yq -r '.service.plugin_configs.nkey.config.nkey_mappings[0].account' "$token_plugin_config")" = "APP"

custom_url_render="$tmpdir/worker-token-auth-custom-url.yaml"
render --set nats.authCallout.workerTokenAuth.enabled=true \
  --set nats.authCallout.workerTokenAuth.webhookUrl=http://api.example.svc.cluster.local:8080/v2/nvcf/webhook/nats-auth > "$custom_url_render"
custom_url_plugin_config="$tmpdir/worker-token-auth-custom-url-plugin.yaml"
yq -r 'select(.kind == "Secret" and .metadata.name == "nats-nkeys") | .data."auth-callout-plugin.yaml"' "$custom_url_render" | base64 -d > "$custom_url_plugin_config"
test "$(yq -r '.service.plugin_configs["nvcf-api"].config.url' "$custom_url_plugin_config")" = "http://api.example.svc.cluster.local:8080/v2/nvcf/webhook/nats-auth"

if render --set nats.authCallout.workerTokenAuth.enabled=true --set nats.authCallout.workerTokenAuth.webhookUrl= > "$tmpdir/no-url.yaml" 2> "$tmpdir/no-url.err"; then
  echo "expected the render to fail when workerTokenAuth.webhookUrl is empty" >&2
  exit 1
fi
assert_contains "$tmpdir/no-url.err" "workerTokenAuth.webhookUrl is required"
