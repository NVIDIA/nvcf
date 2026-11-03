#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
chart_dir="${repo_root}/deploy/helm/icms/icms-api"
work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT
sis_tag="$(yq -r '.sis.image.tag' "${chart_dir}/values.yaml")"
migrations_tag="$(yq -r '.sis.lls.hmacRotation.image.tag' "${chart_dir}/values.yaml")"
test "$(yq -r '.appVersion' "${chart_dir}/Chart.yaml")" = "${sis_tag}"
test "$(yq -r '.sis.lls.hmacRotation.image.tag' "${chart_dir}/values.versions.yaml")" = "${migrations_tag}"

# Runtime defaults must survive chart metadata stamping at packaging time.
helm package "${chart_dir}" --version 99.0.0 --app-version 99.0.0 -d "${work_dir}" >/dev/null
package="${work_dir}/$(yq -r '.name' "${chart_dir}/Chart.yaml")-99.0.0.tgz"
render() {
  helm template sis "${package}" -n sis \
    --set sis.image.registry=example.com \
    --set sis.image.repository=custom/sis \
    --set sis.lls.enabled=true \
    --set sis.lls.hmacRotation.image.registry=example.com \
    --set sis.lls.hmacRotation.image.repository=custom/migrations "$@"
}
job='select(.kind == "Job" and .metadata.name == "addons-lls-migrations") | .spec.template.spec.containers[0]'
cron='select(.kind == "CronJob" and .metadata.name == "addons-lls-turn-hmac-rotation") | .spec.jobTemplate.spec.template.spec.containers[0]'
rendered="${work_dir}/rendered.yaml"
for minor in '' 1.36 1.37; do
  selector_args=()
  if [[ -n "${minor}" ]]; then
    selector_args=(--set-string "sis.lls.hmacRotation.kubectlMinor=${minor}")
  fi
  render "${selector_args[@]}" >"${rendered}"
  for container in "${job}" "${cron}"; do
    test "$(yq -r "${container} | .image" "${rendered}")" = "example.com/custom/migrations:${migrations_tag}"
    test "$(yq -r "${container} | .env[] | select(.name == \"NVCF_KUBECTL_MINOR\") | .value" "${rendered}")" = "${minor}"
    if [[ -n "${minor}" ]]; then
      test "$(yq -r "${container} | .env[] | select(.name == \"NVCF_KUBECTL_MINOR\") | .value | tag" "${rendered}")" = '!!str'
    fi
  done
  test "$(yq -r 'select(.kind == "Deployment") | .spec.template.spec.containers[0].image' "${rendered}")" = "example.com/custom/sis:${sis_tag}"
done

render --set-string sis.lls.hmacRotation.kubectlMinor=1.36 \
  --set sis.lls.hmacRotation.image.registry=mirror.example.com \
  --set sis.lls.hmacRotation.image.repository=other/migrations \
  --set sis.lls.hmacRotation.image.tag=override \
  --set sis.image.tag=runtime-override >"${rendered}"
for container in "${job}" "${cron}"; do
  test "$(yq -r "${container} | .image" "${rendered}")" = mirror.example.com/other/migrations:override
done
test "$(yq -r 'select(.kind == "Deployment") | .spec.template.spec.containers[0].image' "${rendered}")" = example.com/custom/sis:runtime-override
for invalid in 1.35 1.38 false; do
  if render --set-string "sis.lls.hmacRotation.kubectlMinor=${invalid}" >"${work_dir}/invalid.log" 2>&1; then
    echo "unsupported kubectl minor ${invalid} was accepted" >&2
    exit 1
  fi
  grep -Fq 'sis.lls.hmacRotation.kubectlMinor must be empty, 1.36, or 1.37' "${work_dir}/invalid.log"
done

echo "SIS migration client and packaged image checks passed"
