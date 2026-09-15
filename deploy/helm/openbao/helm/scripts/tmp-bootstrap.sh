#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


# =============================================================================
# NVCF NCA Bootstrap Script
# =============================================================================
# Purpose: Creates a default NCA account for NVCF API during Helm installation
# Dependencies: curl, jq, base64 (all available in curlimages/curl)
# =============================================================================

set -euo pipefail  # Exit on error, undefined vars, pipe failures
IFS=$'\n\t'       # Secure internal field separator

# =============================================================================
# CONFIGURATION
# =============================================================================

readonly SCRIPT_VERSION="2.0.0"
readonly SCRIPT_NAME="account-bootstrap"

# Timeouts and retries
readonly API_READY_TIMEOUT=600        # 10 minutes
readonly API_READY_INTERVAL=10        # 10 seconds
readonly API_INITIALIZATION_DELAY=10  # 10 seconds

# Service endpoints
readonly OPENBAO_SERVICE_ADDR="openbao-server.vault-system.svc.cluster.local:8200"
# Vault JWT Auth configuration
# Role used for the /v1/auth/jwt/login endpoint (overridable via env var)
readonly OPENBAO_JWT_AUTH_ROLE="${OPENBAO_JWT_AUTH_ROLE:-nvcf-api-account-bootstrap}"
# Name of the JWT role used for signing (overridable)
readonly OPENBAO_JWT_SIGNING_ROLE="${OPENBAO_JWT_SIGNING_ROLE:-nvcf-api-admin}"

# API paths
readonly HEALTH_PATH="/health"
readonly ACCOUNTS_ENDPOINT="/v2/nvcf/accounts"

# Exit codes
readonly EXIT_SUCCESS=0
readonly EXIT_CONFIG_ERROR=1
readonly EXIT_API_TIMEOUT=2
readonly EXIT_OPENBAO_ERROR=3
readonly EXIT_AUTH_ERROR=4
readonly EXIT_ACCOUNT_ERROR=5

# =============================================================================
# LOGGING FUNCTIONS
# =============================================================================

log() {
    local level="$1"
    shift
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] [$level] $*" >&2
}

log_info() { log "INFO" "$@"; }
log_step() { log "STEP" "$@"; }  
log_success() { log "SUCCESS" "$@"; }
log_warning() { log "WARNING" "$@"; }
log_error() { log "ERROR" "$@"; }
log_debug() { 
    if [[ "${DEBUG:-false}" == "true" ]]; then
        log "DEBUG" "$@"
    fi
}

# =============================================================================
# ERROR HANDLING
# =============================================================================

cleanup() {
    log_debug "Cleaning up sensitive variables..."
    unset vault_token bearer_token 2>/dev/null || true
}

error_exit() {
    local exit_code="${1:-$EXIT_CONFIG_ERROR}"
    local message="${2:-Unknown error occurred}"
    log_error "$message"
    cleanup
    exit "$exit_code"
}

# Trap to ensure cleanup on exit
trap cleanup EXIT

# =============================================================================
# VALIDATION FUNCTIONS
# =============================================================================

validate_environment() {
    log_step "Validating environment variables..."
    
    local required_vars=(
        "API_SERVICE_NAME"
        "API_NAMESPACE" 
        "NVCF_ACCOUNT_NAME"
        "NVCF_ADMIN_CLIENT_ID"
        "NVCF_CONTAINER_REGISTRY_CREDENTIAL"
        "NVCF_MODEL_REGISTRY_CREDENTIAL"
        "NVCF_SIDECAR_CREDENTIAL"
    )
    
    local missing_vars=()
    for var in "${required_vars[@]}"; do
        if [[ -z "${!var:-}" ]]; then
            missing_vars+=("$var")
        fi
    done
    
    if [[ ${#missing_vars[@]} -gt 0 ]]; then
        error_exit "$EXIT_CONFIG_ERROR" "Missing required environment variables: ${missing_vars[*]}"
    fi
    
    log_success "Environment validation completed"
}

validate_dependencies() {
    log_step "Validating dependencies..."
    
    local deps=("curl" "jq" "base64")
    for dep in "${deps[@]}"; do
        if ! command -v "$dep" >/dev/null 2>&1; then
            error_exit "$EXIT_CONFIG_ERROR" "Required dependency not found: $dep"
        fi
    done
    
    log_success "Dependencies validated"
}

# =============================================================================
# KUBERNETES API FUNCTIONS
# =============================================================================

get_k8s_service_account_token() {
    local sa_path="/var/run/secrets/kubernetes.io/serviceaccount"
    
    if [[ ! -f "$sa_path/token" ]]; then
        error_exit "$EXIT_CONFIG_ERROR" "Kubernetes service account token not found"
    fi
    
    cat "$sa_path/token"
}

# =============================================================================
# API HEALTH CHECK FUNCTIONS
# =============================================================================

wait_for_api_ready() {
    log_step "Waiting for NVCF API to be ready..."
    
    local api_url="http://${API_SERVICE_NAME}.${API_NAMESPACE}.svc.cluster.local:8080"
    local health_url="${api_url}${HEALTH_PATH}"
    local max_attempts=$((API_READY_TIMEOUT / API_READY_INTERVAL))
    local attempt=0
    
    log_info "Health check URL: $health_url"
    log_info "Timeout: ${API_READY_TIMEOUT}s, Interval: ${API_READY_INTERVAL}s"
    
    while [[ $attempt -lt $max_attempts ]]; do
        if curl -sf "$health_url" >/dev/null 2>&1; then
            log_success "NVCF API is ready at $api_url"
            log_info "Waiting ${API_INITIALIZATION_DELAY}s for full API initialization..."
            sleep "$API_INITIALIZATION_DELAY"
            return 0
        fi
        
        attempt=$((attempt + 1))
        log_info "API not ready yet (attempt $attempt/$max_attempts), retrying in ${API_READY_INTERVAL}s..."
        sleep "$API_READY_INTERVAL"
    done
    
    error_exit "$EXIT_API_TIMEOUT" "API did not become ready within ${API_READY_TIMEOUT}s timeout"
}

# =============================================================================
# OPENBAO FUNCTIONS
# ------------------
# The bootstrap script authenticates to OpenBao using the built-in **JWT Auth**
# method. It sends the Pod's service-account token to the
# `/v1/auth/jwt/login` endpoint (role: `${OPENBAO_JWT_AUTH_ROLE}`) and receives a
# short-lived client token. That token is subsequently used to call the
# signing endpoint `/v1/services/nvcf-api/jwt/sign/${OPENBAO_JWT_SIGNING_ROLE}`.
# No cluster-wide root token or Secret lookup is required.
# ------------------------------------
# Authenticate to OpenBao via JWT Auth
# ------------------------------------

login_to_openbao() {
    log_step "Authenticating to OpenBao via JWT auth (role: ${OPENBAO_JWT_AUTH_ROLE})..."

    local sa_token openbao_url login_url login_payload login_response vault_token

    sa_token=$(get_k8s_service_account_token)
    openbao_url="http://${OPENBAO_SERVICE_ADDR}"
    login_url="${openbao_url}/v1/auth/jwt/login"

    login_payload=$(jq -n --arg role "${OPENBAO_JWT_AUTH_ROLE}" --arg jwt "${sa_token}" '{role:$role, jwt:$jwt}')

    login_response=$(curl -s -X POST -H "Content-Type: application/json" -d "${login_payload}" "${login_url}")

    if echo "$login_response" | jq -e '.errors' >/dev/null 2>&1; then
        log_error "JWT auth login failed"
        log_debug "Login response: $(echo "$login_response" | jq '.' -c 2>/dev/null || echo "$login_response")"
        error_exit "$EXIT_OPENBAO_ERROR" "Failed to authenticate to OpenBao via JWT auth"
    fi

    vault_token=$(echo "$login_response" | jq -r '.auth.client_token // empty')

    if [[ -z "$vault_token" ]]; then
        log_error "client_token not found in login response"
        log_debug "Login response: $(echo "$login_response" | jq '.' -c 2>/dev/null || echo "$login_response")"
        error_exit "$EXIT_OPENBAO_ERROR" "Failed to obtain Vault client token"
    fi

    log_success "Authenticated to OpenBao (token length: ${#vault_token} chars)"
    echo "$vault_token"
}

test_openbao_connection() {
    local vault_token="$1"
    log_step "Testing OpenBao connection..."
    
    local openbao_url="http://${OPENBAO_SERVICE_ADDR}"
    local status_url="${openbao_url}/v1/sys/seal-status"
    
    if ! curl -sf -H "X-Vault-Token: $vault_token" "$status_url" >/dev/null 2>&1; then
        error_exit "$EXIT_OPENBAO_ERROR" "Cannot connect to OpenBao at $openbao_url"
    fi
    
    log_success "OpenBao connection verified"
}

generate_jwt_token() {
    local vault_token="$1"
    log_step "Generating JWT token..."
    
    local openbao_url="http://${OPENBAO_SERVICE_ADDR}"
    local sign_url="${openbao_url}/v1/services/nvcf-api/jwt/sign/${OPENBAO_JWT_SIGNING_ROLE}"
    
    local jwt_output
    jwt_output=$(curl -s -X PUT -H "X-Vault-Token: $vault_token" -d '{}' "$sign_url")
    
    if echo "$jwt_output" | jq -e '.errors' >/dev/null 2>&1; then
        log_error "Failed to generate JWT token"
        log_debug "JWT response: $(echo "$jwt_output" | jq '.' 2>/dev/null || echo "$jwt_output")"
        error_exit "$EXIT_AUTH_ERROR" "JWT token generation failed"
    fi
    
    local bearer_token
    bearer_token=$(echo "$jwt_output" | jq -r '.data.token // empty')
    
    if [[ -z "$bearer_token" ]]; then
        log_error "Could not extract bearer token from response"
        log_debug "JWT output: $(echo "$jwt_output" | jq '.' 2>/dev/null || echo "$jwt_output")"
        error_exit "$EXIT_AUTH_ERROR" "Bearer token extraction failed"
    fi
    
    log_success "JWT token generated successfully"
    echo "$bearer_token"
}

# =============================================================================
# ACCOUNT CREATION FUNCTIONS
# =============================================================================

create_account_payload() {
    log_debug "Creating account payload..."
    
    local payload
    payload=$(jq -n \
        --arg adminClientId "$NVCF_ADMIN_CLIENT_ID" \
        --arg containerRegistryCredential "$NVCF_CONTAINER_REGISTRY_CREDENTIAL" \
        --arg modelRegistryCredential "$NVCF_MODEL_REGISTRY_CREDENTIAL" \
        --arg sidecarCredential "$NVCF_SIDECAR_CREDENTIAL" \
        --arg name "$NVCF_ACCOUNT_NAME" \
        '{
            adminClientId: $adminClientId,
            containerRegistryCredential: $containerRegistryCredential,
            modelRegistryCredential: $modelRegistryCredential,
            sidecarCredential: $sidecarCredential,
            name: $name
        }')
    
    log_debug "Payload created (${#payload} bytes)"
    echo "$payload"
}

create_nvcf_account() {
    local bearer_token="$1"
    log_step "Creating NVCF account..."
    
    local api_url="http://${API_SERVICE_NAME}.${API_NAMESPACE}.svc.cluster.local:8080"
    local credentials_url="${api_url}${ACCOUNTS_ENDPOINT}/${NVCF_ACCOUNT_NAME}/credentials"
    
    log_info "Account endpoint: $credentials_url"
    
    local payload
    payload=$(create_account_payload)
    
    local response
    response=$(curl -s -w "\n%{http_code}" \
        --location "$credentials_url" \
        --header "Content-Type: application/json" \
        --header "Authorization: Bearer $bearer_token" \
        --data "$payload")
    
    local response_body status_code
    response_body=$(echo "$response" | head -n -1)
    status_code=$(echo "$response" | tail -n 1)
    
    log_info "HTTP Status: $status_code"
    
    # Validate status code format
    if ! [[ "$status_code" =~ ^[0-9]+$ ]]; then
        log_error "Invalid HTTP status code: '$status_code'"
        log_debug "Full response: $response"
        error_exit "$EXIT_ACCOUNT_ERROR" "Invalid HTTP response"
    fi
    
    # Log response (safely)
    if echo "$response_body" | jq '.' >/dev/null 2>&1; then
        log_debug "Response: $(echo "$response_body" | jq -c '.')"
    else
        log_debug "Response: $response_body"
    fi
    
    # Handle response codes
    case "$status_code" in
        2[0-9][0-9])
            log_success "NVCF account created successfully!"
            log_info "Account '$NVCF_ACCOUNT_NAME' is ready"
            return 0
            ;;
        409)
            log_warning "Account already exists (HTTP 409)"
            log_info "Treating as success since the account is available"
            return 0
            ;;
        401|403)
            log_error "Authentication/authorization failed (HTTP $status_code)"
            log_warning "Check JWT token validity and API permissions"
            error_exit "$EXIT_AUTH_ERROR" "Authentication failed"
            ;;
        *)
            log_error "Failed to create account (HTTP $status_code)"
            error_exit "$EXIT_ACCOUNT_ERROR" "Account creation failed"
            ;;
    esac
}

# =============================================================================
# MAIN EXECUTION FLOW
# =============================================================================

main() {
    log_info "Starting $SCRIPT_NAME v$SCRIPT_VERSION"
    log_info "Target account: $NVCF_ACCOUNT_NAME"
    
    # Validation phase
    validate_dependencies
    validate_environment
    
    # API readiness phase
    wait_for_api_ready
    
    # OpenBao authentication phase (JWT auth)
    local vault_token
    vault_token=$(login_to_openbao)
    test_openbao_connection "$vault_token"
    
    # JWT token generation phase
    local bearer_token
    bearer_token=$(generate_jwt_token "$vault_token")
    
    # Account creation phase  
    create_nvcf_account "$bearer_token"
    
    log_success "NCA bootstrap completed successfully!"
}

# =============================================================================
# SCRIPT ENTRY POINT
# =============================================================================

# Only run main if script is executed directly (not sourced)
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi
