#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="$(cd "$stack_dir/../../.." && pwd)"
chart_dir="$repo_root/deploy/helm/nats"
render_chart_dir="$chart_dir"
catalog="$repo_root/docs/version-catalog/main.yaml"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
mkdir -p "$work_dir/self-managed"
cp -R "$stack_dir"/. "$work_dir/self-managed"
printf '{}\n' > "$work_dir/self-managed/secrets/nats-images-test-secrets.yaml"
environment_file="$work_dir/self-managed/environments/nats-images-test.yaml"

fail() { echo "nats-upstream-images: $*" >&2; exit 1; }
assert_equal() { test "$1" = "$2" || fail "$3: got $1, expected $2"; }

write_values() {
  HELMFILE_ENV=nats-images-test HELMFILE_CACHE_HOME="$work_dir/cache" \
    helmfile -f "$work_dir/self-managed/helmfile.d/01-dependencies.yaml.gotmpl" \
    --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
    --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
    --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
    --selector name=nats write-values --output-file-template "$1" >/dev/null
}
render() {
  helm template nats "$render_chart_dir" --namespace nats-system "$@"
}
image_ref() {
  local file="$1" name="$2"
  yq -r "select(.kind == \"StatefulSet\" or .kind == \"Deployment\") | .spec.template.spec.containers[] | select(.name == \"$name\") | .image" "$file"
}

# Derive pins from the catalog, and compare actual chart and stack renders.
# Repository identity is the contract; versions must be free to advance.
server_ref="$(yq -r '.artifacts[] | select(.name == "nats-server" and .type == "image") | .upstream_repository + ":" + .version' "$catalog")"
box_ref="$(yq -r '.artifacts[] | select(.name == "nats-box" and .type == "image") | .upstream_repository + ":" + .version + "@" + .digest' "$catalog")"
reloader_ref="$(yq -r '.artifacts[] | select(.name == "nats-server-config-reloader" and .type == "image") | .upstream_repository + ":" + .version' "$catalog")"
[[ "$server_ref" == docker.io/synadia/nats-server:* ]] || fail 'server must use upstream'
[[ "$reloader_ref" == docker.io/natsio/nats-server-config-reloader:* ]] || fail 'reloader must use upstream'
[[ "$box_ref" =~ ^docker.io/natsio/nats-box:[0-9]+\.[0-9]+\.[0-9]+-nonroot@sha256:[0-9a-f]{64}$ ]] || fail 'box must pin a nonroot version and index digest'
assert_equal "$(yq -r '[.publications[] | select(.name == "nats-box" or .name == "nats-server")] | length' "$catalog")" 0 'NATS publication requirements'
manifest="$repo_root/docs/overview/manifest.md"
if test ! -f "$manifest"; then manifest="$repo_root/docs/user/manifest.md"; fi
for ref in "$server_ref" "$box_ref" "$reloader_ref"; do
  grep -Fq "$ref" "$manifest" || fail "generated manifest is missing $ref"
done

render --set nats.global.image.registry=global.example.com > "$work_dir/chart.yaml"
# Optionally validate the exact chart source selected by the release lane.
render_chart_dir="${NATS_STACK_CHART_DIR:-$chart_dir}"
cat > "$environment_file" <<'YAML'
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
YAML
write_values "$work_dir/default-values.yaml"
render -f "$work_dir/default-values.yaml" > "$work_dir/default.yaml"
for file in "$work_dir/chart.yaml" "$work_dir/default.yaml"; do
  assert_equal "$(image_ref "$file" nats)" "$server_ref" 'server reference'
  assert_equal "$(image_ref "$file" nats-box)" "$box_ref" 'box reference'
  assert_equal "$(image_ref "$file" reloader)" "$reloader_ref" 'reloader reference'
  grep -Fq 'secretName: nats-nkeys' "$file" || fail 'NKeys secret mount missing'
  grep -Fq '/etc/nats-nkeys/default' "$file" || fail 'NKey context path missing'
  grep -Fq '/etc/nats-contexts' "$file" || fail 'NATS contexts mount missing'
done

  assert_equal "$(yq -r 'select(.kind == "Deployment") | .spec.template.spec.securityContext.runAsNonRoot' "$work_dir/chart.yaml")" true 'box nonroot'
  for field in runAsUser runAsGroup fsGroup; do
    assert_equal "$(yq -r "select(.kind == \"Deployment\") | .spec.template.spec.securityContext.$field" "$work_dir/chart.yaml")" 1000 "box $field"
  done

# Registry/repository overrides preserve the immutable pin without requiring a tag.
cat > "$environment_file" <<'YAML'
global:
  image:
    registry: ignored.example.com
    repository: ignored
nats:
  container:
    image:
      registry: mirror.example.com
      repository: dependencies/nats-server
  natsBox:
    container:
      image:
        registry: mirror.example.com
        repository: dependencies/nats-box
  reloader:
    image:
      registry: mirror.example.com
      repository: dependencies/reloader
YAML
write_values "$work_dir/mirror-values.yaml"
render -f "$work_dir/mirror-values.yaml" > "$work_dir/mirror.yaml"
assert_equal "$(image_ref "$work_dir/mirror.yaml" nats)" "mirror.example.com/dependencies/nats-server:${server_ref##*/nats-server:}" 'server mirror'
assert_equal "$(image_ref "$work_dir/mirror.yaml" nats-box)" "mirror.example.com/dependencies/nats-box:${box_ref##*/nats-box:}" 'box mirror pin'
assert_equal "$(image_ref "$work_dir/mirror.yaml" reloader)" "mirror.example.com/dependencies/reloader:${reloader_ref##*:}" 'reloader mirror'

# A customer tag replaces the complete tag@digest pin, not only its tag part.
yq -i '.nats.container.image.tag = "server-custom" | .nats.natsBox.container.image.tag = "box-custom@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" | .nats.reloader.image.tag = "reloader-custom"' "$environment_file"
write_values "$work_dir/custom-values.yaml"
render -f "$work_dir/custom-values.yaml" > "$work_dir/custom.yaml"
assert_equal "$(image_ref "$work_dir/custom.yaml" nats)" 'mirror.example.com/dependencies/nats-server:server-custom' 'server custom tag'
assert_equal "$(image_ref "$work_dir/custom.yaml" nats-box)" 'mirror.example.com/dependencies/nats-box:box-custom@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' 'box custom pin'
assert_equal "$(image_ref "$work_dir/custom.yaml" reloader)" 'mirror.example.com/dependencies/reloader:reloader-custom' 'reloader custom tag'

# Explicit empty strings retain upstream defaults rather than global.image.
yq -i '(.nats.container.image[], .nats.natsBox.container.image[], .nats.reloader.image[]) = ""' "$environment_file"
write_values "$work_dir/empty-values.yaml"
render -f "$work_dir/empty-values.yaml" > "$work_dir/empty.yaml"
assert_equal "$(image_ref "$work_dir/empty.yaml" nats)" "$server_ref" 'empty server overrides'
assert_equal "$(image_ref "$work_dir/empty.yaml" nats-box)" "$box_ref" 'empty box overrides'
assert_equal "$(image_ref "$work_dir/empty.yaml" reloader)" "$reloader_ref" 'empty reloader overrides'
echo 'nats-upstream-images: OK'
