#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
render="$(mktemp)"
trap 'rm -f "$render"' EXIT

openbao_address="plane-a-openbao-server.plane-a-vault-system.svc.cluster.local:8200"

helm template plane-a-api "$chart_dir" \
  --set api.image.registry=example.invalid \
  --set api.image.repository=nvcf-api \
  --set api.accountBootstrap.image.registry=example.invalid \
  --set api.accountBootstrap.image.repository=bootstrap \
  --set-string "api.accountBootstrap.openbaoServiceAddress=${openbao_address}" \
  >"$render"

if ! grep -Fq "readonly OPENBAO_SERVICE_ADDR=\"${openbao_address}\"" "$render"; then
  echo "FAIL: account bootstrap script did not use the configured OpenBao service address" >&2
  exit 1
fi

if ! grep -Fq 'audience: http://openbao-server.vault-system.svc.cluster.local:8200' "$render"; then
  echo "FAIL: account bootstrap changed the shared OpenBao token audience" >&2
  exit 1
fi

if grep -Fq 'readonly OPENBAO_SERVICE_ADDR="openbao-server.vault-system.svc.cluster.local:8200"' "$render"; then
  echo "FAIL: account bootstrap script retained the legacy OpenBao service address" >&2
  exit 1
fi

echo "NVCF API account-bootstrap render checks passed."
