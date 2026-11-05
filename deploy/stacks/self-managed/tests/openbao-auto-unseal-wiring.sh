#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Test that the OpenBao auto-unseal settings in environments/<env>.yaml reach
# the openbao-server release: autoUnseal, the Raft config that carries the seal
# stanza, and the service account for the seal's credentials. autoUnseal.enabled
# also drops the Shamir unseal sidecar and its Secret volume, since the chart
# does not create that Secret under auto-unseal. Unset, nothing is emitted, so
# existing installs render byte-identical and keep the chart's Shamir seal.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="openbao-auto-unseal-wiring-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "openbao-auto-unseal-wiring: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$test_stack_dir/secrets/$environment_name-secrets.yaml"

render_values() {
  local output_file="$1"
  HELMFILE_ENV="$environment_name" \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
      --selector name=openbao-server \
      write-values \
      --output-file-template "$output_file" >/dev/null
}

assert_yaml_value() {
  local input_file="$1" expression="$2" want="$3" label="$4" got
  got="$(yq -r "$expression" "$input_file")"
  [[ "$got" == "$want" ]] || fail "$label: expected $want, got ${got:-<none>}"
}

# 1. Unset: the stack emits none of the keys, so the chart's Shamir seal stands.
printf '{}\n' >"$environment_file"
values="$work_dir/default.yaml"
render_values "$values"
for key in autoUnseal serviceAccount ha.raft extraContainers volumes volumeMounts; do
  assert_yaml_value "$values" ".openbao.server.$key" null "default: openbao.server.$key must not be emitted"
done

# 2. Set: each key reaches the chart, and autoUnseal.enabled replaces the
#    chart's sidecar, volume and mount with empty lists.
cat >"$environment_file" <<'EOF'
openbao:
  server:
    autoUnseal:
      enabled: true
    ha:
      raft:
        config: |
          seal "awskms" {
            kms_key_id = "alias/openbao-unseal"
          }
    serviceAccount:
      annotations:
        eks.amazonaws.com/role-arn: arn:aws:iam::111122223333:role/openbao-unseal
EOF
# An indented first line sets a block scalar's indentation; it must survive.
yq -i '.openbao.server.ha.raft.config = "  ui = true\n" + .openbao.server.ha.raft.config' "$environment_file"
values="$work_dir/set.yaml"
render_values "$values"
assert_yaml_value "$values" '.openbao.server.autoUnseal.enabled' true "set: autoUnseal.enabled"
assert_yaml_value "$values" '.openbao.server.ha.raft.config' "$(yq -r '.openbao.server.ha.raft.config' "$environment_file")" \
  "set: raft config must arrive unchanged"
assert_yaml_value "$values" '.openbao.server.serviceAccount.annotations."eks.amazonaws.com/role-arn"' \
  arn:aws:iam::111122223333:role/openbao-unseal "set: serviceAccount annotations"
for key in extraContainers volumes volumeMounts; do
  assert_yaml_value "$values" ".openbao.server.$key | tag + \" \" + (length | tostring)" "!!seq 0" \
    "set: autoUnseal.enabled must empty $key"
done

# 3. Off, explicitly or by leaving enabled unset like the chart default:
#    autoUnseal is forwarded, but the Shamir sidecar and its unseal volume
#    stay, since the chart still takes the Shamir path.
for setting in 'enabled: false' 'recovery: {shares: 3}'; do
  printf 'openbao:\n  server:\n    autoUnseal:\n      %s\n' "$setting" >"$environment_file"
  values="$work_dir/off.yaml"
  render_values "$values"
  assert_yaml_value "$values" '.openbao.server.autoUnseal | @json' \
    "$(yq -r '.openbao.server.autoUnseal | @json' "$environment_file")" "off ($setting): autoUnseal"
  for key in extraContainers volumes volumeMounts; do
    assert_yaml_value "$values" ".openbao.server.$key" null "off ($setting): $key must keep the chart default"
  done
done

# 4. A quoted "false" is truthy in templates and would drop the sidecar while
#    the chart's init script takes the Shamir path, so it must fail the render.
printf 'openbao:\n  server:\n    autoUnseal:\n      enabled: "false"\n' >"$environment_file"
if render_values "$work_dir/quoted.yaml" 2>"$work_dir/quoted.err"; then
  fail "quoted autoUnseal.enabled must fail the render"
fi
grep -q 'openbao.server.autoUnseal.enabled must be a YAML boolean' "$work_dir/quoted.err" ||
  fail "quoted autoUnseal.enabled: unexpected error: $(tail -3 "$work_dir/quoted.err")"

echo "openbao-auto-unseal-wiring: OK"
