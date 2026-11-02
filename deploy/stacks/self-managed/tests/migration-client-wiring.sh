#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
cp -R "$stack_dir" "$work_dir/self-managed"
printf '{}\n' >"$work_dir/self-managed/secrets/base-secrets.yaml"

fail() { echo "migration-client-wiring: $*" >&2; exit 1; }

render_case() {
  local case_name="$1"
  shift
  local state
  for state in 01-dependencies.yaml.gotmpl 02-core.yaml.gotmpl; do
  HELMFILE_ENV=base HELMFILE_CACHE_HOME="$work_dir/cache" helmfile \
    --file "$work_dir/self-managed/helmfile.d/$state" \
    --environment default \
    --state-values-set ingress.gatewayApi.controllerNamespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gw \
    --state-values-set ingress.gatewayApi.gateways.shared.namespace=envoy-gateway-system \
    --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gw \
    --state-values-set ingress.gatewayApi.gateways.grpc.namespace=envoy-gateway-system \
    --state-values-set addons.lls.enabled=true \
    --state-values-set addons.llm.enabled=true \
    --state-values-set addons.nvcfUi.enabled=true \
    --state-values-set-string addons.llm.pki.allowedDomains=cluster.local \
    --state-values-set-string 'addons.llm.pki.dnsNames[0]=llm-request-router.nvcf.svc.cluster.local' \
    --selector name=openbao-server \
    --selector name=sis \
    --selector name=llm-request-router \
    --selector name=nvcf-ui \
    "$@" \
    write-values --output-file-template "$work_dir/$case_name-{{ .Release.Name }}.yaml" \
    >>"$work_dir/$case_name.log" 2>&1 || {
      cat "$work_dir/$case_name.log" >&2
      fail "$case_name did not render"
    }
  done
}

assert_client() {
  local case_name="$1" release="$2" path="$3" expected="$4" actual
  actual="$(yq -r "$path // \"\"" "$work_dir/$case_name-$release.yaml")"
  test "$actual" = "$expected" || fail "$case_name $release: expected '$expected', got '$actual'"
}

assert_all_clients() {
  local case_name="$1" expected="$2"
  assert_client "$case_name" openbao-server .openbao.migrations.kubectlMinor "$expected"
  assert_client "$case_name" sis .sis.lls.hmacRotation.kubectlMinor "$expected"
  assert_client "$case_name" llm-request-router .llmRequestRouter.pki.kubectlMinor "$expected"
  assert_client "$case_name" nvcf-ui .nvcfUi.openbaoMigrations.kubectlMinor "$expected"
}

render_case default
assert_all_clients default ""

render_case compatible --state-values-set-string openbao.migrations.kubectlMinor=1.36
assert_all_clients compatible 1.36

render_case explicit \
  --state-values-set-string openbao.migrations.kubectlMinor=1.36 \
  --state-values-set-string addons.lls.hmacRotation.kubectlMinor=1.37 \
  --state-values-set-string addons.llm.pki.kubectlMinor=1.37 \
  --state-values-set-string addons.nvcfUi.openbaoMigrations.kubectlMinor=1.37
assert_client explicit openbao-server .openbao.migrations.kubectlMinor 1.36
assert_client explicit sis .sis.lls.hmacRotation.kubectlMinor 1.37
assert_client explicit llm-request-router .llmRequestRouter.pki.kubectlMinor 1.37
assert_client explicit nvcf-ui .nvcfUi.openbaoMigrations.kubectlMinor 1.37

render_case empty-overrides \
  --state-values-set-string openbao.migrations.kubectlMinor=1.36 \
  --state-values-set-string addons.lls.hmacRotation.kubectlMinor= \
  --state-values-set-string addons.llm.pki.kubectlMinor= \
  --state-values-set-string addons.nvcfUi.openbaoMigrations.kubectlMinor=
assert_all_clients empty-overrides 1.36

echo 'migration-client-wiring: all checks passed'
