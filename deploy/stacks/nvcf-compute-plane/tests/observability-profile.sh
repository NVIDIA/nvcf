#!/usr/bin/env bash
set -euo pipefail

stack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d)"
cluster_name="observability-profile-test-$$"
output_dir="$work_dir/render-output"
registration_values="$output_dir/$cluster_name-register-values.yaml"
trap 'rm -rf "$work_dir"' EXIT

fail() {
  echo "observability-profile: $*" >&2
  exit 1
}

mkdir -p "$output_dir"
cp "$stack_dir/testdata/registration/ncp-local-register-values.yaml" \
  "$registration_values"

render_values() {
  local profile="$1"
  local output_file="$2"
  local profile_args=()
  shift 2

  if [[ "$profile" != "default" ]]; then
    profile_args=(--state-values-set "observability.profile=$profile")
  fi

  HELMFILE_ENV=base \
    CLUSTER_NAME="$cluster_name" \
    NCA_ID=nvcf-default \
    OUTPUT_DIR="$output_dir" \
    helmfile \
      --file "$stack_dir/helmfile.d/02-nvca.yaml.gotmpl" \
      --environment default \
      "${profile_args[@]+"${profile_args[@]}"}" \
      "$@" \
      --selector name=nvca-operator \
      write-values \
      --output-file-template "$output_file" >/dev/null
}

collector_enabled() {
  awk '
    /^selfManaged:$/ { self_managed = 1; next }
    self_managed && /^  otelCollector:$/ { collector = 1; next }
    collector && /^    enabled:/ { print $2; exit }
  ' "$1"
}

collector_image_tag() {
  awk '
    /^selfManaged:$/ { self_managed = 1; next }
    self_managed && /^  otelCollector:$/ { collector = 1; next }
    collector && /^    imageTag:/ { gsub(/"/, "", $2); print $2; exit }
  ' "$1"
}

collector_image_repository() {
  awk '
    /^selfManaged:$/ { self_managed = 1; next }
    self_managed && /^  otelCollector:$/ { collector = 1; next }
    collector && /^    imageRepository:/ { gsub(/"/, "", $2); print $2; exit }
  ' "$1"
}

operator_image_tag() {
  awk '
    /^image:$/ { image = 1; next }
    image && /^  tag:/ { gsub(/"/, "", $2); print $2; exit }
  ' "$1"
}

nvca_version() {
  awk '
    /^selfManaged:$/ { self_managed = 1; next }
    self_managed && /^  nvcaVersion:/ { gsub(/"/, "", $2); print $2; exit }
  ' "$1"
}

for profile in default disabled control compute all; do
  values="$work_dir/$profile.yaml"
  render_values "$profile" "$values"

  case "$profile" in
    default|disabled|control|compute|all)
      test "$(collector_enabled "$values")" = "false" ||
        fail "$profile profile enabled the self-managed NVCA collector by default"
      ;;
  esac
done

expected_nvca_version="3.2.19"
test "$(operator_image_tag "$work_dir/default.yaml")" = "$expected_nvca_version" ||
  fail "default operator image tag is not $expected_nvca_version"
test "$(nvca_version "$work_dir/default.yaml")" = "$expected_nvca_version" ||
  fail "default NVCA version is not $expected_nvca_version"

render_values compute "$work_dir/ncp-dev-default.yaml" \
  --state-values-set-string global.image.registry=nvcr.io \
  --state-values-set-string global.image.repository=0651155215864979/ncp-dev

test "$(collector_image_repository "$work_dir/ncp-dev-default.yaml")" = \
  "nvcr.io/0651155215864979/ncp-dev/nvcf-otel-collector" ||
  fail "collector image repository did not inherit the global image repository"

render_values compute "$work_dir/compute-overrides.yaml" \
  --state-values-set-string global.nvcaOperator.imageTag=operator-test-tag \
  --state-values-set-string global.nvcaOperator.selfManaged.nvcaVersion=nvca-test-tag \
  --state-values-set global.nvcaOperator.selfManaged.otelCollector.enabled=false \
  --state-values-set-string global.nvcaOperator.selfManaged.otelCollector.imageRepository=registry.example.com/nvcf/collector \
  --state-values-set-string global.nvcaOperator.selfManaged.otelCollector.imageTag=test-tag

test "$(collector_enabled "$work_dir/compute-overrides.yaml")" = "false" ||
  fail "explicit collector disable did not override the compute profile"
grep -Eq '^    imageRepository: "?registry\.example\.com/nvcf/collector"?$' \
  "$work_dir/compute-overrides.yaml" ||
  fail "explicit collector image repository was not preserved"
test "$(collector_image_tag "$work_dir/compute-overrides.yaml")" = "test-tag" ||
  fail "explicit collector image tag was not preserved"
test "$(operator_image_tag "$work_dir/compute-overrides.yaml")" = "operator-test-tag" ||
  fail "operator image tag override was not preserved"
test "$(nvca_version "$work_dir/compute-overrides.yaml")" = "nvca-test-tag" ||
  fail "NVCA version override was not preserved"

render_values control "$work_dir/control-overrides.yaml" \
  --state-values-set global.nvcaOperator.selfManaged.otelCollector.enabled=true

test "$(collector_enabled "$work_dir/control-overrides.yaml")" = "true" ||
  fail "explicit collector enable did not override the control profile"

if render_values invalid "$work_dir/invalid.yaml" \
  >"$work_dir/invalid.log" 2>&1; then
  fail "invalid observability profile was accepted"
fi
grep -q 'observability.profile must be disabled, control, compute, or all' \
  "$work_dir/invalid.log" ||
  fail "invalid profile did not return the expected error"

echo "observability-profile: all checks passed"
