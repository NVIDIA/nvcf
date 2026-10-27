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
# Render checks for the global-discovery chart. Every case runs helm template
# and asserts on the output with yq and jq.
#
# Usage: scripts/check-render.sh [chart dir]

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/../../../.." && pwd)"
chart_dir="${1:-${script_dir}/../global-discovery}"
ci_values="${CI_VALUES:-${repo_root}/tools/ci/helm-validate-values/global-discovery.yaml}"
release="${RELEASE:-global-discovery}"
namespace="${NAMESPACE:-global-discovery}"
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
ci_region="$(yq -r '.region' "${ci_values}")"

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

# render_production <output> [helm args...]: the CI values with the chart's
# production defaults restored: the DynamoDB store and three replicas.
render_production() {
  local output="$1"
  shift
  render "${output}" --set store.kind=dynamodb --set replicaCount=3 "$@"
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
  yq ea -r "select(.kind == \"Deployment\" and .metadata.name == \"${release}\") | $2" "$1"
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

# Required values: every missing one is reported at once.
assert_render_fails "global-discovery: set the required values: image.repository, region"
assert_render_fails "global-discovery: set the required values: region" \
  --set image.repository=example.com/gds
assert_render_fails "global-discovery: set the required values: store.dynamodb.table" \
  --values "${ci_values}" \
  --set store.kind=dynamodb \
  --set-string store.dynamodb.table=
# Schema: unknown top-level keys, formats and enums.
assert_render_fails "additional properties 'regoin' not allowed" \
  --values "${ci_values}" \
  --set regoin=us-east-1
assert_render_fails "'/region': 'US_EAST_1' does not match pattern" \
  --values "${ci_values}" \
  --set region=US_EAST_1
assert_render_fails "'/store/kind'" \
  --values "${ci_values}" \
  --set store.kind=etcd
assert_render_fails "'/refreshInterval': '2 seconds' does not match pattern" \
  --values "${ci_values}" \
  --set 'refreshInterval=2 seconds'
assert_render_fails "'/logLevel'" \
  --values "${ci_values}" \
  --set logLevel=trace
assert_render_fails "'/serviceAccount/irsaRoleArn'" \
  --values "${ci_values}" \
  --set serviceAccount.irsaRoleArn=gds-server
assert_render_fails "additional properties 'zone' not allowed" \
  --values "${ci_values}" \
  --set-json 'advertiseEndpoints=[{"region":"us-east-1","zone":"a","grpcUrl":"http://gds:9090"}]'
# Cross-field rules of global-discovery.validate.
assert_render_fails "store.kind=memory keeps the registry in each pod" \
  --values "${ci_values}" \
  --set replicaCount=3
assert_render_fails "store.kind=memory keeps the registry in each pod" \
  --values "${ci_values}" \
  --set autoscaling.enabled=true
assert_render_fails "podDisruptionBudget: set exactly one of minAvailable or maxUnavailable" \
  --values "${ci_values}" \
  --set podDisruptionBudget.maxUnavailable=1
assert_render_fails "podDisruptionBudget: set exactly one of minAvailable or maxUnavailable" \
  --values "${ci_values}" \
  --set-string podDisruptionBudget.minAvailable=
assert_render_fails "autoscaling.minReplicas must not exceed autoscaling.maxReplicas" \
  --values "${ci_values}" \
  --set store.kind=dynamodb \
  --set autoscaling.enabled=true \
  --set autoscaling.minReplicas=5 \
  --set autoscaling.maxReplicas=3
assert_render_fails "ports.grpc, ports.http and ports.admin must differ" \
  --values "${ci_values}" \
  --set ports.admin=8080
assert_render_fails "advertiseEndpoints[0] needs grpcUrl or httpUrl" \
  --values "${ci_values}" \
  --set-json 'advertiseEndpoints=[{"region":"us-east-1"}]'
assert_render_fails "at most one advertiseEndpoints entry may be primary" \
  --values "${ci_values}" \
  --set-json 'advertiseEndpoints=[{"region":"a","grpcUrl":"http://a:9090","primary":true},{"region":"b","grpcUrl":"http://b:9090","primary":true}]'

# CI values: the memory store with one replica.
ci_manifest="${tmp_dir}/ci.yaml"
render "${ci_manifest}"
ci_args="$(args_of "${ci_manifest}")"
has_arg "${ci_args}" "--store=memory" || fail "store.kind=memory must reach --store"
! has_arg_prefix "${ci_args}" "--dynamodb-" || fail "the memory store must not get DynamoDB flags"
[ "$(deployment_field "${ci_manifest}" '.spec.replicas')" = "1" ] || fail "CI values run one replica"
[ "$(env_value "${ci_manifest}" GDS_REGION)" = "${ci_region}" ] || fail "region must reach GDS_REGION"
[ "$(env_value "${ci_manifest}" AWS_REGION)" = "${ci_region}" ] || fail "region must reach AWS_REGION"
! has_arg_prefix "${ci_args}" "--region" || fail "the region is passed through GDS_REGION, not --region"

# Production defaults: DynamoDB, three replicas, zero-downtime rollout.
prod_manifest="${tmp_dir}/production.yaml"
render_production "${prod_manifest}"
prod_args="$(args_of "${prod_manifest}")"
[ "$(deployment_field "${prod_manifest}" '.spec.replicas')" = "3" ] || fail "the chart runs three replicas by default"
[ "$(yq -r '.replicaCount' "${chart_dir}/values.yaml")" = "3" ] || fail "values.yaml must default replicaCount to 3"
[ "$(yq -r '.store.kind' "${chart_dir}/values.yaml")" = "dynamodb" ] || fail "values.yaml must default to the DynamoDB store"
[ "$(deployment_field "${prod_manifest}" '.spec.strategy.type')" = "RollingUpdate" ] || fail "rollout must be RollingUpdate"
[ "$(deployment_field "${prod_manifest}" '.spec.strategy.rollingUpdate.maxUnavailable')" = "0" ] || fail "rollout must keep every replica (maxUnavailable 0)"
[ "$(deployment_field "${prod_manifest}" '.spec.strategy.rollingUpdate.maxSurge')" = "1" ] || fail "rollout must surge one replica"
[ "$(deployment_field "${prod_manifest}" '.spec.template.spec.terminationGracePeriodSeconds')" = "40" ] ||
  fail "terminationGracePeriodSeconds must be 40 (drain 5s + stop 25s + flush)"

for expected in \
  "--grpc-listen-address=:9090" \
  "--http-listen-address=:8080" \
  "--admin-listen-address=:9091" \
  "--store=dynamodb" \
  "--dynamodb-table=gds" \
  "--refresh-interval=2s" \
  "--clusterset-discovery-interval=30s" \
  "--lease-skew=2s" \
  "--lease-eval-interval=1s" \
  "--retention=24h" \
  "--compaction-interval=10m" \
  "--bookmark-interval=5s" \
  "--watch-history=10000" \
  "--watch-queue-size=1024" \
  "--max-watchers=5000" \
  "--shutdown-drain=5s" \
  "--shutdown-timeout=25s" \
  "--log-level=info"; do
  has_arg "${prod_args}" "${expected}" || fail "default render is missing ${expected}"
done
for absent in --dynamodb-endpoint --advertise-endpoints --tls-cert-file --tls-key-file --otel-enabled; do
  ! has_arg_prefix "${prod_args}" "${absent}" || fail "default render must not set ${absent}"
done

[ "$(container_field "${prod_manifest}" '.image')" = "${ci_image_repo}:${app_version}" ] ||
  fail "image tag must default to the chart appVersion"
[ "$(container_field "${prod_manifest}" '.env[] | select(.name == "POD_NAME") | .valueFrom.fieldRef.fieldPath')" = "metadata.name" ] ||
  fail "POD_NAME must come from the downward API"
[ "$(container_field "${prod_manifest}" '.readinessProbe.httpGet | .path + " " + .port')" = "/readyz admin" ] ||
  fail "readiness probe must hit /readyz on the admin port"
[ "$(container_field "${prod_manifest}" '.livenessProbe.httpGet | .path + " " + .port')" = "/healthz admin" ] ||
  fail "liveness probe must hit /healthz on the admin port"
[ "$(container_field "${prod_manifest}" '[.ports[] | .name + "=" + (.containerPort | tostring)] | join(",")')" = "grpc=9090,http=8080,admin=9091" ] ||
  fail "container ports must be grpc 9090, http 8080, admin 9091"
[ "$(deployment_field "${prod_manifest}" '.spec.template.spec.enableServiceLinks')" = "false" ] ||
  fail "service links must be off so no <SERVICE>_ variable shadows a GDS_ flag"
[ "$(deployment_field "${prod_manifest}" '.spec.template.spec.automountServiceAccountToken')" = "false" ] ||
  fail "the server does not need a Kubernetes API token"

security_context="$(yq -o=json -I=0 "select(.kind == \"Deployment\") | .spec.template.spec.containers[0].securityContext" "${prod_manifest}")"
[ "$(printf '%s' "${security_context}" | jq -r '.runAsNonRoot')" = "true" ] || fail "container must run as non-root"
[ "$(printf '%s' "${security_context}" | jq -r '.readOnlyRootFilesystem')" = "true" ] || fail "root filesystem must be read-only"
[ "$(printf '%s' "${security_context}" | jq -r '.allowPrivilegeEscalation')" = "false" ] || fail "privilege escalation must be off"
[ "$(printf '%s' "${security_context}" | jq -c '.capabilities.drop')" = '["ALL"]' ] || fail "all capabilities must be dropped"

# Zone spread with the pod's selector labels.
spread="$(yq -o=json -I=0 "select(.kind == \"Deployment\") | .spec.template.spec.topologySpreadConstraints" "${prod_manifest}")"
[ "$(printf '%s' "${spread}" | jq -r '.[0].topologyKey')" = "topology.kubernetes.io/zone" ] || fail "replicas must spread over zones"
[ "$(printf '%s' "${spread}" | jq -r '.[0].labelSelector.matchLabels["app.kubernetes.io/instance"]')" = "${release}" ] ||
  fail "the zone spread must select the release's pods"
no_spread_manifest="${tmp_dir}/no-spread.yaml"
render_production "${no_spread_manifest}" --set-json 'topologySpreadConstraints=[]'
[ "$(deployment_field "${no_spread_manifest}" '.spec.template.spec.topologySpreadConstraints // [] | length')" = "0" ] ||
  fail "an empty topologySpreadConstraints must disable the spread"

# ClusterIP Service with the three ports.
[ "$(kind_field "${prod_manifest}" Service "${release}" '.spec.type')" = "ClusterIP" ] || fail "the main Service must be ClusterIP"
[ "$(kind_field "${prod_manifest}" Service "${release}" '[.spec.ports[] | .name + "=" + (.port | tostring) + ">" + .targetPort] | join(",")')" = \
  "grpc=9090>grpc,http=8080>http,admin=9091>admin" ] || fail "the Service must expose grpc 9090, http 8080 and admin 9091"
[ "$(count_kind "${prod_manifest}" Service)" = "1" ] || fail "the load balancer Service must be off by default"

# PodDisruptionBudget keeps one replica.
[ "$(kind_field "${prod_manifest}" PodDisruptionBudget "${release}" '.spec.minAvailable')" = "1" ] || fail "the PDB must keep minAvailable 1"
[ "$(kind_field "${prod_manifest}" PodDisruptionBudget "${release}" '.spec.selector.matchLabels["app.kubernetes.io/instance"]')" = "${release}" ] ||
  fail "the PDB must select the release's pods"
max_unavailable_manifest="${tmp_dir}/pdb-max-unavailable.yaml"
render_production "${max_unavailable_manifest}" --set-string podDisruptionBudget.minAvailable= --set podDisruptionBudget.maxUnavailable=1
[ "$(kind_field "${max_unavailable_manifest}" PodDisruptionBudget "${release}" '.spec.maxUnavailable')" = "1" ] ||
  fail "podDisruptionBudget.maxUnavailable must render"
no_pdb_manifest="${tmp_dir}/no-pdb.yaml"
render_production "${no_pdb_manifest}" --set podDisruptionBudget.enabled=false
[ "$(count_kind "${no_pdb_manifest}" PodDisruptionBudget)" = "0" ] || fail "podDisruptionBudget.enabled=false must drop the PDB"

# Optional objects are off by default; the ServiceAccount has no IRSA role.
[ "$(count_kind "${prod_manifest}" HorizontalPodAutoscaler)" = "0" ] || fail "the HPA must be off by default"
[ "$(count_kind "${prod_manifest}" ServiceMonitor)" = "0" ] || fail "the ServiceMonitor must be off by default"
[ "$(kind_field "${prod_manifest}" ServiceAccount "${release}" '.metadata.annotations["eks.amazonaws.com/role-arn"] // ""')" = "" ] ||
  fail "no IRSA annotation without serviceAccount.irsaRoleArn"
[ "$(deployment_field "${prod_manifest}" '.spec.template.spec.serviceAccountName')" = "${release}" ] ||
  fail "the pods must use the release ServiceAccount"

# IRSA: the role ARN becomes the eks.amazonaws.com/role-arn annotation.
role_arn="arn:aws:iam::123456789012:role/gds-server"
irsa_manifest="${tmp_dir}/irsa.yaml"
render_production "${irsa_manifest}" --set serviceAccount.irsaRoleArn="${role_arn}" --set serviceAccount.annotations.team=gds
[ "$(kind_field "${irsa_manifest}" ServiceAccount "${release}" '.metadata.annotations["eks.amazonaws.com/role-arn"]')" = "${role_arn}" ] ||
  fail "serviceAccount.irsaRoleArn must render the eks.amazonaws.com/role-arn annotation"
[ "$(kind_field "${irsa_manifest}" ServiceAccount "${release}" '.metadata.annotations.team')" = "gds" ] ||
  fail "serviceAccount.annotations must be kept next to the IRSA annotation"
external_sa_manifest="${tmp_dir}/external-sa.yaml"
render_production "${external_sa_manifest}" --set serviceAccount.create=false --set serviceAccount.name=gds-irsa
[ "$(count_kind "${external_sa_manifest}" ServiceAccount)" = "0" ] || fail "serviceAccount.create=false must not render a ServiceAccount"
[ "$(deployment_field "${external_sa_manifest}" '.spec.template.spec.serviceAccountName')" = "gds-irsa" ] ||
  fail "serviceAccount.name must reach the pods"

# Internal NLB: AWS Load Balancer Controller annotations and the ports.
nlb_service="${release}-external"
nlb_manifest="${tmp_dir}/nlb.yaml"
render_production "${nlb_manifest}" --set externalService.enabled=true
[ "$(kind_field "${nlb_manifest}" Service "${nlb_service}" '.spec.type')" = "LoadBalancer" ] ||
  fail "externalService.enabled must render a LoadBalancer Service"
nlb_annotation() {  # nlb_annotation <manifest> <annotation suffix>
  kind_field "$1" Service "${nlb_service}" ".metadata.annotations[\"service.beta.kubernetes.io/aws-load-balancer-$2\"]"
}
[ "$(nlb_annotation "${nlb_manifest}" type)" = "external" ] || fail "NLB: aws-load-balancer-type must be external (AWS Load Balancer Controller)"
[ "$(nlb_annotation "${nlb_manifest}" nlb-target-type)" = "ip" ] || fail "NLB: targets must be pod IPs"
[ "$(nlb_annotation "${nlb_manifest}" scheme)" = "internal" ] || fail "NLB: scheme must be internal"
[ "$(nlb_annotation "${nlb_manifest}" attributes)" = "load_balancing.cross_zone.enabled=true" ] || fail "NLB: cross-zone load balancing must be on"
[ "$(nlb_annotation "${nlb_manifest}" healthcheck-protocol)" = "HTTP" ] || fail "NLB: health check must be HTTP"
[ "$(nlb_annotation "${nlb_manifest}" healthcheck-path)" = "/readyz" ] || fail "NLB: health check must be /readyz"
[ "$(nlb_annotation "${nlb_manifest}" healthcheck-port)" = "9091" ] || fail "NLB: health check must use the admin port"
[ "$(nlb_annotation "${nlb_manifest}" target-group-attributes)" = "deregistration_delay.timeout_seconds=30" ] ||
  fail "NLB: deregistration delay must be 30s"
[ "$(kind_field "${nlb_manifest}" Service "${nlb_service}" '[.spec.ports[] | .name + "=" + (.port | tostring) + ">" + .targetPort] | join(",")')" = \
  "grpc=9090>grpc,http=8080>http" ] || fail "NLB: listeners must be grpc 9090 and http 8080, without admin"
nlb_tuned_manifest="${tmp_dir}/nlb-tuned.yaml"
render_production "${nlb_tuned_manifest}" \
  --set externalService.enabled=true \
  --set ports.admin=9191 \
  --set externalService.httpPort=0 \
  --set externalService.grpcPort=443 \
  --set externalService.loadBalancerClass=service.k8s.aws/nlb \
  --set 'externalService.loadBalancerSourceRanges={10.1.0.0/16}' \
  --set 'externalService.annotations.service\.beta\.kubernetes\.io/aws-load-balancer-scheme=internet-facing'
[ "$(nlb_annotation "${nlb_tuned_manifest}" healthcheck-port)" = "9191" ] || fail "NLB: health check port must follow ports.admin"
[ "$(nlb_annotation "${nlb_tuned_manifest}" scheme)" = "internet-facing" ] || fail "NLB: externalService.annotations must win over the generated ones"
[ "$(kind_field "${nlb_tuned_manifest}" Service "${nlb_service}" '[.spec.ports[] | .name + "=" + (.port | tostring)] | join(",")')" = "grpc=443" ] ||
  fail "NLB: httpPort 0 must expose gRPC only, on grpcPort"
[ "$(kind_field "${nlb_tuned_manifest}" Service "${nlb_service}" '.spec.loadBalancerClass')" = "service.k8s.aws/nlb" ] ||
  fail "NLB: loadBalancerClass must render"
[ "$(kind_field "${nlb_tuned_manifest}" Service "${nlb_service}" '.spec.loadBalancerSourceRanges[0]')" = "10.1.0.0/16" ] ||
  fail "NLB: loadBalancerSourceRanges must render"
other_cloud_manifest="${tmp_dir}/other-cloud.yaml"
render_production "${other_cloud_manifest}" --set externalService.enabled=true --set externalService.awsNlb.enabled=false
[ "$(kind_field "${other_cloud_manifest}" Service "${nlb_service}" '.metadata.annotations // {} | length')" = "0" ] ||
  fail "awsNlb.enabled=false must drop the AWS annotations"

# HPA: owns the replica count.
hpa_manifest="${tmp_dir}/hpa.yaml"
render_production "${hpa_manifest}" --set autoscaling.enabled=true --set autoscaling.minReplicas=3 --set autoscaling.maxReplicas=6 \
  --set autoscaling.targetMemoryUtilizationPercentage=80
[ "$(kind_field "${hpa_manifest}" HorizontalPodAutoscaler "${release}" '(.spec.minReplicas | tostring) + "-" + (.spec.maxReplicas | tostring)')" = "3-6" ] ||
  fail "the HPA must scale between autoscaling.minReplicas and maxReplicas"
[ "$(kind_field "${hpa_manifest}" HorizontalPodAutoscaler "${release}" '.spec.scaleTargetRef.name')" = "${release}" ] ||
  fail "the HPA must scale the Deployment"
[ "$(kind_field "${hpa_manifest}" HorizontalPodAutoscaler "${release}" '[.spec.metrics[].resource.name] | join(",")')" = "cpu,memory" ] ||
  fail "the HPA must target CPU and, when set, memory"
[ "$(deployment_field "${hpa_manifest}" '.spec.replicas // "unset"')" = "unset" ] ||
  fail "with the HPA the Deployment must leave spec.replicas unset"

# ServiceMonitor scrapes /metrics on the admin port.
sm_manifest="${tmp_dir}/servicemonitor.yaml"
render_production "${sm_manifest}" --set metrics.serviceMonitor.enabled=true --set metrics.serviceMonitor.labels.release=prometheus
[ "$(kind_field "${sm_manifest}" ServiceMonitor "${release}" '.spec.endpoints[0].port + " " + .spec.endpoints[0].path')" = "admin /metrics" ] ||
  fail "the ServiceMonitor must scrape /metrics on the admin port"
[ "$(kind_field "${sm_manifest}" ServiceMonitor "${release}" '.metadata.labels.release')" = "prometheus" ] ||
  fail "metrics.serviceMonitor.labels must reach the ServiceMonitor"

# Every other flag follows its value; digests use @.
tuned_manifest="${tmp_dir}/tuned.yaml"
render_production "${tuned_manifest}" \
  --set store.dynamodb.table=gds-prod \
  --set store.dynamodb.endpoint=http://dynamodb-local:8000 \
  --set refreshInterval=3s \
  --set clustersetDiscoveryInterval=1m \
  --set lease.skew=3s \
  --set lease.evalInterval=500ms \
  --set retention=48h \
  --set compactionInterval=30m \
  --set watch.bookmarkInterval=10s \
  --set watch.history=20000 \
  --set watch.queueSize=2048 \
  --set watch.maxWatchers=100 \
  --set shutdown.drain=10s \
  --set shutdown.timeout=20s \
  --set-json 'advertiseEndpoints=[{"region":"us-east-1","grpcUrl":"http://gds.us-east-1.prod.internal:9090","primary":true},{"region":"us-west-2","httpUrl":"http://gds.us-west-2.prod.internal:8080"}]' \
  --set tls.existingSecret=gds-tls \
  --set otel.enabled=true \
  --set otel.endpoint=http://otel-collector:4317 \
  --set otel.resourceAttributes.deployment\\.environment=prod \
  --set logLevel=debug \
  --set-string image.tag=sha256:0123456789abcdef \
  --set 'extraArgs={--max-message-bytes=8388608}' \
  --set-json 'extraEnv=[{"name":"GOMEMLIMIT","value":"400MiB"}]'
tuned_args="$(args_of "${tuned_manifest}")"
for expected in \
  "--dynamodb-table=gds-prod" \
  "--dynamodb-endpoint=http://dynamodb-local:8000" \
  "--refresh-interval=3s" \
  "--clusterset-discovery-interval=1m" \
  "--lease-skew=3s" \
  "--lease-eval-interval=500ms" \
  "--retention=48h" \
  "--compaction-interval=30m" \
  "--bookmark-interval=10s" \
  "--watch-history=20000" \
  "--watch-queue-size=2048" \
  "--max-watchers=100" \
  "--shutdown-drain=10s" \
  "--shutdown-timeout=20s" \
  "--tls-cert-file=/etc/gds/tls/tls.crt" \
  "--tls-key-file=/etc/gds/tls/tls.key" \
  "--otel-enabled=true" \
  "--log-level=debug" \
  "--max-message-bytes=8388608"; do
  has_arg "${tuned_args}" "${expected}" || fail "tuned render is missing ${expected}"
done
[ "$(printf '%s\n' "${tuned_args}" | tail -n 1)" = "--max-message-bytes=8388608" ] || fail "extraArgs must come last"
advertise="$(printf '%s\n' "${tuned_args}" | sed -n 's/^--advertise-endpoints=//p')"
[ "$(printf '%s' "${advertise}" | jq -c '[.[] | [.region, (.grpcUrl // ""), (.httpUrl // ""), (.primary // false)]]')" = \
  '[["us-east-1","http://gds.us-east-1.prod.internal:9090","",true],["us-west-2","","http://gds.us-west-2.prod.internal:8080",false]]' ] ||
  fail "advertiseEndpoints must reach --advertise-endpoints as JSON, got ${advertise}"
[ "$(container_field "${tuned_manifest}" '.image')" = "${ci_image_repo}@sha256:0123456789abcdef" ] ||
  fail "a sha256 image tag must render as a digest"
[ "$(env_value "${tuned_manifest}" OTEL_EXPORTER_OTLP_ENDPOINT)" = "http://otel-collector:4317" ] ||
  fail "otel.endpoint must reach OTEL_EXPORTER_OTLP_ENDPOINT"
[ "$(env_value "${tuned_manifest}" OTEL_RESOURCE_ATTRIBUTES)" = \
  "k8s.pod.name=\$(POD_NAME),k8s.namespace.name=\$(POD_NAMESPACE),cloud.region=${ci_region},deployment.environment=prod" ] ||
  fail "OTEL_RESOURCE_ATTRIBUTES must name the pod, namespace, region and the extra attributes"
[ "$(env_value "${tuned_manifest}" GOMEMLIMIT)" = "400MiB" ] || fail "extraEnv must reach the container"
[ "$(deployment_field "${tuned_manifest}" '.spec.template.spec.volumes[] | select(.name == "tls") | .secret.secretName')" = "gds-tls" ] ||
  fail "tls.existingSecret must be mounted"
[ "$(container_field "${tuned_manifest}" '.volumeMounts[] | select(.name == "tls") | .mountPath')" = "/etc/gds/tls" ] ||
  fail "the TLS Secret must be mounted at /etc/gds/tls"
[ "$(container_field "${tuned_manifest}" '.readinessProbe.httpGet.scheme // "HTTP"')" = "HTTP" ] ||
  fail "probes stay plain HTTP on the admin port with TLS"

# A release name without the chart name prefixes the resources.
other_manifest="${tmp_dir}/other-release.yaml"
helm template gds "${chart_dir}" --namespace "${namespace}" --values "${ci_values}" --set externalService.enabled=true > "${other_manifest}"
[ "$(count_kind "${other_manifest}" Deployment gds-global-discovery)" = "1" ] || fail "resources must be named <release>-global-discovery"
[ "$(count_kind "${other_manifest}" Service gds-global-discovery-external)" = "1" ] || fail "the load balancer Service must be <fullname>-external"

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
printf '%s\n' "${notes}" | grep -qF "store.kind=memory keeps the registry in the pod" || fail "NOTES must warn about the memory store"
printf '%s\n' "${notes}" | grep -qF "port-forward service/${release} 8080:8080 9091:9091" || fail "NOTES must show the port-forward"
printf '%s\n' "${notes}" | grep -qF "curl -s http://127.0.0.1:9091/readyz" || fail "NOTES must show the readiness check"
prod_notes="$(render_notes --set store.kind=dynamodb --set replicaCount=3 --set externalService.enabled=true --set serviceAccount.irsaRoleArn="${role_arn}")"
printf '%s\n' "${prod_notes}" | grep -qF "gds-admin create-table --table gds --region ${ci_region}" || fail "NOTES must show the table creation"
printf '%s\n' "${prod_notes}" | grep -qF "assumes ${role_arn}" || fail "NOTES must name the IRSA role"
printf '%s\n' "${prod_notes}" | grep -qF "get service ${nlb_service}" || fail "NOTES must show how to read the load balancer address"
! printf '%s\n' "${prod_notes}" | grep -qF "store.kind=memory" || fail "NOTES must not warn about the memory store with DynamoDB"

echo "global-discovery render checks passed"
