#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# The vault-agent template must ask Vault for a short, explicit intermediate
# lifetime. A csr-only sign-intermediate call takes the PKI mount default and
# is rejected whenever that default outlives the signing CA, which leaves a
# restarted pod in Init forever. Run from deploy/helm/container-cache.
set -euo pipefail

OUT_FILE="$(mktemp)"
trap 'rm -f "${OUT_FILE}"' EXIT

VAULT_ARGS=(
  --set vault.enabled=true
  --set vault.vaultAddress=https://vault.example.com
  --set vault.clusterCSP=csp
  --set vault.clusterAccountName=acct
  --set vault.clusterRegion=region
  --set vault.clusterName=cluster
  --set vault.namespace=ns
)

assert_has() {
  if ! grep -F -q -- "$1" "${OUT_FILE}"; then
    echo "FAILED: expected pattern not found: $1" >&2
    exit 1
  fi
}

assert_not_has() {
  if grep -F -q -- "$1" "${OUT_FILE}"; then
    echo "FAILED: unexpected pattern found: $1" >&2
    exit 1
  fi
}

echo "Checking default intermediate TTL on the sign-intermediate call..."
helm template c ./deploy "${VAULT_ARGS[@]}" > "${OUT_FILE}"
assert_has 'pki/server/01/root/sign-intermediate` $arg "ttl=336h" }}'
# The template explains why trunc_by_ca_not_after is avoided; the call itself
# must not carry it.
if grep -F -- 'root/sign-intermediate`' "${OUT_FILE}" | grep -F -q 'trunc_by_ca_not_after'; then
  echo "FAILED: sign-intermediate call sets trunc_by_ca_not_after" >&2
  exit 1
fi
# Init and sidecar agents share template.hcl, so one rendered call covers both.
assert_has 'config-init.hcl'

echo "Checking the template renders issuance-specific output so the exec hook fires..."
# Vault Agent runs a template's exec command only when the rendered destination
# changes. writeToFile output is empty, so without this line every render was
# identical and the bundle rewrite plus nginx reload never ran.
assert_has 'serial={{ .Data.serial_number }} expiration={{ .Data.expiration }}'
assert_has '"source" = "/vault/configs/template.hcl"'

echo "Checking the TTL is overridable (short values are used for rotation tests)..."
helm template c ./deploy "${VAULT_ARGS[@]}" --set vault.intermediateCertTTL=10m > "${OUT_FILE}"
assert_has 'sign-intermediate` $arg "ttl=10m" }}'

echo "Checking an empty TTL is refused rather than rendered as a mount-default call..."
if helm template c ./deploy "${VAULT_ARGS[@]}" --set vault.intermediateCertTTL= > "${OUT_FILE}" 2>&1; then
  echo "FAILED: render succeeded with an empty vault.intermediateCertTTL" >&2
  exit 1
fi
assert_has 'vault.intermediateCertTTL is required'

echo "Checking the certificate expiry gauge is wired in both modes..."
check_gauge() {
  assert_has 'container_cache_tls_cert_not_after_seconds'
  assert_has 'container_cache_tls_bundle_read_ok'
  assert_has 'lua_shared_dict cert_metrics_state'
  assert_not_has 'tls_cert_not_after:reset()'
  assert_has 'lua-cert-expiry.lua: |-'
  assert_has 'path: lua-cert-expiry.lua'
  assert_has 'pcall(dofile, "/etc/nginx/conf.d/lua/lua-cert-expiry.lua")'
}
helm template c ./deploy > "${OUT_FILE}"
check_gauge
helm template c ./deploy "${VAULT_ARGS[@]}" > "${OUT_FILE}"
check_gauge

echo "PASS: vault intermediate TTL and cert expiry metric render checks"
