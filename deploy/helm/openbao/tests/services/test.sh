#!/bin/bash

# Exit on Ctrl+C
trap 'echo ""; exit 130' INT

# Save paths before sourcing utils.sh (which overwrites SCRIPT_DIR)
TEST_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
REPO_ROOT="$(cd "$TEST_DIR/../.." && pwd)"

if [ ! -f "${REPO_ROOT}/utils/utils.sh" ]; then
    echo "Error: utils.sh not found in ${REPO_ROOT}/utils"
    exit 1
fi

source "$REPO_ROOT/utils/utils.sh"

# Usage
usage() {
    echo "Usage: $0 <colima_profile> [--cleanup]"
    echo ""
    echo "Tests the OpenBao agent injector by deploying the nvcf-api pod with secret injection."
    echo ""
    echo "Arguments:"
    echo "  colima_profile   Name of the Colima profile (e.g., amd64, arm64)"
    echo "  --cleanup        Remove the test namespace and pod"
    echo ""
    echo "Examples:"
    echo "  $0 amd64           # Deploy test pod"
    echo "  $0 amd64 --cleanup # Clean up test resources"
    exit 1
}

# Parse arguments
if [[ -z "$1" ]]; then
    usage
fi

COLIMA_PROFILE="$1"
CLEANUP=false

if [[ "$2" == "--cleanup" ]] || [[ "$1" == "--cleanup" ]]; then
    CLEANUP=true
    if [[ "$1" == "--cleanup" ]]; then
        echo "Error: colima_profile is required even for cleanup"
        usage
    fi
fi

namespace="nvcf"
manifest_path="$TEST_DIR/nginx.yaml"

# Switch to correct kubectl context
log_step "Switching kubectl context to colima-${COLIMA_PROFILE}..."
if ! kubectl config use-context "colima-${COLIMA_PROFILE}" > /dev/null 2>&1; then
    log_error "Failed to switch to context colima-${COLIMA_PROFILE}"
    log_info "Available contexts:"
    kubectl config get-contexts -o name
    exit 1
fi
log_success "Using context: colima-${COLIMA_PROFILE}"

if ! check_kubernetes; then
    exit 1
fi

if [[ "$CLEANUP" == "true" ]]; then
    log_step "Cleaning up namespace '$namespace'..."
    kubectl delete namespace "$namespace" --ignore-not-found --wait > /dev/null 2>&1
    log_success "Namespace '$namespace' cleaned up successfully."
    exit 0
fi

# Step 1: Create the namespace
log_step "Creating namespace '$namespace'..."
if ! kubectl create namespace "$namespace" > /dev/null 2>&1; then
    if kubectl get namespace "$namespace" > /dev/null 2>&1; then
        log_success "Namespace '$namespace' already exists."
    else
        log_error "Failed to create namespace '$namespace'."
        exit 1
    fi
else
    log_success "Namespace '$namespace' created successfully."
fi

# Step 2: Apply the manifest to the namespace
log_step "Applying manifest '$manifest_path' to namespace '$namespace'..."
if ! kubectl apply -f "$manifest_path" -n "$namespace"; then
    log_error "Failed to apply manifest '$manifest_path' to namespace '$namespace'."
    exit 1
fi
log_success "Manifest applied successfully."

# Step 3: Wait for pod to be ready
log_step "Waiting for pod to be ready..."
if ! kubectl wait --for=condition=ready pod -l app=nvcf-api -n "$namespace" --timeout=120s; then
    log_warn "Pod not ready within timeout. Checking status..."
fi

# Step 4: Show pod status and injector results
log_step "Pod status:"
kubectl get pod -n "$namespace" -o wide

log_step "Checking injected agent..."
POD_NAME=$(kubectl get pod -n "$namespace" -l app=nvcf-api -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
if [[ -n "$POD_NAME" ]]; then
    echo ""
    log_info "Pod containers:"
    kubectl get pod "$POD_NAME" -n "$namespace" -o jsonpath='{.spec.containers[*].name}' | tr ' ' '\n'
    echo ""
    
    # Show injected agent image version
    log_info "Injected OpenBao Agent image:"
    AGENT_IMAGE=$(kubectl get pod "$POD_NAME" -n "$namespace" -o jsonpath='{.spec.containers[?(@.name=="openbao-agent")].image}')
    if [[ -n "$AGENT_IMAGE" ]]; then
        echo "  $AGENT_IMAGE"
        AGENT_VERSION=$(echo "$AGENT_IMAGE" | sed 's/.*://')
        log_success "Agent version: $AGENT_VERSION"
    else
        log_warn "openbao-agent container not found - injection may have failed"
    fi
    echo ""
    
    log_info "Checking if secrets were rendered..."
    if kubectl exec "$POD_NAME" -n "$namespace" -c nginx -- ls /vault/secrets/ 2>/dev/null; then
        log_success "Secrets directory exists!"
        echo ""
        log_info "Secret contents:"
        kubectl exec "$POD_NAME" -n "$namespace" -c nginx -- cat /vault/secrets/secrets.json 2>/dev/null || true
    else
        log_warn "Secrets not yet rendered. Check agent logs:"
        echo "  kubectl logs $POD_NAME -n $namespace -c openbao-agent"
    fi
else
    log_error "No pod found with label app=nvcf-api"
fi

echo ""
log_success "Injector test complete!"
echo ""
echo "Next steps:"
echo "  - View agent logs: kubectl logs $POD_NAME -n $namespace -c openbao-agent"
echo "  - View init logs:  kubectl logs $POD_NAME -n $namespace -c openbao-agent-init"
echo "  - Cleanup: $0 $COLIMA_PROFILE --cleanup"
