#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="$(cd "$stack_dir/../../.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

test_stack_dir="$work_dir/self-managed"
environment_name=openbao-jwt-lifecycle-test
environment_file="$test_stack_dir/environments/$environment_name.yaml"
mkdir -p "$test_stack_dir"
cp -R "$stack_dir"/. "$test_stack_dir"
printf '{}\n' > "$test_stack_dir/secrets/$environment_name-secrets.yaml"

# The stack's published chart pin predates these modes. Render the source chart
# to check the environment-to-chart contract before its next release.
chart_dir="$work_dir/chart"
cp -R "$repo_root/deploy/helm/openbao/helm" "$chart_dir"
helm dependency build "$chart_dir" >/dev/null

fail() { echo "openbao-jwt-lifecycle-wiring: $*" >&2; exit 1; }

write_values() {
  local output_file="$1"
  HELMFILE_ENV="$environment_name" HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile --file "$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
      --selector name=openbao-server write-values \
      --output-file-template "$output_file" >/dev/null
  test -s "$output_file" || fail "Helmfile wrote no values to $output_file"
}

for mode in legacy bridge versioned; do
  if [[ $mode == legacy ]]; then
    printf '{}\n' > "$environment_file"
  else
    printf 'openbao:\n  jwtPluginLifecycle:\n    mode: %s\n' "$mode" > "$environment_file"
  fi

  values="$work_dir/$mode-values.yaml"
  rendered="$work_dir/$mode-rendered.yaml"
  write_values "$values"
  actual="$(yq -r '.openbao.jwtPluginLifecycle.mode // ""' "$values")"
  if [[ $mode == legacy ]]; then
    test -z "$actual" || fail "unset mode should use the chart default, got $actual"
  else
    test "$actual" = "$mode" || fail "$mode was not forwarded, got ${actual:-missing}"
  fi

  helm template openbao-server "$chart_dir" --namespace vault-system \
    --is-upgrade -f "$values" > "$rendered"
  jobs="$(yq -r 'select(.kind == "Job") | .metadata.name' "$rendered")"
  has_job() { grep -Fxq "$1" <<< "$jobs"; }
  preflight="$(yq -r 'select(.kind == "Job" and .metadata.name == "openbao-server-jwt-lifecycle-preflight") | .spec.template.spec.containers[] | select(.name == "preflight") | .args[]' "$rendered")"
  init_mode="$(yq -r 'select(.kind == "Job" and .metadata.name == "openbao-server-initialize-cluster") | .spec.template.spec.containers[] | select(.name == "bao-init") | .env[] | select(.name == "JWT_PLUGIN_LIFECYCLE_MODE") | .value' "$rendered")"
  test "$init_mode" = "$mode" || fail "$mode did not reach the chart init Job"

  case "$mode" in
    legacy)
      grep -Fq 'Versioned OpenBao images require bridge or versioned lifecycle mode' <<< "$preflight" || fail 'legacy preflight missing'
      has_job openbao-server-refresh-jwt-plugin-catalog || fail 'legacy refresh Job missing'
      has_job openbao-server-migrations || fail 'legacy migrations Job missing'
      ;;
    bridge)
      grep -Fq 'jwt-plugin-lifecycle bridge-preflight' <<< "$preflight" || fail 'bridge preflight missing'
      if has_job openbao-server-refresh-jwt-plugin-catalog || has_job openbao-server-migrations; then
        fail 'bridge mode must defer refresh and migrations Jobs'
      fi
      ;;
    versioned)
      grep -Fq 'jwt-plugin-lifecycle deployment-preflight' <<< "$preflight" || fail 'versioned preflight missing'
      yq -e 'select(.kind == "Job" and .metadata.name == "openbao-server-jwt-lifecycle-preflight") | .spec.template.spec.initContainers[] | select(.name == "check-migrations-and-members")' "$rendered" >/dev/null || fail 'member check missing'
      has_job openbao-server-migrations || fail 'versioned migrations Job missing'
      if has_job openbao-server-refresh-jwt-plugin-catalog; then
        fail 'versioned mode must not refresh the legacy catalog'
      fi
      ;;
  esac
done

echo 'openbao-jwt-lifecycle-wiring: all checks passed'
