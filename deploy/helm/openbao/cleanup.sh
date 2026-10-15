#!/bin/bash

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
if [ ! -f "${SCRIPT_DIR}/utils/utils.sh" ]; then
    echo "Error: utils.sh not found in ${SCRIPT_DIR}/utils"
    exit 1
fi
source "$SCRIPT_DIR/utils/utils.sh"

if ! check_kubernetes; then
    log_error "kubernetes check failed"
    exit 1
fi

if ! check_helm; then
    log_error "helm check failed"
    exit 1
fi

cleanup_cluster() {
    local namespace=$1
    local statefulset=$2

    log_section "Starting OpenBao cleanup"

    # Delete the Helm release
    log_info "Uninstalling OpenBao Helm release in namespace: $namespace"
    if ! helm uninstall $statefulset -n $namespace --ignore-not-found > /dev/null 2>&1; then
        log_error "Failed to uninstall Helm release (exit code: $?): $?"
        exit 1
    fi
    log_success "Cleaned up OpenBao Helm release in namespace: $namespace"

    # Delete PVCs used by OpenBao
    log_info "Deleting OpenBao PVCs in namespace: $namespace"
    if ! kubectl delete pvc -l app.kubernetes.io/name=openbao -n $namespace > /dev/null 2>&1; then
        log_error "Failed to delete PVCs (exit code: $?): $?"
        exit 1
    fi
    log_success "Cleaned up OpenBao PVCs in namespace: $namespace"

    # Delete the unseal key and root token secret
    log_info "Deleting secrets in namespace: $namespace"
    if ! kubectl delete secret $statefulset-unseal -n $namespace --ignore-not-found=true > /dev/null 2>&1; then
        log_error "Failed to delete unseal secret (exit code: $?): $?"
        exit 1
    fi
    if ! kubectl delete secret $statefulset-root-token -n $namespace --ignore-not-found=true > /dev/null 2>&1; then
        log_error "Failed to delete root token secret (exit code: $?): $?"
        exit 1
    fi
    if ! kubectl delete secret cluster-jwt -n $namespace --ignore-not-found=true > /dev/null 2>&1; then
        log_error "Failed to delete cluster-jwt secret (exit code: $?): $?"
        exit 1
    fi
    log_success "Cleaned up secrets in namespace: $namespace"

    # Delete test-nginx namespace if available
    log_info "Deleting k8s test-nginx namespace (if present)"
    if ! kubectl delete namespace test-nginx --ignore-not-found=true > /dev/null 2>&1; then
        log_error "Failed to delete namespace 'test-nginx' (exit code: $?): $?"
        exit 1
    fi
    log_success "Namespace 'test-nginx' deleted successfully (or it didn't exist)."
}


# Usage
namespace="vault-system"
statefulset="openbao-server"

if ! cleanup_cluster "$namespace" "$statefulset"; then
    echo "Failed to cleanup cluster"
    exit 1
fi

