#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Render checks for llmApiGateway.auth, llmApiGateway.vault.enabled and
# llmApiGateway.tls. Every case runs helm template and asserts on the output
# with yq and jq.
#
# Usage: scripts/check-auth-modes-render.sh [chart dir]

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
chart_dir="${1:-${script_dir}/../llm-api-gateway}"
release="${RELEASE:-llm-api-gateway}"
namespace="${NAMESPACE:-nvcf}"
keys_mount="/etc/llm-api-gateway/auth"
tls_mount="/etc/llm-api-gateway/tls"
tmp_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

for tool in helm yq jq; do
  command -v "${tool}" >/dev/null 2>&1 || fail "${tool} not found on PATH"
done

default_grpc_addr="$(yq -r '.llmApiGateway.config.nvcfGrpcAddr' "${chart_dir}/values.yaml")"
[ -n "${default_grpc_addr}" ] || fail "values.yaml must ship a default config.nvcfGrpcAddr"

# render <output> [helm args...]
render() {
  local output="$1"
  shift
  helm template "${release}" "${chart_dir}" \
    --namespace "${namespace}" \
    --set-string llmApiGateway.image.repository=example.invalid/llm-api-gateway \
    "$@" \
    > "${output}"
}

# assert_render_fails <expected error> [helm args...]
assert_render_fails() {
  local expected_error="$1"
  shift
  local error_file="${tmp_dir}/render-error"
  if helm template "${release}" "${chart_dir}" \
    --namespace "${namespace}" \
    --set-string llmApiGateway.image.repository=example.invalid/llm-api-gateway \
    "$@" \
    > /dev/null 2> "${error_file}"; then
    fail "expected render failure: ${expected_error}"
  fi
  grep -Fq -- "${expected_error}" "${error_file}" ||
    fail "render did not return expected error: ${expected_error}; got: $(cat "${error_file}")"
}

deployment() {  # deployment <manifest> <expression>
  yq -r "select(.kind == \"Deployment\" and .metadata.name == \"${release}\") | $2" "$1"
}

config() {  # config <manifest> <key>: ConfigMap value, or the empty string when absent
  yq -r "select(.kind == \"ConfigMap\" and .metadata.name == \"${release}\") | .data.$2 // \"\"" "$1"
}

has_config() {  # has_config <manifest> <key>
  [ "$(yq -r "select(.kind == \"ConfigMap\" and .metadata.name == \"${release}\") | .data | has(\"$2\")" "$1")" = "true" ]
}

env_value() {  # env_value <manifest> <name>: plain env value, or the empty string when absent
  deployment "$1" "(.spec.template.spec.containers[0].env // []) | .[] | select(.name == \"$2\") | .value // \"\""
}

has_env() {  # has_env <manifest> <name>
  [ "$(deployment "$1" "(.spec.template.spec.containers[0].env // []) | map(select(.name == \"$2\")) | length")" != "0" ]
}

volume() {  # volume <manifest> <name>: the volume as JSON, or nothing
  deployment "$1" "(.spec.template.spec.volumes // []) | .[] | select(.name == \"$2\") | @json"
}

mount() {  # mount <manifest> <name>: the container mount as JSON, or nothing
  deployment "$1" "(.spec.template.spec.containers[0].volumeMounts // []) | .[] | select(.name == \"$2\") | @json"
}

vault_annotation_count() {
  deployment "$1" '(.spec.template.metadata.annotations // {}) | keys | map(select(test("^vault\.hashicorp\.com/"))) | length'
}

vault_template_configmaps() {
  yq ea "[select(.kind == \"ConfigMap\" and .metadata.name == \"${release}-vault-agent-tpl\")] | length" "$1"
}

probe_schemes() {  # probe_schemes <manifest>: one scheme per probe, "none" when unset
  deployment "$1" '.spec.template.spec.containers[0] | [.startupProbe, .readinessProbe, .livenessProbe] | .[] | .httpGet.scheme // "none"'
}

assert_no_auth_env() {  # assert_no_auth_env <manifest> <label>
  local name
  for name in API_KEYS_PATH STATIC_ALLOWED_PATHS ALLOW_ANONYMOUS STARGATE_SERVICE_TOKEN TLS_CERT_FILE TLS_KEY_FILE; do
    ! has_env "$1" "${name}" || fail "$2 must not set ${name}"
    ! has_config "$1" "${name}" || fail "$2 must not put ${name} in the ConfigMap"
  done
}

assert_vault_wired() {  # assert_vault_wired <manifest> <label>
  [ "$(vault_annotation_count "$1")" -gt 0 ] || fail "$2 must carry the Vault Agent annotations"
  [ -n "$(volume "$1" vault-token)" ] || fail "$2 must mount the vault-token volume"
  [ -n "$(volume "$1" vault-config-templates)" ] || fail "$2 must mount the vault-config-templates volume"
  [ -n "$(mount "$1" vault-config-templates)" ] || fail "$2 must mount the Vault templates into the container"
  [ "$(vault_template_configmaps "$1")" = "1" ] || fail "$2 must render the Vault agent template ConfigMap"
}

assert_no_vault() {  # assert_no_vault <manifest> <label>
  [ "$(vault_annotation_count "$1")" = "0" ] || fail "$2 must not carry Vault Agent annotations"
  [ -z "$(volume "$1" vault-token)" ] || fail "$2 must not mount the vault-token volume"
  [ -z "$(volume "$1" vault-config-templates)" ] || fail "$2 must not mount the vault-config-templates volume"
  [ -z "$(mount "$1" vault-config-templates)" ] || fail "$2 must not mount the Vault templates into the container"
  [ "$(vault_template_configmaps "$1")" = "0" ] || fail "$2 must not render the Vault agent template ConfigMap"
  ! has_config "$1" SECRETS_PATH || fail "$2 must not point SECRETS_PATH at the absent Vault output"
}

# ---------------------------------------------------------------------------
# nvcf mode (default): NVCF gRPC auth and the Vault Agent, as before.
# ---------------------------------------------------------------------------

default_manifest="${tmp_dir}/default.yaml"
render "${default_manifest}"
[ "$(config "${default_manifest}" NVCF_GRPC_ADDR)" = "${default_grpc_addr}" ] || fail "nvcf mode must set NVCF_GRPC_ADDR from config.nvcfGrpcAddr"
has_config "${default_manifest}" NVCF_GRPC_INSECURE || fail "nvcf mode must set NVCF_GRPC_INSECURE"
has_config "${default_manifest}" NVCF_GRPC_TIMEOUT || fail "nvcf mode must set NVCF_GRPC_TIMEOUT"
[ "$(config "${default_manifest}" SECRETS_PATH)" = "/vault/secrets/secrets.json" ] || fail "nvcf mode must read the Vault secrets file"
assert_no_auth_env "${default_manifest}" "nvcf mode"
assert_vault_wired "${default_manifest}" "nvcf mode"
[ -z "$(volume "${default_manifest}" api-keys)" ] || fail "nvcf mode must not mount an API key Secret"
[ -z "$(volume "${default_manifest}" tls)" ] || fail "TLS is off by default and must not mount a TLS Secret"
[ "$(probe_schemes "${default_manifest}" | sort -u)" = "none" ] || fail "plaintext probes must not set a scheme"

# The legacy switch still drops only the annotations.
legacy_manifest="${tmp_dir}/legacy-no-annotations.yaml"
render "${legacy_manifest}" --set llmApiGateway.vault.noVaultAnnotations=true
[ "$(vault_annotation_count "${legacy_manifest}")" = "0" ] || fail "vault.noVaultAnnotations must drop the Vault annotations"
[ -n "$(volume "${legacy_manifest}" vault-token)" ] || fail "vault.noVaultAnnotations alone must keep the Vault volumes"
[ -n "$(deployment "${legacy_manifest}" '.spec.template.metadata.annotations["checksum/config-env"] // ""')" ] ||
  fail "pod annotations must stay valid YAML with only the config checksum"

# nvcf mode without Vault keeps NVCF auth and its SECRETS_PATH, drops the agent.
nvcf_no_vault_manifest="${tmp_dir}/nvcf-no-vault.yaml"
render "${nvcf_no_vault_manifest}" --set llmApiGateway.vault.enabled=false
[ "$(config "${nvcf_no_vault_manifest}" NVCF_GRPC_ADDR)" = "${default_grpc_addr}" ] || fail "nvcf mode without Vault must keep NVCF_GRPC_ADDR"
[ "$(config "${nvcf_no_vault_manifest}" SECRETS_PATH)" = "/vault/secrets/secrets.json" ] || fail "nvcf mode without Vault must keep SECRETS_PATH"
[ "$(vault_annotation_count "${nvcf_no_vault_manifest}")" = "0" ] || fail "vault.enabled=false must drop the Vault annotations"
[ -z "$(volume "${nvcf_no_vault_manifest}" vault-token)" ] || fail "vault.enabled=false must drop the vault-token volume"
[ -z "$(volume "${nvcf_no_vault_manifest}" vault-config-templates)" ] || fail "vault.enabled=false must drop the vault-config-templates volume"
[ "$(vault_template_configmaps "${nvcf_no_vault_manifest}")" = "0" ] || fail "vault.enabled=false must drop the Vault agent template ConfigMap"

# ---------------------------------------------------------------------------
# staticKeys mode: mounted key file, no NVCF API.
# ---------------------------------------------------------------------------

static_manifest="${tmp_dir}/static.yaml"
render "${static_manifest}" \
  --set llmApiGateway.auth.mode=staticKeys \
  --set llmApiGateway.auth.staticKeys.existingSecret=gateway-api-keys \
  --set llmApiGateway.vault.enabled=false
for key in NVCF_GRPC_ADDR NVCF_GRPC_INSECURE NVCF_GRPC_TIMEOUT; do
  ! has_config "${static_manifest}" "${key}" || fail "staticKeys mode must not set ${key}"
done
[ "$(env_value "${static_manifest}" API_KEYS_PATH)" = "${keys_mount}/api-keys.json" ] || fail "staticKeys mode must set API_KEYS_PATH to the mounted key file"
[ "$(env_value "${static_manifest}" STATIC_ALLOWED_PATHS)" = "/v1/chat/completions" ] || fail "staticKeys mode must default STATIC_ALLOWED_PATHS to /v1/chat/completions"
! has_env "${static_manifest}" ALLOW_ANONYMOUS || fail "staticKeys mode must not allow anonymous access"
assert_no_vault "${static_manifest}" "staticKeys mode without Vault"
keys_volume="$(volume "${static_manifest}" api-keys)"
[ -n "${keys_volume}" ] || fail "staticKeys mode must mount the API key Secret"
[ "$(printf '%s' "${keys_volume}" | jq -r '.secret.secretName')" = "gateway-api-keys" ] || fail "API key volume must reference auth.staticKeys.existingSecret"
[ "$(printf '%s' "${keys_volume}" | jq -c '.secret.items')" = '[{"key":"api-keys.json","path":"api-keys.json"}]' ] || fail "API key volume must project only api-keys.json"
keys_mount_json="$(mount "${static_manifest}" api-keys)"
[ "$(printf '%s' "${keys_mount_json}" | jq -r '.mountPath')" = "${keys_mount}" ] || fail "API keys must mount at ${keys_mount}"
[ "$(printf '%s' "${keys_mount_json}" | jq -r '.readOnly')" = "true" ] || fail "API key mount must be read-only"
[ "$(printf '%s' "${keys_mount_json}" | jq -r '.subPath // ""')" = "" ] || fail "API key mount must not use subPath, which blocks Secret updates"

paths_manifest="${tmp_dir}/static-paths.yaml"
render "${paths_manifest}" \
  --set llmApiGateway.auth.mode=staticKeys \
  --set llmApiGateway.auth.staticKeys.existingSecret=gateway-api-keys \
  --set 'llmApiGateway.auth.staticKeys.allowedPaths={/v1/chat/completions,/v1/models}'
[ "$(env_value "${paths_manifest}" STATIC_ALLOWED_PATHS)" = "/v1/chat/completions,/v1/models" ] || fail "allowedPaths must be comma-joined into STATIC_ALLOWED_PATHS"
# Vault stays on by default: the agent and its tracing-token file remain, NVCF auth does not.
assert_vault_wired "${paths_manifest}" "staticKeys mode with Vault"
[ "$(config "${paths_manifest}" SECRETS_PATH)" = "/vault/secrets/secrets.json" ] || fail "staticKeys mode with Vault must keep SECRETS_PATH"
! has_config "${paths_manifest}" NVCF_GRPC_ADDR || fail "staticKeys mode with Vault must not set NVCF_GRPC_ADDR"

# ---------------------------------------------------------------------------
# anonymous mode: neither authenticator.
# ---------------------------------------------------------------------------

anonymous_manifest="${tmp_dir}/anonymous.yaml"
render "${anonymous_manifest}" --set llmApiGateway.auth.mode=anonymous --set llmApiGateway.vault.enabled=false
[ "$(env_value "${anonymous_manifest}" ALLOW_ANONYMOUS)" = "true" ] || fail "anonymous mode must set ALLOW_ANONYMOUS=true"
! has_config "${anonymous_manifest}" NVCF_GRPC_ADDR || fail "anonymous mode must not set NVCF_GRPC_ADDR"
! has_env "${anonymous_manifest}" API_KEYS_PATH || fail "anonymous mode must not set API_KEYS_PATH"
[ -z "$(volume "${anonymous_manifest}" api-keys)" ] || fail "anonymous mode must not mount an API key Secret"
assert_no_vault "${anonymous_manifest}" "anonymous mode without Vault"

# ---------------------------------------------------------------------------
# Router service token and listener TLS.
# ---------------------------------------------------------------------------

token_manifest="${tmp_dir}/service-token.yaml"
render "${token_manifest}" \
  --set llmApiGateway.auth.serviceToken.existingSecret=router-service-token \
  --set llmApiGateway.auth.serviceToken.key=bearer
[ "$(deployment "${token_manifest}" '.spec.template.spec.containers[0].env[] | select(.name == "STARGATE_SERVICE_TOKEN") | .valueFrom.secretKeyRef | .name + "/" + .key')" = "router-service-token/bearer" ] ||
  fail "serviceToken must reach STARGATE_SERVICE_TOKEN through a secretKeyRef"

tls_manifest="${tmp_dir}/tls.yaml"
render "${tls_manifest}" \
  --set llmApiGateway.tls.enabled=true \
  --set llmApiGateway.tls.existingSecret=gateway-tls
[ "$(env_value "${tls_manifest}" TLS_CERT_FILE)" = "${tls_mount}/tls.crt" ] || fail "TLS must set TLS_CERT_FILE"
[ "$(env_value "${tls_manifest}" TLS_KEY_FILE)" = "${tls_mount}/tls.key" ] || fail "TLS must set TLS_KEY_FILE"
tls_volume="$(volume "${tls_manifest}" tls)"
[ "$(printf '%s' "${tls_volume}" | jq -r '.secret.secretName')" = "gateway-tls" ] || fail "TLS volume must reference tls.existingSecret"
[ "$(printf '%s' "${tls_volume}" | jq -c '[.secret.items[].key]')" = '["tls.crt","tls.key"]' ] || fail "TLS volume must project tls.crt and tls.key"
tls_mount_json="$(mount "${tls_manifest}" tls)"
[ "$(printf '%s' "${tls_mount_json}" | jq -r '.mountPath')" = "${tls_mount}" ] || fail "TLS Secret must mount at ${tls_mount}"
[ "$(printf '%s' "${tls_mount_json}" | jq -r '.readOnly')" = "true" ] || fail "TLS mount must be read-only"
[ "$(probe_schemes "${tls_manifest}" | sort -u)" = "HTTPS" ] || fail "every probe must use HTTPS when TLS is enabled"

# ---------------------------------------------------------------------------
# Invalid combinations fail the render.
# ---------------------------------------------------------------------------

assert_render_fails "llmApiGateway.auth.mode must be nvcf, staticKeys or anonymous" \
  --set llmApiGateway.auth.mode=static-keys
assert_render_fails "llmApiGateway.auth.staticKeys.existingSecret is required" \
  --set llmApiGateway.auth.mode=staticKeys
assert_render_fails "llmApiGateway.auth.staticKeys.allowedPaths entries must start with /" \
  --set llmApiGateway.auth.mode=staticKeys \
  --set llmApiGateway.auth.staticKeys.existingSecret=gateway-api-keys \
  --set 'llmApiGateway.auth.staticKeys.allowedPaths={v1/chat/completions}'
assert_render_fails "llmApiGateway.tls.existingSecret is required" \
  --set llmApiGateway.tls.enabled=true
assert_render_fails "llmApiGateway.config.nvcfGrpcAddr is required" \
  --set-string llmApiGateway.config.nvcfGrpcAddr=
assert_render_fails "llmApiGateway.auth.serviceToken.key must be a Secret key name" \
  --set llmApiGateway.auth.serviceToken.existingSecret=router-service-token \
  --set-string llmApiGateway.auth.serviceToken.key=

echo "llm-api-gateway auth mode render checks passed"
