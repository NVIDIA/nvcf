#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Liveness must not use the http-port /health, which checks the invocation
# service. An upstream outage would otherwise restart every vanity pod.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHART="${ROOT_DIR}/helm-nvcf-vanity-gateway"
VALUES="${ROOT_DIR}/../../../tools/ci/helm-validate-values/vanity-gateway.yaml"

rendered="$(helm template vanity-gateway "${CHART}" -f "${VALUES}" --show-only templates/deployment.yaml)"
actual="$(yq -r '.spec.template.spec.containers[0]
  | .livenessProbe.httpGet.port + " " + .livenessProbe.httpGet.path
    + " | " + .readinessProbe.httpGet.port + " " + .readinessProbe.httpGet.path' <<<"${rendered}")"
if [[ "${actual}" != "admin /health | http /health" ]]; then
  echo "FAILED: expected liveness 'admin /health' and readiness 'http /health', got '${actual}'" >&2
  exit 1
fi

echo "ok: liveness uses the admin port, readiness uses the http port"
