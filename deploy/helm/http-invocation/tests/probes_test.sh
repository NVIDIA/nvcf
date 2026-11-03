#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Liveness must not use /health, which checks NATS and nvcf-api. A shared
# dependency outage would otherwise restart the invocation service. Readiness
# keeps /health and allows for its 2 s nvcf-api check.

set -euo pipefail

chart_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../nvcf-invocation-service" && pwd)"

rendered="$(helm template invocation-service "$chart_root" \
  --namespace nvcf \
  --set-string invocation.image.registry=registry.example.test \
  --set-string invocation.image.repository=nvcf-invocation-service)"

actual="$(yq ea -r 'select(.kind == "Deployment") | .spec.template.spec.containers[0]
  | .livenessProbe.httpGet.path + " " + .readinessProbe.httpGet.path
  + " " + (.readinessProbe.timeoutSeconds | tostring)' - <<<"$rendered")"
if [[ "$actual" != "/info /health 3" ]]; then
  echo "probes_test.sh: expected liveness /info, readiness /health with timeoutSeconds 3, got '$actual'" >&2
  exit 1
fi

echo "probes_test.sh: liveness uses /info, readiness uses /health with a 3 s timeout"
