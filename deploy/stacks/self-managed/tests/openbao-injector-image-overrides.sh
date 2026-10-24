#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="$(cd "$stack_dir/../../.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
cp -R "$stack_dir" "$work_dir/self-managed"
environment_file="$work_dir/self-managed/environments/injector-images-test.yaml"
printf '{}\n' > "$work_dir/self-managed/secrets/injector-images-test-secrets.yaml"
manifest="$repo_root/docs/overview/manifest.md"
if test ! -f "$manifest"; then manifest="$repo_root/docs/user/manifest.md"; fi

fail() { echo "openbao-injector-image-overrides: $*" >&2; exit 1; }
assert_equal() { test "$1" = "$2" || fail "$3: got $1, expected $2"; }
write_values() {
  HELMFILE_ENV=injector-images-test HELMFILE_CACHE_HOME="$work_dir/cache" \
    helmfile -f "$work_dir/self-managed/helmfile.d/01-dependencies.yaml.gotmpl" \
    --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
    --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
    --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
    --selector name=openbao-server write-values --output-file-template "$1" >/dev/null
}

printf '{}\n' > "$environment_file"
write_values "$work_dir/default.yaml"
assert_equal "$(yq '.openbao.injector.image.tag' "$work_dir/default.yaml")" null 'chart owns default tag'
awk '/^### Use the upstream Vault injector image/{section=1; next} section && /^```yaml$/{block=1; next} block && /^```$/{exit} block' "$manifest" > "$environment_file"
test -s "$environment_file" || fail 'missing documented environment example'
write_values "$work_dir/upstream.yaml"
for key in registry repository tag; do
  assert_equal "$(yq ".openbao.injector.image.$key" "$work_dir/upstream.yaml")" \
    "$(yq ".openbao.injector.image.$key" "$environment_file")" "$key forwarding"
done
for path in openbao.injector.agentImage openbao.server.image; do
  assert_equal "$(yq -o=json -I=0 ".$path | sort_keys(.)" "$work_dir/upstream.yaml")" \
    "$(yq -o=json -I=0 ".$path | sort_keys(.)" "$work_dir/default.yaml")" "$path unchanged"
done

check_chart() {
  test -n "${OPENBAO_STACK_CHART_DIR:-}" || return 0
  helm template openbao-server "$OPENBAO_STACK_CHART_DIR" -n vault-system \
    -f "$work_dir/$1.yaml" > "$work_dir/rendered-$1.yaml"
  expected="$(yq -r '.openbao.injector.image | .registry + "/" + .repository + ":" + .tag' "$environment_file")"
  actual="$(yq -r 'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.name == "sidecar-injector") | .image' "$work_dir/rendered-$1.yaml")"
  assert_equal "$actual" "$expected" "$1 published-chart image"
  # Only the webhook image may change; injected agent/server and policy stay intact.
  yq 'select(.kind == "Deployment") | .spec.template.spec.containers[] |= (select(.name == "sidecar-injector").image = "injector-placeholder")' \
    "$work_dir/rendered-$1.yaml" > "$work_dir/injector-$1.yaml"
  if test "$1" = upstream; then
    helm template openbao-server "$OPENBAO_STACK_CHART_DIR" -n vault-system \
      -f "$work_dir/default.yaml" > "$work_dir/rendered-default.yaml"
    yq 'select(.kind == "Deployment") | .spec.template.spec.containers[] |= (select(.name == "sidecar-injector").image = "injector-placeholder")' \
      "$work_dir/rendered-default.yaml" > "$work_dir/injector-default.yaml"
    diff -u "$work_dir/injector-default.yaml" "$work_dir/injector-upstream.yaml"
    assert_equal "$(yq 'select(.kind == "Deployment") | .spec.template.spec.securityContext.runAsNonRoot' "$work_dir/rendered-upstream.yaml")" true 'nonroot enforcement'
  fi
}
check_chart upstream

yq -i '.openbao.injector.image = {"registry": "mirror.example.com", "repository": "team/injector", "tag": "custom@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}' "$environment_file"
write_values "$work_dir/mirror.yaml"
assert_equal "$(yq -o=json -I=0 '.openbao.injector.image | sort_keys(.)' "$work_dir/mirror.yaml")" \
  "$(yq -o=json -I=0 '.openbao.injector.image | sort_keys(.)' "$environment_file")" 'custom mirror forwarding'
check_chart mirror
yq -i '.openbao.injector.image[] = ""' "$environment_file"
write_values "$work_dir/empty.yaml"
diff -u "$work_dir/default.yaml" "$work_dir/empty.yaml"
echo 'openbao-injector-image-overrides: OK'
