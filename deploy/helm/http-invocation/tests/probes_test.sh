#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Liveness must not use /health, which checks NATS. A shared dependency outage
# would otherwise restart the invocation service.

set -euo pipefail

chart_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../nvcf-invocation-service" && pwd)"

rendered="$(helm template invocation-service "$chart_root" \
  --namespace nvcf \
  --set-string invocation.image.registry=registry.example.test \
  --set-string invocation.image.repository=nvcf-invocation-service)"

actual="$(yq ea -r 'select(.kind == "Deployment") | .spec.template.spec.containers[0]
  | .livenessProbe.httpGet.path + " " + .readinessProbe.httpGet.path' - <<<"$rendered")"
if [[ "$actual" != "/info /health" ]]; then
  echo "probes_test.sh: expected liveness /info and readiness /health, got '$actual'" >&2
  exit 1
fi

echo "probes_test.sh: liveness uses /info, readiness uses /health"
