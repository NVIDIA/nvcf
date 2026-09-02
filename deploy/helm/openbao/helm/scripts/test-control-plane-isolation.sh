#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
tmpdir="$(mktemp -d)"
render="$tmpdir/render.yaml"
test_chart="$tmpdir/chart"
trap 'rm -rf "$tmpdir"' EXIT

# Keep the regression self-contained for CI without leaving dependency
# archives in the source tree.
cp -R "$chart_dir" "$test_chart"
helm dependency build "$test_chart" >/dev/null

helm template openbao-server "$test_chart" \
  --namespace plane-a-vault-system \
  --set openbao.fullnameOverride=plane-a-openbao-server \
  --set openbao.controlPlane.id=plane-a \
  --set openbao.server.image.registry=example.invalid \
  --set openbao.server.image.repository=openbao \
  --set openbao.migrations.image.registry=example.invalid \
  --set openbao.migrations.image.repository=migrations \
  --set openbao.migrations.env[0].name=CUSTOM_MIGRATION_ENV \
  --set openbao.migrations.env[0].value=preserved \
  >"$render"

for expected in \
  'name: CUSTOM_MIGRATION_ENV' \
  'value: preserved' \
  'name: BAO_SERVICE' \
  'value: "plane-a-openbao-server.plane-a-vault-system.svc.cluster.local"' \
  'name: OPENBAO_SERVER_INTERNAL_URL' \
  'value: "http://plane-a-openbao-server.plane-a-vault-system.svc.cluster.local:8200"' \
  'name: OPENBAO_JWT_AUDIENCE' \
  'value: "http://openbao-server.vault-system.svc.cluster.local:8200"' \
  'name: NVCF_NAMESPACE' \
  'value: "plane-a-nvcf"' \
  'name: SIS_NAMESPACE' \
  'value: "plane-a-sis"' \
  'name: API_KEYS_NAMESPACE' \
  'value: "plane-a-api-keys"' \
  'name: ESS_NAMESPACE' \
  'value: "plane-a-ess"' \
  'name: NATS_NAMESPACE' \
  'value: "plane-a-nats-system"' \
  'name: NVCF_UI_NAMESPACE' \
  'value: "plane-a-nvcf-ui"' \
  'name: NVCA_NAMESPACE' \
  'value: "plane-a-nvca-system"' \
  'name: NVCA_OPERATOR_NAMESPACE' \
  'value: "plane-a-nvca-operator"'; do
  if ! grep -Fq "$expected" "$render"; then
    echo "FAIL: OpenBao render missing: $expected" >&2
    exit 1
  fi
done

echo "OpenBao control-plane isolation render checks passed."
