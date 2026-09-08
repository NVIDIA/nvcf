#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHART="${ROOT_DIR}/helm-nvcf-vanity-gateway"
VALUES="${ROOT_DIR}/../../../tools/ci/helm-validate-values/vanity-gateway.yaml"

label='nvcf.nvidia.com/observability-target: "true"'

assert_single_label() {
  local rendered="$1" label_count
  label_count="$(grep -F -c -- "${label}" <<<"${rendered}" || true)"
  if [[ "${label_count}" -ne 1 ]]; then
    echo "FAILED: ServiceMonitor contains ${label_count} copies of ${label}" >&2
    exit 1
  fi
}

rendered="$(helm template vanity-gateway "${CHART}" -f "${VALUES}" --show-only templates/servicemonitor.yaml)"
assert_single_label "${rendered}"

rendered="$(helm template vanity-gateway "${CHART}" -f "${VALUES}" --show-only templates/servicemonitor.yaml \
  --set-string 'vanityGateway.serviceMonitor.labels.nvcf\.nvidia\.com/observability-target=true' \
  --set-string 'vanityGateway.serviceMonitor.labels.team=observability')"
assert_single_label "${rendered}"
grep -F -q -- 'team: observability' <<<"${rendered}"

echo "ok: ServiceMonitor includes one ${label}"
