#!/usr/bin/env bash
# Test that the OpenBao service PKI and its managed ClusterIssuer are
# provisioned for the worker callback proxy without the LLM add-on, that the
# proxy's domains reach the signing role, and that LLM-only output is unchanged.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="service-pki-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "service-pki-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$test_stack_dir/secrets/$environment_name-secrets.yaml"

helmfile_stage() {
  local file="$1"
  shift
  (cd "$test_stack_dir" && HELMFILE_ENV="$environment_name" \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "helmfile.d/$file" \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
      "$@")
}

render_values() {
  local file="$1" release="$2" output_file="$3"
  helmfile_stage "$file" --selector "name=$release" write-values --output-file-template "$output_file" >/dev/null
}

nvcf_pki_enabled() {
  helmfile_stage 01-dependencies.yaml.gotmpl list --skip-charts --output json |
    jq -r '.[] | select(.name == "nvcf-pki") | .enabled'
}

migration_env() {
  yq -r ".openbao.migrations.env[] | select(.name == \"$1\") | .value" "$2"
}

write_env() {
  cat >"$environment_file"
}

echo "== LLM and the proxy off: no service PKI =="
printf '{}\n' | write_env
test -z "$(nvcf_pki_enabled)" || fail "nvcf-pki must not render with LLM and the proxy off"
render_values 01-dependencies.yaml.gotmpl openbao-server "$work_dir/off.yaml"
test -z "$(migration_env NVCF_SERVICE_PKI_ENABLED "$work_dir/off.yaml")" ||
  fail "NVCF_SERVICE_PKI_ENABLED must be unset with the proxy off"
test "$(migration_env ADDONS_LLM_ENABLED "$work_dir/off.yaml")" = "false" || fail "ADDONS_LLM_ENABLED must stay false"

echo "== proxy on without LLM =="
write_env <<'EOF'
workerCallbackProxy:
  enabled: true
  workerDomain: grpc-worker.example.com
EOF
test "$(nvcf_pki_enabled)" = "true" || fail "nvcf-pki must render for the proxy without LLM"
render_values 01-dependencies.yaml.gotmpl openbao-server "$work_dir/proxy.yaml"
p="$work_dir/proxy.yaml"
test "$(migration_env NVCF_SERVICE_PKI_ENABLED "$p")" = "true" || fail "expected NVCF_SERVICE_PKI_ENABLED=true"
test "$(migration_env ADDONS_LLM_ENABLED "$p")" = "false" || fail "ADDONS_LLM_ENABLED must follow addons.llm.enabled"
actual="$(migration_env NVCF_SERVICE_PKI_ALLOWED_DOMAINS "$p")"
test "$actual" = "cluster.local,grpc-worker.example.com" ||
  fail "expected the worker domain added to the allowed domains, got $actual"

echo "== covered domains are not repeated =="
write_env <<'EOF'
workerCallbackProxy:
  enabled: true
  workerDomain: grpc-worker.nvcf.svc.cluster.local
EOF
render_values 01-dependencies.yaml.gotmpl openbao-server "$work_dir/covered.yaml"
actual="$(migration_env NVCF_SERVICE_PKI_ALLOWED_DOMAINS "$work_dir/covered.yaml")"
test "$actual" = "cluster.local" || fail "expected cluster.local to cover both proxy domains, got $actual"

write_env <<'EOF'
addons:
  llm:
    pki:
      allowedDomains: example.com
workerCallbackProxy:
  enabled: true
  workerDomain: grpc-worker.example.com
EOF
render_values 01-dependencies.yaml.gotmpl openbao-server "$work_dir/partial.yaml"
actual="$(migration_env NVCF_SERVICE_PKI_ALLOWED_DOMAINS "$work_dir/partial.yaml")"
test "$actual" = "example.com,grpc-headless.nvcf.svc.cluster.local" ||
  fail "expected only the uncovered grpc-proxy pod domain added, got $actual"

echo "== LLM and the proxy: the OpenBao migration is the only role writer =="
write_env <<'EOF'
addons:
  llm:
    enabled: true
workerCallbackProxy:
  enabled: true
  workerDomain: grpc-worker.example.com
EOF
render_values 02-core.yaml.gotmpl llm-request-router "$work_dir/llm-proxy.yaml"
test "$(yq -r '.llmRequestRouter.pki.enabled' "$work_dir/llm-proxy.yaml")" = "false" ||
  fail "the stack must disable the llm-request-router PKI hook"
render_values 01-dependencies.yaml.gotmpl openbao-server "$work_dir/llm-proxy-openbao.yaml"
test "$(migration_env ADDONS_LLM_ENABLED "$work_dir/llm-proxy-openbao.yaml")" = "true" ||
  fail "ADDONS_LLM_ENABLED must stay true with LLM"
actual="$(migration_env NVCF_SERVICE_PKI_ALLOWED_DOMAINS "$work_dir/llm-proxy-openbao.yaml")"
test "$actual" = "cluster.local,grpc-worker.example.com" ||
  fail "the OpenBao migration must keep the worker domain, got $actual"

echo "== LLM without the proxy is unchanged =="
write_env <<'EOF'
addons:
  llm:
    enabled: true
EOF
test "$(nvcf_pki_enabled)" = "true" || fail "nvcf-pki must still render for LLM"
render_values 01-dependencies.yaml.gotmpl openbao-server "$work_dir/llm.yaml"
test -z "$(migration_env NVCF_SERVICE_PKI_ENABLED "$work_dir/llm.yaml")" ||
  fail "NVCF_SERVICE_PKI_ENABLED must be unset without the proxy"
test "$(migration_env NVCF_SERVICE_PKI_ALLOWED_DOMAINS "$work_dir/llm.yaml")" = "cluster.local" ||
  fail "LLM allowed domains must be unchanged"
render_values 02-core.yaml.gotmpl llm-request-router "$work_dir/llm-router.yaml"
test "$(yq -r '.llmRequestRouter.pki.enabled' "$work_dir/llm-router.yaml")" = "false" ||
  fail "the stack must disable the llm-request-router PKI hook without the proxy"

echo "== proxy needs OpenBao =="
write_env <<'EOF'
openbao:
  enabled: false
workerCallbackProxy:
  enabled: true
  workerDomain: grpc-worker.example.com
EOF
if helmfile_stage 01-dependencies.yaml.gotmpl list --skip-charts >"$work_dir/no-openbao.err" 2>&1; then
  fail "expected render to fail without OpenBao"
fi
grep -Fq "openbao.enabled must be true" "$work_dir/no-openbao.err" ||
  fail "unexpected error without OpenBao: $(tail -3 "$work_dir/no-openbao.err")"

echo "service-pki-wiring: ok"
