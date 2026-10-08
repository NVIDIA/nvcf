#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Render checks for the llm-gateway-stack chart. Every case runs helm template
# and asserts on the output with yq and jq. The subcharts are file://
# dependencies, so the script rebuilds them first; a stale charts/ archive
# would otherwise test old subchart templates.
#
# Usage: scripts/check-render.sh [chart dir]

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/../../../.." && pwd)"
chart_dir="${1:-${script_dir}/../llm-gateway-stack}"
ci_values="${CI_VALUES:-${repo_root}/tools/ci/helm-validate-values/llm-gateway-stack.yaml}"
release="${RELEASE:-llm-gateway-stack}"
namespace="${NAMESPACE:-llm-gateway}"
router="llm-request-router"
gateway="llm-api-gateway"
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

helm dependency build --skip-refresh "${chart_dir}" >/dev/null

# Names the stack must render, read from its own values.
stack_values="${chart_dir}/values.yaml"
credentials_secret="$(yq -r '."llm-request-router".llmRequestRouter.auth.credentialsSecret.name' "${stack_values}")"
credentials_key="$(yq -r '."llm-request-router".llmRequestRouter.auth.credentialsSecret.key' "${stack_values}")"
router_tls_secret="$(yq -r '."llm-request-router".llmRequestRouter.tls.secretName' "${stack_values}")"
api_keys_secret="$(yq -r '."llm-api-gateway".llmApiGateway.auth.staticKeys.existingSecret' "${stack_values}")"
gateway_tls_secret="$(yq -r '."llm-api-gateway".llmApiGateway.tls.existingSecret' "${stack_values}")"
ca_name="$(yq -r '.tls.selfSigned.caName' "${stack_values}")"
cluster_id="$(yq -r '.clusterId' "${ci_values}")"
cluster_sha256="$(yq -r '.clusterCredential.sha256' "${ci_values}")"
api_keys_json="$(yq -o=json -I=0 '{"keys": .apiKeys}' "${ci_values}")"

# render <output> [helm args...]: render with the CI values plus overrides.
render() {
  local output="$1"
  shift
  helm template "${release}" "${chart_dir}" \
    --namespace "${namespace}" \
    --values "${ci_values}" \
    "$@" \
    > "${output}"
}

# assert_render_fails <expected error> [helm args...]: render with the CI
# values plus overrides and expect the error.
assert_render_fails() {
  local expected_error="$1"
  shift
  local error_file="${tmp_dir}/render-error"
  if helm template "${release}" "${chart_dir}" \
    --namespace "${namespace}" \
    --values "${ci_values}" \
    "$@" \
    > /dev/null 2> "${error_file}"; then
    fail "expected render failure: ${expected_error}"
  fi
  grep -Fq -- "${expected_error}" "${error_file}" ||
    fail "render did not return expected error: ${expected_error}; got: $(cat "${error_file}")"
}

count() {  # count <manifest> <kind> <name>
  yq ea "[select(.kind == \"$2\" and .metadata.name == \"$3\")] | length" "$1"
}

deployment() {  # deployment <manifest> <name> <expression>
  yq -o=json -I=0 "select(.kind == \"Deployment\" and .metadata.name == \"$2\")" "$1" | jq -r "$3"
}

secret_value() {  # secret_value <manifest> <secret> <key>: decoded value
  yq -r "select(.kind == \"Secret\" and .metadata.name == \"$2\") | .data[\"$3\"] | @base64d" "$1"
}

has_arg() {
  printf '%s\n' "$1" | grep -qx -- "$2"
}

has_arg_prefix() {
  printf '%s\n' "$1" | grep -q -- "^$2"
}

env_value() {  # env_value <manifest> <deployment> <name>
  deployment "$1" "$2" "(.spec.template.spec.containers[0].env // []) | .[] | select(.name == \"$3\") | .value // \"\""
}

has_env() {  # has_env <manifest> <deployment> <name>
  [ "$(deployment "$1" "$2" "(.spec.template.spec.containers[0].env // []) | map(select(.name == \"$3\")) | length")" != "0" ]
}

gateway_config() {  # gateway_config <manifest> <key>: ConfigMap value, or the empty string
  yq -o=json -I=0 "select(.kind == \"ConfigMap\" and .metadata.name == \"${gateway}\")" "$1" | jq -r ".data.$2 // \"\""
}

gateway_config_has() {  # gateway_config_has <manifest> <key>
  [ "$(yq -r "select(.kind == \"ConfigMap\" and .metadata.name == \"${gateway}\") | .data | has(\"$2\")" "$1")" = "true" ]
}

volume_secret() {  # volume_secret <manifest> <deployment> <volume>: Secret name, or nothing
  deployment "$1" "$2" "(.spec.template.spec.volumes // []) | .[] | select(.name == \"$3\") | .secret.secretName"
}

vault_annotations() {  # vault_annotations <manifest> <deployment>
  deployment "$1" "$2" '(.spec.template.metadata.annotations // {}) | keys | map(select(test("^vault[.]hashicorp[.]com/"))) | length'
}

san_annotation() {  # san_annotation <manifest> <secret>
  yq -o=json -I=0 "select(.kind == \"Secret\" and .metadata.name == \"$2\")" "$1" | jq -r '.metadata.annotations["llm-gateway-stack.nvidia.com/subject-alt-names"] // ""'
}

assert_pem() {  # assert_pem <label> <expected block type> <value>
  printf '%s\n' "$3" | grep -q -- "^-----BEGIN $2-----$" || fail "$1 is not a PEM $2"
}

# ---------------------------------------------------------------------------
# Default demo mode with the CI values.
# ---------------------------------------------------------------------------

manifest="${tmp_dir}/stack.yaml"
render "${manifest}"

# Both subcharts render.
[ "$(count "${manifest}" Deployment "${router}")" = "1" ] || fail "the llm-request-router subchart must render its Deployment"
[ "$(count "${manifest}" Service "${router}")" = "1" ] || fail "the llm-request-router subchart must render its Service"
[ "$(count "${manifest}" Deployment "${gateway}")" = "1" ] || fail "the llm-api-gateway subchart must render its Deployment"
[ "$(count "${manifest}" Service "${gateway}")" = "1" ] || fail "the llm-api-gateway subchart must render its Service"

# Router: static credentials from the stack's Secret, TLS verified.
router_args="$(deployment "${manifest}" "${router}" '.spec.template.spec.containers[0].args[]')"
has_arg "${router_args}" "--worker-auth-file=/etc/stargate/worker-auth/${credentials_key}" ||
  fail "router must read the static worker auth file"
! has_arg_prefix "${router_args}" "--worker-auth-endpoint" || fail "router must not call the worker auth endpoint"
! has_arg_prefix "${router_args}" "--secrets-path" || fail "router must not read Vault secrets"
! has_arg "${router_args}" "--allow-open-worker-auth" || fail "router must not accept unauthenticated workers"
! has_arg "${router_args}" "--quic-insecure" || fail "router QUIC must stay verified"
has_arg "${router_args}" "--tls-cert-path=/etc/stargate/tls/tls.crt" || fail "router must load the TLS certificate"
has_arg "${router_args}" "--tls-key-path=/etc/stargate/tls/tls.key" || fail "router must load the TLS key"
[ "$(volume_secret "${manifest}" "${router}" worker-auth-credentials)" = "${credentials_secret}" ] ||
  fail "router must mount the stack's credentials Secret"
[ "$(volume_secret "${manifest}" "${router}" stargate-tls)" = "${router_tls_secret}" ] ||
  fail "router must mount the stack's router TLS Secret"

[ "$(count "${manifest}" Secret "${credentials_secret}")" = "1" ] || fail "stack must render the router credentials Secret"
# The worker auth file is the YAML the router parses: clusters.<clusterId> is a
# list of sha256:<lowercase hex> entries.
[ "$(secret_value "${manifest}" "${credentials_secret}" "${credentials_key}")" = \
  "$(printf 'clusters:\n  %s:\n  - sha256:%s\n' "${cluster_id}" "${cluster_sha256}")" ] ||
  fail "router worker auth file must bind clusterId to sha256:<clusterCredential.sha256>"

# worker_auth_hashes <manifest>: the rendered hash list of clusterId, one per line.
worker_auth_hashes() {
  secret_value "$1" "${credentials_secret}" "${credentials_key}" |
    yq -r ".clusters[\"${cluster_id}\"][]"
}

# sha256Hashes merges after sha256, normalized to lowercase with the prefix.
next_sha256="$(printf '%s' "${cluster_sha256}" | tr '0-9a-f' 'f0-9a-e')"
next_upper="$(printf '%s' "${next_sha256}" | tr 'a-f' 'A-F')"
merged_manifest="${tmp_dir}/merged-hashes.yaml"
render "${merged_manifest}" --set "clusterCredential.sha256Hashes[0]=sha256:${next_upper}"
[ "$(worker_auth_hashes "${merged_manifest}" | tr '\n' ' ')" = "sha256:${cluster_sha256} sha256:${next_sha256} " ] ||
  fail "clusterCredential.sha256Hashes must merge after clusterCredential.sha256 as lowercase sha256: entries"
[ "$(secret_value "${merged_manifest}" "${credentials_secret}" "${credentials_key}" | yq -r '.clusters | keys | length')" = "1" ] ||
  fail "the worker auth file must list only clusterId"

list_manifest="${tmp_dir}/list-only-hashes.yaml"
render "${list_manifest}" \
  --set-string clusterCredential.sha256= \
  --set "clusterCredential.sha256Hashes={sha256:${next_sha256},${cluster_sha256}}"
[ "$(worker_auth_hashes "${list_manifest}" | tr '\n' ' ')" = "sha256:${next_sha256} sha256:${cluster_sha256} " ] ||
  fail "clusterCredential.sha256Hashes alone must render every hash in order, with the sha256: prefix"

# Gateway: static API keys from the stack's Secret, bare model names, public
# discovery reads, no NVCF API, no rate limiting, TLS listener.
! gateway_config_has "${manifest}" NVCF_GRPC_ADDR || fail "gateway must not select NVCF auth"
! gateway_config_has "${manifest}" SECRETS_PATH || fail "gateway must not read Vault secrets"
[ "$(env_value "${manifest}" "${gateway}" API_KEYS_PATH)" = "/etc/llm-api-gateway/auth/api-keys.json" ] ||
  fail "gateway must read the static API key file"
! has_env "${manifest}" "${gateway}" ALLOW_ANONYMOUS || fail "gateway must not allow anonymous access"
[ "$(gateway_config "${manifest}" BARE_MODEL_NAMES_ENABLED)" = "true" ] || fail "gateway must accept bare model names"
[ "$(gateway_config "${manifest}" PUBLIC_READ_ENDPOINTS)" = "true" ] || fail "gateway must serve discovery reads without a key"
[ "$(gateway_config "${manifest}" RATE_LIMIT_ENABLED)" = "false" ] || fail "gateway must not rate limit"
! gateway_config_has "${manifest}" OLRIC_ENABLED || fail "gateway must not run Olric"
[ "$(count "${manifest}" Role "${gateway}-olric")" = "0" ] || fail "stack must not render the Olric Role"
[ "$(volume_secret "${manifest}" "${gateway}" api-keys)" = "${api_keys_secret}" ] ||
  fail "gateway must mount the stack's API key Secret"
[ "$(count "${manifest}" Secret "${api_keys_secret}")" = "1" ] || fail "stack must render the gateway API key Secret"
[ "$(secret_value "${manifest}" "${api_keys_secret}" api-keys.json | jq -c .)" = "$(printf '%s' "${api_keys_json}" | jq -c .)" ] ||
  fail "gateway key file must hold the apiKeys ids and digests"
[ "$(env_value "${manifest}" "${gateway}" TLS_CERT_FILE)" = "/etc/llm-api-gateway/tls/tls.crt" ] || fail "gateway must serve TLS"
[ "$(volume_secret "${manifest}" "${gateway}" tls)" = "${gateway_tls_secret}" ] ||
  fail "gateway must mount the stack's gateway TLS Secret"
[ "$(deployment "${manifest}" "${gateway}" '.spec.template.spec.containers[0].readinessProbe.httpGet.scheme')" = "HTTPS" ] ||
  fail "gateway probes must use HTTPS"

router_http_port="$(yq -r "select(.kind == \"Service\" and .metadata.name == \"${router}\") | .spec.ports[] | select(.name == \"http\") | .port" "${manifest}")"
[ "$(gateway_config "${manifest}" STARGATE_URL)" = "http://${router}:${router_http_port}" ] ||
  fail "gateway must reach the router Service in its namespace"

# No Vault anywhere.
[ "$(vault_annotations "${manifest}" "${router}")" = "0" ] || fail "router must not carry Vault Agent annotations"
[ "$(vault_annotations "${manifest}" "${gateway}")" = "0" ] || fail "gateway must not carry Vault Agent annotations"
[ -z "$(deployment "${manifest}" "${gateway}" '(.spec.template.spec.volumes // []) | .[] | select(.name | test("^vault-")) | .name')" ] ||
  fail "gateway must not mount Vault volumes"
[ "$(count "${manifest}" ConfigMap "${gateway}-vault-agent-tpl")" = "0" ] ||
  fail "gateway must not render the Vault Agent template ConfigMap"

# Self-signed TLS: CA, CA ConfigMap and two server Secrets.
for secret in "${ca_name}" "${router_tls_secret}" "${gateway_tls_secret}"; do
  [ "$(yq -r "select(.kind == \"Secret\" and .metadata.name == \"${secret}\") | .type" "${manifest}")" = "kubernetes.io/tls" ] ||
    fail "${secret} must be a kubernetes.io/tls Secret"
  assert_pem "${secret} tls.crt" CERTIFICATE "$(secret_value "${manifest}" "${secret}" tls.crt)"
  secret_value "${manifest}" "${secret}" tls.key | grep -q -- "^-----BEGIN .*PRIVATE KEY-----$" ||
    fail "${secret} tls.key is not a PEM private key"
done
[ "$(yq -r "select(.kind == \"Secret\" and .metadata.name == \"${ca_name}\") | .metadata.annotations[\"helm.sh/resource-policy\"]" "${manifest}")" = "keep" ] ||
  fail "the CA Secret must survive uninstall"
ca_crt="$(yq -r "select(.kind == \"ConfigMap\" and .metadata.name == \"${ca_name}\") | .data[\"ca.crt\"]" "${manifest}")"
assert_pem "CA ConfigMap ca.crt" CERTIFICATE "${ca_crt}"
[ "$(printf '%s\n' "${ca_crt}")" = "$(secret_value "${manifest}" "${ca_name}" tls.crt)" ] ||
  fail "the CA ConfigMap must hold the CA certificate"
for secret in "${router_tls_secret}" "${gateway_tls_secret}"; do
  [ "$(secret_value "${manifest}" "${secret}" ca.crt)" = "$(printf '%s\n' "${ca_crt}")" ] ||
    fail "${secret} ca.crt must be the stack CA"
done

router_sans="$(san_annotation "${manifest}" "${router_tls_secret}" | tr ',' '\n')"
for name in \
  "${router}.${namespace}.svc.cluster.local" \
  "${router}-backend-router.${namespace}.svc.cluster.local" \
  "*.${router}-headless.${namespace}.svc.cluster.local"; do
  printf '%s\n' "${router_sans}" | grep -qxF -- "${name}" || fail "router certificate must cover ${name}"
done
gateway_sans="$(san_annotation "${manifest}" "${gateway_tls_secret}" | tr ',' '\n')"
for name in "${gateway}" "${gateway}.${namespace}.svc.cluster.local" localhost 127.0.0.1; do
  printf '%s\n' "${gateway_sans}" | grep -qxF -- "${name}" || fail "gateway certificate must cover ${name}"
done

extra_manifest="${tmp_dir}/extra-names.yaml"
render "${extra_manifest}" \
  --set 'tls.selfSigned.routerDnsNames={router.example.com}' \
  --set 'tls.selfSigned.gatewayDnsNames={llm.example.com}' \
  --set 'tls.selfSigned.gatewayIPs={10.0.0.5}'
san_annotation "${extra_manifest}" "${router_tls_secret}" | tr ',' '\n' | grep -qxF router.example.com ||
  fail "tls.selfSigned.routerDnsNames must reach the router certificate"
extra_gateway_sans="$(san_annotation "${extra_manifest}" "${gateway_tls_secret}" | tr ',' '\n')"
printf '%s\n' "${extra_gateway_sans}" | grep -qxF llm.example.com || fail "tls.selfSigned.gatewayDnsNames must reach the gateway certificate"
printf '%s\n' "${extra_gateway_sans}" | grep -qxF 10.0.0.5 || fail "tls.selfSigned.gatewayIPs must reach the gateway certificate"
! printf '%s\n' "${extra_gateway_sans}" | grep -qxF localhost || fail "overriding gatewayDnsNames must replace localhost"

# ---------------------------------------------------------------------------
# Alternatives: operator-supplied TLS Secrets and credentials.
# ---------------------------------------------------------------------------

external_manifest="${tmp_dir}/external.yaml"
render "${external_manifest}" \
  --set tls.selfSigned.enabled=false \
  --set clusterCredential.create=false \
  --set apiKeysSecret.create=false \
  --set-string clusterId= \
  --set-string clusterCredential.sha256= \
  --set 'apiKeys=null'
for secret in "${ca_name}" "${router_tls_secret}" "${gateway_tls_secret}" "${credentials_secret}" "${api_keys_secret}"; do
  [ "$(count "${external_manifest}" Secret "${secret}")" = "0" ] || fail "${secret} must not render when the stack does not own it"
done
[ "$(count "${external_manifest}" ConfigMap "${ca_name}")" = "0" ] || fail "the CA ConfigMap must not render without tls.selfSigned"
[ "$(volume_secret "${external_manifest}" "${router}" stargate-tls)" = "${router_tls_secret}" ] ||
  fail "router must still mount the named TLS Secret without tls.selfSigned"
[ "$(volume_secret "${external_manifest}" "${gateway}" tls)" = "${gateway_tls_secret}" ] ||
  fail "gateway must still mount the named TLS Secret without tls.selfSigned"
[ "$(volume_secret "${external_manifest}" "${router}" worker-auth-credentials)" = "${credentials_secret}" ] ||
  fail "router must still mount the named credentials Secret"

# A multi-replica router renders with the backend router and the same Secret.
multi_manifest="${tmp_dir}/multi-replica.yaml"
render "${multi_manifest}" --set llm-request-router.llmRequestRouter.replicaCount=2
[ "$(volume_secret "${multi_manifest}" "${router}-backend-router" stargate-tls)" = "${router_tls_secret}" ] ||
  fail "the backend router must mount the router TLS Secret"

# ---------------------------------------------------------------------------
# Invalid values fail the render.
# ---------------------------------------------------------------------------

assert_render_fails "llm-gateway-stack: set the required values: clusterId, clusterCredential.sha256 or clusterCredential.sha256Hashes" \
  --set-string clusterId= --set-string clusterCredential.sha256=
assert_render_fails "llm-gateway-stack: set the required values: apiKeys" \
  --set 'apiKeys=null'
assert_render_fails "llm-gateway-stack: set the required values: llm-request-router.llmRequestRouter.image.repository" \
  --set-string llm-request-router.llmRequestRouter.image.repository=
assert_render_fails "clusterId \"Spark_CI\" must be a DNS label" \
  --set clusterId=Spark_CI
assert_render_fails "clusterCredential.sha256 must be a 64 character hex SHA-256 digest" \
  --set clusterCredential.sha256=not-a-digest
assert_render_fails "clusterCredential.sha256Hashes[1] must be a 64 character hex SHA-256 digest" \
  --set "clusterCredential.sha256Hashes[0]=${next_sha256}" \
  --set "clusterCredential.sha256Hashes[1]=sha256:${cluster_sha256:0:63}"
assert_render_fails "clusterCredential.sha256Hashes[0] must be a 64 character hex SHA-256 digest" \
  --set "clusterCredential.sha256Hashes[0]=SHA256:${next_sha256}"
assert_render_fails "clusterCredential.sha256Hashes[0] repeats another cluster credential hash" \
  --set "clusterCredential.sha256Hashes[0]=sha256:$(printf '%s' "${cluster_sha256}" | tr 'a-f' 'A-F')"
assert_render_fails "clusterCredential.sha256Hashes must be a list" \
  --set-string clusterCredential.sha256Hashes=not-a-list
assert_render_fails "apiKeys[0].sha256 must be a 64 character hex SHA-256 digest" \
  --set 'apiKeys[0].id=ci' --set 'apiKeys[0].sha256=abc'
assert_render_fails "apiKeys[0].id \"-bad\" must be 1 to 128 letters" \
  --set 'apiKeys[0].id=-bad' --set "apiKeys[0].sha256=${cluster_sha256}"
assert_render_fails "apiKeys id \"dup\" is listed twice" \
  --set 'apiKeys[0].id=dup' --set "apiKeys[0].sha256=${cluster_sha256}" \
  --set 'apiKeys[1].id=dup' --set "apiKeys[1].sha256=$(printf '%s' "${cluster_sha256}" | tr '0-9a-f' 'f0-9a-e')"
assert_render_fails "clusterCredential.create needs the router in static credentials mode" \
  --set llm-request-router.llmRequestRouter.auth.workerAuthEndpoint=http://api.nvcf.svc.cluster.local:9090 \
  --set-string llm-request-router.llmRequestRouter.auth.credentialsSecret.name=
assert_render_fails "apiKeysSecret.create needs the gateway in static key mode" \
  --set llm-api-gateway.llmApiGateway.auth.mode=anonymous
assert_render_fails "llm-api-gateway.llmApiGateway.namespace must be empty or the release namespace" \
  --set llm-api-gateway.llmApiGateway.namespace=elsewhere

echo "llm-gateway-stack render checks passed"

# The UI credential belongs to this release and must match its registered hash.
ui_key="offline-demo-ui-key"
ui_sha="$(printf '%s' "${ui_key}" | shasum -a 256 | cut -d ' ' -f 1)"
ui_keys="$(printf '%s' "${api_keys_json}" | jq -c --arg hash "${ui_sha}" '.keys + [{id: "demo-ui", sha256: $hash}]')"
render "${tmp_dir}/demo-ui.yaml" --set-string "demoUiApiKey=${ui_key}" --set-json "apiKeys=${ui_keys}"
[ "$(secret_value "${tmp_dir}/demo-ui.yaml" demo-ui-api-key api-key)" = "${ui_key}" ] || fail "UI Secret must contain the supplied key"
[ "$(secret_value "${tmp_dir}/demo-ui.yaml" "${api_keys_secret}" api-keys.json | jq -c '.keys')" = "${ui_keys}" ] || fail "UI key must preserve existing caller keys"
[ "$(count "${manifest}" Secret demo-ui-api-key)" = "0" ] || fail "UI Secret must be absent without a supplied key"
assert_render_fails "demoUiApiKey requires its matching demo-ui hash" --set-string "demoUiApiKey=${ui_key}"
assert_render_fails "demoUiApiKey requires apiKeysSecret.create=true" \
  --set apiKeysSecret.create=false --set-string "demoUiApiKey=${ui_key}" --set-json "apiKeys=${ui_keys}"
echo "PASS: demo UI API key and Secret"
