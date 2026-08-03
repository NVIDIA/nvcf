#!/bin/bash

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
if [ ! -f "${SCRIPT_DIR}/../utils/utils.sh" ]; then
    echo "Error: utils.sh not found in ${SCRIPT_DIR}/../utils"
    exit 1
fi
source "$SCRIPT_DIR/../utils/utils.sh"

generate_nvcf_api_admin_token() {
    local namespace=$1
    local statefulset=$2
    local root_token=$(get_root_token "$namespace" "$statefulset")

    log_step "Generating admin JWT token for nvcf-api"
    
    local jwt_output
    if jwt_output=$(kubectl exec -it $statefulset-0 -c openbao -n $namespace -- \
        env BAO_TOKEN=$root_token \
        bao write -f services/nvcf-api/jwt/sign/nvcf-api-admin 2>&1); then
        
        log_success "Admin JWT token generated successfully for nvcf-api"
        echo "JWT Token Output:"
        echo "$jwt_output"
        
        # Try to extract just the token value if it's in the expected format
        local token=$(echo "$jwt_output" | grep -E "^token\s+" | awk '{print $2}' | tr -d '\r')
        if [ -n "$token" ]; then
            echo ""
            echo "Extracted Token:"
            echo "$token"
            
            # Optionally decode the JWT to show claims (requires base64 and jq)
            if command -v jq >/dev/null 2>&1; then
                echo ""
                echo "JWT Claims (decoded):"
                # Extract payload (second part of JWT)
                local payload=$(echo "$token" | cut -d'.' -f2)
                # Add padding if needed for base64 decoding
                local padded_payload
                case $((${#payload} % 4)) in
                    2) padded_payload="${payload}==" ;;
                    3) padded_payload="${payload}=" ;;
                    *) padded_payload="$payload" ;;
                esac
                echo "$padded_payload" | base64 -d 2>/dev/null | jq . 2>/dev/null || echo "Could not decode JWT payload"
            fi
        fi
    else
        log_error "Failed to generate admin JWT token: $jwt_output"
        return 1
    fi
}

# Default OpenBao configuration
namespace="vault-system"
statefulset="openbao-server"

if ! check_resources "$namespace" "$statefulset"; then
    exit 1
fi

if ! generate_nvcf_api_admin_token "$namespace" "$statefulset"; then
    log_error "Failed to generate nvcf-api admin JWT token"
    exit 1
fi 