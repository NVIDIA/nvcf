#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

chart_dir="${1:-./llm-request-router}"
release="${RELEASE:-llm-request-router}"
namespace="${NAMESPACE:-nvcf}"
mount_path="/etc/stargate/worker-auth"
tmp_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

render() {
  local output="$1"
  shift
  helm template "${release}" "${chart_dir}" \
    --namespace "${namespace}" \
    --values "${chart_dir}/values.yaml" \
    --set llmRequestRouter.image.repository=stargate \
    "$@" \
    > "${output}"
}

assert_render_fails() {
  local expected_error="$1"
  shift
  local error_file="${tmp_dir}/render-error"
  if helm template "${release}" "${chart_dir}" \
    --namespace "${namespace}" \
    --values "${chart_dir}/values.yaml" \
    --set llmRequestRouter.image.repository=stargate \
    "$@" \
    > /dev/null 2> "${error_file}"; then
    fail "expected render failure: ${expected_error}"
  fi
  grep -Fq "${expected_error}" "${error_file}" || fail "render did not return expected error: ${expected_error}"
}

router_field() {
  local manifest="$1"
  local expression="$2"
  yq -r "select(.kind == \"Deployment\" and .metadata.name == \"${release}\") | ${expression}" "${manifest}"
}

router_args() {
  router_field "$1" '.spec.template.spec.containers[0].args[]'
}

has_arg() {
  printf '%s\n' "$1" | grep -qx -- "$2"
}

has_arg_prefix() {
  printf '%s\n' "$1" | grep -q -- "^$2"
}

worker_auth_volume() {
  router_field "$1" '.spec.template.spec.volumes[] | select(.name == "worker-auth-credentials") | @json'
}

worker_auth_mount() {
  router_field "$1" '.spec.template.spec.containers[0].volumeMounts[] | select(.name == "worker-auth-credentials") | @json'
}

# Default: gateway mode with the Vault-provided token, no static credentials.
default_endpoint="$(yq -r '.llmRequestRouter.auth.workerAuthEndpoint' "${chart_dir}/values.yaml")"
[ -n "${default_endpoint}" ] || fail "values.yaml must ship a default workerAuthEndpoint"
default_manifest="${tmp_dir}/default.yaml"
render "${default_manifest}"
default_args="$(router_args "${default_manifest}")"
has_arg "${default_args}" "--worker-auth-endpoint=${default_endpoint}" || fail "default render missing --worker-auth-endpoint"
has_arg "${default_args}" "--secrets-path=/vault/secrets/secrets.json" || fail "default render missing Vault --secrets-path"
has_arg "${default_args}" "--secrets-json-path=nvcfApiToken" || fail "default render missing --secrets-json-path"
! has_arg_prefix "${default_args}" "--worker-auth-file" || fail "default render must not pass a worker auth file"
! has_arg "${default_args}" "--allow-open-worker-auth" || fail "default render must not allow open worker auth"
[ -z "$(worker_auth_volume "${default_manifest}")" ] || fail "default render must not mount worker credentials"

# Gateway mode without Vault keeps the endpoint and drops the Vault token path.
no_vault_manifest="${tmp_dir}/no-vault.yaml"
render "${no_vault_manifest}" --set llmRequestRouter.vault.noVaultAnnotations=true
no_vault_args="$(router_args "${no_vault_manifest}")"
has_arg "${no_vault_args}" "--worker-auth-endpoint=${default_endpoint}" || fail "gateway mode without Vault missing --worker-auth-endpoint"
! has_arg_prefix "${no_vault_args}" "--secrets-path" || fail "gateway mode without Vault must not pass --secrets-path"

# Static credentials Secret mode.
secret_manifest="${tmp_dir}/secret.yaml"
render "${secret_manifest}" \
  --set-string llmRequestRouter.auth.workerAuthEndpoint= \
  --set llmRequestRouter.auth.credentialsSecret.name=router-worker-credentials
secret_args="$(router_args "${secret_manifest}")"
has_arg "${secret_args}" "--worker-auth-file=${mount_path}/credentials.yaml" || fail "secret mode missing --worker-auth-file"
! has_arg_prefix "${secret_args}" "--worker-auth-endpoint" || fail "secret mode must not pass --worker-auth-endpoint"
! has_arg_prefix "${secret_args}" "--secrets-path" || fail "secret mode must not pass the Vault --secrets-path"
! has_arg "${secret_args}" "--allow-open-worker-auth" || fail "secret mode must not allow open worker auth"
secret_volume="$(worker_auth_volume "${secret_manifest}")"
[ -n "${secret_volume}" ] || fail "secret mode missing worker-auth-credentials volume"
[ "$(printf '%s' "${secret_volume}" | jq -r '.secret.secretName')" = "router-worker-credentials" ] || fail "worker credentials volume must reference the configured Secret"
[ "$(printf '%s' "${secret_volume}" | jq -c '.secret.items')" = '[{"key":"credentials.yaml","path":"credentials.yaml"}]' ] || fail "worker credentials volume must project only the configured key"
secret_mount="$(worker_auth_mount "${secret_manifest}")"
[ -n "${secret_mount}" ] || fail "secret mode missing worker-auth-credentials mount"
[ "$(printf '%s' "${secret_mount}" | jq -r '.mountPath')" = "${mount_path}" ] || fail "worker credentials must mount at ${mount_path}"
[ "$(printf '%s' "${secret_mount}" | jq -r '.readOnly')" = "true" ] || fail "worker credentials mount must be read-only"
[ "$(printf '%s' "${secret_mount}" | jq -r '.subPath // ""')" = "" ] || fail "worker credentials mount must not use subPath, which blocks Secret updates"

custom_key_manifest="${tmp_dir}/secret-custom-key.yaml"
render "${custom_key_manifest}" \
  --set-string llmRequestRouter.auth.workerAuthEndpoint= \
  --set llmRequestRouter.auth.credentialsSecret.name=router-worker-credentials \
  --set llmRequestRouter.auth.credentialsSecret.key=clusters.yaml
custom_key_args="$(router_args "${custom_key_manifest}")"
has_arg "${custom_key_args}" "--worker-auth-file=${mount_path}/clusters.yaml" || fail "custom Secret key did not reach --worker-auth-file"
[ "$(worker_auth_volume "${custom_key_manifest}" | jq -r '.secret.items[0].key')" = "clusters.yaml" ] || fail "custom Secret key did not reach the volume items"

assert_render_fails "llmRequestRouter.auth.credentialsSecret.key must be a Secret key name" \
  --set-string llmRequestRouter.auth.workerAuthEndpoint= \
  --set llmRequestRouter.auth.credentialsSecret.name=router-worker-credentials \
  --set-string llmRequestRouter.auth.credentialsSecret.key=../credentials.yaml

# Exactly one mode.
assert_render_fails "llmRequestRouter.auth.workerAuthEndpoint and llmRequestRouter.auth.credentialsSecret.name are mutually exclusive" \
  --set llmRequestRouter.auth.credentialsSecret.name=router-worker-credentials

assert_render_fails "llmRequestRouter.auth requires workerAuthEndpoint or credentialsSecret.name" \
  --set-string llmRequestRouter.auth.workerAuthEndpoint=

# Stargate refuses --allow-open-worker-auth next to a configured authenticator.
assert_render_fails "llmRequestRouter.auth.allowOpen cannot be combined with workerAuthEndpoint or credentialsSecret.name" \
  --set llmRequestRouter.auth.allowOpen=true
assert_render_fails "llmRequestRouter.auth.allowOpen cannot be combined with workerAuthEndpoint or credentialsSecret.name" \
  --set-string llmRequestRouter.auth.workerAuthEndpoint= \
  --set llmRequestRouter.auth.credentialsSecret.name=router-worker-credentials \
  --set llmRequestRouter.auth.allowOpen=true

# Development-only open mode.
open_manifest="${tmp_dir}/open.yaml"
render "${open_manifest}" \
  --set-string llmRequestRouter.auth.workerAuthEndpoint= \
  --set llmRequestRouter.auth.allowOpen=true
open_args="$(router_args "${open_manifest}")"
has_arg "${open_args}" "--allow-open-worker-auth" || fail "allowOpen did not render --allow-open-worker-auth"
! has_arg_prefix "${open_args}" "--worker-auth-endpoint" || fail "open mode must not pass --worker-auth-endpoint"
! has_arg_prefix "${open_args}" "--worker-auth-file" || fail "open mode must not pass a worker auth file"
[ -z "$(worker_auth_volume "${open_manifest}")" ] || fail "open mode must not mount worker credentials"

# The renamed flag must not come back in any mode.
for manifest in "${default_manifest}" "${no_vault_manifest}" "${secret_manifest}" "${custom_key_manifest}" "${open_manifest}"; do
  ! grep -q -- "--worker-auth-credentials-file" "${manifest}" || fail "$(basename "${manifest}") renders the removed --worker-auth-credentials-file flag"
done

echo "worker auth render checks passed"
