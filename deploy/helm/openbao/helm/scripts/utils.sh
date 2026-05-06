#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ ! -f "${SCRIPT_DIR}/log.sh" ]; then
    echo "Error: log.sh not found in ${SCRIPT_DIR}"
    exit 1
fi
source "${SCRIPT_DIR}/log.sh"

get_root_token() {
    local namespace=$1
    local statefulset=$2
    kubectl get secret $statefulset-root-token -n $namespace -o jsonpath='{.data.root_token}' | base64 -d
}

check_docker() {
    if ! command -v docker &>/dev/null; then
        log_error "docker is not installed"
        return 1
    fi
    return 0
}

check_jq() {
    if ! command -v jq &>/dev/null; then
        log_error "jq is not installed"
        return 1
    fi
    return 0
}

check_helm() {
    if ! command -v helm &>/dev/null; then
        log_error "Failed to retrieve Helm version. Please ensure Helm is installed."
        return 1
    fi

    helm_version=$(helm version --template="{{.Version}}" | tr -d "v")
    if ! version_gt "$helm_version" "3.12.0"; then
        log_error "Helm version 3.12+ is required, found version: $helm_version"
        return 1
    fi
    return 0
}

version_gt() {
    local version1=$1
    local version2=$2
    if [[ "$(printf '%s\n' "$version2" "$version1" | sort -V | head -n1)" != "$version1" ]]; then
        return 0
    fi
    return 1
}

check_kubernetes() {
    if ! command -v kubectl &>/dev/null; then
        log_error "kubectl is not installed"
        return 1
    fi

    k8s_version=$(kubectl version --output=json | jq -r '.serverVersion.gitVersion' | tr -d "v")
    if ! version_gt "$k8s_version" "1.29.0"; then
        log_error "Kubernetes version 1.29+ is required, found version: $k8s_version"
        return 1
    fi
    return 0
}

check_colima() {
    if ! command -v colima &>/dev/null; then
        log_error "Colima is not installed"
        return 1
    fi
    return 0
}

check_resources() {
    local namespace=$1
    local statefulset=$2

    # Check if kubernetes version is >= 1.29+
    if ! check_kubernetes; then
        log_error "Kubernetes check failed"
        exit 1
    fi

    # Check if namespace exists
    if ! kubectl get namespace "$namespace" &>/dev/null; then
        log_error "Namespace '$namespace' not found"
        return 1
    fi

    # Check if pod exists and is running
    if ! kubectl get pod "$statefulset-0" -n "$namespace" &>/dev/null; then
        log_error "Pod '$statefulset-0' not found in namespace '$namespace'"
        return 1
    fi

    pod_status=$(kubectl get pod "$statefulset-0" -n "$namespace" -o jsonpath='{.status.phase}')
    if [ "$pod_status" != "Running" ]; then
        log_error "Pod '$statefulset-0' is not running (status: $pod_status)"
        return 1
    fi

    # Check if root token secret exists
    if ! kubectl get secret "$statefulset-root-token" -n "$namespace" &>/dev/null; then
        log_error "Secret '$statefulset-root-token' not found in namespace '$namespace'"
        return 1
    fi

    return 0
}
