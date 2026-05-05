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
# Discovers the OIDC issuer URL from a .well-known/openid-configuration endpoint.
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
#   - Exports OPENBAO_JWT_ISSUER with the discovered or fallback issuer URL.
#   - Exports OPENBAO_JWT_JWKS_URL with the discovered JWKS URL (if successful).
#

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

# Source utility functions for logging
source "${SCRIPT_DIR}/utils.sh"
source "${SCRIPT_DIR}/functions.sh"

DEFAULT_K8S_ISSUER_URL="https://kubernetes.default.svc.cluster.local"

# --- Helper Functions ---

# Performs the actual curl command to fetch discovery document
# @param discovery_url The URL to fetch
# @return The HTTP response body on success, empty string on failure.
function fetch_discovery_document() {
    local discovery_url=$1
    # For in-cluster communication, always trust the service account's mounted CA.
    local sa_ca_path="/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
    local curl_opts=("-s" "-S" "-L" "--fail" "--cacert" "${sa_ca_path}")

    if [[ "${OIDC_DISCOVERY_INSECURE}" == "true" ]]; then
        log_warn "Performing insecure TLS connection to discovery endpoint." >&2
        # Remove the --cacert and add --insecure if insecure is explicitly requested.
        curl_opts=("-s" "-S" "-L" "--fail" "--insecure")
    elif [[ -n "${OIDC_CA_BUNDLE}" && -f "${OIDC_CA_BUNDLE}" ]]; then
        log_info "Using custom CA bundle: ${OIDC_CA_BUNDLE}" >&2
        # Override the default SA CA with the custom one if provided.
        curl_opts=("-s" "-S" "-L" "--fail" "--cacert" "${OIDC_CA_BUNDLE}")
    fi

    # EKS requires an authenticated request to return the external OIDC issuer.
    if [[ -f "${K8S_SA_TOKEN_PATH}" ]]; then
        log_info "Attaching ServiceAccount token to discovery request." >&2
        curl_opts+=("-H" "Authorization: Bearer $(<"${K8S_SA_TOKEN_PATH}")")
    else
        log_warn "ServiceAccount token not found at ${K8S_SA_TOKEN_PATH}. Discovery might fail or return incorrect issuer on EKS." >&2
    fi

    log_step "Fetching OIDC discovery document from: ${discovery_url}" >&2
    curl "${curl_opts[@]}" "${discovery_url}"
}

# --- Main Logic ---

function discover_issuer() {
    if [[ -z "${OIDC_DISCOVERY_URL}" ]]; then
        log_error "OIDC_DISCOVERY_URL is not set. Cannot perform issuer discovery."
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

    log_info "Received raw discovery document:"
    log_info "${response}"

    local issuer
    local jwks_uri
    issuer=$(echo "${response}" | jq -r '.issuer')
    jwks_uri=$(echo "${response}" | jq -r '.jwks_uri')

    log_info "Parsed issuer from discovery document: '${issuer}'"
    log_info "Parsed jwks_uri from discovery document: '${jwks_uri}'"

    if [[ -z "$issuer" || "$issuer" == "null" ]]; then
        log_error "Could not parse 'issuer' from discovery document."
        return 1
    fi

    if [[ -z "$jwks_uri" || "$jwks_uri" == "null" ]]; then
        log_error "Could not parse 'jwks_uri' from discovery document."
        return 1
    fi

    # In EKS, the discovered jwks_uri is an internal, often unreachable IP.
    # We must construct the public JWKS URL by appending /keys to the public issuer URL.
    if [[ "$issuer" == *"eks"* ]]; then
        log_info "EKS issuer detected. Reconstructing JWKS URI to be publicly reachable." >&2
        # The correct public JWKS URL is the public issuer URL plus /keys.
        local new_jwks_uri="${issuer}/keys"
        log_info "Original jwks_uri: '${jwks_uri}'" >&2
        log_info "Reconstructed public jwks_uri: '${new_jwks_uri}'" >&2
        jwks_uri="$new_jwks_uri"
    fi

    log_success "Successfully discovered OIDC issuer: ${issuer}"
    export OPENBAO_JWT_ISSUER="${issuer}"
    export OPENBAO_JWT_JWKS_URL="${jwks_uri}"
    return 0
}

# --- Execution ---

log_info "Starting OIDC issuer discovery process..."

if [[ "${OIDC_DISCOVERY_ENABLED}" != "true" ]]; then
    log_info "OIDC issuer discovery is disabled. Using default Kubernetes issuer."
    export OPENBAO_JWT_ISSUER="${DEFAULT_K8S_ISSUER_URL}"
    # Explicitly set JWKS URL to empty to avoid unbound variable errors
    export OPENBAO_JWT_JWKS_URL=""
else
    if ! discover_issuer; then
        log_warn "OIDC issuer discovery failed. Falling back to default Kubernetes issuer."
        export OPENBAO_JWT_ISSUER="${DEFAULT_K8S_ISSUER_URL}"
        # Explicitly set JWKS URL to empty to avoid unbound variable errors
        export OPENBAO_JWT_JWKS_URL=""
    fi
fi

log_info "Final OpenBao JWT Issuer set to: ${OPENBAO_JWT_ISSUER}"
