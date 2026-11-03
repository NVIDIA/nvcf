#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

chart_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../nvcf-invocation-service" && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "access_policy_test.sh: $*" >&2
  exit 1
}

render() {
  local output="$1"
  shift
  helm template invocation-service "$chart_root" --namespace nvcf \
    --set-string invocation.image.registry=registry.example.test \
    --set-string invocation.image.repository=nvcf-invocation-service \
    "$@" >"$output"
}

env_value() {
  local file="$1" key="$2"
  POLICY_KEY="$key" yq ea -r \
    'select(.kind == "ConfigMap" and (.metadata.name | test("-env$"))) | .data[strenv(POLICY_KEY)] // "absent"' \
    "$file"
}

checksum() {
  yq ea -r 'select(.kind == "Deployment") | .spec.template.metadata.annotations."checksum/config-env"' "$1"
}

render "$work_dir/default.yaml"
[[ "$(env_value "$work_dir/default.yaml" ENFORCE_POLLING_OWNER)" == false ]] || fail "preparation must be the default"
[[ "$(env_value "$work_dir/default.yaml" CORS_ALLOWED_ORIGINS)" == absent ]] || fail "empty CORS list must use the application default"

render "$work_dir/one.yaml" \
  --set-string 'invocation.cors.allowedOrigins[0]=https://console.example'
[[ "$(env_value "$work_dir/one.yaml" CORS_ALLOWED_ORIGINS)" == https://console.example ]] || fail "single CORS origin not rendered"

render "$work_dir/two.yaml" \
  --set-string 'invocation.cors.allowedOrigins[0]=https://console.example' \
  --set-string 'invocation.cors.allowedOrigins[1]=https://other.example'
[[ "$(env_value "$work_dir/two.yaml" CORS_ALLOWED_ORIGINS)" == https://console.example,https://other.example ]] || fail "CORS origins must render as CSV"
[[ "$(checksum "$work_dir/default.yaml")" != "$(checksum "$work_dir/one.yaml")" ]] || fail "origin changes must restart pods"
[[ "$(checksum "$work_dir/one.yaml")" != "$(checksum "$work_dir/two.yaml")" ]] || fail "additional origins must restart pods"

render "$work_dir/enforce.yaml" --set invocation.polling.enforceOwnership=true
[[ "$(env_value "$work_dir/enforce.yaml" ENFORCE_POLLING_OWNER)" == true ]] || fail "enforcement value not rendered"
[[ "$(checksum "$work_dir/default.yaml")" != "$(checksum "$work_dir/enforce.yaml")" ]] || fail "enforcement changes must restart pods"

render "$work_dir/override.yaml" \
  --set-string 'invocation.cors.allowedOrigins[0]=https://console.example' \
  --set-string invocation.env.CORS_ALLOWED_ORIGINS=https://override.example \
  --set invocation.polling.enforceOwnership=true \
  --set-string invocation.env.ENFORCE_POLLING_OWNER=false
[[ "$(env_value "$work_dir/override.yaml" CORS_ALLOWED_ORIGINS)" == https://override.example ]] || fail "explicit env origin must win"
[[ "$(env_value "$work_dir/override.yaml" ENFORCE_POLLING_OWNER)" == false ]] || fail "explicit env enforcement must win"
for key in CORS_ALLOWED_ORIGINS ENFORCE_POLLING_OWNER; do
  count="$(awk -v key="$key:" '$1 == key { count++ } END { print count+0 }' "$work_dir/override.yaml")"
  [[ "$count" == 1 ]] || fail "duplicate env key $key"
done

render "$work_dir/no-env.yaml" --set invocation.env=null \
  --set invocation.polling.enforceOwnership=true \
  --set-string 'invocation.cors.allowedOrigins[0]=https://console.example'
[[ "$(env_value "$work_dir/no-env.yaml" CORS_ALLOWED_ORIGINS)" == https://console.example ]] || fail "policy must work without the default env map"
[[ "$(env_value "$work_dir/no-env.yaml" ENFORCE_POLLING_OWNER)" == true ]] || fail "enforcement must work without the default env map"
[[ "$(yq ea -r 'select(.kind == "Deployment") | .spec.template.spec.containers[0].envFrom[0].configMapRef.name' "$work_dir/no-env.yaml")" == *-env ]] || fail "policy ConfigMap must reach the pod"

for bad_value in \
  'invocation.polling.enforceOwnership=not-a-boolean' \
  'invocation.cors.allowedOrigins=not-a-list' \
  'invocation.cors.allowedOrigins[0]='; do
  if render "$work_dir/invalid.yaml" --set-string "$bad_value" 2>"$work_dir/error.txt"; then
    fail "invalid policy value was accepted: $bad_value"
  fi
done

echo "access_policy_test.sh: CORS, staged ownership, overrides, and rollout checksums passed"
