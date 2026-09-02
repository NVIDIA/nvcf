#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -eu

script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
chart_dir=$(CDPATH= cd -- "${script_dir}/.." && pwd)
tmpdir=$(mktemp -d)

cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT

release_name=nvcf-pki
namespace=cert-manager
issuer_name=nvcf-openbao-pki
server=https://openbao.example.invalid:8200
signing_path=services/all/pki/nvcf-service-issuing/sign/nvcf-service-server
auth_mount=/v1/auth/jwt
auth_role=cert-manager
service_account=cert-manager
audience=https://openbao.example.invalid:8200

render() {
  helm template "$release_name" "$chart_dir" \
    --namespace "$namespace" \
    "$@"
}

render_enabled() {
  render \
    --set clusterIssuer.enabled=true \
    --set-string clusterIssuer.name="$issuer_name" \
    --set-string clusterIssuer.server="$server" \
    --set-string clusterIssuer.path="$signing_path" \
    --set-string clusterIssuer.auth.mountPath="$auth_mount" \
    --set-string clusterIssuer.auth.role="$auth_role" \
    --set-string clusterIssuer.auth.serviceAccount.name="$service_account" \
    --set-string clusterIssuer.auth.serviceAccount.audience="$audience" \
    "$@"
}

assert_contains() {
  file=$1
  pattern=$2

  if ! grep -Fq -- "$pattern" "$file"; then
    echo "expected ${file} to contain: ${pattern}" >&2
    return 1
  fi
}

assert_clusterissuer_count() {
  file=$1
  expected=$2
  actual=$(grep -c '^kind: ClusterIssuer$' "$file" || true)

  if [ "$actual" -ne "$expected" ]; then
    echo "expected ${expected} ClusterIssuer resources, found ${actual}" >&2
    return 1
  fi
}

assert_object_count() {
  file=$1
  expected=$2
  actual=$(grep -c '^apiVersion:' "$file" || true)

  if [ "$actual" -ne "$expected" ]; then
    echo "expected ${expected} rendered objects, found ${actual}" >&2
    return 1
  fi
}

assert_clusterissuer_manifest() {
  file=$1
  expected_name=$2
  actual="${tmpdir}/actual-$(basename "$file")"
  expected="${tmpdir}/expected-$(basename "$file")"

  sed -n '/^apiVersion:/,$p' "$file" >"$actual"
  cat >"$expected" <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: "${expected_name}"
  annotations:
    "helm.sh/resource-policy": keep
spec:
  vault:
    server: "${server}"
    path: "${signing_path}"
    auth:
      kubernetes:
        mountPath: "${auth_mount}"
        role: "${auth_role}"
        serviceAccountRef:
          name: "${service_account}"
          audiences:
            - "${audience}"
EOF

  if ! diff -u "$expected" "$actual"; then
    echo "rendered ClusterIssuer does not match the expected structure" >&2
    return 1
  fi
}

assert_render_fails() {
  label=$1
  expected_error=$2
  shift 2
  output="${tmpdir}/${label}.yaml"
  error="${tmpdir}/${label}.err"

  if render_enabled "$@" >"$output" 2>"$error"; then
    echo "expected ${label} render to fail" >&2
    return 1
  fi
  assert_contains "$error" "$expected_error"
}

default_render="${tmpdir}/default.yaml"
render >"$default_render"
assert_object_count "$default_render" 0
assert_clusterissuer_count "$default_render" 0

enabled_render="${tmpdir}/enabled.yaml"
render_enabled >"$enabled_render"
assert_object_count "$enabled_render" 1
assert_clusterissuer_count "$enabled_render" 1
assert_clusterissuer_manifest "$enabled_render" "$issuer_name"

# Named control planes need an explicit ownership marker because Helm retains
# this cluster-scoped resource on uninstall. The empty/default chart value must
# continue to render the exact legacy manifest asserted above.
named_render="${tmpdir}/named.yaml"
render_enabled \
  --set-string clusterIssuer.name=alpha-nvcf-openbao-pki \
  --set-string clusterIssuer.controlPlaneID=alpha \
  >"$named_render"
assert_object_count "$named_render" 1
assert_clusterissuer_count "$named_render" 1
assert_contains "$named_render" 'nvcf.nvidia.com/control-plane-id: "alpha"'
assert_contains "$named_render" '"helm.sh/resource-policy": keep'

custom_name_render="${tmpdir}/custom-name.yaml"
render_enabled --set-string clusterIssuer.name=custom-openbao-issuer >"$custom_name_render"
assert_object_count "$custom_name_render" 1
assert_clusterissuer_count "$custom_name_render" 1
assert_clusterissuer_manifest "$custom_name_render" custom-openbao-issuer

assert_render_fails empty-name \
  "clusterIssuer.name is required when clusterIssuer.enabled=true" \
  --set-string clusterIssuer.name=
assert_render_fails empty-server \
  "clusterIssuer.server is required when clusterIssuer.enabled=true" \
  --set-string clusterIssuer.server=
assert_render_fails empty-path \
  "clusterIssuer.path is required when clusterIssuer.enabled=true" \
  --set-string clusterIssuer.path=
assert_render_fails empty-auth-mount \
  "clusterIssuer.auth.mountPath is required when clusterIssuer.enabled=true" \
  --set-string clusterIssuer.auth.mountPath=
assert_render_fails empty-auth-role \
  "clusterIssuer.auth.role is required when clusterIssuer.enabled=true" \
  --set-string clusterIssuer.auth.role=
assert_render_fails empty-service-account \
  "clusterIssuer.auth.serviceAccount.name is required when clusterIssuer.enabled=true" \
  --set-string clusterIssuer.auth.serviceAccount.name=
assert_render_fails empty-audience \
  "clusterIssuer.auth.serviceAccount.audience is required when clusterIssuer.enabled=true" \
  --set-string clusterIssuer.auth.serviceAccount.audience=

echo "nvcf-pki render checks passed"
