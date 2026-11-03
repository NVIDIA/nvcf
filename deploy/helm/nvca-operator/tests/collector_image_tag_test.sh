#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# values.yaml is the only place the chart pins collector image tags. The
# release bump rewrites the values paths it declares for the collector and
# nothing else, so a tag copied into a template fallback, the schema, or the
# README goes stale on the next collector release.

set -euo pipefail

chart_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="${chart_root}/nvca-operator"
work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT

fail() {
  echo "collector-image-tag: $*" >&2
  exit 1
}

# Collector tags have the form <upstream>-nv-<release>. Older collector images
# used a plain <upstream> tag, so also match a collector image with one.
tag_pattern='[0-9]+\.[0-9]+\.[0-9]+-nv-[0-9]+\.[0-9]+\.[0-9]+|(byoo|nvcf)-otel-collector:[0-9]+\.[0-9]+\.[0-9]+'
# deployment.yaml compares env overrides with this exact image so it can
# replace the value an older chart packaged. It names that past value, not a
# default, so it never moves with a release and is not a copy.
legacy_override='nvcr\.io/nvidia/nvcf-byoc/byoo-otel-collector:0\.157\.11"'
copies="$(
  grep -rEn --exclude=values.yaml "${tag_pattern}" "${chart}" |
    sed -E "/^[^:]*\/templates\/deployment\.yaml:/ s|${legacy_override}||g" |
    grep -E "${tag_pattern}" || true
)"
if [[ -n "${copies}" ]]; then
  printf '%s\n' "${copies}" >&2
  fail "collector tag literal outside values.yaml; read the tag from values instead"
fi

render() {
  local manifest="$1"
  shift
  helm template nvca-operator "${chart}" \
    --namespace nvca-operator \
    --set-string "ngcConfig.serviceKey=test-service-key" \
    --set-string "fullnameOverride=nvca-operator" \
    --set-string ngcConfig.clusterSource=self-managed \
    --set-string selfManaged.icmsServiceURL=http://icms.example.invalid:8080 \
    --set-string selfManaged.revalServiceURL=http://reval.example.invalid:8080 \
    --set-string selfManaged.natsURL=nats://nats.example.invalid:4222 \
    "$@" >"${manifest}"
}

operator_container='select(.kind == "Deployment" and .metadata.name == "nvca-operator") |
  .spec.template.spec.containers[] | select(.name == "nvca-operator")'

# operator_tag prints OTEL_COLLECTOR_IMAGE_TAG, or nothing when it is not set.
operator_tag() {
  yq -r "${operator_container} | .env[] | select(.name == \"OTEL_COLLECTOR_IMAGE_TAG\") | .value" "$1"
}

# byoo_image prints BYOO_OTEL_COLLECTOR_CONTAINER from FLAG's overrides, or
# nothing when the chart passes no such override.
byoo_image() {
  local encoded
  encoded="$(
    yq -r "${operator_container} | .args[]" "$1" |
      awk -v flag="$2" 'take { print; exit } $0 == flag { take = 1 }'
  )"
  if [[ -n "${encoded}" ]]; then
    ENCODED="${encoded}" yq -n -r \
      'strenv(ENCODED) | @base64d | from_json | .BYOO_OTEL_COLLECTOR_CONTAINER // ""'
  fi
}

assert_tags() {
  local manifest="$1" want_operator="$2" want_byoo="$3" case_name="$4" flag got
  got="$(operator_tag "${manifest}")"
  test "${got}" = "${want_operator}" ||
    fail "${case_name}: OTEL_COLLECTOR_IMAGE_TAG is '${got}', expected '${want_operator}'"
  for flag in --function-env-overrides-b64 --task-env-overrides-b64; do
    got="$(byoo_image "${manifest}" "${flag}")"
    test "${got}" = "${want_byoo}" ||
      fail "${case_name}: ${flag} collector is '${got}', expected '${want_byoo}'"
  done
}

byoo_repository="nvcr.io/nvidia/nvcf-byoc/byoo-otel-collector"

default_manifest="${work_dir}/default.yaml"
render "${default_manifest}"
assert_tags "${default_manifest}" \
  "$(yq -r '.otelCollector.imageTag' "${chart}/values.yaml")" \
  "${byoo_repository}:$(yq -r '.agent.byooOtelCollector.imageTag' "${chart}/values.yaml")" \
  "values.yaml default"

set_manifest="${work_dir}/set.yaml"
render "${set_manifest}" \
  --set-string otelCollector.imageTag=1.2.3-test \
  --set-string agent.byooOtelCollector.imageTag=4.5.6-test
assert_tags "${set_manifest}" "1.2.3-test" "${byoo_repository}:4.5.6-test" "explicit tag"

# With the collector disabled, an empty tag keeps the chart out of the way:
# no OTEL_COLLECTOR_IMAGE_TAG and no BYOO_OTEL_COLLECTOR_CONTAINER override.
empty_manifest="${work_dir}/empty.yaml"
render "${empty_manifest}" \
  --set-string otelCollector.imageTag= \
  --set-string agent.byooOtelCollector.imageTag=
assert_tags "${empty_manifest}" "" "" "empty tag"

# null removes the key from the chart defaults. It must not bring back a
# literal tag that the release bump does not maintain.
null_manifest="${work_dir}/null.yaml"
render "${null_manifest}" \
  --set otelCollector.imageTag=null \
  --set agent.byooOtelCollector.imageTag=null
assert_tags "${null_manifest}" "" "" "null tag"

# An NVCFBackend tag takes precedence over OTEL_COLLECTOR_IMAGE_TAG, so an
# enabled backend collector with its own tag does not need the operator tag.
backend_tag_manifest="${work_dir}/backend-tag.yaml"
render "${backend_tag_manifest}" \
  --set-string otelCollector.imageTag= \
  --set selfManaged.otelCollector.enabled=true
assert_tags "${backend_tag_manifest}" "" \
  "${byoo_repository}:$(yq -r '.agent.byooOtelCollector.imageTag' "${chart}/values.yaml")" \
  "enabled backend collector with its own tag"

# expect_render_failure CASE HELM_ARGS... checks that an enabled collector
# without any tag fails the render and names the value to set. Otherwise the
# operator would fall back to a built-in tag that is not kept current.
expect_render_failure() {
  local case_name="$1" output="${work_dir}/failure.log"
  shift
  if render "${work_dir}/failure.yaml" "$@" 2>"${output}"; then
    fail "${case_name}: render succeeded without a collector tag"
  fi
  grep -Fq "otelCollector.imageTag is required" "${output}" ||
    fail "${case_name}: render failed without naming otelCollector.imageTag: $(cat "${output}")"
}

expect_render_failure "enabled with empty tag" \
  --set otelCollector.enabled=true \
  --set-string otelCollector.imageTag=
expect_render_failure "enabled with null tag" \
  --set otelCollector.enabled=true \
  --set otelCollector.imageTag=null
expect_render_failure "enabled backend collector without a tag" \
  --set selfManaged.otelCollector.enabled=true \
  --set-string selfManaged.otelCollector.imageTag= \
  --set-string otelCollector.imageTag=

echo "collector-image-tag: all checks passed"
