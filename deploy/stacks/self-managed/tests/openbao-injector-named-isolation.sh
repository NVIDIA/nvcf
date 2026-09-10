#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
stack_dir="$(cd "$script_dir/.." && pwd)"
repo_root="$(cd "$stack_dir/../../.." && pwd)"
work_dir="$(mktemp -d)"
test_stack_dir="$work_dir/self-managed"
chart_dir="$work_dir/openbao-chart"
values_file="$work_dir/openbao-values.yaml"
manifest_dir="$work_dir/manifests"
manifest_file="$manifest_dir/openbao-render.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "openbao-injector-named-isolation: $*" >&2
  exit 1
}

command -v helm >/dev/null 2>&1 || fail "helm is required"
command -v helmfile >/dev/null 2>&1 || fail "helmfile is required"
command -v python3 >/dev/null 2>&1 || fail "python3 is required"
command -v yq >/dev/null 2>&1 || fail "yq is required"

cp -R "$stack_dir" "$test_stack_dir"
cp -R "$repo_root/deploy/helm/openbao/helm" "$chart_dir"
printf '{}\n' >"$test_stack_dir/secrets/base-secrets.yaml"

# Named mode is intentionally still blocked in the source tree while the last
# object-level leak classes are fixed. This test unlocks only the temp copy so
# it can inspect the values and manifests produced by this subphase.
perl -0pi -e 's/\$namedControlPlaneObjectIdentitiesReady := false/\$namedControlPlaneObjectIdentitiesReady := true/g' \
  "$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
  "$test_stack_dir/global.yaml.gotmpl"

render_openbao_values() {
  local owner="$1"
  local output_file="$2"

  HELMFILE_ENV=base \
    NVCF_CONTROL_PLANE_OWNER="$owner" \
    NVCF_ALPHA_NAMED_CONTROL_PLANE=true \
    HELMFILE_CACHE_HOME="$work_dir/helmfile-cache" \
    helmfile \
      --file "$test_stack_dir/helmfile.d/01-dependencies.yaml.gotmpl" \
      --environment default \
      --state-values-set ingress.gatewayApi.controllerNamespace=gateway \
      --state-values-set ingress.gatewayApi.gateways.shared.name=shared-gateway \
      --state-values-set ingress.gatewayApi.gateways.shared.namespace=gateway \
      --state-values-set ingress.gatewayApi.gateways.grpc.name=grpc-gateway \
      --state-values-set ingress.gatewayApi.gateways.grpc.namespace=gateway \
      --selector name=openbao-server \
      write-values \
      --output-file-template "$output_file" >/dev/null ||
    fail "helmfile could not render named OpenBao values for $owner"

  test -s "$output_file" ||
    fail "helmfile wrote no OpenBao values for $owner"
}

build_openbao_dependencies() {
  mkdir -p "$work_dir/helm-repository-cache"
  printf 'repositories: []\n' >"$work_dir/repositories.yaml"
  HELM_CACHE_HOME="${HELM_CACHE_HOME:-$work_dir/helm-cache}" \
    HELM_REPOSITORY_CACHE="${HELM_REPOSITORY_CACHE:-$work_dir/helm-repository-cache}" \
    HELM_REPOSITORY_CONFIG="${HELM_REPOSITORY_CONFIG:-$work_dir/repositories.yaml}" \
    XDG_CACHE_HOME="${XDG_CACHE_HOME:-$work_dir/xdg-cache}" \
    helm dependency build "$chart_dir" >/dev/null ||
    fail "helm dependency build failed for OpenBao"
}

render_openbao_chart() {
  local owner="$1"

  rm -rf "$manifest_dir"
  mkdir -p "$manifest_dir"
  helm template openbao-server "$chart_dir" \
    --namespace "$owner-vault-system" \
    --values "$values_file" \
    >"$manifest_file"
  test -s "$manifest_file" ||
    fail "helm template wrote no OpenBao manifests for $owner"
}

assert_value() {
  local file="$1"
  local expression="$2"
  local expected="$3"
  local actual

  actual="$(yq -r "$expression" "$file")"
  test "$actual" = "$expected" ||
    fail "expected $expression in $(basename "$file") to be $expected, got $actual"
}

assert_contains() {
  local file="$1"
  local expression="$2"
  local expected="$3"
  local actual

  actual="$(yq -r "$expression" "$file")"
  grep -Fq -- "$expected" <<<"$actual" ||
    fail "expected $expression in $(basename "$file") to contain $expected"
}

assert_render_value() {
  local expression="$1"
  local expected="$2"
  local actual

  actual="$(yq -r "$expression" "$manifest_file")"
  test "$actual" = "$expected" ||
    fail "expected rendered $expression to be $expected, got $actual"
}

check_named_owner() {
  local owner="$1"
  local openbao_name="$owner-openbao"
  local namespace="$owner-vault-system"
  local injector_name="$openbao_name-agent-injector"
  local injector_service="$injector_name-svc"
  local webhook_config="$injector_name-cfg"
  local cluster_role="$injector_name-clusterrole"
  local cluster_role_binding="$injector_name-binding"
  local initialize_service_account="$openbao_name-initialize-cluster"
  local unseal_secret="$openbao_name-unseal"
  local openbao_host="$openbao_name.$namespace.svc.cluster.local"
  local openbao_url="http://$openbao_host:8200"

  render_openbao_values "$owner" "$values_file"

  assert_value "$values_file" '.openbao.fullnameOverride' "$openbao_name"
  assert_value "$values_file" '.openbao.migrations.env[] | select(.name == "BAO_SERVICE") | .value' "$openbao_host"
  assert_value "$values_file" '.openbao.migrations.env[] | select(.name == "OPENBAO_SERVER_INTERNAL_URL") | .value' "$openbao_url"
  assert_value "$values_file" '.openbao.server.volumes[0].name' "$unseal_secret"
  assert_value "$values_file" '.openbao.server.volumes[0].secret.secretName' "$unseal_secret"
  assert_value "$values_file" '.openbao.server.volumeMounts[0].name' "$unseal_secret"
  assert_value "$values_file" '.openbao.server.extraContainers[] | select(.name == "auto-unseal-sidecar") | .volumeMounts[0].name' "$unseal_secret"
  assert_contains "$values_file" '.openbao.server.ha.raft.config' "leader_api_addr = \"http://$openbao_name-0.$openbao_name-internal:8200\""
  assert_contains "$values_file" '.openbao.server.ha.raft.config' "leader_api_addr = \"http://$openbao_name-2.$openbao_name-internal:8200\""

  render_openbao_chart "$owner"

  assert_render_value 'select(.kind == "MutatingWebhookConfiguration") | .metadata.name' "$webhook_config"
  assert_render_value 'select(.kind == "MutatingWebhookConfiguration") | .webhooks[0].clientConfig.service.name' "$injector_service"
  assert_render_value 'select(.kind == "MutatingWebhookConfiguration") | .webhooks[0].clientConfig.service.namespace' "$namespace"
  assert_render_value 'select(.kind == "ClusterRole" and .metadata.name == "'"$cluster_role"'") | .metadata.name' "$cluster_role"
  assert_render_value 'select(.kind == "ClusterRoleBinding" and .metadata.name == "'"$cluster_role_binding"'") | .roleRef.name' "$cluster_role"
  assert_render_value 'select(.kind == "ClusterRoleBinding" and .metadata.name == "'"$cluster_role_binding"'") | .subjects[0].name' "$injector_name"
  assert_render_value 'select(.kind == "ClusterRoleBinding" and .metadata.name == "'"$cluster_role_binding"'") | .subjects[0].namespace' "$namespace"
  assert_render_value 'select(.kind == "ServiceAccount" and .metadata.name == "'"$injector_name"'") | .metadata.namespace' "$namespace"
  assert_render_value 'select(.kind == "ServiceAccount" and .metadata.name == "'"$initialize_service_account"'") | .metadata.namespace' "$namespace"
  assert_render_value 'select(.kind == "RoleBinding" and .metadata.name == "'"$initialize_service_account"'") | .subjects[0].name' "$initialize_service_account"
  assert_render_value 'select(.kind == "Service" and .metadata.name == "'"$injector_service"'") | .metadata.namespace' "$namespace"
  assert_render_value 'select(.kind == "Deployment" and .metadata.name == "'"$injector_name"'") | .spec.template.spec.serviceAccountName' "$injector_name"
  assert_render_value 'select(.kind == "Deployment" and .metadata.name == "'"$injector_name"'") | .spec.template.spec.containers[0].env[] | select(.name == "AGENT_INJECT_TLS_AUTO") | .value' "$webhook_config"
  assert_render_value 'select(.kind == "Deployment" and .metadata.name == "'"$injector_name"'") | .spec.template.spec.containers[0].env[] | select(.name == "AGENT_INJECT_TLS_AUTO_HOSTS") | .value' "$injector_service,$injector_service.$namespace,$injector_service.$namespace.svc"
  assert_render_value 'select(.kind == "Deployment" and .metadata.name == "'"$injector_name"'") | .spec.template.spec.containers[0].env[] | select(.name == "AGENT_INJECT_VAULT_ADDR") | .value' "http://$openbao_name.$namespace.svc:8200"
  assert_render_value 'select(.kind == "StatefulSet" and .metadata.name == "'"$openbao_name"'") | .spec.serviceName' "$openbao_name-internal"
  assert_render_value 'select(.kind == "StatefulSet" and .metadata.name == "'"$openbao_name"'") | .spec.template.spec.volumes[] | select(.name == "'"$unseal_secret"'") | .secret.secretName' "$unseal_secret"
  assert_render_value 'select(.kind == "Secret" and .metadata.name == "'"$unseal_secret"'") | .metadata.namespace' "$namespace"
  assert_contains "$manifest_file" 'select(.kind == "ConfigMap" and .metadata.name == "'"$openbao_name"'-config") | .data."extraconfig-from-values.hcl"' "leader_api_addr = \"http://$openbao_name-0.$openbao_name-internal:8200\""

  python3 "$repo_root/deploy/stacks/tests/verify-named-control-plane-render-isolation.py" \
    --render "$owner=$manifest_dir" >/dev/null
}

build_openbao_dependencies
check_named_owner plane-a
check_named_owner aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa

echo "openbao-injector-named-isolation: all checks passed"
