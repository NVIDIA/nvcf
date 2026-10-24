#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="$(cd "$stack_dir/../../.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
mkdir -p "$work_dir/self-managed"
cp -R "$stack_dir"/. "$work_dir/self-managed"
environment_file="$work_dir/self-managed/environments/cert-images-test.yaml"
printf '{}\n' > "$work_dir/self-managed/secrets/cert-images-test-secrets.yaml"
manifest="$repo_root/docs/overview/manifest.md"
if test ! -f "$manifest"; then manifest="$repo_root/docs/user/manifest.md"; fi
chart="${CERT_MANAGER_STACK_CHART_DIR:-$repo_root/deploy/helm/cert-manager}"

fail() { echo "cert-manager-image-overrides: $*" >&2; exit 1; }
assert_equal() { test "$1" = "$2" || fail "$3: got $1, expected $2"; }
write_values() {
  HELMFILE_ENV=cert-images-test HELMFILE_CACHE_HOME="$work_dir/cache" \
    helmfile -f "$work_dir/self-managed/helmfile.d/01-dependencies.yaml.gotmpl" \
    --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
    --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
    --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
    --selector name=cert-manager write-values --output-file-template "$1" >/dev/null
}
render() {
  write_values "$work_dir/values.yaml"
  helm template cert-manager "$chart" -n cert-manager -f "$work_dir/values.yaml" > "$work_dir/rendered.yaml"
}
image_path() {
  if test "$1" = controller; then echo image; else echo "$1.image"; fi
}
rendered_image() {
  if test "$1" = acmesolver; then
    yq -r 'select(.kind == "Deployment" and .metadata.name == "cert-manager") | .spec.template.spec.containers[].args[] | select(test("^--acme-http01-solver-image="))' "$work_dir/rendered.yaml" | sed 's/^--acme-http01-solver-image=//'
  else
    local name=cert-manager
    if test "$1" != controller; then name="cert-manager-$1"; fi
    yq -r "select((.kind == \"Deployment\" or .kind == \"Job\") and .metadata.name == \"$name\") | .spec.template.spec.containers[0].image" "$work_dir/rendered.yaml"
  fi
}
assert_images() {
  local component path repository tag digest expected
  for component in controller webhook cainjector startupapicheck acmesolver; do
    path="$(image_path "$component")"
    repository="$(yq -r ".certManager.$path.repository" "$environment_file")"
    tag="$(yq -r ".certManager.$path.tag // \"\"" "$environment_file")"
    digest="$(yq -r ".certManager.$path.digest // \"\"" "$environment_file")"
    if test -z "$tag$digest"; then
      tag="$(yq -r '.appVersion' "$chart/charts/cert-manager/Chart.yaml")"
    fi
    expected="$repository"
    if test -n "$tag"; then expected="$expected:$tag"; fi
    if test -n "$digest"; then expected="$expected@$digest"; fi
    assert_equal "$(rendered_image "$component")" "$expected" "$component rendered reference"
  done
}

printf '{}\n' > "$environment_file"
render
cp "$work_dir/rendered.yaml" "$work_dir/default.yaml"
# Exercise the authored example rather than duplicating its pins in the test.
awk '/^### Use upstream cert-manager images/{section=1; next} section && /^```yaml$/{block=1; next} block && /^```$/{exit} block' "$manifest" > "$environment_file"
test -s "$environment_file" || fail 'missing documented environment example'
# A global private mirror must not capture explicit upstream repositories.
yq -i '.global.image = {"registry": "global.example.com", "repository": "private"}' "$environment_file"
render
assert_images
for component in controller webhook cainjector startupapicheck acmesolver; do
  path="$(image_path "$component")"
  assert_equal "$(yq -r ".certManager.$path.tag" "$environment_file")" \
    "$(yq -r '.appVersion' "$chart/charts/cert-manager/Chart.yaml")" "$component matches selected subchart"
done
for name in cert-manager cert-manager-webhook cert-manager-cainjector cert-manager-startupapicheck; do
  assert_equal "$(yq "select((.kind == \"Deployment\" or .kind == \"Job\") and .metadata.name == \"$name\") | .spec.template.spec.securityContext.runAsNonRoot" "$work_dir/rendered.yaml")" true "$name retains nonroot"
done
# Distinct mirrors and tags catch accidental forwarding between components.
for component in controller webhook cainjector startupapicheck acmesolver; do
  path="$(image_path "$component")"
  yq -i ".certManager.$path.repository = \"mirror.example.com/team/$component\" | .certManager.$path.tag = \"custom-$component\" | .certManager.$path.pullPolicy = \"Always\"" "$environment_file"
done
render
assert_images
for component in controller webhook cainjector startupapicheck acmesolver; do
  path="$(image_path "$component")"
  assert_equal "$(yq -r ".\"cert-manager\".$path.pullPolicy" "$work_dir/values.yaml")" Always "$component pullPolicy"
  yq -i "del(.certManager.$path.tag)" "$environment_file"
done
render
assert_images
for component in controller webhook cainjector startupapicheck acmesolver; do
  path="$(image_path "$component")"
  yq -i "del(.certManager.$path.digest) | .certManager.$path.tag = \"custom-$component\"" "$environment_file"
done
render
assert_images
# Repository-only mirrors retain the selected chart version.
for component in controller webhook cainjector startupapicheck acmesolver; do
  path="$(image_path "$component")"
  yq -i "del(.certManager.$path.tag)" "$environment_file"
done
render
assert_images
# Empty overrides retain all baseline manifests, including CRDs and settings.
yq -i 'del(.global)' "$environment_file"
for component in controller webhook cainjector startupapicheck acmesolver; do
  path="$(image_path "$component")"
  yq -i ".certManager.$path = {\"repository\": \"\", \"tag\": \"\", \"digest\": \"\", \"pullPolicy\": \"\"}" "$environment_file"
done
render
cmp "$work_dir/default.yaml" "$work_dir/rendered.yaml" || fail 'empty overrides changed defaults'
echo 'cert-manager-image-overrides: OK'
