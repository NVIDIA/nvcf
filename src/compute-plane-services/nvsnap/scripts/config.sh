#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP Configuration
# This file contains configurable paths for the NVSNAP scripts.
# Override these by setting environment variables before running scripts.

# Project root (auto-detected)
export NVSNAP_PROJECT_ROOT="${NVSNAP_PROJECT_ROOT:-$(dirname "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")")}"

# CRIU fork source directory
# Set CRIU_FORK_SRC to your forked CRIU repository path
export CRIU_FORK_SRC="${CRIU_FORK_SRC:-${NVSNAP_PROJECT_ROOT}/../criu}"

# cuda-checkpoint binary path
# Set CUDA_CHECKPOINT_PATH to the cuda-checkpoint binary (official NVIDIA repo)
export CUDA_CHECKPOINT_PATH="${CUDA_CHECKPOINT_PATH:-${NVSNAP_PROJECT_ROOT}/../cuda-checkpoint/bin/x86_64_Linux/cuda-checkpoint}"

# Kubeconfig for test cluster.
#
# Intentionally NOT overridden here. Previous versions hard-coded a
# fallback to $HOME/.kube/configs/gcp/example-gpu-cluster — if a
# user unset KUBECONFIG to use their default ~/.kube/config (the
# kubectl-standard path), the export silently set KUBECONFIG to that
# non-existent file and every kubectl call fell back to localhost:8080
# (the e2e test-e2e.sh / checkpoint.sh silent-fail). Now we leave
# KUBECONFIG alone: if the user has it set, we honor it; if not,
# kubectl picks up ~/.kube/config the standard way.

# SSH credentials file
export NVSNAP_CREDENTIALS="${NVSNAP_CREDENTIALS:-$HOME/.nvsnap/credentials}"

# Function to find cuda-checkpoint binary
find_cuda_checkpoint() {
    # Check explicit path first
    if [ -n "${CUDA_CHECKPOINT_PATH:-}" ] && [ -f "$CUDA_CHECKPOINT_PATH" ] && [ -x "$CUDA_CHECKPOINT_PATH" ]; then
        echo "$CUDA_CHECKPOINT_PATH"
        return 0
    fi
    
    # Search common locations
    local search_paths=(
        "${NVSNAP_PROJECT_ROOT}/bin/cuda-checkpoint"
        "/usr/local/bin/cuda-checkpoint"
        "/usr/bin/cuda-checkpoint"
    )
    
    for path in "${search_paths[@]}"; do
        if [ -f "$path" ] && [ -x "$path" ]; then
            echo "$path"
            return 0
        fi
    done
    
    return 1
}

# Function to validate CRIU source exists
validate_criu_source() {
    if [ ! -d "${CRIU_FORK_SRC}" ]; then
        echo "ERROR: CRIU fork source not found at ${CRIU_FORK_SRC}" >&2
        echo "Set CRIU_FORK_SRC environment variable to your forked CRIU repository path" >&2
        return 1
    fi
    # Check for Makefile (exists in all CRIU versions)
    if [ ! -f "${CRIU_FORK_SRC}/Makefile" ]; then
        echo "ERROR: ${CRIU_FORK_SRC} doesn't look like a CRIU source directory (no Makefile)" >&2
        return 1
    fi
    # Check for criu subdirectory
    if [ ! -d "${CRIU_FORK_SRC}/criu" ]; then
        echo "ERROR: ${CRIU_FORK_SRC} doesn't look like a CRIU source directory (no criu/ subdir)" >&2
        return 1
    fi
    return 0
}

# Function to load SSH credentials
load_credentials() {
    if [ -f "$NVSNAP_CREDENTIALS" ]; then
        source "$NVSNAP_CREDENTIALS"
    fi
    
    if [ -z "${SSH_USER:-}" ] || [ -z "${SSH_PASS:-}" ]; then
        echo "ERROR: SSH_USER and SSH_PASS must be set" >&2
        echo "Either export them or create $NVSNAP_CREDENTIALS with:" >&2
        echo "  SSH_USER=your_username" >&2
        echo "  SSH_PASS=your_password" >&2
        return 1
    fi
    return 0
}

# Function to validate kubeconfig
validate_kubeconfig() {
    if [ -z "${KUBECONFIG:-}" ]; then
        echo "ERROR: KUBECONFIG environment variable not set" >&2
        return 1
    fi
    if [ ! -f "$KUBECONFIG" ]; then
        echo "ERROR: KUBECONFIG file not found: $KUBECONFIG" >&2
        return 1
    fi
    return 0
}
