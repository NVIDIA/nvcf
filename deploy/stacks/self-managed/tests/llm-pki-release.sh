#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="llm-pki-release-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "llm-pki-release: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

cat >"$environment_file" <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
addons:
  llm:
    enabled: true
    pki:
      enabled: true
      allowedDomains: cluster.local
      dnsNames:
        - llm-request-router.nvcf.svc.cluster.local
      image:
        tag: 0.16.2
EOF

releases="$(
  HELMFILE_ENV="$environment_name" \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
      list 2>/dev/null
)"

test "$(printf '%s\n' "$releases" | awk 'NR > 1 && $1 == "nvcf-pki" && $2 == "cert-manager" { count++ } END { print count + 0 }')" = "1" ||
  fail "LLM PKI did not install exactly one nvcf-pki release in cert-manager"

echo "llm-pki-release: all checks passed"
