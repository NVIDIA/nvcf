#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Asserts the cluster-validator wiring the chart renders: the CronJob's Job
# spec (which the operator also runs), the operator's init container, and the
# validator RBAC, which must grant exactly the requests listed in the
# validator's RBAC inventory.

set -euo pipefail

chart_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="${chart_root}/nvca-operator"
repo_root="$(cd "${chart_root}/../../.." && pwd)"
inventory="${repo_root}/src/compute-plane-services/nvca/internal/clustervalidator/rbac_inventory.yaml"
work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT

fail() {
  echo "cluster-validator: FAIL $*" >&2
  exit 1
}

assert_eq() {
  local want=${1} got=${2} message=${3}
  [[ "${got}" == "${want}" ]] || fail "${message}: got '${got}', want '${want}'"
}

render() {
  local out=${1}
  shift
  helm template test-release "${chart}" --namespace nvca-operator \
    --set-string "ngcConfig.serviceKey=fakekey" "$@" >"${out}"
}

job='select(.kind == "CronJob") | .spec.jobTemplate'
pod="${job} | .spec.template.spec"
job_env="${pod} | .containers[0].env[]"
init_env='select(.kind == "Deployment") | .spec.template.spec.initContainers[0].env[]'
operator_env='select(.kind == "Deployment") | .spec.template.spec.containers[0].env[]'
spec_hash="${job} | .metadata.annotations.\"nvca.nvcf.nvidia.io/cluster-validator-spec\""

# yq prints an empty document for every manifest a collecting expression does
# not match, so drop those.
yq_values() {
  yq "${1}" "${2}" | grep -v -e '^---$' -e '^$' || true
}

env_value() {
  yq "${1} | select(.name == \"${2}\") | .value" "${3}"
}

# --- Every clusterValidator value set, rendered once ------------------------
full_values="${work_dir}/full-values.yaml"
cat >"${full_values}" <<'EOF'
clusterValidator:
  enabled: true
  role: Control-Plane
  openBaoNamespace: openbao
  envoyGatewayNamespace: eg-system
  gatewayNames: [nvcf/shared-gw, nvcf/grpc-gw]
  externalComponents: [nats, cassandra]
  nodeToNodeProbeImage: mirror.example.com/busybox:1.36
  configMapName: site-checks
  storageClass: ceph-rbd
  schedule: "*/30 * * * *"
  image:
    repository: mirror.example.com/cluster-validator
    tag: v9
    pullPolicy: Always
  tolerations:
    - {key: dedicated, operator: Equal, value: infra, effect: NoSchedule}
  networkChecks:
    enforcement: {enabled: true, critical: false}
tolerations:
  - {key: operator-only, operator: Exists}
nodeSelector:
  key: node-pool
  value: infra
EOF
full="${work_dir}/full.yaml"
render "${full}" --values "${full_values}"

job_env_names="NVCF_ENVOY_GATEWAY_NAMESPACE NVCF_EXTERNAL_COMPONENTS NVCF_GATEWAY_NAMES NVCF_N2N_PROBE_IMAGE"
job_env_names+=" NVCF_OPENBAO_NAMESPACE NVCF_STORAGE_CLASS VALIDATOR_CONFIG_NAME VALIDATOR_CONFIG_NAMESPACE"
job_env_names+=" VALIDATOR_POST_INSTALL VALIDATOR_REQUIRE_SUMMARY VALIDATOR_ROLE VALIDATOR_SUMMARY_NAMESPACE"
assert_eq "${job_env_names}" "$(yq "${job_env} | .name" "${full}" | sort | xargs)" \
  "validator Job sets exactly the env the validator reads"
assert_eq "eg-system" "$(env_value "${job_env}" NVCF_ENVOY_GATEWAY_NAMESPACE "${full}")" "Envoy Gateway namespace"
assert_eq "nats,cassandra" "$(env_value "${job_env}" NVCF_EXTERNAL_COMPONENTS "${full}")" "external components"
assert_eq "nvcf/shared-gw,nvcf/grpc-gw" "$(env_value "${job_env}" NVCF_GATEWAY_NAMES "${full}")" "gateway names"
assert_eq "mirror.example.com/busybox:1.36" "$(env_value "${job_env}" NVCF_N2N_PROBE_IMAGE "${full}")" "probe image"
assert_eq "openbao" "$(env_value "${job_env}" NVCF_OPENBAO_NAMESPACE "${full}")" "OpenBao namespace"
assert_eq "ceph-rbd" "$(env_value "${job_env}" NVCF_STORAGE_CLASS "${full}")" "storage class"
assert_eq "site-checks" "$(env_value "${job_env}" VALIDATOR_CONFIG_NAME "${full}")" "config ConfigMap name"
assert_eq "true" "$(env_value "${job_env}" VALIDATOR_POST_INSTALL "${full}")" "the Job declares the stack installed"
assert_eq "true" "$(env_value "${job_env}" VALIDATOR_REQUIRE_SUMMARY "${full}")" "the Job requires its summary"
assert_eq "Control-Plane" "$(env_value "${job_env}" VALIDATOR_ROLE "${full}")" "the Job runs the configured role"
for name in VALIDATOR_CONFIG_NAMESPACE VALIDATOR_SUMMARY_NAMESPACE; do
  assert_eq "metadata.namespace" \
    "$(yq "${job_env} | select(.name == \"${name}\") | .valueFrom.fieldRef.fieldPath" "${full}")" \
    "${name} is the Job's own namespace"
done

assert_eq "mirror.example.com/cluster-validator:v9" "$(yq "${pod} | .containers[0].image" "${full}")" "image"
assert_eq "Always" "$(yq "${pod} | .containers[0].imagePullPolicy" "${full}")" "pull policy"
assert_eq "*/30 * * * *" "$(yq 'select(.kind == "CronJob") | .spec.schedule' "${full}")" "schedule"
assert_eq "Forbid" "$(yq 'select(.kind == "CronJob") | .spec.concurrencyPolicy' "${full}")" "concurrency policy"
assert_eq "node-role.kubernetes.io/control-plane node-role.kubernetes.io/master dedicated operator-only" \
  "$(yq "${pod} | .tolerations[].key" "${full}" | xargs)" \
  "tolerations: control-plane, then clusterValidator.tolerations, then the operator's"
assert_eq "infra" "$(yq "${pod} | .tolerations[] | select(.key == \"dedicated\") | .value" "${full}")" \
  "extra toleration is rendered whole"
assert_eq "infra" "$(yq "${pod} | .nodeSelector.\"node-pool\"" "${full}")" "the Job follows the operator's nodeSelector"
assert_eq "2" "$(yq "${job} | .spec.backoffLimit" "${full}")" "backoffLimit"
assert_eq "600" "$(yq "${job} | .spec.activeDeadlineSeconds" "${full}")" "activeDeadlineSeconds"
assert_eq "120" "$(yq "${pod} | .terminationGracePeriodSeconds" "${full}")" \
  "an interrupted run has time to delete its probe resources"
failure_rules="${job} | .spec.podFailurePolicy.rules[]"
failure_rules+=' | [.action, .onExitCodes.containerName, .onExitCodes.operator, .onExitCodes.values[]] | join(" ")'
assert_eq "FailJob cluster-validator In 3" "$(yq_values "${failure_rules}" "${full}")" \
  "a published Not-Ready (exit 3) fails the Job without a retry"

# Restricted Pod Security: the same pod shape the nvcf-cli Job uses.
pod_security="${pod} | .securityContext"
pod_security+=' | [.runAsNonRoot, .seccompProfile.type, .runAsUser, .runAsGroup, .fsGroup] | join(" ")'
assert_eq "true RuntimeDefault 65534 65534 65534" "$(yq_values "${pod_security}" "${full}")" \
  "pod securityContext is restricted-compliant"
container_security="${pod} | .containers[0].securityContext"
container_security+=' | [.runAsNonRoot, .readOnlyRootFilesystem, .allowPrivilegeEscalation, .capabilities.drop[]]'
container_security+=' | join(" ")'
assert_eq "true true false ALL" "$(yq_values "${container_security}" "${full}")" \
  "container securityContext is restricted-compliant"

# The init container gates operator startup. It runs the compute-plane checks
# whatever the role, publishes nothing under the control-plane role, and fails
# only on an observed critical failure.
init_env_names="VALIDATOR_CONFIG_NAME VALIDATOR_CONFIG_NAMESPACE VALIDATOR_PREFLIGHT VALIDATOR_ROLE"
init_env_names+=" VALIDATOR_STARTUP_GATE VALIDATOR_SUMMARY_NAMESPACE"
assert_eq "${init_env_names}" "$(yq "${init_env} | .name" "${full}" | sort | xargs)" \
  "init container env"
assert_eq "compute-plane" "$(env_value "${init_env}" VALIDATOR_ROLE "${full}")" \
  "init container stays on the compute-plane checks"
assert_eq "true" "$(env_value "${init_env}" VALIDATOR_PREFLIGHT "${full}")" \
  "init container skips the summary under a mixed-case control-plane role"
assert_eq "true" "$(env_value "${init_env}" VALIDATOR_STARTUP_GATE "${full}")" \
  "init container fails only on observed failures"

# Under the control-plane role the operator runs the CronJob when its spec
# changes. No Job is rendered: a Job in the release made the install wait on
# the validator and fail with it.
assert_eq "$(yq 'select(.kind == "CronJob") | .metadata.name' "${full}")" \
  "$(env_value "${operator_env}" NVCA_CLUSTER_VALIDATOR_CRONJOB "${full}")" \
  "the operator watches the CronJob under the control-plane role"
assert_eq "true" "$(env_value "${operator_env}" NVCA_CLUSTER_VALIDATOR_ENABLED "${full}")" \
  "the operator is told the validator runs"
validation_job='select(.kind == "Job" and .metadata.labels."app.kubernetes.io/component" == "validation")'
assert_eq "" "$(yq "${validation_job} | .metadata.name" "${full}")" "no validation Job in the release"
[[ "$(yq "${spec_hash}" "${full}")" =~ ^[0-9a-f]{16}$ ]] || fail "the Job template carries no validator spec hash"
echo "ok validator Job, init container and operator wiring"

# --- The spec hash covers every validator input, and only those --------------
operator_pod='select(.kind == "Deployment") | .spec.template'
base_hash="$(yq "${spec_hash}" "${full}")"
base_operator="$(yq "${operator_pod}" "${full}")"
again="${work_dir}/again.yaml"
render "${again}" --values "${full_values}"
assert_eq "${base_hash}" "$(yq "${spec_hash}" "${again}")" "the spec hash is stable"
for change in \
  "clusterValidator.gatewayNames={nvcf/shared-gw}" \
  "clusterValidator.tolerations[0].key=other" \
  "clusterValidator.externalComponents={openbao}" \
  "clusterValidator.storageClass=local-path" \
  "clusterValidator.networkChecks.enforcement.critical=true" \
  "clusterValidator.configMapName=other-checks" \
  "clusterValidator.image.tag=v10"; do
  changed="${work_dir}/changed.yaml"
  render "${changed}" --values "${full_values}" --set "${change}"
  [[ "$(yq "${spec_hash}" "${changed}")" != "${base_hash}" ]] ||
    fail "${change} leaves the validator spec hash unchanged"
  # A change to the Job spec or the network checks alone must not roll the
  # operator: its init container would gate the release on a validator run.
  # The values the init container itself uses do roll it.
  case "${change}" in
    clusterValidator.configMapName=* | clusterValidator.image.tag=*)
      [[ "$(yq "${operator_pod}" "${changed}")" != "${base_operator}" ]] ||
        fail "${change} does not reach the operator's init container"
      ;;
    *)
      assert_eq "${base_operator}" "$(yq "${operator_pod}" "${changed}")" "${change} rolls the operator pod"
      ;;
  esac
done
echo "ok the spec hash covers the Job spec and the network checks, and only init container values roll the operator"

# --- RBAC: exactly the inventory, per role ------------------------------------
rendered_rbac() {
  local kind scope
  for kind in ClusterRole Role; do
    scope=cluster
    [[ "${kind}" == Role ]] && scope=namespace
    yq -r "select(.kind == \"${kind}\" and .metadata.labels.\"app.kubernetes.io/component\" == \"validation\") |
      .rules[] | select(has(\"resources\")) |
      .apiGroups[] as \$g | .resources[] as \$res | .verbs[] as \$v | (.resourceNames // [\"*\"])[] as \$n |
      \"${scope}|\" + \$g + \"|\" + \$res + \"|\" + \$v + \"|\" + \$n" "${1}"
    yq -r "select(.kind == \"${kind}\" and .metadata.labels.\"app.kubernetes.io/component\" == \"validation\") |
      .rules[] | select(has(\"nonResourceURLs\")) | .nonResourceURLs[] as \$u | .verbs[] as \$v |
      \"${scope}|nonResourceURL|\" + \$u + \"|\" + \$v + \"|*\"" "${1}"
  done | grep -v -e '^---$' -e '^$' || true
}

inventory_rbac() {
  local role=${1}
  yq -r ".rules[] | select(.roles[] == \"${role}\") | select(has(\"resources\")) |
    .apiGroup as \$g | .resources[] as \$res | .verbs[] as \$v | (.resourceNames // [\"*\"])[] as \$n |
    .scope + \"|\" + \$g + \"|\" + \$res + \"|\" + \$v + \"|\" + \$n" "${inventory}"
  yq -r ".rules[] | select(.roles[] == \"${role}\") | select(has(\"nonResourceURLs\")) |
    .nonResourceURLs[] as \$u | .verbs[] as \$v | .scope + \"|nonResourceURL|\" + \$u + \"|\" + \$v + \"|*\"" \
    "${inventory}" | grep -v -e '^---$' -e '^$' || true
}

assert_rbac_matches_inventory() {
  local manifest=${1} role=${2}
  local want got
  want="$(inventory_rbac "${role}" | sort -u)"
  got="$(rendered_rbac "${manifest}" | sort -u)"
  [[ -n "${want}" ]] || fail "no ${role} rules in ${inventory}"
  if [[ "${got}" != "${want}" ]]; then
    diff <(printf '%s\n' "${want}") <(printf '%s\n' "${got}") >&2 || true
    fail "validator RBAC for role ${role} differs from the inventory (< inventory, > chart)"
  fi
  # Each Role and ClusterRole must be bound to the validator ServiceAccount.
  local sa
  local component='.metadata.labels."app.kubernetes.io/component" == "validation"'
  sa="$(yq "select(.kind == \"ServiceAccount\" and ${component}) | .metadata.name" "${manifest}")"
  for kind in ClusterRole Role; do
    assert_eq "${sa}" "$(yq "select(.kind == \"${kind}Binding\" and ${component}) | .subjects[0].name" "${manifest}")" \
      "${kind} is bound to the validator ServiceAccount"
  done
  echo "ok validator RBAC matches the ${role} inventory"
}

assert_rbac_matches_inventory "${full}" control-plane

default_role="${work_dir}/default-role.yaml"
render "${default_role}" --set clusterValidator.enabled=true \
  --set "tolerations[0].key=dedicated" --set "tolerations[0].operator=Exists"
assert_rbac_matches_inventory "${default_role}" compute-plane
assert_eq "" "$(yq 'select(.kind == "ClusterRole" and .metadata.labels."app.kubernetes.io/component" == "validation") |
    .rules[] | select(.resources[] == "daemonsets") | .verbs[]' "${default_role}")" \
  "the default role grants no DaemonSet access"

# --- Default (compute-plane) role ---------------------------------------------
assert_eq "" "$(env_value "${init_env}" VALIDATOR_PREFLIGHT "${default_role}")" \
  "init container writes the summary under the default role"
assert_eq "true" "$(env_value "${init_env}" VALIDATOR_STARTUP_GATE "${default_role}")" \
  "init container fails only on observed failures under the default role"
assert_eq "" "$(env_value "${operator_env}" NVCA_CLUSTER_VALIDATOR_CRONJOB "${default_role}")" \
  "no operator-started run when the init container writes the summary"
assert_eq "" "$(env_value "${job_env}" NVCF_EXTERNAL_COMPONENTS "${default_role}")" \
  "no external components unless configured"
assert_eq "dedicated" "$(yq "${pod} | .tolerations[] | select(.key == \"dedicated\") | .key" "${default_role}")" \
  "validator Job carries the operator's tolerations"

disabled="${work_dir}/disabled.yaml"
render "${disabled}"
assert_eq "" "$(env_value "${operator_env}" NVCA_CLUSTER_VALIDATOR_ENABLED "${disabled}")" \
  "the operator is not told the validator runs when it is disabled"
assert_eq "" "$(yq 'select(.metadata.labels."app.kubernetes.io/component" == "validation") | .kind' "${disabled}")" \
  "nothing validator-related renders when it is disabled"
echo "ok default and disabled roles"

# --- Schema --------------------------------------------------------------------
rejects() {
  local message=${1}
  shift
  if render /dev/null --set clusterValidator.enabled=true "$@" 2>/dev/null; then
    fail "schema accepted ${message}"
  fi
}
rejects "an unknown clusterValidator.role" --set "clusterValidator.role=controlplane"
rejects "a gateway name without a namespace" --set "clusterValidator.gatewayNames={gateway}"
rejects "an unknown external component" --set "clusterValidator.externalComponents={redis}"
render /dev/null --set clusterValidator.enabled=true --set "clusterValidator.gatewayNames={gw/shared-gw}" \
  --set "clusterValidator.externalComponents={nats,openbao,cassandra}"
echo "ok schema"

echo "cluster-validator: all checks passed"
