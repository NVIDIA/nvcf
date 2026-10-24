#!/bin/bash

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
if [ ! -f "${SCRIPT_DIR}/../../utils/utils.sh" ]; then
    echo "Error: utils.sh not found in ${SCRIPT_DIR}/../../utils"
    exit 1
fi
source "$SCRIPT_DIR/../../utils/utils.sh"

test_kv() {
    local namespace=$1
    local statefulset=$2
    local root_token=$(get_root_token "$namespace" "$statefulset")
    local secret_path="services/all/kv/test"
    log_section "Starting continuous KV test (Ctrl+C to stop)..."
    while true; do
        log_step "Writing test value - openbao-server-2 (bao kv put $secret_path foo=bar)"
        if output=$(kubectl exec $statefulset-2 -c openbao -n $namespace -- \
            env BAO_TOKEN=$root_token \
            bao kv put $secret_path foo=bar 2>&1); then
            log_success "Successfully wrote to KV2 store: $output"
        else
            log_error "Failed to write to KV2 store (exit code: $?): $output"
        fi

        log_step "Reading test value - openbao-server-2 (bao kv get $secret_path)"
        if output=$(kubectl exec $statefulset-2 -c openbao -n $namespace -- \
            env BAO_TOKEN=$root_token \
            bao kv get $secret_path 2>&1); then
            log_success "Successfully read the KV2 secret: $output"
        else
            log_error "Failed to read the KV2 secret (exit code: $?): $output"
        fi

        log_step "Deleting test value - openbao-server-2 (bao kv delete $secret_path)"
        if output=$(kubectl exec $statefulset-2 -c openbao -n $namespace -- \
            env BAO_TOKEN=$root_token \
            bao kv delete $secret_path 2>&1); then
            log_success "Successfully deleted the KV2 secret: $output"
        else
            log_error "Failed to delete the KV2 secret (exit code: $?): $output"
        fi
        sleep 1
    done
}

# Usage
namespace="vault-system"
statefulset="openbao-server"

if ! check_kubernetes; then
    exit 1
fi

if ! check_resources "$namespace" "$statefulset"; then
    exit 1
fi

if ! test_kv "$namespace" "$statefulset"; then
    log_error "Failed to test KV store"
    exit 1
fi