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


# Include guard: Ensures this script's contents are only processed once,
# even if it is sourced multiple times.
if [[ -n "${_FUNCTIONS_SH_SOURCED:-}" ]]; then
  return 0
fi
readonly _FUNCTIONS_SH_SOURCED=1

# Define standard Kubernetes paths as constants for clarity and maintainability.
readonly K8S_SA_TOKEN_PATH="/var/run/secrets/kubernetes.io/serviceaccount/token"

# initialize auth and secrets mount lists
declare AUTH_MOUNT_LIST
declare SECRETS_MOUNT_LIST

function initialize_mount_lists() {
    log_step "Initializing auth and secrets mount lists..."
    AUTH_MOUNT_LIST=$(bao auth list)
    SECRETS_MOUNT_LIST=$(bao secrets list)
    log_success "Mount lists initialized."
}

OPENBAO_SERVER_INTERNAL_URL="http://openbao-server.vault-system.svc.cluster.local:8200"

##
# Enable a auth engine
#
# @param mount_path The mount path of the auth engine
# @param mount_type The type of the auth engine
#
function enable_auth_mount() {
    local mount_path=$1
    local mount_type=$2

    if bao auth list | grep -q "^$mount_path/"; then
        log_success "$mount_type auth engine mounted at path '$mount_path'"
        return 0
    fi

    log_step "Enabling $mount_type auth engine at mount path '$mount_path'"
    if ! output=$(bao auth enable -path=$mount_path $mount_type 2>&1); then
        log_error "Error enabling $mount_type auth engine: $output"
        return 1
    fi
}

##
# Configure the global JWT auth method
#
# This function reads the issuer and optional JWKS URL from environment variables
# set by issuer_discovery.sh and configures the auth/jwt/config endpoint.
#
function configure_auth_jwt() {
    log_step "Configuring JWT/OIDC auth method"

    if [[ -z "${OPENBAO_JWT_ISSUER:-}" ]]; then
        log_error "OPENBAO_JWT_ISSUER is not set; cannot configure JWT auth."
        return 1
    fi

    if [[ -n "${OPENBAO_JWT_JWKS_URL:-}" ]]; then
        log_info "Configuring with ServiceAccount issuer and anonymously reachable JWKS URL: ${OPENBAO_JWT_ISSUER}"
        log_step "Writing JWT config with args: jwks_url=${OPENBAO_JWT_JWKS_URL} bound_issuer=${OPENBAO_JWT_ISSUER}"
        if ! output=$(bao write auth/jwt/config \
            jwks_url="${OPENBAO_JWT_JWKS_URL}" \
            bound_issuer="${OPENBAO_JWT_ISSUER}" \
            2>&1); then
            log_error "Error configuring JWT auth with JWKS URL: $output"
            return 1
        fi
    else
        # When the provider's JWKS endpoint is not anonymously reachable by OpenBao,
        # validate service account tokens with the public signing key mounted by the chart.
        local pub_key=""
        if [[ -f /secrets/jwt/cluster_jwt.pem ]]; then
            if ! pub_key=$(base64 -d < /secrets/jwt/cluster_jwt.pem 2>/dev/null); then
                log_error "Failed to decode JWT public key at /secrets/jwt/cluster_jwt.pem"
                return 1
            fi
        fi

        if [[ -z "${pub_key}" ]]; then
            log_error "JWT public key not found or empty at /secrets/jwt/cluster_jwt.pem"
            return 1
        fi

        if [[ ! -f "${K8S_SA_TOKEN_PATH}" ]]; then
            log_error "ServiceAccount token not found at ${K8S_SA_TOKEN_PATH}"
            return 1
        fi

        log_info "Configuring with ServiceAccount issuer and mounted public key: ${OPENBAO_JWT_ISSUER}"
        log_step "Writing JWT config with args: kubernetes_service_account_token_reviewer_jwt=<redacted> bound_issuer=${OPENBAO_JWT_ISSUER} jwt_validation_pubkeys=<redacted>"
        if ! output=$(bao write auth/jwt/config \
            kubernetes_service_account_token_reviewer_jwt="$(cat "${K8S_SA_TOKEN_PATH}")" \
            bound_issuer="${OPENBAO_JWT_ISSUER}" \
            jwt_validation_pubkeys="${pub_key}" \
            2>&1); then
            log_error "Error configuring JWT auth with mounted public key: $output"
            return 1
        fi
    fi

    log_success "JWT/OIDC auth method configured successfully."
}

##
# Enable a secrets engine
#
# @param mount_path The mount path of the secrets engine
# @param mount_type The type of the secrets engine
#
function enable_secrets_mount() {
    local mount_path=$1
    local mount_type=$2

    if ! bao secrets list | grep -q "^$mount_path/"; then
      log_step "Enabling $mount_type secrets engine at mount path '$mount_path'"
      if ! output=$(bao secrets enable -path=$mount_path $mount_type 2>&1); then
          log_error "Error enabling $mount_type secrets engine: $output"
          return 1
      fi
    fi

    log_info "$mount_type secrets engine already mounted at path '$mount_path'"
    return 0
}

##
# Configure a auth engine
#
# @param mount_path The mount path of the auth engine
# @param default_lease_ttl The default lease TTL of the auth engine
# @param max_lease_ttl The maximum lease TTL of the auth engine
#
function configure_auth_tuning() {
    local mount_path=$1
    local default_lease_ttl=$2
    local max_lease_ttl=$3

    if ! echo $AUTH_MOUNT_LIST | grep -q "^$mount_path/"; then
        log_error "$mount_type auth engine not mounted at path '$mount_path'"
        return 1
    fi

    log_step "Tuning $mount_type auth engine at mount path '$mount_path'"
    if ! output=$(bao auth tune -path=$mount_path default_lease_ttl=$default_lease_ttl max_lease_ttl=$max_lease_ttl 2>&1); then
        log_error "Error configuring $mount_type auth engine: $output"
        return 1
    fi

    log_success "Successfully configured $mount_type auth engine at mount path '$mount_path'"
}

##
# Configure a secrets engine
#
# @param mount_path The mount path of the secrets engine
# @param default_lease_ttl The default lease TTL of the secrets engine
# @param max_lease_ttl The maximum lease TTL of the secrets engine
#
function configure_secrets_tuning() {
    local mount_path=$1
    local default_lease_ttl=$2
    local max_lease_ttl=$3

    if ! echo $SECRETS_MOUNT_LIST | grep -q "^$mount_path/"; then
        log_error "$mount_type secrets engine not mounted at path '$mount_path'"
        return 1
    fi

    log_step "Tuning $mount_type secrets engine at mount path '$mount_path'"
    if ! output=$(bao secrets tune -path=$mount_path default_lease_ttl=$default_lease_ttl max_lease_ttl=$max_lease_ttl 2>&1); then
        log_error "Error configuring $mount_type secrets engine: $output"
        return 1
    fi

    log_success "Successfully tuned $mount_type secrets engine at mount path '$mount_path'"
}

##
# Write a policy to the Vault
#
# @param policy_name The name of the policy
# @param policy_content The content of the policy
#
function write_policy() {
    local policy_name=$1
    local policy_content=$2

    log_step "Creating/Updating policy '$policy_name'"
    if ! output=$(bao write sys/policies/acl/$policy_name policy="$policy_content"); then
        log_error "Error writing policy '$policy_name': $output"
        return 1
    fi

    log_success "Policy '$policy_name' created/updated successfully"
}

##
# Create a JWT auth role
#
# @param role_name The name of the JWT auth role
# @param role_content The content of the JWT auth role
#
function create_auth_jwt_role() {
    local role_name=$1
    local role_content=$2

    log_step "Creating/Updating JWT role '$role_name'"
    if ! output=$(bao write auth/jwt/role/$role_name - <<EOF
$role_content
EOF
); then
        log_error "Error writing JWT role '$role_name': $output"
        return 1
    fi
    log_success "JWT Auth role '$role_name' created/updated successfully"
}

##
# Merge a set of policies into an existing JWT auth role's policy list,
# preserving anything attached by other migrations. Use this everywhere a
# migration may add policies to a role that other migrations also write to:
# `bao write auth/jwt/role/<name>` is a full PUT, so a naive write would
# silently overwrite policies the current caller doesn't know about.
#
# Returns the merged comma-separated policy list, suitable for feeding into
# generate_jwt_auth_role. Each policy from the supplied set is added only if
# not already present (exact, case-sensitive match). If the role doesn't
# exist yet, the supplied policies are returned unchanged so the caller can
# create the role with them.
#
# Usage:
#   merged=$(merge_jwt_role_policies <role_name> <policies_csv>)
#
function merge_jwt_role_policies() {
    local role_name=$1
    local new_policies_csv=$2

    local existing
    existing=$(bao read -format=json "auth/jwt/role/${role_name}" 2>/dev/null \
        | jq -r '.data.token_policies // [] | join(",")' 2>/dev/null) || existing=""

    if [ -z "${existing}" ]; then
        echo "${new_policies_csv}"
        return 0
    fi

    local merged="${existing}"
    local IFS=,
    local policy
    for policy in ${new_policies_csv}; do
        case ",${merged}," in
            *",${policy},"*) : ;;
            *) merged="${merged},${policy}" ;;
        esac
    done
    echo "${merged}"
}

##
# Write secrets KV to a path
#
# @param mount_path The mount path of the secrets KV
# @param secret The secret to be written
# @param value The value to be written to the secret
# @param overwrite Whether to overwrite the secret if it already exists (default: false)
#
function write_secrets_kv() {
    local mount_path=$1
    local secret=$2
    local value=$3
    local overwrite=${4:-"false"}

    log_step "Writing secrets KV to '$mount_path/$secret'"

    if ! output=$(bao kv get $mount_path/$secret > /dev/null 2>&1) || [ "$overwrite" == "true" ]; then
      if ! output=$(bao kv put $mount_path/$secret $value 2>&1); then
          log_error "Error writing secrets KV to '$mount_path/$secret': $output"
          return 1
      fi
      log_success "Secrets KV '$secret' written successfully to '$mount_path'"
    else
      log_info "Secrets KV '$secret' already exists in '$mount_path', skipping..."
    fi
}

##
# Configure a JWT secret mount config
#
# @param mount_path The mount path of the JWT secret mount config
# @param config_content The content of the JWT secret mount config
#
function config_jwt_secret_mount_config() {
  local mount_path=$1
  local config_content=$2

  log_step "Configuring JWT secret mount config for mount '$mount_path'"
  if ! output=$(bao write $mount_path/config - <<EOF
$config_content
EOF
); then
    log_error "Error writing JWT secret mount config '${mount_path}/config': $output"
    return 1
  fi

  log_success "JWT secret mount config for mount '$mount_path' created/updated successfully"
}

##
# Create a JWT secret role
#
# @param mount_path The mount path of the JWT secret role
# @param role_name The name of the JWT secret role
# @param role_content The content of the JWT secret role
#
function create_secret_jwt_role() {
  local mount_path=$1
  local role_name=$2
  local role_content=$3

  log_step "Creating/Updating JWT secret role '$mount_path/roles/$role_name'"
  if ! output=$(bao write $mount_path/roles/$role_name - <<EOF
$role_content
EOF
); then
    log_error "Error writing JWT secret role '$mount_path/roles/$role_name': $output"
    return 1
  fi

  log_success "JWT secret role '$role_name' created/updated successfully at $mount_path/roles/$role_name"
}

##
# Generate an ACL policy for a path
#
# @param path The path to be added to the ACL policy
# @param capabilities The capabilities to be added to the ACL policy
#
function generate_acl_policy() {
  local path=$1
  local capabilities=$2

  local quoted_capabilities=$(sed 's/\([^,]*\)/"\1"/g' <<< "$capabilities")

  local policy=$(cat <<EOF
path "$path" {
  capabilities = [${quoted_capabilities}]
}
EOF
)
  echo "$policy"
}

##
# Generate a JWT auth role for a service
#
# @param service_name The name of the service
# @param service_account_namespace The namespace of the service account
# @param policies The policies to be added to the JWT auth role
#
function generate_jwt_auth_role() {
  local service_name=$1
  local service_account_namespace=$2
  local policies=$3
  # Allow audience to be overridden, but default to the server's internal URL
  local audience=${4:-"${OPENBAO_SERVER_INTERNAL_URL}"}

  local quoted_policies=$(sed 's/\([^,]*\)/"\1"/g' <<< "$policies")
  local quoted_audiences=$(sed 's/\([^,]*\)/"\1"/g' <<< "$audience")

  # Start with a base role JSON
  local role_json=$(cat <<EOF
{
  "role_type": "jwt",
  "bound_audiences": [${quoted_audiences}],
  "bound_claims_type": "string",
  "bound_claims": {
    "/kubernetes.io/serviceaccount/name": "${service_name}",
    "/kubernetes.io/namespace": "${service_account_namespace}"
  },
  "claim_mappings": {
    "/kubernetes.io/namespace": "service_account_namespace",
    "/kubernetes.io/serviceaccount/name": "service_name",
    "/kubernetes.io/pod/name": "pod_name"
  },
  "user_claim": "sub",
  "clock_skew_leeway": 60,
  "expiration_leeway": 60,
  "not_before_leeway": 60,
  "token_period": 43200,
  "token_type": "service",
  "policies": [${quoted_policies}]
}
EOF
)

  # Always bind the role to the specific issuer for this environment.
  # This provides defense-in-depth, ensuring the role is secure even
  # if the global auth config were ever misconfigured.
  role_json=$(echo "${role_json}" | jq --arg issuer "${OPENBAO_JWT_ISSUER}" '. + {bound_issuer: $issuer}')

  echo "$role_json"
}

##
# Generate a JWT secret mount config for a service
#
# @param allowed_claims The allowed claims to be added to the JWT secret mount config
# @param audience_pattern The audience pattern to be added to the JWT secret mount config
# @param jwt_ttl The JWT TTL to be added to the JWT secret mount config
# @param key_ttl The key TTL to be added to the JWT secret mount config
#
function generate_jwt_secret_mount_config() {
  local allowed_claims=${1:-"azp,aud,sub,scopes"}
  local audience_pattern=${2:-".*"}
  local jwt_ttl=${3:-"12h"}
  local key_ttl=${4:-"87660h"}

  local quoted_allowed_claims=$(sed 's/\([^,]*\)/"\1"/g' <<< "$allowed_claims")

  local mount_config=$(cat <<EOF
{
  "allowed_claims":[${quoted_allowed_claims}],
  "allowed_headers":null,
  "audience_pattern":"${audience_pattern}",
  "jwt_ttl":"${jwt_ttl}",
  "key_ttl":"${key_ttl}",
  "max_audiences":-1,
  "rsa_key_bits":2048,
  "set_iat":true,
  "set_jti":true,
  "set_nbf":true,
  "sig_alg":"ES256",
  "subject_pattern":".*"
}
EOF
)
  echo "$mount_config"
}

##
# Generate a JWT secret role for a service
#
# @param target_service_name The name of the target service
# @param target_service_account_namespace The namespace of the target service account
# @param client_service_name The name of the client service
# @param scopes The scopes to be added to the JWT secret role
#
function generate_jwt_secret_role() {
  local target_service_account_namespace=$1
  local target_service_name=$2
  local client_service_name=$3
  local scopes=$4

  local quoted_scopes=$(sed 's/\([^,]*\)/"\1"/g' <<< "$scopes")
  local issuer="http://${target_service_name}.${target_service_account_namespace}.svc.cluster.local"

  local role_json=$(cat <<EOF
{
  "issuer":"${issuer}",
  "claims":{
    "azp":"${client_service_name}",
    "aud":["${client_service_name}","s:${target_service_name}"],
    "scopes":[${quoted_scopes}],
    "sub":"${client_service_name}"
  }
}
EOF
)
  echo "$role_json"
}
