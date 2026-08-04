#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

in_cluster_address="llm-request-router.nvcf.svc.cluster.local:50071"
external_address="llm-router.example.com:443"

fail() {
  echo "llm-request-router-worker-address: $*" >&2
  exit 1
}

# The api release inherits secrets/<env>-secrets.yaml, so render from a copy of
# the stack with a stub secrets file rather than writing into the source tree.
sandbox="$work_dir/stack"
cp -r "$stack_dir" "$sandbox"
printf '{}\n' >"$sandbox/secrets/base-secrets.yaml"

state_values=(
  --state-values-set ingress.gatewayApi.gateways.shared.name=shared
  --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy
  --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc
  --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy
)

write_api_values() {
  local output_file="$1"
  shift

  HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" helmfile \
    --file "$sandbox/helmfile.d/02-core.yaml.gotmpl" \
    --environment default \
    "${state_values[@]}" \
    "$@" \
    --selector name=api \
    write-values \
    --output-file-template "$output_file" >/dev/null
}

# LLM addon on: nvcf-api advertises the in-cluster request router to LLM workers.
# An empty worker-address fails NVCA launch-spec translation, so this value must
# always be rendered.
enabled_values="$work_dir/api-llm-enabled.yaml"
write_api_values "$enabled_values" --state-values-set addons.llm.enabled=true
grep -q "worker-address: $in_cluster_address" "$enabled_values" ||
  fail "LLM addon did not render the in-cluster request router worker address"

# Compute planes outside the control plane cluster cannot resolve the in-cluster
# service name, so the worker endpoint override must win.
override_values="$work_dir/api-llm-override.yaml"
write_api_values "$override_values" \
  --state-values-set addons.llm.enabled=true \
  --state-values-set-string "global.workerEndpoints.llmRequestRouterAddress=$external_address"
grep -q "worker-address: $external_address" "$override_values" ||
  fail "worker endpoint override did not replace the request router worker address"
if grep -q "worker-address: $in_cluster_address" "$override_values"; then
  fail "worker endpoint override left the in-cluster request router worker address"
fi

# LLM addon off: leave the chart default alone so the stack does not advertise a
# router that was never deployed.
disabled_values="$work_dir/api-llm-disabled.yaml"
write_api_values "$disabled_values" --state-values-set addons.llm.enabled=false
if grep -q 'worker-address:' "$disabled_values"; then
  fail "disabled LLM addon rendered a request router worker address"
fi

# An explicit override still applies when the stack does not deploy the router.
disabled_override_values="$work_dir/api-llm-disabled-override.yaml"
write_api_values "$disabled_override_values" \
  --state-values-set addons.llm.enabled=false \
  --state-values-set-string "global.workerEndpoints.llmRequestRouterAddress=$external_address"
grep -q "worker-address: $external_address" "$disabled_override_values" ||
  fail "worker endpoint override was dropped when the LLM addon is disabled"

echo "llm-request-router-worker-address: ok"
