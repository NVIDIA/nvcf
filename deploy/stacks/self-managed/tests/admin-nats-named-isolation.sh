#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
stack_dir="$(cd "$script_dir/.." && pwd)"
repo_root="$(cd "$stack_dir/../../.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
admin_values_file="$work_dir/admin-issuer-proxy-values.yaml"
nats_values_file="$work_dir/nats-values.yaml"
admin_render_file="$work_dir/admin-issuer-proxy-render.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "admin-nats-named-isolation: $*" >&2
  exit 1
}

command -v helm >/dev/null 2>&1 || fail "helm is required"
command -v helmfile >/dev/null 2>&1 || fail "helmfile is required"
command -v yq >/dev/null 2>&1 || fail "yq is required"

cp -R "$stack_dir" "$test_stack_dir"
printf '{}\n' >"$test_stack_dir/secrets/base-secrets.yaml"

# Named mode is intentionally still blocked in the source tree while other
# object-level leak classes are fixed. This test unlocks only the temp copy so
# it can inspect the values produced by this subphase.
perl -0pi -e 's/\$namedControlPlaneObjectIdentitiesReady := false/\$namedControlPlaneObjectIdentitiesReady := true/g' \
  "$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
  "$test_stack_dir/helmfile.d/02-core.yaml.gotmpl" \
  "$test_stack_dir/global.yaml.gotmpl"

run_helmfile() {
  HELMFILE_ENV=base \
    NVCF_CONTROL_PLANE_OWNER=plane-a \
    NVCF_ALPHA_NAMED_CONTROL_PLANE=true \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=gateway \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gateway \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=gateway \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gateway \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=gateway \
      --state-values-set addons.nvcfUi.enabled=true \
      "$@"
}

render_release_values() {
  local state_file="$1"
  local release_name="$2"
  local output_file="$3"

  if ! run_helmfile \
    --file "$test_stack_dir/helmfile.d/$state_file" \
    --selector "name=$release_name" \
    write-values \
    --output-file-template "$output_file" >/dev/null; then
    fail "helmfile could not render values for $release_name"
  fi
  test -s "$output_file" ||
    fail "helmfile wrote no values for $release_name"
}

assert_value() {
  local file="$1"
  local expression="$2"
  local expected="$3"
  local actual

  actual="$(yq -r "$expression" "$file")"
  test "$actual" = "$expected" ||
    fail "expected $expression in $(basename "$file") to be $expected, got $actual"
}

assert_render_value() {
  local file="$1"
  local kind="$2"
  local name="$3"
  local namespace="$4"
  local expression="$5"
  local expected="$6"
  local actual

  actual="$(yq -r \
    "select(.kind == \"$kind\" and .metadata.name == \"$name\" and .metadata.namespace == \"$namespace\") | $expression" \
    "$file")"
  test "$actual" = "$expected" ||
    fail "expected $kind/$namespace/$name $expression to be $expected, got $actual"
}

render_release_values "02-core.yaml.gotmpl" "admin-issuer-proxy" "$admin_values_file"
assert_value "$admin_values_file" '.adminIssuerProxy.fullnameOverride' plane-a-admin-token-issuer-proxy
assert_value "$admin_values_file" '.nvcfUi.controlPlane.components[] | select(.name == "admin-issuer-proxy") | .namespace' plane-a-api-keys
assert_value "$admin_values_file" '.nvcfUi.controlPlane.components[] | select(.name == "admin-issuer-proxy") | .endpoints[0]' plane-a-admin-token-issuer-proxy.plane-a-api-keys.svc.cluster.local:8080/healthz

helm template admin-issuer-proxy "$repo_root/deploy/helm/admin-token-issuer-proxy/chart" \
  --namespace plane-a-api-keys \
  --values "$admin_values_file" \
  >"$admin_render_file"

assert_render_value "$admin_render_file" Service plane-a-admin-token-issuer-proxy plane-a-api-keys '.metadata.name' plane-a-admin-token-issuer-proxy
assert_render_value "$admin_render_file" HTTPRoute plane-a-admin-token-issuer-proxy gateway '.spec.rules[0].backendRefs[0].name' plane-a-admin-token-issuer-proxy
assert_render_value "$admin_render_file" HTTPRoute plane-a-admin-token-issuer-proxy gateway '.spec.rules[0].backendRefs[0].namespace' plane-a-api-keys
assert_render_value "$admin_render_file" ReferenceGrant plane-a-admin-token-issuer-proxy plane-a-api-keys '.spec.from[0].namespace' gateway

render_release_values "01-dependencies.yaml.gotmpl" "nats" "$nats_values_file"
assert_value "$nats_values_file" '.nats.rbac.openbao.namespace' plane-a-vault-system
assert_value "$nats_values_file" '.nats.rbac.openbao.serviceAccountName' plane-a-openbao-initialize-cluster

echo "admin-nats-named-isolation: all checks passed"
