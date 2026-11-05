#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Test that the OpenBao auto-unseal settings in environments/<env>.yaml reach
# the openbao-server release: autoUnseal, the Raft config that carries the seal
# stanza, and the [] overrides that drop the Shamir unseal sidecar and its
# Secret volume. Unset, nothing is emitted, so existing installs render
# byte-identical and keep the chart's Shamir seal.
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
for key in autoUnseal ha.raft extraContainers volumes volumeMounts; do
  assert_yaml_value "$values" ".openbao.server.$key" null "default: openbao.server.$key must not be emitted"
done

# 2. Set, under each HA mode: every key reaches the chart, and [] stays an
#    empty list so it replaces the chart's sidecar, volume and mount.
for ha_mode in none preferred; do
  cat >"$environment_file" <<EOF
highAvailability:
  mode: $ha_mode
openbao:
  server:
    autoUnseal:
      enabled: true
      recovery:
        shares: 5
        threshold: 3
    ha:
      raft:
        config: |
          ui = true
          storage "raft" {
            path = "/openbao/data/"
          }
          seal "awskms" {
            region     = "us-west-2"
            kms_key_id = "alias/openbao-unseal"
          }
    extraContainers: []
    volumes: []
    volumeMounts: []
EOF
  values="$work_dir/set-$ha_mode.yaml"
  render_values "$values"
  assert_yaml_value "$values" '.openbao.server.autoUnseal.enabled' true "$ha_mode: autoUnseal.enabled"
  assert_yaml_value "$values" '.openbao.server.autoUnseal.recovery.threshold' 3 "$ha_mode: autoUnseal.recovery.threshold"
  assert_yaml_value "$values" '.openbao.server.ha.raft.config | test("seal \"awskms\"")' true "$ha_mode: raft config carries the seal stanza"
  for key in extraContainers volumes volumeMounts; do
    assert_yaml_value "$values" ".openbao.server.$key | tag + \" \" + (length | tostring)" "!!seq 0" "$ha_mode: $key must be an empty list"
  done
done
assert_yaml_value "$work_dir/set-preferred.yaml" '.openbao.server.ha.enabled' true "preferred: raft config sits next to the HA settings"

echo "openbao-auto-unseal-wiring: OK"
