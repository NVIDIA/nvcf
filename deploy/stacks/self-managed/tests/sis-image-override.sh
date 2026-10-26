#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Test that the SIS image repository, like its tag, can be overridden from an
# environment file through global.yaml.gotmpl into the rendered chart values.
# The default stays <global.image.repository>/icms-service-oss, and the
# registry keeps following global.image.registry.
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
environment_name="sis-image-override-test"
environment_file="$test_stack_dir/environments/$environment_name.yaml"
secrets_file="$test_stack_dir/secrets/$environment_name-secrets.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "sis-image-override: $*" >&2
  exit 1
}

mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' >"$secrets_file"

write_environment() {
  cat >"$environment_file"
}

render_sis_values() {
  local output_file="$1"

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
      --selector name=sis \
      write-values \
      --output-file-template "$output_file" >/dev/null
}

assert_value() {
  local values_file="$1" expression="$2" want="$3" label="$4" got
  got="$(yq -r "$expression // \"\"" "$values_file")"
  [[ "$got" == "$want" ]] ||
    fail "$label: expected $want, got ${got:-<none>}"
}

# ---------------------------------------------------------------------------
# 1. No override: the default repository and the global registry are unchanged
#    and no tag is forced.
# ---------------------------------------------------------------------------
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
EOF

render_sis_values "$work_dir/default-values.yaml"
assert_value "$work_dir/default-values.yaml" .sis.image.registry nvcr.io "default registry"
assert_value "$work_dir/default-values.yaml" .sis.image.repository test/nvcf/icms-service-oss "default repository"
assert_value "$work_dir/default-values.yaml" .sis.image.tag "" "default tag"

# ---------------------------------------------------------------------------
# 2. Repository override: honored as given, registry still follows the global.
# ---------------------------------------------------------------------------
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
sis:
  image:
    repository: mirror/team/sis-custom
EOF

render_sis_values "$work_dir/repository-values.yaml"
assert_value "$work_dir/repository-values.yaml" .sis.image.registry nvcr.io "repository override registry"
assert_value "$work_dir/repository-values.yaml" .sis.image.repository mirror/team/sis-custom "repository override"
assert_value "$work_dir/repository-values.yaml" .sis.image.tag "" "repository override tag"

# ---------------------------------------------------------------------------
# 3. Repository and tag overrides compose.
# ---------------------------------------------------------------------------
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
sis:
  image:
    repository: mirror/team/sis-custom
    tag: 9.9.9-test
EOF

render_sis_values "$work_dir/both-values.yaml"
assert_value "$work_dir/both-values.yaml" .sis.image.repository mirror/team/sis-custom "repository and tag: repository"
assert_value "$work_dir/both-values.yaml" .sis.image.tag 9.9.9-test "repository and tag: tag"

# ---------------------------------------------------------------------------
# 4. An empty repository override falls back to the default instead of
#    rendering an empty repository.
# ---------------------------------------------------------------------------
write_environment <<'EOF'
global:
  image:
    registry: nvcr.io
    repository: test/nvcf
sis:
  image:
    repository: ""
EOF

render_sis_values "$work_dir/empty-values.yaml"
assert_value "$work_dir/empty-values.yaml" .sis.image.repository test/nvcf/icms-service-oss "empty repository override"

echo "sis-image-override: all checks passed"
