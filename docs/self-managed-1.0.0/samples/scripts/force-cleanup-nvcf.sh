#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# =============================================================================
# force-cleanup-nvcf.sh - NVCA Component Removal Script
# =============================================================================
# This script forcefully removes all NVCA components from a cluster.
# Use this as a LAST RESORT when normal cleanup methods fail due to:
# - Stuck finalizers on namespaces or custom resources
# - Orphaned resources blocking deletion
# - Partial deployments that need complete removal
#
# WARNING: This script will FORCEFULLY remove all NVCA resources, including
# removing finalizers which bypasses normal cleanup procedures.
#
# Usage: ./force-cleanup-nvcf.sh [--dry-run]
# =============================================================================

set -euo pipefail

# --- Configuration ---
# NVCA-related namespaces
NVCA_NAMESPACES=(
    "nvcf-backend"
    "nvca-system"
    "nvca-operator"
)

# CRDs created by NVCA components
NVCA_CRDS=(
    "nvcfbackends.nvcf.nvidia.io"
)

# --- Parse Arguments ---
DRY_RUN=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [--dry-run]"
            echo ""
            echo "Options:"
            echo "  --dry-run            Show what would be deleted without making changes"
            echo "  -h, --help           Show this help message"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

echo "=============================================="
echo "NVCA Force Cleanup Script"
echo "=============================================="
if $DRY_RUN; then
    echo "MODE: DRY-RUN (no changes will be made)"
fi
echo ""

# --- Step 1: Show and delete function pods in nvcf-backend ---
echo ">>> Step 1: Checking for function pods in nvcf-backend namespace..."
if kubectl get namespace nvcf-backend >/dev/null 2>&1; then
    pods=$(kubectl get pods -n nvcf-backend -o name 2>/dev/null || true)
    if [[ -n "$pods" ]]; then
        echo "    Found the following pods that will be deleted:"
        kubectl get pods -n nvcf-backend -o wide 2>/dev/null || true
        echo ""
        if ! $DRY_RUN; then
            echo "    Deleting all pods in nvcf-backend..."
            kubectl delete pods -n nvcf-backend --all --force --grace-period=0 2>/dev/null || true
        else
            echo "[DRY-RUN] Would delete all pods in nvcf-backend namespace"
        fi
    else
        echo "    No pods found in nvcf-backend namespace"
    fi
else
    echo "    nvcf-backend namespace not found, skipping..."
fi
echo ""

# --- Step 2: Delete NVCFBackend Custom Resources ---
echo ">>> Step 2: Deleting NVCFBackend custom resources..."
if kubectl get crd nvcfbackends.nvcf.nvidia.io >/dev/null 2>&1; then
    echo "    Found NVCFBackend CRD, deleting all instances..."
    if ! $DRY_RUN; then
        kubectl delete nvcfbackends -A --all --wait=false 2>/dev/null || true
        echo "    Waiting 15 seconds for operator cleanup..."
        sleep 15
    else
        echo "[DRY-RUN] Would delete all NVCFBackends and wait for cleanup"
    fi
else
    echo "    NVCFBackend CRD not found, skipping..."
fi
echo ""

# --- Step 3: Delete Helm Releases ---
echo ">>> Step 3: Deleting Helm releases in NVCA namespaces..."
for ns in "${NVCA_NAMESPACES[@]}"; do
    if kubectl get namespace "$ns" >/dev/null 2>&1; then
        releases=$(helm list -n "$ns" -q 2>/dev/null || true)
        if [[ -n "$releases" ]]; then
            for release in $releases; do
                echo "    Deleting Helm release: $release (namespace: $ns)"
                if ! $DRY_RUN; then
                    helm delete -n "$ns" "$release" --wait=false 2>/dev/null || true
                fi
            done
        fi
    fi
done
echo ""

# --- Step 4: Force-delete stuck NVCFBackend resources (remove finalizers) ---
echo ">>> Step 4: Removing finalizers from stuck NVCFBackend resources..."
if kubectl get crd nvcfbackends.nvcf.nvidia.io >/dev/null 2>&1; then
    nvcfbackends=$(kubectl get nvcfbackends -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
    if [[ -n "$nvcfbackends" ]]; then
        while IFS= read -r backend; do
            if [[ -n "$backend" ]]; then
                ns=$(echo "$backend" | cut -d'/' -f1)
                name=$(echo "$backend" | cut -d'/' -f2)
                echo "    Removing finalizers from NVCFBackend: $name (namespace: $ns)"
                if ! $DRY_RUN; then
                    kubectl patch nvcfbackend "$name" -n "$ns" -p '{"metadata":{"finalizers":[]}}' --type=merge 2>/dev/null || true
                    kubectl delete nvcfbackend "$name" -n "$ns" --wait=false 2>/dev/null || true
                fi
            fi
        done <<< "$nvcfbackends"
    else
        echo "    No stuck NVCFBackend resources found"
    fi
else
    echo "    NVCFBackend CRD not found, skipping..."
fi
echo ""

# --- Step 5: Delete Namespaces ---
echo ">>> Step 5: Deleting NVCA namespaces..."
for ns in "${NVCA_NAMESPACES[@]}"; do
    if kubectl get namespace "$ns" >/dev/null 2>&1; then
        echo "    Deleting namespace: $ns"
        if ! $DRY_RUN; then
            kubectl delete namespace "$ns" --wait=false 2>/dev/null || true
        fi
    fi
done
echo "    Waiting 10 seconds for namespace deletion..."
if ! $DRY_RUN; then
    sleep 10
fi
echo ""

# --- Step 6: Force-remove finalizers from stuck namespaces ---
echo ">>> Step 6: Removing finalizers from stuck namespaces..."
for ns in "${NVCA_NAMESPACES[@]}"; do
    phase=$(kubectl get namespace "$ns" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    if [[ "$phase" == "Terminating" ]]; then
        echo "    Namespace $ns is stuck in Terminating, removing finalizers..."
        if ! $DRY_RUN; then
            # First, try to remove finalizers from all resources in the namespace
            for resource_type in deployments statefulsets daemonsets replicasets pods services configmaps secrets serviceaccounts roles rolebindings; do
                kubectl get "$resource_type" -n "$ns" -o name 2>/dev/null | while read -r resource; do
                    kubectl patch "$resource" -n "$ns" -p '{"metadata":{"finalizers":[]}}' --type=merge 2>/dev/null || true
                done
            done

            # Remove namespace finalizers using the API
            kubectl get namespace "$ns" -o json | \
                jq '.spec.finalizers = []' | \
                kubectl replace --raw "/api/v1/namespaces/$ns/finalize" -f - 2>/dev/null || true
        fi
    fi
done
echo ""

# --- Step 7: Delete CRDs ---
echo ">>> Step 7: Deleting NVCA CRDs..."
for crd in "${NVCA_CRDS[@]}"; do
    if kubectl get crd "$crd" >/dev/null 2>&1; then
        echo "    Deleting CRD: $crd"
        if ! $DRY_RUN; then
            kubectl delete crd "$crd" --wait=false 2>/dev/null || true
        fi
    fi
done
echo ""

# --- Step 8: Verification ---
echo ">>> Step 8: Verification..."
echo ""
echo "Remaining NVCA namespaces:"
remaining_ns=0
for ns in "${NVCA_NAMESPACES[@]}"; do
    if kubectl get namespace "$ns" >/dev/null 2>&1; then
        phase=$(kubectl get namespace "$ns" -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")
        echo "    - $ns (status: $phase)"
        remaining_ns=$((remaining_ns + 1))
    fi
done
if [[ $remaining_ns -eq 0 ]]; then
    echo "    None - all namespaces removed successfully"
fi

echo ""
echo "Remaining NVCA CRDs:"
remaining_crds=0
for crd in "${NVCA_CRDS[@]}"; do
    if kubectl get crd "$crd" >/dev/null 2>&1; then
        echo "    - $crd"
        remaining_crds=$((remaining_crds + 1))
    fi
done
if [[ $remaining_crds -eq 0 ]]; then
    echo "    None - all CRDs removed successfully"
fi

echo ""
echo "=============================================="
if $DRY_RUN; then
    echo "DRY-RUN complete. No changes were made."
else
    if [[ $remaining_ns -eq 0 ]] && [[ $remaining_crds -eq 0 ]]; then
        echo "Cleanup complete! All NVCA resources have been removed."
    else
        echo "Cleanup finished with some resources remaining."
        echo "You may need to run this script again or investigate manually."
    fi
fi
echo "=============================================="
