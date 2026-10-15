#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

chart_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "auth-config.sh: $*" >&2
  exit 1
}

render() {
  local output_file="$1"
  shift
  helm template reval "$chart_root" \
    --namespace nvcf \
    --set-string reval.image.registry=registry.example.test \
    --set-string reval.image.repository=reval-service \
    "$@" >"$output_file"
}

assert_config_eq() {
  local file="$1"
  local expression="$2"
  local expected="$3"
  local actual

  actual="$(yq ea -r \
    "select(.kind == \"ConfigMap\") | .data.\"config.yaml\" | from_yaml | $expression" \
    "$file")"
  [[ "$actual" == "$expected" ]] ||
    fail "expected $expression to equal '$expected', got '$actual'"
}

default_manifest="$work_dir/default.yaml"
disabled_manifest="$work_dir/disabled.yaml"
custom_manifest="$work_dir/custom.yaml"

render "$default_manifest"
assert_config_eq "$default_manifest" '.auth.jwt.enabled' true
assert_config_eq \
  "$default_manifest" \
  '.auth.jwt."jwk-set-url"' \
  'http://openbao-server.vault-system.svc.cluster.local:8200/v1/services/reval/jwt/jwks'
assert_config_eq "$default_manifest" '.auth.oidc.enabled' true
assert_config_eq \
  "$default_manifest" \
  '.auth.oidc."introspect-url"' \
  'http://api.sis.svc.cluster.local:8080/v1/nvca/tokens/introspect'

render "$disabled_manifest" \
  --set reval.serviceConfig.auth.jwt.enabled=false \
  --set reval.serviceConfig.auth.oidc.enabled=false
assert_config_eq "$disabled_manifest" '.auth.jwt.enabled' false
assert_config_eq "$disabled_manifest" '.auth.jwt."jwk-set-url" // "absent"' absent
assert_config_eq "$disabled_manifest" '.auth.oidc.enabled' false
assert_config_eq "$disabled_manifest" '.auth.oidc."introspect-url" // "absent"' absent

render "$custom_manifest" \
  --set-string reval.serviceConfig.auth.jwt.jwkSetUrl=https://jwt.example.test/jwks \
  --set-string reval.serviceConfig.auth.jwt.validateRequiredScopes=reval.validate \
  --set-string reval.serviceConfig.auth.jwt.renderRequiredScopes=reval.render \
  --set-string reval.serviceConfig.auth.oidc.introspectUrl=https://oidc.example.test/introspect \
  --set-string reval.serviceConfig.auth.oidc.cacheTtl=10m
assert_config_eq "$custom_manifest" '.auth.jwt."jwk-set-url"' 'https://jwt.example.test/jwks'
assert_config_eq "$custom_manifest" '.auth.jwt."validate-required-scopes"' reval.validate
assert_config_eq "$custom_manifest" '.auth.jwt."render-required-scopes"' reval.render
assert_config_eq "$custom_manifest" '.auth.oidc."introspect-url"' 'https://oidc.example.test/introspect'
assert_config_eq "$custom_manifest" '.auth.oidc."cache-ttl"' 10m

echo "auth-config.sh: ReVal authorization configuration is valid"
