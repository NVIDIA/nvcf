#!/bin/bash

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
if [ ! -f "${SCRIPT_DIR}/../utils/utils.sh" ]; then
    echo "Error: utils.sh not found in ${SCRIPT_DIR}/../utils"
    exit 1
fi
source "$SCRIPT_DIR/../utils/utils.sh"

register_and_enable_plugin() {
    local namespace="$1"
    local statefulset=$2
    local root_token=$(get_root_token "$namespace" "$statefulset")
    local plugin_dir="/openbao/plugins"
    local plugin_name="vault-plugin-secrets-jwt"
    local enable_path="jwt"
    local pod_name=$statefulset-0

    # Step 1: Verify Plugin Binary Exists
    log_step "Verifying if plugin binary exists at $plugin_dir/$plugin_name..."
    if ! kubectl exec -n "$namespace" "$pod_name" -c openbao -- test -f "$plugin_dir/$plugin_name"; then
        log_error "Plugin binary not found at $plugin_dir/$plugin_name. Ensure it has been copied to this location."
        return 1
    fi
    log_success "Plugin binary found"

    # Step 2: Calculate SHA256 Checksum of the Plugin Binary
    log_step "Calculating SHA256 checksum for $plugin_name..."
    local plugin_sha256=$(kubectl exec -n "$namespace" "$pod_name" -c openbao -- sha256sum "$plugin_dir/$plugin_name" | awk '{print $1}')
    if [[ -z "$plugin_sha256" ]]; then
        log_error "Failed to calculate SHA256 checksum for $plugin_name."
        return 1
    fi
    log_success "SHA256 checksum: $plugin_sha256"

    # Step 3: Register the Plugin
    log_step "Registering plugin '$plugin_name'..."
    kubectl exec -n "$namespace" "$pod_name" -c openbao -- \
        env BAO_TOKEN="$root_token" \
        bao plugin register \
        -sha256="$plugin_sha256" \
        -command="$plugin_name" \
        secret "$plugin_name"
    if [[ $? -ne 0 ]]; then
        log_error "Failed to register plugin '$plugin_name'."
        return 1
    fi
    log_success "Plugin '$plugin_name' registered successfully."

    # Step 4: Enable the Plugin as a Secrets Engine
    log_step "Enabling plugin '$plugin_name' as a secrets engine at path '$enable_path'..."
    kubectl exec -n "$namespace" "$pod_name" -c openbao -- \
        env BAO_TOKEN="$root_token" \
        bao secrets enable -path="$enable_path" "$plugin_name"
    if [[ $? -ne 0 ]]; then
        log_error "Failed to enable plugin '$plugin_name' as a secrets engine."
        return 1
    fi

    log_success "Plugin '$plugin_name' enabled as a secrets engine at path '$enable_path'."
}

# Usage
namespace="vault-system"
statefulset="openbao-server"

if ! check_resources "$namespace" "$statefulset"; then
    exit 1
fi

if ! register_and_enable_plugin "$namespace" "$statefulset"; then
    log_error "Failed to register the sample plugin (outfoxx/vault-plugin-secrets-jwt)"
    exit 1
fi
