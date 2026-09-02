#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

stack_root="$(cd "$(dirname "$0")/.." && pwd)"
monorepo_root="$(cd "${stack_root}/../../.." && pwd)"
chart_path="${monorepo_root}/deploy/helm/nvca-operator/nvca-operator"
tmp_dir="$(mktemp -d)"
test_stack="${tmp_dir}/stack"

cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

cp -R "${stack_root}/." "${test_stack}"

write_environment() {
  local id="$1"
  local environment_file="$2"

  cat > "${environment_file}" <<EOF
global:
  helm:
    sources:
      registry: nvcr.io
      repository: test/example
  image:
    registry: nvcr.io
    repository: test/example
  controlPlane:
    id: "${id}"
  nvcaOperator:
    chartPath: "${chart_path}"
    selfManaged:
      icmsServiceURL: http://icms.example.invalid:8080
      revalServiceURL: http://reval.example.invalid:8080
      natsURL: nats://nats.example.invalid:4222
observability:
  profile: disabled
addons:
  topologyAwareScheduling:
    enabled: false
  kaiScheduler:
    enabled: false
  groveOperator:
    enabled: false
  dynamoOperator:
    enabled: false
EOF
}

write_registration() {
  local cluster_name="$1"
  local output_dir="$2"

  mkdir -p "${output_dir}"
  cat > "${output_dir}/${cluster_name}-register-values.yaml" <<EOF
clusterName: ${cluster_name}
clusterID: 00000000-0000-0000-0000-00000000000${cluster_name: -1}
clusterGroupID: 10000000-0000-0000-0000-00000000000${cluster_name: -1}
ncaID: nvcf-default
selfManaged:
  identitySource: psat
EOF
}

render_plane() {
  local id="$1"
  local cluster_name="$2"
  local output_dir="${tmp_dir}/${id}-out"

  write_environment "${id}" "${test_stack}/environments/${id}.yaml"
  write_registration "${cluster_name}" "${output_dir}"

  HELMFILE_ENV="${id}" \
  CLUSTER_NAME="${cluster_name}" \
  NCA_ID=nvcf-default \
  OUTPUT_DIR="${output_dir}" \
  PATH="${stack_root}/bin:${PATH}" \
  HELM_PLUGINS="${stack_root}/bin/helm-plugins" \
    "${stack_root}/bin/helmfile" --file "${test_stack}/helmfile.d/02-nvca.yaml.gotmpl" \
      --environment default --selector release-group=workers template \
      --output-dir "${output_dir}/rendered" \
      --output-dir-template '{{ .OutputDir }}/{{ .Release.Name }}'

  find "${output_dir}/rendered" -type f -name '*.yaml' -exec cat {} + > "${tmp_dir}/${id}.yaml"
}

render_shared_prerequisites() {
  local id="$1"
  local output_dir="${tmp_dir}/shared-out"

  mkdir -p "${output_dir}"
  HELMFILE_ENV="${id}" \
  PATH="${stack_root}/bin:${PATH}" \
  HELM_PLUGINS="${stack_root}/bin/helm-plugins" \
    "${stack_root}/bin/helmfile" --file "${test_stack}/helmfile.d/01-dependencies.yaml.gotmpl" \
      --environment default --selector release-group=shared-prerequisites template \
      --output-dir "${output_dir}/rendered" \
      --output-dir-template '{{ .OutputDir }}/{{ .Release.Name }}'

  find "${output_dir}/rendered" -type f -name '*.yaml' -exec cat {} + > "${tmp_dir}/shared.yaml"
}

render_environment_override_pair() {
  local control_plane_id="$1"
  local environment_name="override-empty"
  local cluster_name="cluster-override"
  local output_dir="${tmp_dir}/override-out"

  write_environment "" "${test_stack}/environments/${environment_name}.yaml"
  write_registration "${cluster_name}" "${output_dir}"

  CONTROL_PLANE_ID="${control_plane_id}" \
  HELMFILE_ENV="${environment_name}" \
  CLUSTER_NAME="${cluster_name}" \
  NCA_ID=nvcf-default \
  OUTPUT_DIR="${output_dir}" \
  PATH="${stack_root}/bin:${PATH}" \
  HELM_PLUGINS="${stack_root}/bin/helm-plugins" \
    "${stack_root}/bin/helmfile" --file "${test_stack}/helmfile.d/02-nvca.yaml.gotmpl" \
      --environment default --selector release-group=workers template \
      --output-dir "${output_dir}/worker-rendered" \
      --output-dir-template '{{ .OutputDir }}/{{ .Release.Name }}'

  CONTROL_PLANE_ID="${control_plane_id}" \
  HELMFILE_ENV="${environment_name}" \
  PATH="${stack_root}/bin:${PATH}" \
  HELM_PLUGINS="${stack_root}/bin/helm-plugins" \
    "${stack_root}/bin/helmfile" --file "${test_stack}/helmfile.d/01-dependencies.yaml.gotmpl" \
      --environment default --selector release-group=shared-prerequisites template \
      --output-dir "${output_dir}/shared-rendered" \
      --output-dir-template '{{ .OutputDir }}/{{ .Release.Name }}'

  find "${output_dir}/worker-rendered" -type f -name '*.yaml' -exec cat {} + > "${tmp_dir}/override-worker.yaml"
  find "${output_dir}/shared-rendered" -type f -name '*.yaml' -exec cat {} + > "${tmp_dir}/override-shared.yaml"
}

assert_equal() {
  local expected="$1"
  local actual="$2"
  local description="$3"

  if [[ "${actual}" != "${expected}" ]]; then
    printf 'expected %s to be %q, got %q\n' "${description}" "${expected}" "${actual}" >&2
    exit 1
  fi
}

assert_no_collisions() {
  local manifest_a="$1"
  local manifest_b="$2"
  local resources_a="${tmp_dir}/resources-a.txt"
  local resources_b="${tmp_dir}/resources-b.txt"
  local collisions="${tmp_dir}/collisions.txt"

  yq -r 'select(.kind != null and .kind != "CustomResourceDefinition") |
    [.apiVersion, .kind, (.metadata.namespace // "<cluster>"), .metadata.name] | @tsv' \
    "${manifest_a}" | sed '/^$/d' | sort -u > "${resources_a}"
  yq -r 'select(.kind != null and .kind != "CustomResourceDefinition") |
    [.apiVersion, .kind, (.metadata.namespace // "<cluster>"), .metadata.name] | @tsv' \
    "${manifest_b}" | sed '/^$/d' | sort -u > "${resources_b}"
  comm -12 "${resources_a}" "${resources_b}" > "${collisions}"

  if [[ -s "${collisions}" ]]; then
    echo "compute-plane stack renders collide:" >&2
    cat "${collisions}" >&2
    exit 1
  fi
}

run_named_destroy() {
  local namespace_label="$1"
  local lifecycle_log="$2"
  local output_file="$3"
  local fake_bin="${tmp_dir}/fake-bin"

  mkdir -p "${fake_bin}"
  cat > "${fake_bin}/helmfile" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'helmfile:%s\n' "$*" >> "${LIFECYCLE_LOG}"
EOF
  cat > "${fake_bin}/kubectl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

command_line="$*"
if [[ "${command_line}" == *'get namespaces'* ]] && [[ "${command_line}" == *'nvcf.nvidia.com/control-plane-id'* ]]; then
  printf '%s' "${FAKE_ACTIVE_NAMED_NAMESPACES:-}"
  exit 0
fi
if [[ "${command_line}" == *'get namespace'* ]]; then
  namespace=""
  previous=""
  for argument in "$@"; do
    if [[ "${previous}" == "namespace" ]]; then
      namespace="${argument}"
      break
    fi
    previous="${argument}"
  done
  printf 'get:%s\n' "${namespace}" >> "${LIFECYCLE_LOG}"
  if [[ "${namespace}" == "plane-a-nvca-operator" ]]; then
    if [[ "${FAKE_PLANE_A_EXISTS:-1}" != "1" ]]; then
      exit 1
    fi
    printf '%s' "${FAKE_PLANE_A_LABEL}"
    exit 0
  fi
  if [[ "${namespace}" == "plane-b-nvca-operator" ]]; then
    printf '%s' 'plane-b'
    exit 0
  fi
  exit 1
fi

if [[ "${command_line}" == *'create namespace'* ]]; then
  namespace=""
  previous=""
  for argument in "$@"; do
    if [[ "${previous}" == "namespace" ]]; then
      namespace="${argument}"
      break
    fi
    previous="${argument}"
  done
  printf 'create:%s\n' "${namespace}" >> "${LIFECYCLE_LOG}"
  exit 0
fi

if [[ "${command_line}" == *'label namespace'* ]]; then
  namespace=""
  ownership_label=""
  previous=""
  for argument in "$@"; do
    if [[ "${previous}" == "namespace" ]]; then
      namespace="${argument}"
    fi
    if [[ "${argument}" == nvcf.nvidia.com/control-plane-id=* ]]; then
      ownership_label="${argument#*=}"
    fi
    previous="${argument}"
  done
  printf 'label:%s:%s\n' "${namespace}" "${ownership_label}" >> "${LIFECYCLE_LOG}"
  exit 0
fi

if [[ "${command_line}" == *'delete namespace'* ]]; then
  namespace=""
  previous=""
  for argument in "$@"; do
    if [[ "${previous}" == "namespace" ]]; then
      namespace="${argument}"
      break
    fi
    previous="${argument}"
  done
  printf 'delete:%s\n' "${namespace}" >> "${LIFECYCLE_LOG}"
  exit 0
fi

printf 'unexpected-kubectl:%s\n' "${command_line}" >> "${LIFECYCLE_LOG}"
exit 1
EOF
  chmod +x "${fake_bin}/helmfile" "${fake_bin}/kubectl"

  PATH="${fake_bin}:${PATH}" \
  DEV_BIN_DIR="${fake_bin}" \
  LIFECYCLE_LOG="${lifecycle_log}" \
  FAKE_PLANE_A_LABEL="${namespace_label}" \
    make -C "${test_stack}" destroy \
      CLUSTER_NAME=cluster-a \
      HELMFILE_ENV=override-empty \
      CONTROL_PLANE_ID=plane-a \
      OUTPUT_DIR="${tmp_dir}/plane-a-out" > "${output_file}" 2>&1
}

run_named_install() {
  local namespace_exists="$1"
  local namespace_label="$2"
  local lifecycle_log="$3"
  local output_file="$4"
  local fake_bin="${tmp_dir}/fake-bin"

  PATH="${fake_bin}:${PATH}" \
  DEV_BIN_DIR="${fake_bin}" \
  LIFECYCLE_LOG="${lifecycle_log}" \
  FAKE_PLANE_A_EXISTS="${namespace_exists}" \
  FAKE_PLANE_A_LABEL="${namespace_label}" \
    make -C "${test_stack}" install \
      CLUSTER_NAME=cluster-a \
      HELMFILE_ENV=override-empty \
      CONTROL_PLANE_ID=plane-a \
      REGISTRATION_VALUES_DIR="${tmp_dir}/plane-a-out" \
      OUTPUT_DIR="${tmp_dir}/named-install-out" > "${output_file}" 2>&1
}

run_shared_destroy() {
  local active_named_namespaces="$1"
  local lifecycle_log="$2"
  local output_file="$3"
  local fake_bin="${tmp_dir}/fake-bin"

  PATH="${fake_bin}:${PATH}" \
  DEV_BIN_DIR="${fake_bin}" \
  LIFECYCLE_LOG="${lifecycle_log}" \
  FAKE_PLANE_A_LABEL=plane-a \
  FAKE_ACTIVE_NAMED_NAMESPACES="${active_named_namespaces}" \
    make -C "${test_stack}" destroy-shared-prerequisites \
      HELMFILE_ENV=override-empty > "${output_file}" 2>&1
}

"${stack_root}/bin/helmfile" version >/dev/null 2>&1 || {
  echo "pinned helmfile is missing; run 'make ensure-binaries' first" >&2
  exit 1
}

render_plane plane-a cluster-a
render_plane plane-b cluster-b
render_shared_prerequisites plane-a
render_environment_override_pair plane-env

manifest_a="${tmp_dir}/plane-a.yaml"
manifest_b="${tmp_dir}/plane-b.yaml"
shared_manifest="${tmp_dir}/shared.yaml"
override_worker_manifest="${tmp_dir}/override-worker.yaml"
override_shared_manifest="${tmp_dir}/override-shared.yaml"

assert_equal plane-a-nvca-operator "$(yq -r 'select(.kind == "Deployment") | .metadata.name' "${manifest_a}")" "plane A operator name"
assert_equal plane-b-nvca-operator "$(yq -r 'select(.kind == "Deployment") | .metadata.name' "${manifest_b}")" "plane B operator name"
assert_equal plane-a-nvca-operator "$(yq -r 'select(.kind == "Deployment") | .metadata.namespace' "${manifest_a}")" "plane A operator namespace"
assert_equal plane-b-nvca-operator "$(yq -r 'select(.kind == "Deployment") | .metadata.namespace' "${manifest_b}")" "plane B operator namespace"
assert_equal 0 "$(grep -Fc 'kind: CustomResourceDefinition' "${manifest_a}")" "plane A worker CRD ownership"
assert_equal 0 "$(grep -Fc 'kind: CustomResourceDefinition' "${manifest_b}")" "plane B worker CRD ownership"
assert_equal 1 "$(grep -Fc 'kind: CustomResourceDefinition' "${shared_manifest}")" "shared prerequisite CRD ownership"
assert_equal nvcfbackends.nvcf.nvidia.io "$(yq -r 'select(.kind == "CustomResourceDefinition") | .metadata.name' "${shared_manifest}")" "shared prerequisite CRD name"
assert_equal plane-env-nvca-operator "$(yq -r 'select(.kind == "Deployment") | .metadata.name' "${override_worker_manifest}")" "environment-overridden worker name"
assert_equal 1 "$(grep -Fc 'kind: CustomResourceDefinition' "${override_shared_manifest}")" "environment-overridden shared prerequisite CRD ownership"
assert_no_collisions "${manifest_a}" "${manifest_b}"

override_shared_dry_run="$({
  make -C "${test_stack}" -n install-shared-prerequisites \
    CONTROL_PLANE_ID=plane-env \
    HELMFILE_ENV=override-empty
} 2>&1)"
if [[ "${override_shared_dry_run}" != *'helmfile.d/01-dependencies.yaml.gotmpl'* ]]; then
  printf 'install-shared-prerequisites dry-run did not select the shared dependency helmfile:\n%s\n' "${override_shared_dry_run}" >&2
  exit 1
fi

destroy_dry_run="$({
  make -C "${test_stack}" -n destroy \
    CLUSTER_NAME=cluster-a \
    HELMFILE_ENV=plane-a \
    OUTPUT_DIR="${tmp_dir}/plane-a-out"
} 2>&1)"
if [[ "${destroy_dry_run}" != *'--selector release-group=workers'* ]]; then
  printf 'expected named destroy to select only the per-instance release, got:\n%s\n' "${destroy_dry_run}" >&2
  exit 1
fi
if [[ "${destroy_dry_run}" == *'delete namespace "nvca-shared-system"'* ]] ||
   [[ "${destroy_dry_run}" == *'delete namespace "kai-scheduler"'* ]] ||
   [[ "${destroy_dry_run}" == *'delete namespace "grove-system"'* ]] ||
   [[ "${destroy_dry_run}" == *'delete namespace "dynamo-system"'* ]]; then
  printf 'named destroy attempts to delete shared prerequisite namespaces:\n%s\n' "${destroy_dry_run}" >&2
  exit 1
fi
expected_namespace_cleanup="ns=\"\${CONTROL_PLANE_ID}-nvca-operator\""
if [[ "${destroy_dry_run}" != *"${expected_namespace_cleanup}"* ]]; then
  printf 'named destroy does not limit namespace cleanup to plane A:\n%s\n' "${destroy_dry_run}" >&2
  exit 1
fi
if [[ "${destroy_dry_run}" != *'nvcf\.nvidia\.com/control-plane-id'* ]]; then
  printf 'named destroy does not verify namespace ownership before cleanup:\n%s\n' "${destroy_dry_run}" >&2
  exit 1
fi

legacy_destroy_dry_run="$({
  make -C "${test_stack}" -n destroy \
    CLUSTER_NAME=legacy \
    HELMFILE_ENV=default \
    OUTPUT_DIR="${tmp_dir}/legacy-out"
} 2>&1)"
if [[ "${legacy_destroy_dry_run}" == *'--selector release-group=workers'* ]]; then
  echo "legacy destroy unexpectedly selects only the per-instance release" >&2
  exit 1
fi
for legacy_namespace in nvca-operator grove-system dynamo-system kai-scheduler; do
  if [[ "${legacy_destroy_dry_run}" != *"${legacy_namespace}"* ]]; then
    printf 'legacy destroy no longer includes namespace %s\n' "${legacy_namespace}" >&2
    exit 1
  fi
done

positive_lifecycle_log="${tmp_dir}/positive-lifecycle.log"
positive_lifecycle_output="${tmp_dir}/positive-lifecycle.out"
: > "${positive_lifecycle_log}"
run_named_destroy plane-a "${positive_lifecycle_log}" "${positive_lifecycle_output}"
if ! grep -Fxq 'delete:plane-a-nvca-operator' "${positive_lifecycle_log}"; then
  printf 'owned plane A namespace was not deleted:\n%s\n' "$(cat "${positive_lifecycle_log}")" >&2
  exit 1
fi
if grep -Fxq 'delete:plane-b-nvca-operator' "${positive_lifecycle_log}"; then
  printf 'plane A teardown deleted plane B namespace:\n%s\n' "$(cat "${positive_lifecycle_log}")" >&2
  exit 1
fi

for negative_case in absent mismatched; do
  negative_label=""
  if [[ "${negative_case}" == "mismatched" ]]; then
    negative_label=plane-b
  fi
  negative_lifecycle_log="${tmp_dir}/${negative_case}-lifecycle.log"
  negative_lifecycle_output="${tmp_dir}/${negative_case}-lifecycle.out"
  : > "${negative_lifecycle_log}"
  if run_named_destroy "${negative_label}" "${negative_lifecycle_log}" "${negative_lifecycle_output}"; then
    printf 'named destroy unexpectedly accepted %s namespace ownership:\n%s\n' \
      "${negative_case}" "$(cat "${negative_lifecycle_output}")" >&2
    exit 1
  fi
  if grep -Fxq 'delete:plane-a-nvca-operator' "${negative_lifecycle_log}"; then
    printf 'named destroy deleted plane A with %s ownership label:\n%s\n' \
      "${negative_case}" "$(cat "${negative_lifecycle_log}")" >&2
    exit 1
  fi
  if grep -Fq 'helmfile:' "${negative_lifecycle_log}"; then
    printf 'named destroy invoked Helmfile before rejecting %s ownership:\n%s\n' \
      "${negative_case}" "$(cat "${negative_lifecycle_log}")" >&2
    exit 1
  fi
  if ! grep -Fq 'refusing to delete' "${negative_lifecycle_output}"; then
    printf 'named destroy did not explain %s ownership refusal:\n%s\n' \
      "${negative_case}" "$(cat "${negative_lifecycle_output}")" >&2
    exit 1
  fi
done

active_shared_lifecycle_log="${tmp_dir}/active-shared-lifecycle.log"
active_shared_lifecycle_output="${tmp_dir}/active-shared-lifecycle.out"
: > "${active_shared_lifecycle_log}"
if run_shared_destroy 'namespace/plane-b-nvca-operator' \
  "${active_shared_lifecycle_log}" "${active_shared_lifecycle_output}"; then
  printf 'shared prerequisite destroy accepted an active named worker:\n%s\n' \
    "$(cat "${active_shared_lifecycle_output}")" >&2
  exit 1
fi
if grep -Fq 'helmfile:' "${active_shared_lifecycle_log}"; then
  printf 'shared prerequisite destroy invoked Helmfile with an active named worker:\n%s\n' \
    "$(cat "${active_shared_lifecycle_log}")" >&2
  exit 1
fi
if ! grep -Fq 'refusing to destroy shared prerequisites' "${active_shared_lifecycle_output}"; then
  printf 'shared prerequisite destroy did not explain its active-worker refusal:\n%s\n' \
    "$(cat "${active_shared_lifecycle_output}")" >&2
  exit 1
fi

inactive_shared_lifecycle_log="${tmp_dir}/inactive-shared-lifecycle.log"
inactive_shared_lifecycle_output="${tmp_dir}/inactive-shared-lifecycle.out"
: > "${inactive_shared_lifecycle_log}"
run_shared_destroy '' "${inactive_shared_lifecycle_log}" "${inactive_shared_lifecycle_output}"
if ! grep -Fq 'helmfile:' "${inactive_shared_lifecycle_log}"; then
  printf 'shared prerequisite destroy did not invoke Helmfile after a clean preflight:\n%s\n' \
    "$(cat "${inactive_shared_lifecycle_log}")" >&2
  exit 1
fi

new_namespace_lifecycle_log="${tmp_dir}/new-namespace-lifecycle.log"
new_namespace_lifecycle_output="${tmp_dir}/new-namespace-lifecycle.out"
: > "${new_namespace_lifecycle_log}"
run_named_install 0 plane-a "${new_namespace_lifecycle_log}" "${new_namespace_lifecycle_output}"
expected_new_namespace_lifecycle=$'get:plane-a-nvca-operator\ncreate:plane-a-nvca-operator\nlabel:plane-a-nvca-operator:plane-a'
if [[ "$(head -n 3 "${new_namespace_lifecycle_log}")" != "${expected_new_namespace_lifecycle}" ]]; then
  printf 'named install did not establish namespace ownership before Helmfile:\n%s\n' \
    "$(cat "${new_namespace_lifecycle_log}")" >&2
  exit 1
fi
if ! grep -Fq 'helmfile:' "${new_namespace_lifecycle_log}"; then
  printf 'named install did not invoke Helmfile after namespace ownership was established:\n%s\n' \
    "$(cat "${new_namespace_lifecycle_log}")" >&2
  exit 1
fi

if make -C "${test_stack}" check-control-plane-id CONTROL_PLANE_ID=default > /dev/null 2>&1; then
  echo "expected Make lifecycle validation to reject reserved CONTROL_PLANE_ID=default" >&2
  exit 1
fi

if CONTROL_PLANE_ID=default HELMFILE_ENV=override-empty \
  PATH="${stack_root}/bin:${PATH}" HELM_PLUGINS="${stack_root}/bin/helm-plugins" \
  "${stack_root}/bin/helmfile" --file "${test_stack}/helmfile.d/01-dependencies.yaml.gotmpl" \
    --environment default --selector release-group=shared-prerequisites template \
    --output-dir "${tmp_dir}/reserved-shared-rendered" > /dev/null 2>&1; then
  echo "expected 01-dependencies to reject reserved CONTROL_PLANE_ID=default" >&2
  exit 1
fi

if CONTROL_PLANE_ID=default HELMFILE_ENV=override-empty CLUSTER_NAME=cluster-override \
  NCA_ID=nvcf-default OUTPUT_DIR="${tmp_dir}/override-out" \
  PATH="${stack_root}/bin:${PATH}" HELM_PLUGINS="${stack_root}/bin/helm-plugins" \
  "${stack_root}/bin/helmfile" --file "${test_stack}/helmfile.d/02-nvca.yaml.gotmpl" \
    --environment default --selector release-group=workers template \
    --output-dir "${tmp_dir}/reserved-worker-rendered" > /dev/null 2>&1; then
  echo "expected 02-nvca to reject reserved CONTROL_PLANE_ID=default" >&2
  exit 1
fi

make_expression_marker="${tmp_dir}/make-expression-executed"
injected_control_plane_id="\$(shell touch ${make_expression_marker})"
if make -C "${test_stack}" check-control-plane-id \
  "CONTROL_PLANE_ID=${injected_control_plane_id}" > /dev/null 2>&1; then
  echo "expected literal Make-expression identity to be rejected" >&2
  exit 1
fi
if [[ -e "${make_expression_marker}" ]]; then
  echo "CONTROL_PLANE_ID expanded and executed a Make expression" >&2
  exit 1
fi

if make -C "${test_stack}" check-control-plane-id \
  "CONTROL_PLANE_ID=plane-a'quoted" > /dev/null 2>&1; then
  echo "expected quoted CONTROL_PLANE_ID to be rejected literally" >&2
  exit 1
fi

write_environment plane_a "${test_stack}/environments/invalid.yaml"
write_registration invalid "${tmp_dir}/invalid-out"
if HELMFILE_ENV=invalid CLUSTER_NAME=invalid NCA_ID=nvcf-default OUTPUT_DIR="${tmp_dir}/invalid-out" \
  PATH="${stack_root}/bin:${PATH}" HELM_PLUGINS="${stack_root}/bin/helm-plugins" \
  "${stack_root}/bin/helmfile" --file "${test_stack}/helmfile.d/02-nvca.yaml.gotmpl" \
    --environment default --selector release-group=workers template \
    --output-dir "${tmp_dir}/invalid-rendered" > /dev/null 2>&1; then
  echo "expected invalid global.controlPlane.id to fail" >&2
  exit 1
fi

echo "validated dual compute-plane isolation, literal identity handling, reserved-ID rejection, namespace ownership, and guarded worker/shared teardown"
