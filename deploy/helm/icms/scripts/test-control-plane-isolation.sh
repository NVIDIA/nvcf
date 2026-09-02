#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

chart_dir="$(cd "$(dirname "$0")/../icms-api" && pwd)"
render="$(mktemp)"
trap 'rm -f "$render"' EXIT

helm template plane-a-sis "$chart_dir" \
  --namespace plane-a-sis \
  --set sis.image.registry=example.invalid \
  --set sis.image.repository=sis \
  --set sis.lls.enabled=true \
  --set sis.lls.namespace=plane-a-vault-system \
  --set sis.lls.turn.serviceAccountName=turn \
  --set sis.lls.turn.serviceAccountNamespace=plane-a-gdn-streaming \
  --set sis.lls.hmacRotation.image.registry=example.invalid \
  --set sis.lls.hmacRotation.image.repository=migrations \
  --set sis.lls.hmacRotation.image.tag=test \
  --set sis.lls.hmacRotation.baoService=plane-a-openbao-server.plane-a-vault-system.svc.cluster.local \
  --set sis.lls.hmacRotation.serviceAccountName=plane-a-openbao-server-initialize-cluster \
  --set sis.lls.hmacRotation.rootTokenSecretName=plane-a-openbao-server-root-token \
  >"$render"

for expected in \
  'namespace: plane-a-vault-system' \
  'serviceAccountName: plane-a-openbao-server-initialize-cluster' \
  'value: "plane-a-openbao-server.plane-a-vault-system.svc.cluster.local"' \
  'name: TURN_SERVICE_ACCOUNT_NAMESPACE' \
  'value: "plane-a-gdn-streaming"' \
  'secretName: plane-a-openbao-server-root-token'; do
  if ! grep -Fq "$expected" "$render"; then
    echo "FAIL: SIS LLS render missing: $expected" >&2
    exit 1
  fi
done

echo "SIS LLS control-plane isolation render checks passed."
