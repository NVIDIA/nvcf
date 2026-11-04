#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Script to clean up disk space on all nodes in the cluster
# This runs a privileged pod on each node to clean up checkpoints, containerd garbage, etc.

set -e

KUBECONFIG="${KUBECONFIG:?KUBECONFIG must point at your cluster kubeconfig — export it before running this script}"
export KUBECONFIG

NAMESPACE="nvsnap-system"

echo "=== Node Disk Cleanup Script ==="
echo "Using KUBECONFIG: $KUBECONFIG"
echo ""

# Get all nodes (or just GPU nodes if you prefer)
# To limit to GPU nodes, uncomment the label selector
# NODES=$(kubectl get nodes -l nvidia.com/gpu.present=true -o jsonpath='{.items[*].metadata.name}')
NODES=$(kubectl get nodes -o jsonpath='{.items[*].metadata.name}')

echo "Nodes to clean: $NODES"
echo ""

cleanup_node() {
    local NODE=$1
    echo "========================================"
    echo "Cleaning node: $NODE"
    echo "========================================"
    
    # Create a cleanup pod on the node
    cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: cleanup-${NODE}
  namespace: ${NAMESPACE}
spec:
  nodeName: ${NODE}
  restartPolicy: Never
  hostPID: true
  hostNetwork: true
  tolerations:
  - operator: Exists
  containers:
  - name: cleanup
    image: ubuntu:22.04
    command:
    - /bin/bash
    - -c
    - |
      echo "=== Disk usage BEFORE cleanup ==="
      df -h / /var/lib/containerd /usr/local 2>/dev/null || df -h /
      
      echo ""
      echo "=== Cleaning up NVSNAP checkpoints ==="
      # Clean up old checkpoints
      if [ -d /host/var/lib/nvsnap/checkpoints ]; then
        echo "Found checkpoints directory"
        du -sh /host/var/lib/nvsnap/checkpoints 2>/dev/null || true
        # Keep only the most recent 2 checkpoints per pod
        find /host/var/lib/nvsnap/checkpoints -mindepth 1 -maxdepth 1 -type d -mtime +1 -exec rm -rf {} \; 2>/dev/null || true
        echo "After cleanup:"
        du -sh /host/var/lib/nvsnap/checkpoints 2>/dev/null || true
      fi
      
      echo ""
      echo "=== Cleaning up containerd garbage ==="
      # Clean up containerd content store garbage
      if [ -d /host/var/lib/containerd ]; then
        echo "Containerd directory size:"
        du -sh /host/var/lib/containerd 2>/dev/null || true
        # Remove unused layers (be careful here)
        rm -rf /host/var/lib/containerd/io.containerd.content.v1.content/ingest/* 2>/dev/null || true
      fi
      
      echo ""
      echo "=== Cleaning up /tmp ==="
      rm -rf /host/tmp/criu* 2>/dev/null || true
      rm -rf /host/tmp/nvsnap* 2>/dev/null || true
      
      echo ""
      echo "=== Cleaning up /usr/local/nvsnap (our tools directory) ==="
      if [ -d /host/usr/local/nvsnap ]; then
        echo "Found nvsnap tools directory"
        du -sh /host/usr/local/nvsnap 2>/dev/null || true
        rm -rf /host/usr/local/nvsnap/* 2>/dev/null || true
        echo "Removed old tools"
      fi
      
      echo ""
      echo "=== Cleaning up old containerd snapshots ==="
      # Clean stale snapshots
      rm -rf /host/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/*/work 2>/dev/null || true
      
      echo ""
      echo "=== Cleaning up journal logs ==="
      if command -v journalctl &> /dev/null; then
        journalctl --vacuum-size=500M 2>/dev/null || true
      fi
      
      echo ""
      echo "=== Disk usage AFTER cleanup ==="
      df -h / /var/lib/containerd /usr/local 2>/dev/null || df -h /
      
      echo ""
      echo "=== Cleanup complete for $(hostname) ==="
    securityContext:
      privileged: true
    volumeMounts:
    - name: host-root
      mountPath: /host
  volumes:
  - name: host-root
    hostPath:
      path: /
EOF

    # Wait for pod to complete
    echo "Waiting for cleanup pod to complete..."
    kubectl wait --for=condition=Ready pod/cleanup-${NODE} -n ${NAMESPACE} --timeout=60s 2>/dev/null || true
    
    # Wait for completion
    for i in {1..60}; do
        STATUS=$(kubectl get pod cleanup-${NODE} -n ${NAMESPACE} -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")
        if [ "$STATUS" = "Succeeded" ] || [ "$STATUS" = "Failed" ]; then
            break
        fi
        sleep 2
    done
    
    # Get logs
    echo ""
    echo "--- Cleanup output for ${NODE} ---"
    kubectl logs cleanup-${NODE} -n ${NAMESPACE} 2>/dev/null || echo "Could not get logs"
    
    # Delete the cleanup pod
    kubectl delete pod cleanup-${NODE} -n ${NAMESPACE} --ignore-not-found 2>/dev/null || true
    
    echo ""
}

# Clean up any existing cleanup pods first
echo "Cleaning up any existing cleanup pods..."
kubectl delete pods -n ${NAMESPACE} -l app=node-cleanup --ignore-not-found 2>/dev/null || true
for NODE in $NODES; do
    kubectl delete pod cleanup-${NODE} -n ${NAMESPACE} --ignore-not-found 2>/dev/null || true
done

# Run cleanup on each node
for NODE in $NODES; do
    cleanup_node "$NODE"
done

echo ""
echo "=== All nodes cleaned ==="
echo ""

# Restart any stuck daemonset pods
echo "Restarting nvsnap-agent daemonset to pick up fresh mounts..."
kubectl rollout restart daemonset/nvsnap-agent -n ${NAMESPACE} 2>/dev/null || true

echo ""
echo "Done! Monitor with: kubectl get pods -n ${NAMESPACE} -w"
