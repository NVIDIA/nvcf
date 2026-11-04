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


# Include guard: Ensures this script's contents are only processed once.
if [[ -n "${_ISSUER_DISCOVERY_SH_SOURCED:-}" ]]; then
  return 0
fi
readonly _ISSUER_DISCOVERY_SH_SOURCED=1

#
# Discovers the service account issuer and, when possible, a JWKS URL that
# OpenBao can fetch without Kubernetes credentials.
#
# This script handles fetching, validation, and fallback logic for configuring
# OpenBao's JWT auth method.
#
# Environment Variables:
#   OIDC_DISCOVERY_URL:      (Required) The full URL to the OIDC discovery endpoint.
#   OIDC_DISCOVERY_INSECURE: (Optional) If "true", curl will skip TLS verification. Defaults to "false".
#   OIDC_CA_BUNDLE:          (Optional) Path to a custom CA bundle file for TLS verification.
#
# Outputs:
#   - Exports OPENBAO_JWT_ISSUER with the service account token issuer.
#   - Exports OPENBAO_JWT_JWKS_URL only when the JWKS URL is anonymously reachable.
#

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

# Source utility functions for logging
source "${SCRIPT_DIR}/utils.sh"
source "${SCRIPT_DIR}/functions.sh"

DEFAULT_K8S_ISSUER_URL="https://kubernetes.default.svc.cluster.local"
OIDC_ANONYMOUS_CONNECT_TIMEOUT_SECONDS="${OIDC_ANONYMOUS_CONNECT_TIMEOUT_SECONDS:-5}"
OIDC_ANONYMOUS_MAX_TIME_SECONDS="${OIDC_ANONYMOUS_MAX_TIME_SECONDS:-10}"

# --- Helper Functions ---

function curl_tls_opts() {
    if [[ "${OIDC_DISCOVERY_INSECURE:-false}" == "true" ]]; then
        log_warn "Performing insecure TLS connection." >&2
        printf '%s\n' "-s" "-S" "-L" "--fail" "--insecure"
        return 0
    fi

    if [[ -n "${OIDC_CA_BUNDLE:-}" && -f "${OIDC_CA_BUNDLE}" ]]; then
        log_info "Using custom CA bundle: ${OIDC_CA_BUNDLE}" >&2
        printf '%s\n' "-s" "-S" "-L" "--fail" "--cacert" "${OIDC_CA_BUNDLE}"
        return 0
    fi

    printf '%s\n' "-s" "-S" "-L" "--fail"
}

function service_account_curl_opts() {
    local sa_ca_path="/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
    if [[ "${OIDC_DISCOVERY_INSECURE:-false}" == "true" ]]; then
        printf '%s\n' "-s" "-S" "-L" "--fail" "--insecure"
    elif [[ -n "${OIDC_CA_BUNDLE:-}" && -f "${OIDC_CA_BUNDLE}" ]]; then
        printf '%s\n' "-s" "-S" "-L" "--fail" "--cacert" "${OIDC_CA_BUNDLE}"
    elif [[ -f "${sa_ca_path}" ]]; then
        printf '%s\n' "-s" "-S" "-L" "--fail" "--cacert" "${sa_ca_path}"
    else
        printf '%s\n' "-s" "-S" "-L" "--fail"
    fi
}

function base64url_decode() {
    local input=$1
    input="${input//-/+}"
    input="${input//_//}"

    case $((${#input} % 4)) in
        2) input="${input}==" ;;
        3) input="${input}=" ;;
        1) return 1 ;;
    esac

    printf '%s' "${input}" | base64 -d 2>/dev/null
}

function get_service_account_issuer() {
    if [[ ! -f "${K8S_SA_TOKEN_PATH}" ]]; then
        log_warn "ServiceAccount token not found at ${K8S_SA_TOKEN_PATH}; using default issuer." >&2
        return 1
    fi

    local token_payload
    token_payload=$(cut -d '.' -f 2 < "${K8S_SA_TOKEN_PATH}")
    if [[ -z "${token_payload}" ]]; then
        log_warn "ServiceAccount token has no JWT payload; using default issuer." >&2
        return 1
    fi

    local issuer
    issuer=$(base64url_decode "${token_payload}" | jq -r '.iss // empty')
    if [[ -z "${issuer}" ]]; then
        log_warn "ServiceAccount token does not contain an issuer claim; using default issuer." >&2
        return 1
    fi

    printf '%s' "${issuer}"
}

function well_known_url_for_issuer() {
    local issuer=$1
    printf '%s/.well-known/openid-configuration' "${issuer%/}"
}

function parse_discovery_issuer() {
    jq -r '.issuer // empty'
}

function parse_discovery_jwks_uri() {
    jq -r '.jwks_uri // empty'
}

function fetch_anonymous_document() {
    local url=$1
    local curl_opts=()
    mapfile -t curl_opts < <(curl_tls_opts)

    log_step "Fetching anonymous OIDC document from: ${url}" >&2
    curl "${curl_opts[@]}" \
        --connect-timeout "${OIDC_ANONYMOUS_CONNECT_TIMEOUT_SECONDS}" \
        --max-time "${OIDC_ANONYMOUS_MAX_TIME_SECONDS}" \
        "${url}"
}

function jwks_url_is_anonymous() {
    local jwks_uri=$1
    local curl_opts=()
    mapfile -t curl_opts < <(curl_tls_opts)

    log_step "Checking anonymous JWKS reachability: ${jwks_uri}" >&2
    curl "${curl_opts[@]}" \
        --connect-timeout "${OIDC_ANONYMOUS_CONNECT_TIMEOUT_SECONDS}" \
        --max-time "${OIDC_ANONYMOUS_MAX_TIME_SECONDS}" \
        -o /dev/null \
        "${jwks_uri}"
}

# Performs the actual curl command to fetch discovery document
# @param discovery_url The URL to fetch
# @return The HTTP response body on success, empty string on failure.
function fetch_discovery_document() {
    local discovery_url=$1
    local curl_opts=()
    mapfile -t curl_opts < <(service_account_curl_opts)

    if [[ -f "${K8S_SA_TOKEN_PATH}" ]]; then
        log_info "Attaching ServiceAccount token to discovery request." >&2
        curl_opts+=("-H" "Authorization: Bearer $(<"${K8S_SA_TOKEN_PATH}")")
    else
        log_warn "ServiceAccount token not found at ${K8S_SA_TOKEN_PATH}. Discovery might fail." >&2
    fi

    log_step "Fetching OIDC discovery document from: ${discovery_url}" >&2
    curl "${curl_opts[@]}" "${discovery_url}"
}

function parse_and_validate_discovery_document() {
    local response=$1
    local expected_issuer=$2
    local source=$3

    log_info "Received discovery document from ${source}:"
    log_info "${response}"

    local issuer
    local jwks_uri
    issuer=$(echo "${response}" | parse_discovery_issuer)
    jwks_uri=$(echo "${response}" | parse_discovery_jwks_uri)

    log_info "Parsed issuer from ${source}: '${issuer}'"
    log_info "Parsed jwks_uri from ${source}: '${jwks_uri}'"

    if [[ -z "${issuer}" ]]; then
        log_warn "Could not parse 'issuer' from ${source}."
        return 1
    fi

    local bound_issuer="${issuer}"
    if [[ -n "${expected_issuer}" ]]; then
        local normalized_issuer="${issuer%/}"
        local normalized_expected_issuer="${expected_issuer%/}"
        if [[ "${normalized_issuer}" != "${normalized_expected_issuer}" ]]; then
            log_warn "Ignoring ${source}; issuer '${issuer}' does not match ServiceAccount issuer '${expected_issuer}'."
            return 1
        fi
        bound_issuer="${expected_issuer}"
    fi

    if [[ -z "${jwks_uri}" ]]; then
        log_warn "Could not parse 'jwks_uri' from ${source}."
        return 1
    fi

    if ! jwks_url_is_anonymous "${jwks_uri}"; then
        log_warn "JWKS URL from ${source} is not anonymously reachable by OpenBao: ${jwks_uri}"
        return 1
    fi

    export OPENBAO_JWT_ISSUER="${bound_issuer}"
    export OPENBAO_JWT_JWKS_URL="${jwks_uri}"
    log_success "Using anonymously reachable JWKS URL from ${source}: ${jwks_uri}"
    return 0
}

function discover_public_issuer_jwks() {
    local issuer=$1
    local discovery_url
    discovery_url=$(well_known_url_for_issuer "${issuer}")

    local response=""
    local retries=3
    local delay=5

    local i
    for ((i=1; i<=retries; i++)); do
        if response=$(fetch_anonymous_document "${discovery_url}") && [[ -n "${response}" ]]; then
            break
        fi
        log_warn "Anonymous issuer discovery failed (attempt ${i}/${retries}) for ${discovery_url}. Retrying in ${delay}s..."
        sleep "${delay}"
    done

    if [[ -z "${response}" ]]; then
        log_warn "Anonymous issuer discovery failed after ${retries} attempts for ${discovery_url}."
        return 1
    fi

    parse_and_validate_discovery_document "${response}" "${issuer}" "${discovery_url}"
}

# --- Main Logic ---

function discover_issuer() {
    local expected_issuer="${OPENBAO_JWT_ISSUER:-}"

    if [[ -n "${expected_issuer}" ]]; then
        if discover_public_issuer_jwks "${expected_issuer}"; then
            return 0
        fi
    fi

    if [[ -z "${OIDC_DISCOVERY_URL:-}" ]]; then
        log_warn "OIDC_DISCOVERY_URL is not set. Skipping configured issuer discovery."
        return 1
    fi

    local response
    local retries=3
    local delay=5

    for ((i=1; i<=retries; i++)); do
        response=$(fetch_discovery_document "${OIDC_DISCOVERY_URL}")
        if [[ $? -eq 0 && -n "$response" ]]; then
            break
        fi
        log_warn "Failed to fetch discovery document (attempt ${i}/${retries}). Retrying in ${delay}s..."
        sleep $delay
    done

    if [[ -z "$response" ]]; then
        log_error "Failed to fetch OIDC discovery document after ${retries} attempts from: ${OIDC_DISCOVERY_URL}"
        return 1
    fi

    parse_and_validate_discovery_document "${response}" "${expected_issuer}" "${OIDC_DISCOVERY_URL}"
}

# --- Execution ---

log_info "Starting OIDC issuer discovery process..."

OPENBAO_JWT_ISSUER=$(get_service_account_issuer || printf '%s' "${DEFAULT_K8S_ISSUER_URL}")
export OPENBAO_JWT_ISSUER
export OPENBAO_JWT_JWKS_URL=""

if [[ "${OIDC_DISCOVERY_ENABLED:-false}" != "true" ]]; then
    log_info "OIDC issuer discovery is disabled. Using ServiceAccount issuer and mounted public key fallback."
else
    if ! discover_issuer; then
        log_warn "OIDC issuer discovery did not find an anonymous JWKS URL. Using mounted public key fallback."
    fi
fi

log_info "Final OpenBao JWT Issuer set to: ${OPENBAO_JWT_ISSUER}"
if [[ -n "${OPENBAO_JWT_JWKS_URL}" ]]; then
    log_info "Final OpenBao JWT JWKS URL set to: ${OPENBAO_JWT_JWKS_URL}"
else
    log_info "Final OpenBao JWT JWKS URL is empty; configure_auth_jwt will use mounted public keys."
fi
