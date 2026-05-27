#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


set -euo pipefail

# Source utilities from the migrations directory (mirrors addons/lls/setup_lls.sh)
migrations_dir="$( cd "$( dirname "${BASH_SOURCE[0]}" )/../../migrations" && pwd )"
if [ ! -f "${migrations_dir}/utils/utils.sh" ]; then
    echo "Error: utils.sh not found in ${migrations_dir}/utils/utils.sh"
    return 1
else
  source "${migrations_dir}/utils/utils.sh"
  source "${migrations_dir}/utils/functions.sh"
fi

trap 'rm -f /tmp/nvcf-service-intermediate.csr /tmp/nvcf-service-intermediate.pem' EXIT

# NVCF_SERVICE_PKI_ALLOWED_DOMAINS is the OpenBao PKI role's allowed_domains
# constraint — the security guardrail that limits cert-manager-issued certs
# to the configured DNS suffixes. The stack chart derives this from
# global.domain (plus cluster.local for in-cluster services); operators do
# not set it directly.
#
# Hard requirement when this addon is enabled. The entrypoint owns the
# opt-in gate via ADDONS_LLM_ENABLED — if it invoked us, the operator
# opted in, so a missing required value is a hard failure (script aborts,
# entrypoint accumulates the failure, Job exits non-zero).
: "${NVCF_SERVICE_PKI_ALLOWED_DOMAINS:?NVCF_SERVICE_PKI_ALLOWED_DOMAINS is required when ADDONS_LLM_ENABLED=true (comma-separated DNS suffixes; the stack chart should derive this from global.domain)}"

VAULT_SECRET_BASE_PATH="${VAULT_SECRET_BASE_PATH:-services/all}"
VAULT_POLICY_BASE_PATH="${VAULT_POLICY_BASE_PATH:-services-all}"
OPENBAO_SERVER_INTERNAL_URL="${OPENBAO_SERVER_INTERNAL_URL:-http://openbao-server.vault-system.svc.cluster.local:8200}"

ROOT_PKI_PATH="${ROOT_PKI_PATH:-${VAULT_SECRET_BASE_PATH}/pki/root}"
NVCF_SERVICE_PKI_PATH="${NVCF_SERVICE_PKI_PATH:-${VAULT_SECRET_BASE_PATH}/pki/nvcf-service-issuing}"
NVCF_SERVICE_PKI_ROLE_NAME="${NVCF_SERVICE_PKI_ROLE_NAME:-nvcf-service-server}"

# TTL hierarchy: root TTL > intermediate max TTL > leaf max TTL. Leaf max TTL
# defaults to 2160h (90 days) per the canonical PKI plan; cert-manager
# Certificate resources set duration=2160h with renewBefore=720h (30 days),
# so the role must accept the full 90-day issuance window. The intermediate's
# 43800h max-lease-ttl (5 years) must exceed any leaf max TTL so cert-manager
# can issue freely until the intermediate itself needs rotation.
ROOT_PKI_MAX_TTL="${NVCF_SERVICE_PKI_ROOT_MAX_TTL:-87600h}"
ROOT_PKI_TTL="${NVCF_SERVICE_PKI_ROOT_TTL:-87600h}"
NVCF_SERVICE_PKI_INTERMEDIATE_MAX_TTL="${NVCF_SERVICE_PKI_INTERMEDIATE_MAX_TTL:-43800h}"
NVCF_SERVICE_PKI_INTERMEDIATE_TTL="${NVCF_SERVICE_PKI_INTERMEDIATE_TTL:-${NVCF_SERVICE_PKI_INTERMEDIATE_MAX_TTL}}"
NVCF_SERVICE_PKI_LEAF_MAX_TTL="${NVCF_SERVICE_PKI_LEAF_MAX_TTL:-2160h}"

CERT_MANAGER_SERVICE_ACCOUNT_NAME="${CERT_MANAGER_SERVICE_ACCOUNT_NAME:-cert-manager}"
CERT_MANAGER_SERVICE_ACCOUNT_NAMESPACE="${CERT_MANAGER_SERVICE_ACCOUNT_NAMESPACE:-cert-manager}"
CERT_MANAGER_POLICY_NAME="${VAULT_POLICY_BASE_PATH}-pki-nvcf-service-sign"

SIS_SERVICE_ACCOUNT_NAME="${SIS_SERVICE_ACCOUNT_NAME:-sis-api}"
SIS_SERVICE_ACCOUNT_NAMESPACE="${SIS_SERVICE_ACCOUNT_NAMESPACE:-sis}"
SIS_POLICY_BASE_PATH="${SIS_POLICY_BASE_PATH:-services-${SIS_SERVICE_ACCOUNT_NAME}}"
SPOT_ROOT_CA_POLICY_NAME="${VAULT_POLICY_BASE_PATH}-pki-root-ca-ro"

log_section "Configuring NVCF service-issuing PKI"

enable_secrets_mount "${ROOT_PKI_PATH}" "pki"
enable_secrets_mount "${NVCF_SERVICE_PKI_PATH}" "pki"

bao secrets tune -max-lease-ttl="${ROOT_PKI_MAX_TTL}" "${ROOT_PKI_PATH}"
bao secrets tune -max-lease-ttl="${NVCF_SERVICE_PKI_INTERMEDIATE_MAX_TTL}" "${NVCF_SERVICE_PKI_PATH}"

bao write "${ROOT_PKI_PATH}/config/urls" \
  issuing_certificates="${OPENBAO_SERVER_INTERNAL_URL}/v1/${ROOT_PKI_PATH}/ca" \
  crl_distribution_points="${OPENBAO_SERVER_INTERNAL_URL}/v1/${ROOT_PKI_PATH}/crl"

if ! bao read "${ROOT_PKI_PATH}/cert/ca" >/dev/null 2>&1; then
  log_step "Generating root CA at ${ROOT_PKI_PATH}"
  bao write "${ROOT_PKI_PATH}/root/generate/internal" \
    common_name="${NVCF_SERVICE_PKI_ROOT_COMMON_NAME:-NVCF Root CA}" \
    ttl="${ROOT_PKI_TTL}"
else
  log_info "Root CA already exists at ${ROOT_PKI_PATH}"
fi

bao write "${NVCF_SERVICE_PKI_PATH}/config/urls" \
  issuing_certificates="${OPENBAO_SERVER_INTERNAL_URL}/v1/${NVCF_SERVICE_PKI_PATH}/ca" \
  crl_distribution_points="${OPENBAO_SERVER_INTERNAL_URL}/v1/${NVCF_SERVICE_PKI_PATH}/crl"

if ! bao read "${NVCF_SERVICE_PKI_PATH}/cert/ca" >/dev/null 2>&1; then
  log_step "Generating service-issuing intermediate CA at ${NVCF_SERVICE_PKI_PATH}"
  bao write -field=csr "${NVCF_SERVICE_PKI_PATH}/intermediate/generate/internal" \
    common_name="${NVCF_SERVICE_PKI_INTERMEDIATE_COMMON_NAME:-NVCF Service Issuing Intermediate CA}" \
    ttl="${NVCF_SERVICE_PKI_INTERMEDIATE_TTL}" \
    > /tmp/nvcf-service-intermediate.csr

  bao write -field=certificate "${ROOT_PKI_PATH}/root/sign-intermediate" \
    csr=@/tmp/nvcf-service-intermediate.csr \
    format=pem_bundle \
    ttl="${NVCF_SERVICE_PKI_INTERMEDIATE_TTL}" \
    > /tmp/nvcf-service-intermediate.pem

  bao write "${NVCF_SERVICE_PKI_PATH}/intermediate/set-signed" \
    certificate=@/tmp/nvcf-service-intermediate.pem
else
  log_info "Service-issuing intermediate CA already exists at ${NVCF_SERVICE_PKI_PATH}"
fi

# allow_bare_domains=false keeps the role from issuing apex certs; only
# subdomain and wildcard issuance is allowed against the configured domain
# list. The canonical PKI policy forbids bare-domain issuance to minimize the
# blast radius of cert-manager or Certificate misconfiguration.
bao write "${NVCF_SERVICE_PKI_PATH}/roles/${NVCF_SERVICE_PKI_ROLE_NAME}" \
  allowed_domains="${NVCF_SERVICE_PKI_ALLOWED_DOMAINS}" \
  allow_subdomains=true \
  allow_bare_domains=false \
  allow_wildcard_certificates=true \
  allow_ip_sans=false \
  require_cn=false \
  server_flag=true \
  client_flag=false \
  key_type=rsa \
  key_bits=2048 \
  max_ttl="${NVCF_SERVICE_PKI_LEAF_MAX_TTL}"

cert_manager_policy=$(cat <<EOF
path "${NVCF_SERVICE_PKI_PATH}/sign/${NVCF_SERVICE_PKI_ROLE_NAME}" {
  capabilities = ["create", "update"]
}

path "${NVCF_SERVICE_PKI_PATH}/ca" {
  capabilities = ["read"]
}

path "${NVCF_SERVICE_PKI_PATH}/cert/ca" {
  capabilities = ["read"]
}

path "${ROOT_PKI_PATH}/ca" {
  capabilities = ["read"]
}

path "${ROOT_PKI_PATH}/cert/ca" {
  capabilities = ["read"]
}
EOF
)
write_policy "${CERT_MANAGER_POLICY_NAME}" "${cert_manager_policy}"

root_ca_policy=$(cat <<EOF
path "${ROOT_PKI_PATH}/ca" {
  capabilities = ["read"]
}

path "${ROOT_PKI_PATH}/cert/ca" {
  capabilities = ["read"]
}
EOF
)
write_policy "${SPOT_ROOT_CA_POLICY_NAME}" "${root_ca_policy}"

cert_manager_auth_role=$(generate_jwt_auth_role \
  "${CERT_MANAGER_SERVICE_ACCOUNT_NAME}" \
  "${CERT_MANAGER_SERVICE_ACCOUNT_NAMESPACE}" \
  "${CERT_MANAGER_POLICY_NAME}")
create_auth_jwt_role "${CERT_MANAGER_SERVICE_ACCOUNT_NAME}" "${cert_manager_auth_role}"

# Add the root-CA read policy to the SIS JWT auth role, preserving any
# policies attached by 05_setup_sis.sh or other earlier migrations. The
# helper handles both the augment case (role exists) and the bootstrap
# fallback (role missing — supplies the full canonical SIS set so the role
# is functional even if 05 didn't run first).
sis_auth_role_policies=$(merge_jwt_role_policies \
  "${SIS_SERVICE_ACCOUNT_NAME}" \
  "services-all-kv-ro,${SIS_POLICY_BASE_PATH}-kv-ro,${SPOT_ROOT_CA_POLICY_NAME}")
sis_auth_role=$(generate_jwt_auth_role \
  "${SIS_SERVICE_ACCOUNT_NAME}" \
  "${SIS_SERVICE_ACCOUNT_NAMESPACE}" \
  "${sis_auth_role_policies}")
create_auth_jwt_role "${SIS_SERVICE_ACCOUNT_NAME}" "${sis_auth_role}"
