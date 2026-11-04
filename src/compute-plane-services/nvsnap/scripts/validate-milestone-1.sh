#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Milestone 1 Validation Script
# Validates: Project setup, storage backends, CRI abstraction

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Counters
PASSED=0
FAILED=0
SKIPPED=0

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_pass() {
    echo -e "${GREEN}[PASS]${NC} $1"
    ((PASSED++))
}

log_fail() {
    echo -e "${RED}[FAIL]${NC} $1"
    ((FAILED++))
}

log_skip() {
    echo -e "${YELLOW}[SKIP]${NC} $1"
    ((SKIPPED++))
}

log_header() {
    echo ""
    echo -e "${BLUE}========================================${NC}"
    echo -e "${BLUE} $1${NC}"
    echo -e "${BLUE}========================================${NC}"
}

# Check if we're in the right directory
check_project_root() {
    if [[ ! -f "go.mod" ]] || [[ ! -d "cmd" ]]; then
        echo -e "${RED}Error: Must run from project root directory${NC}"
        exit 1
    fi
}

# Test 1: Project builds successfully
test_build() {
    log_header "Test 1: Project Build"
    
    log_info "Running 'make build'..."
    if make build 2>&1; then
        log_pass "Project builds successfully"
    else
        log_fail "Project build failed"
    fi
}

# Test 2: Unit tests pass with coverage
test_unit_tests() {
    log_header "Test 2: Unit Tests"
    
    log_info "Running unit tests with coverage..."
    if make test-coverage 2>&1; then
        # Check coverage percentage
        COVERAGE=$(go tool cover -func=coverage/coverage.out | tail -1 | awk '{print $3}' | sed 's/%//')
        if (( $(echo "$COVERAGE >= 80" | bc -l) )); then
            log_pass "Unit tests pass with ${COVERAGE}% coverage (>= 80%)"
        else
            log_fail "Coverage is ${COVERAGE}% (required >= 80%)"
        fi
    else
        log_fail "Unit tests failed"
    fi
}

# Test 3: Linting passes
test_lint() {
    log_header "Test 3: Code Linting"
    
    log_info "Running linters..."
    if make lint 2>&1; then
        log_pass "Linting passed"
    else
        log_fail "Linting failed"
    fi
}

# Test 4: CRD types compile
test_crd_types() {
    log_header "Test 4: CRD Type Generation"
    
    log_info "Checking CRD types..."
    if [[ -f "pkg/apis/nvsnap.io/v1alpha1/types.go" ]]; then
        if go build ./pkg/apis/... 2>&1; then
            log_pass "CRD types compile successfully"
        else
            log_fail "CRD types failed to compile"
        fi
    else
        log_fail "CRD types not found"
    fi
}

# Test 5: Storage backend - Local filesystem
test_storage_local() {
    log_header "Test 5: Local Storage Backend"
    
    log_info "Testing local filesystem storage..."
    
    TEST_DIR=$(mktemp -d)
    TEST_FILE="$TEST_DIR/test-upload.txt"
    echo "Hello, NVSNAP!" > "$TEST_FILE"
    
    # Run storage test
    if go test -v -run TestLocalStorage ./internal/storage/... 2>&1; then
        log_pass "Local storage backend works"
    else
        log_fail "Local storage backend test failed"
    fi
    
    rm -rf "$TEST_DIR"
}

# Test 6: Storage backend - S3 (MinIO)
test_storage_s3() {
    log_header "Test 6: S3 Storage Backend"
    
    # Check if MinIO is available
    if ! command -v mc &> /dev/null && ! kubectl get pods -l app=minio &> /dev/null 2>&1; then
        log_skip "MinIO not available, skipping S3 tests"
        return
    fi
    
    log_info "Testing S3 storage backend..."
    
    if go test -v -run TestS3Storage -tags=integration ./internal/storage/... 2>&1; then
        log_pass "S3 storage backend works"
    else
        log_fail "S3 storage backend test failed"
    fi
}

# Test 7: CRI abstraction
test_cri_abstraction() {
    log_header "Test 7: CRI Abstraction"
    
    log_info "Testing CRI interface..."
    
    if go test -v ./pkg/cri/... 2>&1; then
        log_pass "CRI abstraction tests pass"
    else
        log_fail "CRI abstraction tests failed"
    fi
}

# Test 8: CRI connectivity (requires cluster)
test_cri_connectivity() {
    log_header "Test 8: CRI Connectivity"
    
    # Check if we have access to containerd socket
    CONTAINERD_SOCK="/run/containerd/containerd.sock"
    CRIO_SOCK="/run/crio/crio.sock"
    
    if [[ -S "$CONTAINERD_SOCK" ]]; then
        log_info "Testing containerd connectivity..."
        if go test -v -run TestContainerdConnectivity -tags=integration ./pkg/cri/... 2>&1; then
            log_pass "containerd connectivity works"
        else
            log_fail "containerd connectivity failed"
        fi
    elif [[ -S "$CRIO_SOCK" ]]; then
        log_info "Testing CRI-O connectivity..."
        if go test -v -run TestCRIOConnectivity -tags=integration ./pkg/cri/... 2>&1; then
            log_pass "CRI-O connectivity works"
        else
            log_fail "CRI-O connectivity failed"
        fi
    else
        log_skip "No container runtime socket found, skipping connectivity test"
    fi
}

# Test 9: Configuration loading
test_configuration() {
    log_header "Test 9: Configuration System"
    
    log_info "Testing configuration loading..."
    
    if go test -v -run TestConfig ./internal/config/... 2>&1; then
        log_pass "Configuration system works"
    else
        log_fail "Configuration system test failed"
    fi
}

# Test 10: Metrics endpoint
test_metrics() {
    log_header "Test 10: Metrics Endpoint"
    
    log_info "Testing Prometheus metrics..."
    
    if go test -v -run TestMetrics ./internal/observability/... 2>&1; then
        log_pass "Metrics endpoint works"
    else
        log_fail "Metrics endpoint test failed"
    fi
}

# Print summary
print_summary() {
    log_header "Validation Summary"
    
    TOTAL=$((PASSED + FAILED + SKIPPED))
    
    echo ""
    echo -e "  ${GREEN}Passed:${NC}  $PASSED"
    echo -e "  ${RED}Failed:${NC}  $FAILED"
    echo -e "  ${YELLOW}Skipped:${NC} $SKIPPED"
    echo -e "  Total:   $TOTAL"
    echo ""
    
    if [[ $FAILED -gt 0 ]]; then
        echo -e "${RED}========================================${NC}"
        echo -e "${RED} MILESTONE 1 VALIDATION FAILED${NC}"
        echo -e "${RED}========================================${NC}"
        exit 1
    else
        echo -e "${GREEN}========================================${NC}"
        echo -e "${GREEN} MILESTONE 1 VALIDATION PASSED${NC}"
        echo -e "${GREEN}========================================${NC}"
        exit 0
    fi
}

# Main execution
main() {
    echo ""
    echo -e "${BLUE}╔════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║          NVSNAP Milestone 1 Validation                      ║${NC}"
    echo -e "${BLUE}║          Foundation: Project Setup & Abstractions          ║${NC}"
    echo -e "${BLUE}╚════════════════════════════════════════════════════════════╝${NC}"
    echo ""
    
    check_project_root
    
    test_build
    test_unit_tests
    test_lint
    test_crd_types
    test_storage_local
    test_storage_s3
    test_cri_abstraction
    test_cri_connectivity
    test_configuration
    test_metrics
    
    print_summary
}

main "$@"
