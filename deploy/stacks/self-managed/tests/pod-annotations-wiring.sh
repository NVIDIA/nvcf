#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Test that pod annotations set in environments/<env>.yaml reach each
# control-plane release, at the key its chart reads. Operators use them for
# agent opt-ins such as log collection. Unset, nothing is emitted, so existing
# installs render byte-identical.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="pod-annotations-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "pod-annotations-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$test_stack_dir/secrets/$environment_name-secrets.yaml"

render_values() {
  local release="$1" output_file="$2" helmfile_name=02-core
  case "$release" in nats | openbao-server) helmfile_name=01-dependencies ;; esac
  HELMFILE_ENV="$environment_name" \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "$test_stack_dir/helmfile.d/$helmfile_name.yaml.gotmpl" \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
      --selector "name=$release" \
      write-values \
      --output-file-template "$output_file" >/dev/null
}

assert_yaml_value() {
  local input_file="$1" expression="$2" want="$3" label="$4" got
  got="$(yq -r "$expression" "$input_file")"
  [[ "$got" == "$want" ]] || fail "$label: expected $want, got ${got:-<none>}"
}

# release | where its chart reads pod annotations | environment key
releases=(
  "api|.api.podAnnotations|api.podAnnotations"
  "nvct-api|.nvctApi.podAnnotations|nvctApi.podAnnotations"
  "invocation-service|.invocation.podAnnotations|invocation.podAnnotations"
  "grpc-proxy|.grpcproxy.podAnnotations|grpcproxy.podAnnotations"
  "api-keys|.apikeys.podAnnotations|apikeys.podAnnotations"
  "ess-api|.ess.podAnnotations|ess.podAnnotations"
  "nats-auth-callout-service|.podAnnotations|natsAuthCalloutService.podAnnotations"
  "nats|.nats.podTemplate.merge.metadata.annotations|nats.podAnnotations"
  "openbao-server|.openbao.server.annotations|openbao.server.annotations"
)

# 1. Unset: no release gets pod annotations from the stack.
printf '{}\n' >"$environment_file"
for entry in "${releases[@]}"; do
  IFS='|' read -r release path key <<<"$entry"
  values="$work_dir/$release-default.yaml"
  render_values "$release" "$values"
  assert_yaml_value "$values" "$path" null "default $release: $key must not be emitted"
done

# 2. Set: each release gets its own annotations and no other release's.
: >"$environment_file"
for entry in "${releases[@]}"; do
  IFS='|' read -r release path key <<<"$entry"
  yq -i "(.$key.\"example.com/owner\") = \"$release\"" "$environment_file"
done
for entry in "${releases[@]}"; do
  IFS='|' read -r release path key <<<"$entry"
  values="$work_dir/$release-set.yaml"
  render_values "$release" "$values"
  assert_yaml_value "$values" "$path.\"example.com/owner\"" "$release" "set $release: $key"
  if [[ "$release" != nats-auth-callout-service ]]; then
    assert_yaml_value "$values" '.podAnnotations' null "set $release: callout annotations must stay in the callout release"
  fi
done

# 3. In token mode the callout keeps operator annotations next to the stack's
#    restart annotation. The restart keys are reserved: an operator copy would
#    survive a switch back to nkey and stop that switch restarting the callout.
cat >"$environment_file" <<'EOF'
nats:
  workerAuth: token
  authCallout:
    workerTokenAuth:
      webhookUrl: http://api.nvcf.svc.cluster.local:8080/v2/nvcf/webhook/nats-auth
natsAuthCalloutService:
  podAnnotations:
    example.com/owner: operator
EOF
values="$work_dir/callout-token.yaml"
render_values nats-auth-callout-service "$values"
assert_yaml_value "$values" '.podAnnotations."example.com/owner"' operator "token mode keeps operator annotations"
assert_yaml_value "$values" '.podAnnotations."nvcf.nvidia.com/nats-worker-auth"' token "token mode sets the restart annotation"

yq -i '.natsAuthCalloutService.podAnnotations."nvcf.nvidia.com/nats-worker-auth" = "token"' "$environment_file"
if render_values nats-auth-callout-service "$work_dir/callout-reserved.yaml" 2>"$work_dir/callout-reserved.err"; then
  fail "reserved callout annotation must fail the render"
fi
grep -q 'nvcf.nvidia.com/nats-worker-auth is reserved for the stack' "$work_dir/callout-reserved.err" ||
  fail "reserved callout annotation: unexpected error: $(tail -3 "$work_dir/callout-reserved.err")"

echo "pod-annotations-wiring: OK"
