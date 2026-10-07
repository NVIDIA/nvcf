#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Render checks for the pylon-operator chart. Every case runs helm template and
# asserts on the output with yq and jq.
#
# Usage: scripts/check-render.sh [chart dir]

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/../../../.." && pwd)"
chart_dir="${1:-${script_dir}/../pylon-operator}"
ci_values="${CI_VALUES:-${repo_root}/tools/ci/helm-validate-values/pylon-operator.yaml}"
release="${RELEASE:-pylon-operator}"
namespace="${NAMESPACE:-pylon-operator}"
tmp_dir="$(mktemp -d)"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

for tool in helm yq jq; do
  command -v "${tool}" >/dev/null 2>&1 || fail "${tool} not found on PATH"
done

app_version="$(yq -r '.appVersion' "${chart_dir}/Chart.yaml")"

# render <output> [helm args...]: render with the CI values plus overrides.
render() {
  local output="$1"
  shift
  helm template "${release}" "${chart_dir}" \
    --namespace "${namespace}" \
    --values "${ci_values}" \
    "$@" \
    > "${output}"
}

# assert_render_fails <expected error> [helm args...]: render with the chart
# defaults only, plus overrides, and expect the error.
assert_render_fails() {
  local expected_error="$1"
  shift
  local error_file="${tmp_dir}/render-error"
  if helm template "${release}" "${chart_dir}" \
    --namespace "${namespace}" \
    "$@" \
    > /dev/null 2> "${error_file}"; then
    fail "expected render failure: ${expected_error}"
  fi
  grep -Fq -- "${expected_error}" "${error_file}" ||
    fail "render did not return expected error: ${expected_error}; got: $(cat "${error_file}")"
}

count_kind() {  # count_kind <manifest> <kind> [name]
  local filter="select(.kind == \"$2\")"
  if [ -n "${3:-}" ]; then
    filter="select(.kind == \"$2\" and .metadata.name == \"$3\")"
  fi
  yq ea "[${filter}] | length" "$1"
}

deployment_field() {  # deployment_field <manifest> <expression>
  yq -r "select(.kind == \"Deployment\" and .metadata.name == \"${release}\") | $2" "$1"
}

args_of() {
  deployment_field "$1" '.spec.template.spec.containers[0].args[]'
}

has_arg() {
  printf '%s\n' "$1" | grep -qx -- "$2"
}

has_arg_prefix() {
  printf '%s\n' "$1" | grep -q -- "^$2"
}

# Default values: every required value is reported at once.
assert_render_fails "set the required values: image.repository, clusterId, router.grpcAddress, pylon.image.repository, pylon.image.tag"
assert_render_fails "set the required values: clusterId" \
  --set image.repository=example.com/pylon-operator \
  --set router.grpcAddress=router:50071 \
  --set pylon.image.repository=example.com/pylon \
  --set pylon.image.tag=1.0.0
assert_render_fails "set the required values: router.grpcAddress" \
  --values "${ci_values}" \
  --set-string router.grpcAddress=
assert_render_fails "set the required values: pylon.image.repository" \
  --values "${ci_values}" \
  --set-string pylon.image.repository=
assert_render_fails "'/clusterId': 'Spark_01' does not match pattern" \
  --values "${ci_values}" \
  --set clusterId=Spark_01
assert_render_fails "'/watchNamespaces/0': 'Team_A' does not match pattern" \
  --values "${ci_values}" \
  --set 'watchNamespaces={Team_A}'
assert_render_fails "'/transport/replicas': minimum" \
  --values "${ci_values}" \
  --set transport.replicas=0
assert_render_fails "'/transport/initialInputTPS'" \
  --values "${ci_values}" \
  --set transport.initialInputTPS=0
assert_render_fails "credential.key must be cluster-token" \
  --values "${ci_values}" \
  --set credential.key=token
assert_render_fails "'/trustBundle/configMap': 'Router_CA' does not match pattern" \
  --values "${ci_values}" \
  --set trustBundle.configMap=Router_CA

# CI values render.
default_manifest="${tmp_dir}/default.yaml"
render "${default_manifest}"
default_args="$(args_of "${default_manifest}")"

ci_cluster_id="$(yq -r '.clusterId' "${ci_values}")"
ci_router="$(yq -r '.router.grpcAddress' "${ci_values}")"
ci_pylon_image="$(yq -r '.pylon.image.repository + ":" + .pylon.image.tag' "${ci_values}")"
ci_image_repo="$(yq -r '.image.repository' "${ci_values}")"
credential_secret="${release}-cluster-credential"

has_arg "${default_args}" "--cluster-id=${ci_cluster_id}" || fail "missing --cluster-id"
has_arg "${default_args}" "--router-grpc-address=${ci_router}" || fail "missing --router-grpc-address"
has_arg "${default_args}" "--pylon-image=${ci_pylon_image}" || fail "missing --pylon-image"
has_arg "${default_args}" "--pylon-image-pull-policy=IfNotPresent" || fail "missing --pylon-image-pull-policy=IfNotPresent"
has_arg "${default_args}" "--transport-replicas=1" || fail "missing --transport-replicas=1"
has_arg "${default_args}" "--initial-input-tps=100" || fail "missing --initial-input-tps=100"
! has_arg_prefix "${default_args}" "--trust-bundle-configmap" || fail "default must not mount a trust bundle"
has_arg "${default_args}" "--probe-interval=10s" || fail "missing --probe-interval=10s"
has_arg "${default_args}" "--scrape-interval=5s" || fail "missing --scrape-interval=5s"
has_arg "${default_args}" "--cluster-credential-secret=${credential_secret}" || fail "missing --cluster-credential-secret"
has_arg "${default_args}" "--metrics-bind-address=:8080" || fail "missing --metrics-bind-address"
has_arg "${default_args}" "--health-probe-bind-address=:8081" || fail "missing --health-probe-bind-address"
has_arg "${default_args}" "--leader-elect" || fail "leader election must be on by default"
! has_arg_prefix "${default_args}" "--watch-namespaces" || fail "default must watch all namespaces"
! has_arg "${default_args}" "--dev-insecure-transport" || fail "default must not disable transport TLS verification"

[ "$(deployment_field "${default_manifest}" '.spec.replicas')" = "1" ] || fail "operator must run one replica"
[ "$(deployment_field "${default_manifest}" '.spec.strategy.type')" = "RollingUpdate" ] || fail "leader election should allow RollingUpdate"
[ "$(deployment_field "${default_manifest}" '.spec.template.spec.containers[0].image')" = "${ci_image_repo}:${app_version}" ] ||
  fail "operator image tag must default to the chart appVersion"
[ "$(deployment_field "${default_manifest}" '.spec.template.spec.containers[0].env[] | select(.name == "POD_NAMESPACE") | .valueFrom.fieldRef.fieldPath')" = "metadata.namespace" ] ||
  fail "POD_NAMESPACE must come from the downward API"
[ "$(deployment_field "${default_manifest}" '.spec.template.spec.containers[0].livenessProbe.httpGet | .path + " " + .port')" = "/healthz health" ] ||
  fail "liveness probe must hit /healthz on the health port"
[ "$(deployment_field "${default_manifest}" '.spec.template.spec.containers[0].readinessProbe.httpGet | .path + " " + .port')" = "/readyz health" ] ||
  fail "readiness probe must hit /readyz on the health port"
[ "$(deployment_field "${default_manifest}" '.spec.template.spec.containers[0].ports[] | select(.name == "health") | .containerPort')" = "8081" ] ||
  fail "health port must be 8081"
[ "$(deployment_field "${default_manifest}" '.spec.template.spec.automountServiceAccountToken')" = "true" ] ||
  fail "operator needs its ServiceAccount token"

security_context="$(yq -o=json -I=0 "select(.kind == \"Deployment\") | .spec.template.spec.containers[0].securityContext" "${default_manifest}")"
[ "$(printf '%s' "${security_context}" | jq -r '.runAsNonRoot')" = "true" ] || fail "container must run as non-root"
[ "$(printf '%s' "${security_context}" | jq -r '.readOnlyRootFilesystem')" = "true" ] || fail "root filesystem must be read-only"
[ "$(printf '%s' "${security_context}" | jq -r '.allowPrivilegeEscalation')" = "false" ] || fail "privilege escalation must be off"
[ "$(printf '%s' "${security_context}" | jq -c '.capabilities.drop')" = '["ALL"]' ] || fail "all capabilities must be dropped"
[ "$(deployment_field "${default_manifest}" '.spec.template.spec.securityContext.seccompProfile.type')" = "RuntimeDefault" ] ||
  fail "pod must use the RuntimeDefault seccomp profile"

# Generated credential Secret, named exactly as --cluster-credential-secret.
[ "$(count_kind "${default_manifest}" Secret "${credential_secret}")" = "1" ] || fail "generate=true must render ${credential_secret}"
token_b64="$(yq -r "select(.kind == \"Secret\") | .data[\"cluster-token\"]" "${default_manifest}")"
[ "$(printf '%s' "${token_b64}" | base64 -d | tr -d '\n' | wc -c | tr -d ' ')" = "48" ] || fail "generated token must be 48 characters"
[ "$(yq -r "select(.kind == \"Secret\") | .metadata.annotations[\"helm.sh/resource-policy\"]" "${default_manifest}")" = "keep" ] ||
  fail "generated credential must survive uninstall"

# Existing Secret: no Secret, and the operator points at it.
existing_manifest="${tmp_dir}/existing.yaml"
render "${existing_manifest}" --set credential.existingSecret=my-cluster-token
existing_args="$(args_of "${existing_manifest}")"
[ "$(count_kind "${existing_manifest}" Secret)" = "0" ] || fail "existingSecret must not render a Secret"
has_arg "${existing_args}" "--cluster-credential-secret=my-cluster-token" || fail "existingSecret must reach --cluster-credential-secret"

# generate=false without existingSecret: no Secret, operator uses the default name.
no_generate_manifest="${tmp_dir}/no-generate.yaml"
render "${no_generate_manifest}" --set credential.generate=false
[ "$(count_kind "${no_generate_manifest}" Secret)" = "0" ] || fail "generate=false must not render a Secret"
has_arg "$(args_of "${no_generate_manifest}")" "--cluster-credential-secret=${credential_secret}" ||
  fail "generate=false must keep the default Secret name"

# CRD gated by installCRDs, with chart labels and the keep policy.
crd_name="inferenceendpoints.pylon.nvidia.com"
[ "$(count_kind "${default_manifest}" CustomResourceDefinition "${crd_name}")" = "1" ] || fail "installCRDs=true must render the CRD"
[ "$(yq -r "select(.kind == \"CustomResourceDefinition\") | .metadata.labels[\"app.kubernetes.io/instance\"]" "${default_manifest}")" = "${release}" ] ||
  fail "CRD must carry the chart labels"
[ "$(yq -r "select(.kind == \"CustomResourceDefinition\") | .metadata.annotations[\"helm.sh/resource-policy\"]" "${default_manifest}")" = "keep" ] ||
  fail "CRD must carry helm.sh/resource-policy: keep"
no_crd_manifest="${tmp_dir}/no-crd.yaml"
render "${no_crd_manifest}" --set installCRDs=false
[ "$(count_kind "${no_crd_manifest}" CustomResourceDefinition)" = "0" ] || fail "installCRDs=false must not render the CRD"

# Offline auto mode has no cluster CRD and must prepare the first install.
auto_crd_manifest="${tmp_dir}/auto-crd.yaml"
render "${auto_crd_manifest}" --set installCRDs=auto
[ "$(count_kind "${auto_crd_manifest}" CustomResourceDefinition "${crd_name}")" = "1" ] ||
  fail "installCRDs=auto must render the CRD when it is absent"
assert_render_fails "installCRDs" --values "${ci_values}" --set installCRDs=invalid

# RBAC: ClusterRole from config/rbac/role.yaml, bound cluster-wide; leader
# election Role in the release namespace.
[ "$(count_kind "${default_manifest}" ClusterRole "${release}")" = "1" ] || fail "missing ClusterRole"
[ "$(yq -r "select(.kind == \"ClusterRoleBinding\") | .subjects[0].namespace" "${default_manifest}")" = "${namespace}" ] ||
  fail "ClusterRoleBinding must bind the release namespace ServiceAccount"
lease_verbs="$(yq -o=json -I=0 "select(.kind == \"Role\") | .rules[] | select(.apiGroups[0] == \"coordination.k8s.io\") | .verbs" "${default_manifest}")"
[ "${lease_verbs}" = '["get","list","watch","create","update","patch","delete"]' ] || fail "leader election Role has lease verbs ${lease_verbs}"
[ "$(yq -r "select(.kind == \"Role\") | .metadata.namespace" "${default_manifest}")" = "${namespace}" ] ||
  fail "leader election Role must live in the release namespace"
[ "$(yq -r "select(.kind == \"Service\") | .spec.ports[0].targetPort" "${default_manifest}")" = "metrics" ] ||
  fail "metrics Service must target the metrics port"

# trustBundle.configMap reaches --trust-bundle-configmap.
trust_manifest="${tmp_dir}/trust.yaml"
render "${trust_manifest}" --set trustBundle.configMap=router-ca
has_arg "$(args_of "${trust_manifest}")" "--trust-bundle-configmap=router-ca" || fail "trustBundle.configMap must reach --trust-bundle-configmap"

# watchNamespaces renders one comma-separated flag.
watch_manifest="${tmp_dir}/watch.yaml"
render "${watch_manifest}" --set 'watchNamespaces={team-b,team-a}'
has_arg "$(args_of "${watch_manifest}")" "--watch-namespaces=team-b,team-a" || fail "watchNamespaces must render a comma list"

# devInsecureTransport adds the flag.
insecure_manifest="${tmp_dir}/insecure.yaml"
render "${insecure_manifest}" --set devInsecureTransport=true
has_arg "$(args_of "${insecure_manifest}")" "--dev-insecure-transport" || fail "devInsecureTransport=true must add --dev-insecure-transport"

# Leader election off: no flag and no overlapping rollout.
no_le_manifest="${tmp_dir}/no-leader-election.yaml"
render "${no_le_manifest}" --set leaderElection.enabled=false
! has_arg "$(args_of "${no_le_manifest}")" "--leader-elect" || fail "leaderElection.enabled=false must drop --leader-elect"
[ "$(deployment_field "${no_le_manifest}" '.spec.strategy.type')" = "Recreate" ] || fail "without leader election the rollout must be Recreate"

# A numeric string from --set is accepted for the float flag.
tps_string_manifest="${tmp_dir}/tps-string.yaml"
render "${tps_string_manifest}" --set transport.initialInputTPS=12.5
has_arg "$(args_of "${tps_string_manifest}")" "--initial-input-tps=12.5" || fail "--set transport.initialInputTPS=12.5 must render"

# Other flags follow their values; digests use @.
tuned_manifest="${tmp_dir}/tuned.yaml"
render "${tuned_manifest}" \
  --set transport.replicas=2 \
  --set-json transport.initialInputTPS=2.5 \
  --set probeInterval=30s \
  --set scrapeInterval=15s \
  --set metrics.port=9090 \
  --set healthProbe.port=9091 \
  --set-string pylon.image.tag=sha256:0123456789abcdef \
  --set pylon.image.pullPolicy=Always \
  --set-string image.tag=1.2.3 \
  --set 'extraArgs={--zap-devel}'
tuned_args="$(args_of "${tuned_manifest}")"
has_arg "${tuned_args}" "--transport-replicas=2" || fail "transport.replicas must reach --transport-replicas"
has_arg "${tuned_args}" "--initial-input-tps=2.5" || fail "transport.initialInputTPS must reach --initial-input-tps"
has_arg "${tuned_args}" "--probe-interval=30s" || fail "probeInterval must reach --probe-interval"
has_arg "${tuned_args}" "--scrape-interval=15s" || fail "scrapeInterval must reach --scrape-interval"
has_arg "${tuned_args}" "--metrics-bind-address=:9090" || fail "metrics.port must reach --metrics-bind-address"
has_arg "${tuned_args}" "--health-probe-bind-address=:9091" || fail "healthProbe.port must reach --health-probe-bind-address"
has_arg "${tuned_args}" "--pylon-image=$(yq -r '.pylon.image.repository' "${ci_values}")@sha256:0123456789abcdef" ||
  fail "a sha256 pylon tag must render as a digest"
has_arg "${tuned_args}" "--pylon-image-pull-policy=Always" || fail "pylon.image.pullPolicy must reach --pylon-image-pull-policy"
has_arg "${tuned_args}" "--zap-devel" || fail "extraArgs must be appended"
[ "$(deployment_field "${tuned_manifest}" '.spec.template.spec.containers[0].image')" = "${ci_image_repo}:1.2.3" ] ||
  fail "image.tag must override the appVersion"
[ "$(yq -r "select(.kind == \"Service\") | .spec.ports[0].port" "${tuned_manifest}")" = "9090" ] ||
  fail "metrics.port must reach the Service"

# A release name without the chart name prefixes the resources, and the
# credential Secret follows.
other_manifest="${tmp_dir}/other-release.yaml"
helm template spark "${chart_dir}" --namespace "${namespace}" --values "${ci_values}" > "${other_manifest}"
[ "$(count_kind "${other_manifest}" Secret spark-pylon-operator-cluster-credential)" = "1" ] ||
  fail "credential Secret must follow the release full name"
has_arg "$(yq -r 'select(.kind == "Deployment") | .spec.template.spec.containers[0].args[]' "${other_manifest}")" \
  "--cluster-credential-secret=spark-pylon-operator-cluster-credential" ||
  fail "--cluster-credential-secret must match the generated Secret name"

# NOTES.txt: helm template does not render it, and helm install --dry-run
# consults the cluster. Render it offline from a copy of the chart that wraps
# the notes in a template.
notes_chart="${tmp_dir}/notes-chart"
cp -R "${chart_dir}" "${notes_chart}"
mkdir -p "${notes_chart}/files"
mv "${notes_chart}/templates/NOTES.txt" "${notes_chart}/files/notes.txt"
printf 'notes: |\n{{ tpl (.Files.Get "files/notes.txt") . | indent 2 }}\n' > "${notes_chart}/templates/zz-notes.yaml"
notes="$(helm template "${release}" "${notes_chart}" --namespace "${namespace}" --values "${ci_values}" \
  --show-only templates/zz-notes.yaml | yq -r '.notes')"
cluster_id="$(yq -r '.clusterId' "${ci_values}")"
printf '%s\n' "${notes}" | grep -qF 'TOKEN_HASH="sha256:$(kubectl' ||
  fail "NOTES must print the router hash entry as sha256:<hex>"
printf '%s\n' "${notes}" | grep -qF "printf 'clusters:\\n  ${cluster_id}:\\n    - %s\\n' \"\${TOKEN_HASH}\" > credentials.yaml" ||
  fail "NOTES must show the YAML worker auth file for clusterId"
! printf '%s\n' "${notes}" | grep -qF 'credentials.json' || fail "NOTES must not describe the JSON credentials file"

echo "pylon-operator render checks passed"
