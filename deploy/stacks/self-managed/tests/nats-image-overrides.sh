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
environment_file="$work_dir/self-managed/environments/nats-images-test.yaml"
printf '{}\n' > "$work_dir/self-managed/secrets/nats-images-test-secrets.yaml"
manifest="$repo_root/docs/overview/manifest.md"
if test ! -f "$manifest"; then manifest="$repo_root/docs/user/manifest.md"; fi

fail() { echo "nats-image-overrides: $*" >&2; exit 1; }
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

printf '{}\n' > "$environment_file"
write_values "$work_dir/default.yaml"
assert_equal "$(yq '.nats.natsBox.podTemplate' "$work_dir/default.yaml")" null 'no unconditional box policy'
# Exercise the documented environment block, not a second copy of its pins.
awk '/^### Use upstream NATS images/{section=1; next} section && /^```yaml$/{block=1; next} block && /^```$/{exit} block' "$manifest" > "$environment_file"
test -s "$environment_file" || fail 'missing documented environment example'
write_values "$work_dir/upstream.yaml"
for path in container reloader natsBox.container; do
  for key in registry repository tag; do
    assert_equal "$(yq ".nats.$path.image.$key" "$work_dir/upstream.yaml")" \
      "$(yq ".nats.$path.image.$key" "$environment_file")" "$path $key forwarding"
  done
done
for path in podTemplate natsBox.podTemplate; do
  assert_equal "$(yq -o=json -I=0 ".nats.$path.merge.spec.securityContext | sort_keys(.)" "$work_dir/upstream.yaml")" \
    "$(yq -o=json -I=0 ".nats.$path.merge.spec.securityContext | sort_keys(.)" "$environment_file")" "$path security forwarding"
done

# Optional integration with the immutable published chart selected by this lane.
if test -n "${NATS_STACK_CHART_DIR:-}"; then
  # Distinct identities catch accidental forwarding of the server policy to the box.
  yq -i '(.nats.natsBox.podTemplate.merge.spec.securityContext.runAsUser, .nats.natsBox.podTemplate.merge.spec.securityContext.runAsGroup) = 2000' "$environment_file"
  write_values "$work_dir/upstream.yaml"
  helm template nats "$NATS_STACK_CHART_DIR" -n nats-system -f "$work_dir/upstream.yaml" > "$work_dir/rendered.yaml"
  for pair in nats:container reloader:reloader nats-box:natsBox.container; do
    container="${pair%%:*}"; path="${pair#*:}"
    expected="$(yq -r ".nats.$path.image | .registry + \"/\" + .repository + \":\" + .tag" "$environment_file")"
    actual="$(yq -r "select(.kind == \"StatefulSet\" or .kind == \"Deployment\") | .spec.template.spec.containers[] | select(.name == \"$container\") | .image" "$work_dir/rendered.yaml")"
    assert_equal "$actual" "$expected" "$container rendered reference"
  done
  for pair in StatefulSet:podTemplate Deployment:natsBox.podTemplate; do
    kind="${pair%%:*}"; path="${pair#*:}"
    for field in runAsNonRoot runAsUser runAsGroup; do
      expected="$(yq ".nats.$path.merge.spec.securityContext.$field" "$environment_file")"
      assert_equal "$(yq "select(.kind == \"$kind\") | .spec.template.spec.securityContext.$field" "$work_dir/rendered.yaml")" "$expected" "$kind $field"
    done
  done
fi

# Custom mirrors and tag@digest must flow through all three image settings.
yq -i '(.nats.container.image, .nats.reloader.image, .nats.natsBox.container.image) = {"registry": "mirror.example.com", "repository": "team/custom", "tag": "custom@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}' "$environment_file"
write_values "$work_dir/mirror.yaml"
for path in container reloader natsBox.container; do
  assert_equal "$(yq -o=json -I=0 ".nats.$path.image | sort_keys(.)" "$work_dir/mirror.yaml")" \
    "$(yq -o=json -I=0 ".nats.$path.image | sort_keys(.)" "$environment_file")" "$path mirror forwarding"
done
# Empty per-image keys retain each lane's defaults, including future pin bumps.
yq -i '(.nats.container.image[], .nats.reloader.image[], .nats.natsBox.container.image[]) = ""' "$environment_file"
write_values "$work_dir/empty.yaml"
for path in container reloader natsBox.container; do
  assert_equal "$(yq -o=json -I=0 ".nats.$path.image | sort_keys(.)" "$work_dir/empty.yaml")" \
    "$(yq -o=json -I=0 ".nats.$path.image | sort_keys(.)" "$work_dir/default.yaml")" "$path default fallback"
done
echo 'nats-image-overrides: OK'
