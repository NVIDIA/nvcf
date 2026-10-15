#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Milestone 3 Validation Script
# Validates: Kubernetes integration, CRDs, controllers, E2E

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

PASSED=0
FAILED=0
SKIPPED=0

NAMESPACE="nvsnap-system"
TEST_NAMESPACE="nvsnap-test"

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_pass() { echo -e "${GREEN}[PASS]${NC} $1"; ((PASSED++)); }
log_fail() { echo -e "${RED}[FAIL]${NC} $1"; ((FAILED++)); }
log_skip() { echo -e "${YELLOW}[SKIP]${NC} $1"; ((SKIPPED++)); }
log_header() {
    echo ""
    echo -e "${BLUE}========================================${NC}"
    echo -e "${BLUE} $1${NC}"
    echo -e "${BLUE}========================================${NC}"
}

cleanup() {
    log_info "Cleaning up..."
    kubectl delete namespace $TEST_NAMESPACE --ignore-not-found 2>/dev/null || true
}

trap cleanup EXIT

setup() {
    log_info "Setting up test environment..."
    kubectl create namespace $TEST_NAMESPACE --dry-run=client -o yaml | kubectl apply -f -
    
    # Build and load images if using kind
    if kind get clusters 2>/dev/null | grep -q nvsnap; then
        log_info "Loading images into kind cluster..."
        make docker-build
        kind load docker-image ghcr.io/nvsnap/nvsnap-controller:dev --name nvsnap
        kind load docker-image ghcr.io/nvsnap/nvsnap-agent:dev --name nvsnap
        kind load docker-image ghcr.io/nvsnap/nvsnap-api:dev --name nvsnap
    fi
}

# Test 1: CRD Installation
test_crd_installation() {
    log_header "Test 1: CRD Installation"
    
    log_info "Installing CRDs..."
    if kubectl apply -f deploy/helm/nvsnap/crds/ 2>&1; then
        sleep 2
        
        # Verify each CRD
        for CRD in checkpoints.nvsnap.io restores.nvsnap.io nvsnapnodes.nvsnap.io checkpointpolicies.nvsnap.io; do
            if kubectl get crd $CRD &> /dev/null; then
                log_pass "CRD $CRD installed"
            else
                log_fail "CRD $CRD not found"
            fi
        done
    else
        log_fail "CRD installation failed"
    fi
}

# Test 2: Controller Deployment
test_controller_deployment() {
    log_header "Test 2: Controller Deployment"
    
    log_info "Deploying controller..."
    if helm upgrade --install nvsnap deploy/helm/nvsnap \
        --namespace $NAMESPACE \
        --create-namespace \
        --set controller.image.tag=dev \
        --set agent.image.tag=dev \
        --set api.image.tag=dev \
        --wait \
        --timeout=300s 2>&1; then
        
        # Check controller pod
        if kubectl get pods -n $NAMESPACE -l app.kubernetes.io/component=controller -o name | grep -q pod; then
            log_pass "Controller pod deployed"
            
            # Check leader election
            sleep 5
            LEADER=$(kubectl get lease nvsnap-controller -n $NAMESPACE -o jsonpath='{.spec.holderIdentity}' 2>/dev/null || echo "")
            if [[ -n "$LEADER" ]]; then
                log_pass "Leader election working (leader: $LEADER)"
            else
                log_fail "Leader election not working"
            fi
        else
            log_fail "Controller pod not found"
        fi
    else
        log_fail "Controller deployment failed"
    fi
}

# Test 3: Agent DaemonSet
test_agent_daemonset() {
    log_header "Test 3: Agent DaemonSet"
    
    # Check DaemonSet exists
    if kubectl get daemonset -n $NAMESPACE nvsnap-agent &> /dev/null; then
        log_pass "Agent DaemonSet created"
        
        # Check pods on GPU nodes
        GPU_NODES=$(kubectl get nodes -l nvidia.com/gpu.present=true -o name | wc -l)
        AGENT_PODS=$(kubectl get pods -n $NAMESPACE -l app.kubernetes.io/component=agent --field-selector=status.phase=Running -o name | wc -l)
        
        if [[ $AGENT_PODS -ge $GPU_NODES ]] || [[ $GPU_NODES -eq 0 ]]; then
            log_pass "Agent running on all GPU nodes ($AGENT_PODS agents, $GPU_NODES GPU nodes)"
        else
            log_fail "Agent not running on all GPU nodes ($AGENT_PODS/$GPU_NODES)"
        fi
        
        # Check agent health
        AGENT_POD=$(kubectl get pods -n $NAMESPACE -l app.kubernetes.io/component=agent -o name | head -1)
        if [[ -n "$AGENT_POD" ]]; then
            if kubectl exec -n $NAMESPACE ${AGENT_POD#pod/} -- /bin/sh -c "curl -s localhost:8080/healthz" | grep -q "ok"; then
                log_pass "Agent health check passed"
            else
                log_fail "Agent health check failed"
            fi
        fi
    else
        log_fail "Agent DaemonSet not found"
    fi
}

# Test 4: NVSNAPNode Resources
test_nvsnapnode_resources() {
    log_header "Test 4: NVSNAPNode Resources"
    
    log_info "Checking NVSNAPNode resources..."
    
    # Wait for controller to create NVSNAPNodes
    sleep 10
    
    NVSNAP_NODES=$(kubectl get nvsnapnodes -o name | wc -l)
    GPU_NODES=$(kubectl get nodes -l nvidia.com/gpu.present=true -o name | wc -l)
    
    if [[ $NVSNAP_NODES -ge $GPU_NODES ]] || [[ $GPU_NODES -eq 0 ]]; then
        log_pass "NVSNAPNode resources created ($NVSNAP_NODES nodes)"
        
        # Check node status
        for NODE in $(kubectl get nvsnapnodes -o name); do
            STATUS=$(kubectl get $NODE -o jsonpath='{.status.phase}')
            if [[ "$STATUS" == "Ready" ]]; then
                log_info "$NODE status: $STATUS"
            else
                log_info "$NODE status: $STATUS (may be expected if no GPU)"
            fi
        done
    else
        log_fail "NVSNAPNode resources not created properly"
    fi
}

# Test 5: Create Checkpoint via CR
test_checkpoint_cr() {
    log_header "Test 5: Checkpoint CR"
    
    # Deploy test workload
    log_info "Deploying test workload..."
    cat <<EOF | kubectl apply -n $TEST_NAMESPACE -f -
apiVersion: v1
kind: Pod
metadata:
  name: checkpoint-test-pod
spec:
  containers:
  - name: test
    image: python:3.11-slim
    command: ["python3", "-c"]
    args:
    - |
      import time
      counter = 0
      while True:
          with open('/tmp/counter.txt', 'w') as f:
              f.write(str(counter))
          counter += 1
          time.sleep(1)
EOF
    
    kubectl wait -n $TEST_NAMESPACE --for=condition=ready pod/checkpoint-test-pod --timeout=60s
    sleep 5
    
    # Create checkpoint
    log_info "Creating Checkpoint CR..."
    cat <<EOF | kubectl apply -n $TEST_NAMESPACE -f -
apiVersion: nvsnap.io/v1alpha1
kind: Checkpoint
metadata:
  name: test-checkpoint
spec:
  target:
    kind: Pod
    name: checkpoint-test-pod
  storage:
    type: local
    path: /tmp/checkpoints
  options:
    leaveRunning: true
EOF
    
    # Wait for checkpoint to complete
    log_info "Waiting for checkpoint to complete..."
    for i in {1..60}; do
        STATUS=$(kubectl get checkpoint -n $TEST_NAMESPACE test-checkpoint -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
        if [[ "$STATUS" == "Completed" ]]; then
            log_pass "Checkpoint completed successfully"
            
            # Verify status fields
            SIZE=$(kubectl get checkpoint -n $TEST_NAMESPACE test-checkpoint -o jsonpath='{.status.size}')
            LOCATION=$(kubectl get checkpoint -n $TEST_NAMESPACE test-checkpoint -o jsonpath='{.status.location}')
            log_info "Checkpoint size: $SIZE, location: $LOCATION"
            
            return
        elif [[ "$STATUS" == "Failed" ]]; then
            MSG=$(kubectl get checkpoint -n $TEST_NAMESPACE test-checkpoint -o jsonpath='{.status.message}')
            log_fail "Checkpoint failed: $MSG"
            return
        fi
        sleep 2
    done
    
    log_fail "Checkpoint timed out (status: $STATUS)"
}

# Test 6: Create Restore via CR
test_restore_cr() {
    log_header "Test 6: Restore CR"
    
    # Delete original pod
    log_info "Deleting original pod..."
    kubectl delete pod -n $TEST_NAMESPACE checkpoint-test-pod --wait=true
    
    # Create restore
    log_info "Creating Restore CR..."
    cat <<EOF | kubectl apply -n $TEST_NAMESPACE -f -
apiVersion: nvsnap.io/v1alpha1
kind: Restore
metadata:
  name: test-restore
spec:
  checkpoint: test-checkpoint
  target:
    name: restored-pod
EOF
    
    # Wait for restore to complete
    log_info "Waiting for restore to complete..."
    for i in {1..60}; do
        STATUS=$(kubectl get restore -n $TEST_NAMESPACE test-restore -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
        if [[ "$STATUS" == "Completed" ]]; then
            log_pass "Restore completed successfully"
            
            # Verify restored pod
            RESTORED_POD=$(kubectl get restore -n $TEST_NAMESPACE test-restore -o jsonpath='{.status.restoredPod}')
            if kubectl get pod -n $TEST_NAMESPACE $RESTORED_POD &> /dev/null; then
                log_pass "Restored pod exists: $RESTORED_POD"
            else
                log_fail "Restored pod not found"
            fi
            
            return
        elif [[ "$STATUS" == "Failed" ]]; then
            MSG=$(kubectl get restore -n $TEST_NAMESPACE test-restore -o jsonpath='{.status.message}')
            log_fail "Restore failed: $MSG"
            return
        fi
        sleep 2
    done
    
    log_fail "Restore timed out (status: $STATUS)"
}

# Test 7: Webhooks
test_webhooks() {
    log_header "Test 7: Admission Webhooks"
    
    # Test invalid checkpoint (missing target)
    log_info "Testing validation webhook..."
    
    if kubectl apply -n $TEST_NAMESPACE -f - 2>&1 <<EOF | grep -qi "denied\|invalid\|required"; then
apiVersion: nvsnap.io/v1alpha1
kind: Checkpoint
metadata:
  name: invalid-checkpoint
spec:
  storage:
    type: s3
EOF
        log_pass "Validation webhook rejected invalid checkpoint"
    else
        log_fail "Validation webhook did not reject invalid checkpoint"
    fi
    
    # Clean up
    kubectl delete checkpoint -n $TEST_NAMESPACE invalid-checkpoint --ignore-not-found 2>/dev/null || true
}

# Test 8: Controller Recovery
test_controller_recovery() {
    log_header "Test 8: Controller Recovery"
    
    # Get current controller pod
    CONTROLLER_POD=$(kubectl get pods -n $NAMESPACE -l app.kubernetes.io/component=controller -o name | head -1)
    
    if [[ -z "$CONTROLLER_POD" ]]; then
        log_skip "No controller pod found"
        return
    fi
    
    log_info "Deleting controller pod to test recovery..."
    kubectl delete $CONTROLLER_POD -n $NAMESPACE
    
    # Wait for new pod
    sleep 5
    if kubectl wait --for=condition=ready pod -l app.kubernetes.io/component=controller -n $NAMESPACE --timeout=60s 2>&1; then
        log_pass "Controller recovered successfully"
        
        # Check leader election works
        sleep 5
        LEADER=$(kubectl get lease nvsnap-controller -n $NAMESPACE -o jsonpath='{.spec.holderIdentity}' 2>/dev/null || echo "")
        if [[ -n "$LEADER" ]]; then
            log_pass "Leader re-elected after recovery"
        else
            log_fail "Leader election failed after recovery"
        fi
    else
        log_fail "Controller failed to recover"
    fi
}

# Test 9: Metrics
test_metrics() {
    log_header "Test 9: Metrics Endpoint"
    
    CONTROLLER_POD=$(kubectl get pods -n $NAMESPACE -l app.kubernetes.io/component=controller -o name | head -1)
    
    if [[ -n "$CONTROLLER_POD" ]]; then
        METRICS=$(kubectl exec -n $NAMESPACE ${CONTROLLER_POD#pod/} -- curl -s localhost:8080/metrics 2>/dev/null)
        
        if echo "$METRICS" | grep -q "nvsnap_"; then
            log_pass "NVSNAP metrics available"
            
            # Check specific metrics
            for METRIC in nvsnap_checkpoint_total nvsnap_restore_total nvsnap_controller_reconcile_total; do
                if echo "$METRICS" | grep -q "$METRIC"; then
                    log_info "Metric found: $METRIC"
                fi
            done
        else
            log_fail "NVSNAP metrics not found"
        fi
    else
        log_skip "Controller pod not available for metrics test"
    fi
}

# Test 10: E2E Test Suite
test_e2e_suite() {
    log_header "Test 10: E2E Test Suite"
    
    log_info "Running Ginkgo E2E tests..."
    
    if go test -v -tags=e2e ./test/e2e/... 2>&1; then
        log_pass "E2E test suite passed"
    else
        log_fail "E2E test suite failed"
    fi
}

# Print summary
print_summary() {
    log_header "Milestone 3 Validation Summary"
    
    TOTAL=$((PASSED + FAILED + SKIPPED))
    
    echo ""
    echo -e "  ${GREEN}Passed:${NC}  $PASSED"
    echo -e "  ${RED}Failed:${NC}  $FAILED"
    echo -e "  ${YELLOW}Skipped:${NC} $SKIPPED"
    echo -e "  Total:   $TOTAL"
    echo ""
    
    if [[ $FAILED -gt 0 ]]; then
        echo -e "${RED}========================================${NC}"
        echo -e "${RED} MILESTONE 3 VALIDATION FAILED${NC}"
        echo -e "${RED}========================================${NC}"
        exit 1
    else
        echo -e "${GREEN}========================================${NC}"
        echo -e "${GREEN} MILESTONE 3 VALIDATION PASSED${NC}"
        echo -e "${GREEN}========================================${NC}"
        exit 0
    fi
}

main() {
    echo ""
    echo -e "${BLUE}╔════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║          NVSNAP Milestone 3 Validation                      ║${NC}"
    echo -e "${BLUE}║          Kubernetes Integration                            ║${NC}"
    echo -e "${BLUE}╚════════════════════════════════════════════════════════════╝${NC}"
    echo ""
    
    setup
    
    test_crd_installation
    test_controller_deployment
    test_agent_daemonset
    test_nvsnapnode_resources
    test_checkpoint_cr
    test_restore_cr
    test_webhooks
    test_controller_recovery
    test_metrics
    test_e2e_suite
    
    print_summary
}

main "$@"
