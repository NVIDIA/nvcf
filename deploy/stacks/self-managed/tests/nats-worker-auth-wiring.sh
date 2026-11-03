#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Test that nats.workerAuth in environments/<env>.yaml reaches the three releases
# that act on it: nats, which routes worker tokens to the NVCF API webhook; api,
# which stops rendering worker-nkey-seed so workers get no NKEY_SEED; and
# nats-auth-callout-service, which must restart to load the new plugin config
# whenever the mode or the webhook URL changes. The api values are rendered
# through the local nvcf-api chart, so the vault agent template the chart builds
# from them is covered too.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
stacks_dir="$(cd "$stack_dir/.." && pwd)"
api_chart_dir="$(cd "$stack_dir/../../helm/cloud-functions/nvcf-api" && pwd)"
work_dir="$(mktemp -d)"
test_stacks_dir="$work_dir/stacks"
test_stack_dir="$test_stacks_dir/self-managed"
environment_name="nats-worker-auth-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "nats-worker-auth-wiring: $*" >&2
  exit 1
}

# Copy the whole stacks directory: the self-managed helmfile.d reaches a sibling
# stack through a relative path, so self-managed alone is not a working stack.
mkdir -p "$test_stacks_dir"
cp -R "$stacks_dir"/. "$test_stacks_dir"
mkdir -p "$(dirname "$secrets_file")"
printf '{}\n' >"$secrets_file"
for stack_environments in "$test_stacks_dir"/*/environments; do
  test -d "$stack_environments" || continue
  test -e "$stack_environments/$environment_name.yaml" ||
    printf '{}\n' >"$stack_environments/$environment_name.yaml"
done

helmfile_common=(
  --file "$test_stack_dir/helmfile.d"
  --environment default
  --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system
)

write_env() {
  cat >"$environment_file"
}

write_values() {
  local release="$1"
  local output_file="$2"
  local render_log="$work_dir/$release-write-values.log"
  if ! HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile "${helmfile_common[@]}" \
      --selector "name=$release" \
      write-values \
      --output-file-template "$output_file" 2>"$render_log"; then
    cat "$render_log" >&2
    fail "$release: helmfile could not render the stack"
  fi
  test -s "$output_file" || fail "$release: helmfile wrote no values to $output_file"
}

vault_agent_template() {
  local api_values="$1"
  local output_file="$2"
  helm template api "$api_chart_dir" \
    --namespace nvcf \
    --values "$api_values" \
    --show-only templates/configmap-vault-agent-template.yaml |
    yq -r '.data."secrets.json.tmpl"' >"$output_file" ||
    fail "the nvcf-api chart did not render with $api_values"
  test -s "$output_file" || fail "the nvcf-api chart rendered an empty vault agent template"
}

# The template is JSON around vault agent actions; blank the actions and it must parse.
assert_valid_json() {
  python3 -I -c 'import json, re, sys; json.loads(re.sub(r"\{\{.*?\}\}", "", sys.stdin.read(), flags=re.S))' <"$1" ||
    fail "$2: the vault agent template is not valid JSON"
}

expect_render_failure() {
  local name="$1"
  local message="$2"
  if HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile "${helmfile_common[@]}" --selector name=api write-values \
    --output-file-template "$work_dir/$name-api.yaml" >/dev/null 2>"$work_dir/$name.log"; then
    fail "$name: rendered without an error"
  fi
  grep -qF "$message" "$work_dir/$name.log" ||
    fail "$name: the render failed for another reason: $(tail -3 "$work_dir/$name.log")"
}

# The default is the NVCF API's in-cluster Service, from environments/base.yaml.
default_webhook_url=http://api.nvcf.svc.cluster.local:8080/v2/nvcf/webhook/nats-auth
custom_webhook_url=http://nvcf-api.example.svc.cluster.local:8080/v2/nvcf/webhook/nats-auth

base_env='global:
  image:
    registry: nvcr.io
    repository: test/nvcf'

# 1. Default (nkey): no webhook for workers, and the API keeps worker-nkey-seed.
write_env <<<"$base_env"
write_values nats "$work_dir/nkey-nats.yaml"
write_values api "$work_dir/nkey-api.yaml"
test "$(yq -r '.nats.authCallout.workerTokenAuth.enabled // false' "$work_dir/nkey-nats.yaml")" = false ||
  fail "nkey: nats.authCallout.workerTokenAuth.enabled is set"
test "$(yq -r '.api | has("workerNkeySeed")' "$work_dir/nkey-api.yaml")" = false ||
  fail "nkey: api.workerNkeySeed is set"
vault_agent_template "$work_dir/nkey-api.yaml" "$work_dir/nkey-secrets.json.tmpl"
grep -q '"worker-nkey-seed"' "$work_dir/nkey-secrets.json.tmpl" ||
  fail "nkey: the vault agent template has no worker-nkey-seed"
assert_valid_json "$work_dir/nkey-secrets.json.tmpl" nkey
write_values nats-auth-callout-service "$work_dir/nkey-callout.yaml"
for annotation in nats-worker-auth nats-worker-auth-webhook-url; do
  test "$(yq -r ".podAnnotations.\"nvcf.nvidia.com/$annotation\" // \"unset\"" "$work_dir/nkey-callout.yaml")" = unset ||
    fail "nkey: the auth callout carries the $annotation annotation"
done

# 2. token: the auth callout routes worker tokens to the API, and the API sends no NKEY_SEED.
write_env <<EOF
$base_env
nats:
  workerAuth: token
EOF
write_values nats "$work_dir/token-nats.yaml"
write_values api "$work_dir/token-api.yaml"
test "$(yq -r '.nats.authCallout.workerTokenAuth.enabled' "$work_dir/token-nats.yaml")" = true ||
  fail "token: nats.authCallout.workerTokenAuth.enabled is not true"
test "$(yq -r '.nats.authCallout.workerTokenAuth.webhookUrl' "$work_dir/token-nats.yaml")" = "$default_webhook_url" ||
  fail "token: the nats release does not get the default webhook URL"
test "$(yq -r '.api.workerNkeySeed.enabled' "$work_dir/token-api.yaml")" = false ||
  fail "token: api.workerNkeySeed.enabled is not false"
vault_agent_template "$work_dir/token-api.yaml" "$work_dir/token-secrets.json.tmpl"
if grep -q '"worker-nkey-seed"' "$work_dir/token-secrets.json.tmpl"; then
  fail "token: the vault agent template still has worker-nkey-seed"
fi
assert_valid_json "$work_dir/token-secrets.json.tmpl" token
write_values nats-auth-callout-service "$work_dir/token-callout.yaml"
test "$(yq -r '.podAnnotations."nvcf.nvidia.com/nats-worker-auth" // "unset"' "$work_dir/token-callout.yaml")" = token ||
  fail "token: the auth callout has no worker auth annotation, so it would keep its old plugin config"
test "$(yq -r '.podAnnotations."nvcf.nvidia.com/nats-worker-auth-webhook-url" // "unset"' "$work_dir/token-callout.yaml")" = "$default_webhook_url" ||
  fail "token: the auth callout's annotation does not carry the webhook URL"

# 3. A new webhook URL reaches the nats release and changes the auth callout's pod
# template, so the auth callout restarts and loads it.
write_env <<EOF
$base_env
nats:
  workerAuth: token
  authCallout:
    workerTokenAuth:
      webhookUrl: $custom_webhook_url
EOF
write_values nats "$work_dir/custom-url-nats.yaml"
write_values nats-auth-callout-service "$work_dir/custom-url-callout.yaml"
test "$(yq -r '.nats.authCallout.workerTokenAuth.webhookUrl' "$work_dir/custom-url-nats.yaml")" = "$custom_webhook_url" ||
  fail "custom URL: the nats release does not get the configured webhook URL"
test "$(yq -r '.podAnnotations."nvcf.nvidia.com/nats-worker-auth-webhook-url" // "unset"' "$work_dir/custom-url-callout.yaml")" = "$custom_webhook_url" ||
  fail "custom URL: the auth callout's pod template does not change with the webhook URL"

# 4. token with no webhook URL fails the render.
write_env <<EOF
$base_env
nats:
  workerAuth: token
  authCallout:
    workerTokenAuth:
      webhookUrl: ""
EOF
expect_render_failure empty-url 'nats.authCallout.workerTokenAuth.webhookUrl is required when nats.workerAuth is token'

# 5. Any other mode fails the render instead of silently falling back to nkey.
write_env <<EOF
$base_env
nats:
  workerAuth: oauth
EOF
expect_render_failure invalid 'nats.workerAuth must be nkey or token'

echo "nats-worker-auth-wiring: ok"
