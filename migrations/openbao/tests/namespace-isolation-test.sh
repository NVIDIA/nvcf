#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

OPENBAO_SERVER_INTERNAL_URL="http://plane-a-openbao-server.plane-a-vault-system.svc.cluster.local:8200"
export OPENBAO_SERVER_INTERNAL_URL
# shellcheck source=../migrations/utils/functions.sh
source "$root/migrations/utils/functions.sh"
if [[ "$OPENBAO_SERVER_INTERNAL_URL" != "http://plane-a-openbao-server.plane-a-vault-system.svc.cluster.local:8200" ]]; then
  echo "FAIL: functions.sh overwrote OPENBAO_SERVER_INTERNAL_URL" >&2
  exit 1
fi
if [[ "$OPENBAO_JWT_AUDIENCE" != "http://openbao-server.vault-system.svc.cluster.local:8200" ]]; then
  echo "FAIL: JWT role audience followed the plane-specific network address" >&2
  exit 1
fi
OPENBAO_JWT_ISSUER="https://kubernetes.default.svc"
role_json="$(generate_jwt_auth_role test-service plane-a-nvcf test-policy)"
role_audience="$(printf '%s' "$role_json" | jq -r '.bound_audiences[0]')"
if [[ "$role_audience" != "http://openbao-server.vault-system.svc.cluster.local:8200" ]]; then
  echo "FAIL: generated JWT role does not use the shared projected-token audience" >&2
  exit 1
fi
if [[ "$role_audience" == "$OPENBAO_SERVER_INTERNAL_URL" ]]; then
  echo "FAIL: generated JWT role uses the plane-specific OpenBao network address as its audience" >&2
  exit 1
fi

for namespace_pair in \
  "nvcf:NVCF_NAMESPACE" \
  "sis:SIS_NAMESPACE" \
  "api-keys:API_KEYS_NAMESPACE" \
  "ess:ESS_NAMESPACE" \
  "nats-system:NATS_NAMESPACE" \
  "nvca-system:NVCA_NAMESPACE" \
  "nvca-operator:NVCA_OPERATOR_NAMESPACE"; do
  namespace="${namespace_pair%%:*}"
  env_name="${namespace_pair#*:}"
  if grep -R -E "^[A-Za-z_][A-Za-z0-9_]*(NAMESPACE|namespace)=\"${namespace}\"$" \
      "$root/migrations" "$root/addons" >/dev/null; then
    echo "FAIL: hard-coded service-account namespace remains: ${namespace}" >&2
    grep -R -n -E "^[A-Za-z_][A-Za-z0-9_]*(NAMESPACE|namespace)=\"${namespace}\"$" \
      "$root/migrations" "$root/addons" >&2
    exit 1
  fi
  expected='${'"${env_name}:-${namespace}"'}'
  if ! grep -R -F -q "$expected" "$root/migrations" "$root/addons"; then
    echo "FAIL: migration scripts do not consume ${env_name}" >&2
    exit 1
  fi
done

if ! grep -Fq 'SERVICE_ACCOUNT_NAMESPACE="${TURN_SERVICE_ACCOUNT_NAMESPACE:-gdn-streaming}"' \
    "$root/addons/lls/setup_lls.sh"; then
  echo "FAIL: LLS migration does not consume TURN_SERVICE_ACCOUNT_NAMESPACE" >&2
  exit 1
fi
if ! grep -Fq 'SERVICE_ACCOUNT_NAMESPACE="${NVCF_UI_NAMESPACE:-nvcf-ui}"' \
    "$root/addons/nvcf-ui/setup_nvcf-ui.sh"; then
  echo "FAIL: NVCF UI migration does not consume NVCF_UI_NAMESPACE" >&2
  exit 1
fi

# NVCT is part of the core stack, so its resource-server JWKS mount must exist
# even when the optional UI addon (and its signing role) is disabled.
if ! grep -Fq 'enable_secrets_mount "${VAULT_SECRET_BASE_PATH}/jwt" "vault-plugin-secrets-jwt"' \
    "$root/migrations/20_setup_nvct.sh"; then
  echo "FAIL: core NVCT migration does not own the NVCT JWT mount" >&2
  exit 1
fi
if ! grep -Fq 'config_jwt_secret_mount_config "${VAULT_SECRET_BASE_PATH}/jwt"' \
    "$root/migrations/20_setup_nvct.sh"; then
  echo "FAIL: core NVCT migration does not configure the NVCT JWT mount" >&2
  exit 1
fi
if grep -Fq 'enable_secrets_mount "${NVCT_API_SECRET_BASE_PATH}/jwt"' \
    "$root/addons/nvcf-ui/setup_nvcf-ui.sh"; then
  echo "FAIL: optional UI addon still owns the core NVCT JWT mount" >&2
  exit 1
fi

echo "OpenBao migration namespace-isolation checks passed."
