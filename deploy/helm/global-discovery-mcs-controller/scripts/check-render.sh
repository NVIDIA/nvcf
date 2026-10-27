#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Render checks for the global-discovery-mcs-controller chart. Every case runs
# helm template and asserts on the output with yq and jq. The expected flags
# and RBAC rules are read from the controller module, not copied here.
#
# Usage: scripts/check-render.sh [chart dir]

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/../../../.." && pwd)"
chart_dir="${1:-${script_dir}/../global-discovery-mcs-controller}"
module_dir="${repo_root}/src/control-plane-services/global-discovery"
ci_values="${CI_VALUES:-${repo_root}/tools/ci/helm-validate-values/global-discovery-mcs-controller.yaml}"
release="${RELEASE:-global-discovery-mcs-controller}"
namespace="${NAMESPACE:-gds-system}"
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
ci_image_repo="$(yq -r '.image.repository' "${ci_values}")"
ci_url="$(yq -r '.gds.urls[0]' "${ci_values}")"
ci_clusterset="$(yq -r '.clusterset.id' "${ci_values}")"
ci_cluster="$(yq -r '.cluster.id' "${ci_values}")"

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
  yq ea -r "select(.kind == \"Deployment\") | $2" "$1"
}

container_field() {  # container_field <manifest> <expression below the container>
  deployment_field "$1" ".spec.template.spec.containers[0] | $2"
}

env_value() {  # env_value <manifest> <name>
  container_field "$1" ".env[] | select(.name == \"$2\") | .value"
}

kind_field() {  # kind_field <manifest> <kind> <name> <expression>
  yq ea -r "select(.kind == \"$2\" and .metadata.name == \"$3\") | $4" "$1"
}

args_of() {
  container_field "$1" '.args[]'
}

has_arg() {
  printf '%s\n' "$1" | grep -qx -- "$2"
}

has_arg_prefix() {
  printf '%s\n' "$1" | grep -q -- "^$2"
}

# rule_verbs <manifest> <resource>: the sorted verbs the ClusterRole grants on
# a resource, comma-separated.
rule_verbs() {
  yq ea -o=json -I=0 "select(.kind == \"ClusterRole\") | .rules" "$1" |
    jq -r --arg r "$2" '[.[] | select(.resources | index($r)) | .verbs[]] | unique | join(",")'
}

# rule_triples <rules JSON>: one "group|resource|verb" line per grant, sorted.
rule_triples() {
  jq -r '.[] | . as $r | $r.apiGroups[] as $g | $r.resources[] as $res | $r.verbs[] as $v | "\($g)|\($res)|\($v)"' <<<"$1" | sort -u
}

# ---------------------------------------------------------------------------
# Required values, schema and cross-field rules.

assert_render_fails "global-discovery-mcs-controller: set the required values: image.repository, gds.urls, clusterset.id, cluster.id"
assert_render_fails "global-discovery-mcs-controller: set the required values: clusterset.id, cluster.id" \
  --set image.repository=example.com/gds-mcs-controller \
  --set 'gds.urls={https://gds.example.internal}'
assert_render_fails "global-discovery-mcs-controller: set the required values: gds.urls" \
  --values "${ci_values}" \
  --set 'gds.urls=null'
assert_render_fails "global-discovery-mcs-controller: set the required values: cluster.id" \
  --values "${ci_values}" \
  --set-string cluster.id=
assert_render_fails "additional properties 'clusterId' not allowed" \
  --values "${ci_values}" \
  --set clusterId=a
assert_render_fails "additional properties 'createNamespace' not allowed" \
  --values "${ci_values}" \
  --set imports.createNamespace=true
assert_render_fails "'/cluster/id': 'Cluster_A' does not match pattern" \
  --values "${ci_values}" \
  --set cluster.id=Cluster_A
assert_render_fails "'/identity/source'" \
  --values "${ci_values}" \
  --set identity.source=env
assert_render_fails "'/lease/ttl': '30 seconds' does not match pattern" \
  --values "${ci_values}" \
  --set 'lease.ttl=30 seconds'
assert_render_fails "'/gds/urls/0'" \
  --values "${ci_values}" \
  --set 'gds.urls={gds.example.internal:443}'
assert_render_fails "'/logLevel'" \
  --values "${ci_values}" \
  --set logLevel=warn
assert_render_fails "'/endpointsPerSlice'" \
  --values "${ci_values}" \
  --set endpointsPerSlice=1001
assert_render_fails "gds.urls uses http://; set gds.allowInsecureHttp=true" \
  --values "${ci_values}" \
  --set 'gds.urls={http://gds.example.internal:9090}'
assert_render_fails "gds.urls must all use one scheme" \
  --values "${ci_values}" \
  --set gds.allowInsecureHttp=true \
  --set 'gds.urls={https://gds.example.internal,http://gds-fallback.example.internal:9090}'
assert_render_fails "replicaCount above 1 needs leaderElection.enabled" \
  --values "${ci_values}" \
  --set leaderElection.enabled=false
assert_render_fails "metrics.port and healthProbe.port must differ" \
  --values "${ci_values}" \
  --set healthProbe.port=8080
assert_render_fails "podDisruptionBudget: set exactly one of minAvailable or maxUnavailable" \
  --values "${ci_values}" \
  --set podDisruptionBudget.maxUnavailable=1

# ---------------------------------------------------------------------------
# Default render (CI values): flags, leader election, probes, security.

ci_manifest="${tmp_dir}/ci.yaml"
render "${ci_manifest}"
ci_args="$(args_of "${ci_manifest}")"

# Every rendered flag is a real controller flag: the flags internal/mcs/config
# binds, plus controller-runtime's --zap-* logging flags.
known_flags="$(grep -o 'fs\.[A-Za-z]*Var([^,]*, "[a-z-]*"' "${module_dir}/internal/mcs/config/config.go" | sed 's/.*"\([a-z-]*\)"/\1/')"
[ -n "${known_flags}" ] || fail "cannot read the controller flags from internal/mcs/config/config.go"
while IFS= read -r arg; do
  name="${arg%%=*}"
  name="${name#--}"
  case "${name}" in
    zap-log-level) continue ;;
  esac
  printf '%s\n' "${known_flags}" | grep -qx -- "${name}" || fail "the chart renders --${name}, which gds-mcs-controller does not define"
done <<<"${ci_args}"

for expected in \
  "--gds-url=${ci_url}" \
  "--gds-allow-insecure-http=false" \
  "--cluster-identity-source=flags" \
  "--clusterset-id=${ci_clusterset}" \
  "--cluster-id=${ci_cluster}" \
  "--write-cluster-properties=false" \
  "--lease-ttl=30s" \
  "--deregister-on-shutdown=false" \
  "--create-namespaces=false" \
  "--adopt-unmanaged-imports=false" \
  "--auto-export-pools=true" \
  "--endpoints-per-slice=100" \
  "--max-concurrent-reconciles=4" \
  "--metrics-bind-address=:8080" \
  "--health-probe-bind-address=:8081" \
  "--leader-elect=true" \
  "--otel-enabled=false" \
  "--zap-log-level=info"; do
  has_arg "${ci_args}" "${expected}" || fail "default render is missing ${expected}"
done
for absent in --gds-tls-ca-file= --region= --cluster-labels= --watch-namespaces= --operator-namespace=; do
  ! has_arg_prefix "${ci_args}" "${absent}" || fail "default render must not set ${absent}"
done

[ "$(container_field "${ci_manifest}" '.image')" = "${ci_image_repo}:${app_version}" ] ||
  fail "image tag must default to the chart appVersion"
[ "$(deployment_field "${ci_manifest}" '.spec.replicas')" = "2" ] || fail "the chart runs two replicas by default"
[ "$(yq -r '.replicaCount' "${chart_dir}/values.yaml")" = "2" ] || fail "values.yaml must default replicaCount to 2"
[ "$(deployment_field "${ci_manifest}" '.spec.strategy.type')" = "RollingUpdate" ] || fail "with leader election the rollout is RollingUpdate"
[ "$(deployment_field "${ci_manifest}" '.spec.strategy.rollingUpdate.maxUnavailable')" = "0" ] || fail "the rollout must keep every replica"
[ "$(container_field "${ci_manifest}" '.env[] | select(.name == "POD_NAMESPACE") | .valueFrom.fieldRef.fieldPath')" = "metadata.namespace" ] ||
  fail "POD_NAMESPACE (the leader election namespace) must come from the downward API"
[ "$(container_field "${ci_manifest}" '.readinessProbe.httpGet | .path + " " + .port')" = "/readyz health" ] ||
  fail "readiness probe must hit /readyz on the health port"
[ "$(container_field "${ci_manifest}" '.livenessProbe.httpGet | .path + " " + .port')" = "/healthz health" ] ||
  fail "liveness probe must hit /healthz on the health port"
[ "$(container_field "${ci_manifest}" '[.ports[] | .name + "=" + (.containerPort | tostring)] | join(",")')" = "metrics=8080,health=8081" ] ||
  fail "container ports must be metrics 8080 and health 8081"
[ "$(deployment_field "${ci_manifest}" '.spec.template.spec.automountServiceAccountToken')" = "true" ] ||
  fail "the controller needs its Kubernetes API token"
[ "$(kind_field "${ci_manifest}" ServiceAccount "${release}" '.automountServiceAccountToken')" = "true" ] ||
  fail "the ServiceAccount must allow the token mount"
[ "$(deployment_field "${ci_manifest}" '.spec.template.spec.serviceAccountName')" = "${release}" ] ||
  fail "the pods must use the release ServiceAccount"
[ "$(deployment_field "${ci_manifest}" '.spec.template.spec.affinity.podAntiAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].podAffinityTerm.topologyKey')" = "kubernetes.io/hostname" ] ||
  fail "replicas must prefer separate nodes by default"

security_context="$(yq -o=json -I=0 'select(.kind == "Deployment") | .spec.template.spec.containers[0].securityContext' "${ci_manifest}")"
[ "$(jq -r '.runAsNonRoot' <<<"${security_context}")" = "true" ] || fail "container must run as non-root"
[ "$(jq -r '.readOnlyRootFilesystem' <<<"${security_context}")" = "true" ] || fail "root filesystem must be read-only"
[ "$(jq -r '.allowPrivilegeEscalation' <<<"${security_context}")" = "false" ] || fail "privilege escalation must be off"
[ "$(jq -c '.capabilities.drop' <<<"${security_context}")" = '["ALL"]' ] || fail "all capabilities must be dropped"

# Leader election: a Role for Leases and Events in the release namespace,
# bound to the controller's ServiceAccount.
le_name="${release}-leader-election"
[ "$(kind_field "${ci_manifest}" Role "${le_name}" '.metadata.namespace')" = "${namespace}" ] ||
  fail "the leader election Role must live in the release namespace"
[ "$(kind_field "${ci_manifest}" Role "${le_name}" '.rules[] | select(.resources[0] == "leases") | .apiGroups[0] + ":" + (.verbs | sort | join(","))')" = \
  "coordination.k8s.io:create,delete,get,list,patch,update,watch" ] || fail "the leader election Role must manage Leases"
[ "$(kind_field "${ci_manifest}" RoleBinding "${le_name}" '.subjects[0].name + "/" + .subjects[0].namespace + " -> " + .roleRef.name')" = \
  "${release}/${namespace} -> ${le_name}" ] || fail "the leader election RoleBinding must bind the ServiceAccount to the Role"
single_manifest="${tmp_dir}/single.yaml"
render "${single_manifest}" --set leaderElection.enabled=false --set replicaCount=1
single_args="$(args_of "${single_manifest}")"
has_arg "${single_args}" "--leader-elect=false" || fail "leaderElection.enabled=false must pass --leader-elect=false (the controller default is true)"
[ "$(deployment_field "${single_manifest}" '.spec.strategy.type')" = "Recreate" ] || fail "without leader election the rollout must be Recreate"
[ "$(count_kind "${single_manifest}" Role)" = "0" ] || fail "without leader election no Role is needed"
[ "$(count_kind "${single_manifest}" PodDisruptionBudget)" = "0" ] || fail "one replica must not get a PodDisruptionBudget"

# PodDisruptionBudget with more than one replica.
[ "$(kind_field "${ci_manifest}" PodDisruptionBudget "${release}" '.spec.minAvailable')" = "1" ] || fail "the PDB must keep one replica"
[ "$(kind_field "${ci_manifest}" PodDisruptionBudget "${release}" '.spec.selector.matchLabels["app.kubernetes.io/instance"]')" = "${release}" ] ||
  fail "the PDB must select the release's pods"
no_pdb_manifest="${tmp_dir}/no-pdb.yaml"
render "${no_pdb_manifest}" --set podDisruptionBudget.enabled=false
[ "$(count_kind "${no_pdb_manifest}" PodDisruptionBudget)" = "0" ] || fail "podDisruptionBudget.enabled=false must drop the PDB"

# Metrics Service; ServiceMonitor off by default.
[ "$(kind_field "${ci_manifest}" Service "${release}-metrics" '.spec.ports[0].name + "=" + (.spec.ports[0].port | tostring)')" = "metrics=8080" ] ||
  fail "the metrics Service must expose the metrics port"
[ "$(count_kind "${ci_manifest}" ServiceMonitor)" = "0" ] || fail "the ServiceMonitor must be off by default"
sm_manifest="${tmp_dir}/servicemonitor.yaml"
render "${sm_manifest}" --set metrics.serviceMonitor.enabled=true --set metrics.serviceMonitor.labels.release=prometheus
[ "$(kind_field "${sm_manifest}" ServiceMonitor "${release}" '.spec.endpoints[0].port + " " + .spec.endpoints[0].path')" = "metrics /metrics" ] ||
  fail "the ServiceMonitor must scrape /metrics on the metrics port"
[ "$(kind_field "${sm_manifest}" ServiceMonitor "${release}" '.metadata.labels.release')" = "prometheus" ] ||
  fail "metrics.serviceMonitor.labels must reach the ServiceMonitor"

# ---------------------------------------------------------------------------
# RBAC: config/rbac/role.yaml trimmed by the values.

role_yaml="${module_dir}/config/rbac/role.yaml"
[ "$(rule_verbs "${ci_manifest}" namespaces)" = "get,list,watch" ] || fail "namespaces create must be dropped without imports.createNamespaces"
[ "$(rule_verbs "${ci_manifest}" clusterproperties)" = "get,list,watch" ] ||
  fail "clusterproperties writes must be dropped without identity.writeClusterProperties"
[ "$(rule_verbs "${ci_manifest}" serviceexports)" = "create,delete,get,list,patch,update,watch" ] ||
  fail "serviceexports create and delete stay: label-driven export always runs"
[ "$(kind_field "${ci_manifest}" ClusterRoleBinding "${release}" '.subjects[0].name + "/" + .subjects[0].namespace + " -> " + .roleRef.name')" = \
  "${release}/${namespace} -> ${release}" ] || fail "the ClusterRoleBinding must bind the ServiceAccount to the ClusterRole"

ns_manifest="${tmp_dir}/create-namespaces.yaml"
render "${ns_manifest}" --set imports.createNamespaces=true
[ "$(rule_verbs "${ns_manifest}" namespaces)" = "create,get,list,watch" ] || fail "imports.createNamespaces must grant namespaces create"
has_arg "$(args_of "${ns_manifest}")" "--create-namespaces=true" || fail "imports.createNamespaces must pass --create-namespaces=true"
[ "$(rule_verbs "${ns_manifest}" clusterproperties)" = "get,list,watch" ] || fail "imports.createNamespaces must not grant clusterproperties writes"

cp_manifest="${tmp_dir}/write-cluster-properties.yaml"
render "${cp_manifest}" --set identity.writeClusterProperties=true
[ "$(rule_verbs "${cp_manifest}" clusterproperties)" = "create,get,list,patch,update,watch" ] ||
  fail "identity.writeClusterProperties must grant clusterproperties create, update and patch"
has_arg "$(args_of "${cp_manifest}")" "--write-cluster-properties=true" || fail "identity.writeClusterProperties must pass --write-cluster-properties=true"
[ "$(rule_verbs "${cp_manifest}" namespaces)" = "get,list,watch" ] || fail "identity.writeClusterProperties must not grant namespaces create"

# With every option on, the ClusterRole grants exactly what the generated
# role.yaml grants.
full_manifest="${tmp_dir}/full-rbac.yaml"
render "${full_manifest}" --set imports.createNamespaces=true --set identity.writeClusterProperties=true
chart_triples="$(rule_triples "$(yq ea -o=json -I=0 'select(.kind == "ClusterRole") | .rules' "${full_manifest}")")"
module_triples="$(rule_triples "$(yq -o=json -I=0 '.rules' "${role_yaml}")")"
if [ "${chart_triples}" != "${module_triples}" ]; then
  diff <(printf '%s\n' "${module_triples}") <(printf '%s\n' "${chart_triples}") >&2 || true
  fail "the ClusterRole with every option on must equal ${role_yaml#"${repo_root}/"} (< module, > chart)"
fi

# ---------------------------------------------------------------------------
# CRDs: present, kept on uninstall, opt-out, and in sync with the module.

for crd in serviceexports.multicluster.x-k8s.io serviceimports.multicluster.x-k8s.io clusterproperties.about.k8s.io; do
  [ "$(count_kind "${ci_manifest}" CustomResourceDefinition "${crd}")" = "1" ] || fail "CRD ${crd} must render with crds.install"
  [ "$(kind_field "${ci_manifest}" CustomResourceDefinition "${crd}" '.metadata.annotations["helm.sh/resource-policy"]')" = "keep" ] ||
    fail "CRD ${crd} must carry helm.sh/resource-policy: keep"
  [ "$(kind_field "${ci_manifest}" CustomResourceDefinition "${crd}" '.metadata.labels["app.kubernetes.io/instance"]')" = "${release}" ] ||
    fail "CRD ${crd} must carry the chart labels"
done
mcs_version="$(awk '$1 == "sigs.k8s.io/mcs-api" {print $2; exit}' "${module_dir}/go.mod")"
[ "$(kind_field "${ci_manifest}" CustomResourceDefinition serviceexports.multicluster.x-k8s.io '.metadata.labels["multicluster.x-k8s.io/release-version"]')" = "${mcs_version}" ] ||
  fail "the ServiceExport CRD must come from sigs.k8s.io/mcs-api ${mcs_version} (go.mod)"
[ "$(kind_field "${ci_manifest}" CustomResourceDefinition clusterproperties.about.k8s.io '.metadata.annotations["api-approved.kubernetes.io"] // ""')" != "" ] ||
  fail "the ClusterProperty CRD needs the api-approved.kubernetes.io annotation of the protected about.k8s.io group"
no_crds_manifest="${tmp_dir}/no-crds.yaml"
render "${no_crds_manifest}" --set crds.install=false
[ "$(count_kind "${no_crds_manifest}" CustomResourceDefinition)" = "0" ] || fail "crds.install=false must drop every CRD"
bash "${script_dir}/sync-crds.sh" --check >&2 || fail "templates/crds differs from the module; run make sync-crds"

# ---------------------------------------------------------------------------
# Identity sources and every other flag.

auto_manifest="${tmp_dir}/auto.yaml"
render "${auto_manifest}" --set identity.source=auto --set-string clusterset.id= --set-string cluster.id=
auto_args="$(args_of "${auto_manifest}")"
has_arg "${auto_args}" "--cluster-identity-source=auto" || fail "identity.source must reach --cluster-identity-source"
! has_arg_prefix "${auto_args}" "--clusterset-id=" || fail "an empty clusterset.id must not render --clusterset-id"
! has_arg_prefix "${auto_args}" "--cluster-id=" || fail "an empty cluster.id must not render --cluster-id"

ca_manifest="${tmp_dir}/gds-ca.yaml"
render "${ca_manifest}" --set gds.tlsCaConfigMap=gds-ca
has_arg "$(args_of "${ca_manifest}")" "--gds-tls-ca-file=/etc/gds/ca/ca.crt" || fail "gds.tlsCaConfigMap must pass --gds-tls-ca-file"
[ "$(deployment_field "${ca_manifest}" '.spec.template.spec.volumes[] | select(.name == "gds-ca") | .configMap.name + ":" + .configMap.items[0].key')" = "gds-ca:ca.crt" ] ||
  fail "gds.tlsCaConfigMap must be mounted with its ca.crt key"
[ "$(container_field "${ca_manifest}" '.volumeMounts[] | select(.name == "gds-ca") | .mountPath')" = "/etc/gds/ca" ] ||
  fail "the GDS CA must be mounted at /etc/gds/ca"

tuned_manifest="${tmp_dir}/tuned.yaml"
render "${tuned_manifest}" \
  --set gds.allowInsecureHttp=true \
  --set 'gds.urls={http://gds-a.example.internal:9090,http://gds-b.example.internal:9090}' \
  --set cluster.region=us-east-1 \
  --set cluster.labels.tier=gpu \
  --set cluster.labels.env=prod \
  --set lease.ttl=15s \
  --set deregisterOnShutdown=true \
  --set imports.adoptUnmanaged=true \
  --set pools.autoExport=false \
  --set 'watchNamespaces={llm,dynamo}' \
  --set endpointsPerSlice=50 \
  --set maxConcurrentReconciles=8 \
  --set metrics.port=9443 \
  --set healthProbe.port=9444 \
  --set otel.enabled=true \
  --set otel.endpoint=http://otel-collector:4317 \
  --set otel.resourceAttributes.deployment\\.environment=prod \
  --set logLevel=debug \
  --set-string image.tag=sha256:0123456789abcdef \
  --set 'extraArgs={--zap-encoder=console}' \
  --set-json 'extraEnv=[{"name":"GOMEMLIMIT","value":"200MiB"}]'
tuned_args="$(args_of "${tuned_manifest}")"
for expected in \
  "--gds-url=http://gds-a.example.internal:9090" \
  "--gds-url=http://gds-b.example.internal:9090" \
  "--gds-allow-insecure-http=true" \
  "--region=us-east-1" \
  "--cluster-labels=env=prod,tier=gpu" \
  "--lease-ttl=15s" \
  "--deregister-on-shutdown=true" \
  "--adopt-unmanaged-imports=true" \
  "--auto-export-pools=false" \
  "--watch-namespaces=llm,dynamo" \
  "--endpoints-per-slice=50" \
  "--max-concurrent-reconciles=8" \
  "--metrics-bind-address=:9443" \
  "--health-probe-bind-address=:9444" \
  "--otel-enabled=true" \
  "--zap-log-level=debug"; do
  has_arg "${tuned_args}" "${expected}" || fail "tuned render is missing ${expected}"
done
[ "$(printf '%s\n' "${tuned_args}" | grep -n -- '--gds-url=' | cut -d: -f1 | tr '\n' ' ')" = "1 2 " ] ||
  fail "gds.urls must keep their failover order"
[ "$(printf '%s\n' "${tuned_args}" | tail -n 1)" = "--zap-encoder=console" ] || fail "extraArgs must come last"
[ "$(container_field "${tuned_manifest}" '.image')" = "${ci_image_repo}@sha256:0123456789abcdef" ] ||
  fail "a sha256 image tag must render as a digest"
[ "$(container_field "${tuned_manifest}" '[.ports[] | .name + "=" + (.containerPort | tostring)] | join(",")')" = "metrics=9443,health=9444" ] ||
  fail "container ports must follow metrics.port and healthProbe.port"
[ "$(env_value "${tuned_manifest}" OTEL_EXPORTER_OTLP_ENDPOINT)" = "http://otel-collector:4317" ] ||
  fail "otel.endpoint must reach OTEL_EXPORTER_OTLP_ENDPOINT"
[ "$(env_value "${tuned_manifest}" OTEL_RESOURCE_ATTRIBUTES)" = \
  "k8s.pod.name=\$(POD_NAME),k8s.namespace.name=\$(POD_NAMESPACE),deployment.environment=prod" ] ||
  fail "OTEL_RESOURCE_ATTRIBUTES must name the pod, namespace and the extra attributes"
[ "$(env_value "${tuned_manifest}" GOMEMLIMIT)" = "200MiB" ] || fail "extraEnv must reach the container"

sa_manifest="${tmp_dir}/external-sa.yaml"
render "${sa_manifest}" --set serviceAccount.create=false --set serviceAccount.name=gds-mcs
[ "$(count_kind "${sa_manifest}" ServiceAccount)" = "0" ] || fail "serviceAccount.create=false must not render a ServiceAccount"
[ "$(deployment_field "${sa_manifest}" '.spec.template.spec.serviceAccountName')" = "gds-mcs" ] || fail "serviceAccount.name must reach the pods"
[ "$(kind_field "${sa_manifest}" ClusterRoleBinding "${release}" '.subjects[0].name')" = "gds-mcs" ] ||
  fail "the ClusterRoleBinding must bind serviceAccount.name"

# A release name without the chart name prefixes the resources.
other_manifest="${tmp_dir}/other-release.yaml"
helm template mcs "${chart_dir}" --namespace "${namespace}" --values "${ci_values}" > "${other_manifest}"
[ "$(count_kind "${other_manifest}" Deployment mcs-global-discovery-mcs-controller)" = "1" ] ||
  fail "resources must be named <release>-global-discovery-mcs-controller"
[ "$(yq ea '[select(.metadata.name | length > 63)] | length' "${other_manifest}")" = "0" ] ||
  fail "object names must stay within 63 characters"

# NOTES.txt: helm template does not render it, and helm install --dry-run
# consults the cluster. Render it offline from a copy of the chart that wraps
# the notes in a template.
notes_chart="${tmp_dir}/notes-chart"
cp -R "${chart_dir}" "${notes_chart}"
mkdir -p "${notes_chart}/files"
mv "${notes_chart}/templates/NOTES.txt" "${notes_chart}/files/notes.txt"
printf 'notes: |\n{{ tpl (.Files.Get "files/notes.txt") . | indent 2 }}\n' > "${notes_chart}/templates/zz-notes.yaml"
render_notes() {
  helm template "${release}" "${notes_chart}" --namespace "${namespace}" --values "${ci_values}" \
    --show-only templates/zz-notes.yaml "$@" | yq -r '.notes'
}
notes="$(render_notes)"
printf '%s\n' "${notes}" | grep -qF "Cluster:              ${ci_cluster}" || fail "NOTES must name the cluster id"
printf '%s\n' "${notes}" | grep -qF "label service <name> gds.nvidia.com/export=true" || fail "NOTES must show the label-driven export"
! printf '%s\n' "${notes}" | grep -qF "crds.install is false" || fail "NOTES must not warn about CRDs by default"
dev_notes="$(render_notes --set crds.install=false --set gds.allowInsecureHttp=true --set identity.source=auto --set-string cluster.id=)"
printf '%s\n' "${dev_notes}" | grep -qF "crds.install is false" || fail "NOTES must warn when the chart installs no CRDs"
printf '%s\n' "${dev_notes}" | grep -qF "plaintext" || fail "NOTES must warn about plaintext GDS"
printf '%s\n' "${dev_notes}" | grep -qF "from ClusterProperty cluster.clusterset.k8s.io" || fail "NOTES must say where the cluster id comes from"

echo "global-discovery-mcs-controller render checks passed"
