#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2023-2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Verify that monitoring.enabled fully gates the Prometheus integration:
#   - the nginx-prometheus-exporter sidecar container,
#   - the metrics port (9113) on that sidecar,
#   - and the PodMonitor resource.
#
# When monitoring is off, none of those should be rendered. The lua-prometheus
# endpoint that nginx itself exposes on cache-metrics (9145) is independent of
# this gate and stays whether or not the sidecar is in the pod.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OFF_FILE="$(mktemp)"
ON_FILE="$(mktemp)"
trap 'rm -f "${OFF_FILE}" "${ON_FILE}"' EXIT

cd "${ROOT_DIR}"

helm template c ./deploy --set monitoring.enabled=false > "${OFF_FILE}"
helm template c ./deploy --set monitoring.enabled=true  > "${ON_FILE}"

assert_has() {
  local file="$1" needle="$2"
  if ! grep -F -q -- "${needle}" "${file}"; then
    echo "FAILED: expected pattern not found in $(basename "${file}"): ${needle}" >&2
    exit 1
  fi
}

assert_not_has() {
  local file="$1" needle="$2"
  if grep -F -q -- "${needle}" "${file}"; then
    echo "FAILED: unexpected pattern found in $(basename "${file}"): ${needle}" >&2
    exit 1
  fi
}

# ----- Assertions: monitoring disabled --------------------------------------

echo "Checking monitoring.enabled=false render..."
assert_not_has "${OFF_FILE}" 'nginx-prometheus-exporter'
assert_not_has "${OFF_FILE}" 'containerPort: 9113'
assert_not_has "${OFF_FILE}" 'kind: PodMonitor'
# The lua-prometheus port on the cache pod stays regardless.
assert_has "${OFF_FILE}" 'containerPort: 9145'

# ----- Assertions: monitoring enabled ---------------------------------------

echo "Checking monitoring.enabled=true render..."
assert_has "${ON_FILE}" 'name: nginx-prometheus-exporter'
assert_has "${ON_FILE}" 'containerPort: 9113'
assert_has "${ON_FILE}" 'kind: PodMonitor'
assert_has "${ON_FILE}" 'containerPort: 9145'

echo "Monitoring gating checks passed."
