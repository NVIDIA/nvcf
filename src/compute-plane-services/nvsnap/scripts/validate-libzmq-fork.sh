#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Validate libzmq fork with checkpoint/restore API
# This script builds libzmq from source and runs checkpoint tests

set -euo pipefail

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }
log_step() { echo -e "${BLUE}[STEP]${NC} $*"; }

# Configuration
source "$(dirname "${BASH_SOURCE[0]}")/_deps.sh"
nvsnap_resolve_sibling LIBZMQ_SRC libzmq
BUILD_DIR="${LIBZMQ_SRC}/build"
INSTALL_PREFIX="${INSTALL_PREFIX:-/usr/local}"

# Check if libzmq source exists
if [ ! -d "$LIBZMQ_SRC" ]; then
    log_error "libzmq source not found at $LIBZMQ_SRC"
    log_info "Clone from: https://github.com/balajinvda/libzmq.git"
    log_info "Branch: checkpoint-restore-v1"
    exit 1
fi

cd "$LIBZMQ_SRC"

# Verify branch
current_branch=$(git branch --show-current 2>/dev/null || echo "unknown")
if [ "$current_branch" != "checkpoint-restore-v1" ]; then
    log_warn "Current branch is '$current_branch', expected 'checkpoint-restore-v1'"
    log_info "Switch with: git checkout checkpoint-restore-v1"
fi

log_info "Validating libzmq fork with checkpoint/restore API"
log_info "Source: $LIBZMQ_SRC"
log_info "Branch: $current_branch"
echo ""

# Step 1: Clean build directory
log_step "Step 1/6: Cleaning build directory..."
if [ -d "$BUILD_DIR" ]; then
    log_info "Removing existing build directory"
    rm -rf "$BUILD_DIR"
fi
mkdir -p "$BUILD_DIR"
cd "$BUILD_DIR"

# Step 2: Check dependencies
log_step "Step 2/6: Checking build dependencies..."
missing_deps=()

if ! command -v cmake >/dev/null 2>&1; then
    missing_deps+=("cmake")
fi

if ! command -v g++ >/dev/null 2>&1; then
    missing_deps+=("g++")
fi

if ! dpkg -l libsodium-dev 2>/dev/null | grep -q "^ii"; then
    missing_deps+=("libsodium-dev")
fi

if [ ${#missing_deps[@]} -gt 0 ]; then
    log_error "Missing dependencies: ${missing_deps[*]}"
    log_info "Install with: sudo apt-get install -y ${missing_deps[*]}"
    exit 1
fi

log_info "All dependencies found ✓"
echo ""

# Step 3: Configure with CMake
log_step "Step 3/6: Configuring with CMake..."
log_info "Install prefix: $INSTALL_PREFIX"

if ! cmake -DCMAKE_INSTALL_PREFIX="$INSTALL_PREFIX" \
           -DCMAKE_BUILD_TYPE=Release \
           -DBUILD_TESTS=ON \
           ..; then
    log_error "CMake configuration failed"
    exit 1
fi

log_info "Configuration successful ✓"
echo ""

# Step 4: Build libzmq
log_step "Step 4/6: Building libzmq..."
NPROC=$(nproc)
log_info "Building with $NPROC parallel jobs"

if ! make -j"$NPROC"; then
    log_error "Build failed"
    exit 1
fi

log_info "Build successful ✓"
echo ""

# Step 5: Verify checkpoint.cpp was built
log_step "Step 5/6: Verifying checkpoint API..."
if [ ! -f "lib/libzmq.so" ]; then
    log_error "libzmq.so not found in build directory"
    exit 1
fi

# Check if checkpoint symbols are present
if nm lib/libzmq.so | grep -q "zmq_ctx_checkpoint"; then
    log_info "Found checkpoint API symbols ✓"
else
    log_error "Checkpoint API symbols not found in libzmq.so"
    log_info "Expected symbols: zmq_ctx_checkpoint, zmq_ctx_restore, zmq_get_all_contexts"
    nm lib/libzmq.so | grep zmq_ctx || true
    exit 1
fi

# List all checkpoint-related symbols
log_info "Checkpoint API symbols found:"
nm lib/libzmq.so | grep -E "zmq_(ctx_checkpoint|ctx_restore|get_all_contexts|checkpoint_destroy|ctx_checkpoint_resume)" | while read -r line; do
    echo "  $line"
done
echo ""

# Step 6: Run tests
log_step "Step 6/6: Running checkpoint/restore tests..."

# Find test binaries
test_binaries=(
    "bin/test_checkpoint_basic"
    "bin/test_checkpoint_socket"
    "bin/test_checkpoint_endpoints"
)

# Check if tests were built
tests_found=0
for test_bin in "${test_binaries[@]}"; do
    if [ -f "$test_bin" ]; then
        tests_found=$((tests_found + 1))
    fi
done

if [ $tests_found -eq 0 ]; then
    log_error "No test binaries found in $BUILD_DIR/bin/"
    log_info "Tests may not have been built. Check CMakeLists.txt"
    ls -la bin/ 2>/dev/null || log_info "bin/ directory not found"
    exit 1
fi

log_info "Found $tests_found checkpoint tests"
echo ""

# Run each test
failed_tests=()
passed_tests=()

for test_bin in "${test_binaries[@]}"; do
    test_name=$(basename "$test_bin")

    if [ ! -f "$test_bin" ]; then
        log_warn "Test not found: $test_name (skipping)"
        continue
    fi

    echo "----------------------------------------"
    log_info "Running: $test_name"
    echo ""

    # Set LD_LIBRARY_PATH to use built library
    export LD_LIBRARY_PATH="$BUILD_DIR/lib:${LD_LIBRARY_PATH:-}"

    if ./"$test_bin"; then
        echo ""
        log_info "✓ $test_name PASSED"
        passed_tests+=("$test_name")
    else
        echo ""
        log_error "✗ $test_name FAILED"
        failed_tests+=("$test_name")
    fi
    echo ""
done

# Summary
echo "========================================"
echo ""
log_info "VALIDATION SUMMARY"
echo ""
log_info "Tests passed: ${#passed_tests[@]}"
for test in "${passed_tests[@]}"; do
    echo "  ✓ $test"
done

if [ ${#failed_tests[@]} -gt 0 ]; then
    echo ""
    log_error "Tests failed: ${#failed_tests[@]}"
    for test in "${failed_tests[@]}"; do
        echo "  ✗ $test"
    done
    echo ""
    echo "========================================"
    log_error "VALIDATION FAILED"
    exit 1
fi

echo ""
echo "========================================"
log_info "✓ ALL TESTS PASSED"
echo "========================================"
echo ""

# Show next steps
log_info "Next steps:"
echo "  1. Install libzmq:  sudo make install && sudo ldconfig"
echo "  2. Build libzmq image: ./scripts/build-libzmq-image.sh"
echo "  3. Verify system install: ldconfig -p | grep libzmq"
echo ""

exit 0
