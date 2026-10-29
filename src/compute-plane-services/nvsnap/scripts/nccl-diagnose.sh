#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# nccl-diagnose.sh — Diagnose cuda-checkpoint hang on NCCL multi-GPU workloads
#
# Runs bpftrace on a GPU node to trace nvidia_ioctl calls while triggering
# a 70B checkpoint. Captures exactly which ioctl cuda-checkpoint blocks on.
#
# Usage:
#   ./scripts/nccl-diagnose.sh [node]
#
# Prerequisites:
#   - bpftrace installed on the GPU node (apt install bpftrace)
#   - vllm-70b pod running on the target node
#   - SSH access to the node (or kubectl debug)
#
# The script:
#   1. Checks bpftrace availability on the node
#   2. Starts ioctl tracing in background
#   3. Triggers checkpoint via API
#   4. Waits for hang (or success), then collects trace
#   5. Outputs diagnosis

set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_step()  { echo -e "${CYAN}[STEP]${NC} $*"; }

NODE="${1:-}"
NAMESPACE="default"
POD_NAME="vllm-70b"
TRACE_FILE="/tmp/nccl-ioctl-trace-$(date +%Y%m%d-%H%M%S).log"

# ─── Find node ───────────────────────────────────────────────────────────────
if [ -z "$NODE" ]; then
    log_info "No node specified, finding node running $POD_NAME..."
    NODE=$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.nodeName}' 2>/dev/null || true)
    if [ -z "$NODE" ]; then
        log_error "Pod $POD_NAME not found. Deploy it first: ./scripts/test-e2e.sh vllm-70b"
        log_info ""
        log_info "Available GPU nodes:"
        kubectl get nodes -l nvidia.com/gpu.present=true -o wide 2>/dev/null || true
        exit 1
    fi
    log_info "Found $POD_NAME on node: $NODE"
fi

# ─── Method selection ────────────────────────────────────────────────────────
# We need to run bpftrace on the node. Two options:
# 1. kubectl debug node (creates privileged pod) — works on GKE
# 2. SSH directly — works on bare metal

log_step "1/5 Checking bpftrace availability on $NODE"

# Try kubectl debug approach (GKE-friendly)
TRACE_POD="nccl-trace-$(date +%s)"

cat <<EOF
┌─────────────────────────────────────────────────────────────────────┐
│  NCCL cuda-checkpoint Diagnosis                                     │
│                                                                     │
│  This will trace nvidia_ioctl calls on node: $NODE
│  Target: $POD_NAME (TP=4, 4xH100)                                  │
│  Output: $TRACE_FILE
│                                                                     │
│  The trace captures EVERY nvidia ioctl call. When cuda-checkpoint   │
│  hangs, the last ENTER without a matching SLOW/return is the        │
│  blocking ioctl command.                                            │
└─────────────────────────────────────────────────────────────────────┘
EOF

echo ""
log_step "2/5 Deploying trace pod on $NODE"

# Deploy a privileged debug pod that can run bpftrace
kubectl apply -f - <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: $TRACE_POD
  namespace: nvsnap-system
spec:
  nodeName: $NODE
  hostPID: true
  hostNetwork: true
  containers:
  - name: trace
    image: quay.io/iovisor/bpftrace:latest
    securityContext:
      privileged: true
    command: ["sleep", "3600"]
    volumeMounts:
    - name: sys
      mountPath: /sys
    - name: modules
      mountPath: /lib/modules
      readOnly: true
    - name: debug
      mountPath: /sys/kernel/debug
    - name: tmp
      mountPath: /tmp/trace
  volumes:
  - name: sys
    hostPath:
      path: /sys
  - name: modules
    hostPath:
      path: /lib/modules
  - name: debug
    hostPath:
      path: /sys/kernel/debug
  - name: tmp
    hostPath:
      path: /tmp
  restartPolicy: Never
  tolerations:
  - operator: Exists
YAML

log_info "Waiting for trace pod to be ready..."
kubectl wait pod/$TRACE_POD -n nvsnap-system --for=condition=Ready --timeout=120s 2>/dev/null || {
    log_error "Trace pod failed to start. Check: kubectl describe pod/$TRACE_POD -n nvsnap-system"
    exit 1
}

log_step "3/5 Starting ioctl trace (background)"

# Copy the bpftrace script into the pod and start tracing
kubectl cp "$PROJECT_ROOT/scripts/nccl-ioctl-trace.bt" "nvsnap-system/$TRACE_POD:/tmp/trace/nccl-ioctl-trace.bt"

# Start bpftrace in background, output to file
kubectl exec -n nvsnap-system "$TRACE_POD" -- bash -c \
    'bpftrace /tmp/trace/nccl-ioctl-trace.bt > /tmp/trace/trace.log 2>&1 &
     echo $! > /tmp/trace/bpftrace.pid
     sleep 2
     if kill -0 $(cat /tmp/trace/bpftrace.pid) 2>/dev/null; then
         echo "bpftrace started (pid=$(cat /tmp/trace/bpftrace.pid))"
     else
         echo "FAILED to start bpftrace"
         cat /tmp/trace/trace.log
         exit 1
     fi'

log_info "Trace running. Waiting 3s for kprobe attachment..."
sleep 3

log_step "4/5 Triggering checkpoint for $POD_NAME"
echo ""
log_warn "This will likely HANG on cuda-checkpoint. That's expected."
log_warn "Wait ~60s, then press Ctrl+C to collect the trace."
echo ""

# Trigger checkpoint via the nvsnap API
NVSNAP_POD=$(kubectl get pod -n nvsnap-system -l app=nvsnap-server -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [ -n "$NVSNAP_POD" ]; then
    log_info "Triggering checkpoint via nvsnap-server..."
    kubectl exec -n nvsnap-system "$NVSNAP_POD" -- curl -s -X POST \
        "http://localhost:8080/api/v1/checkpoint/pod" \
        -H "Content-Type: application/json" \
        -d "{\"podName\":\"$POD_NAME\",\"namespace\":\"$NAMESPACE\"}" || true
else
    log_warn "No nvsnap-server pod found. Trigger checkpoint manually:"
    log_info "  curl -X POST http://<server>:8080/api/v1/checkpoint/pod \\"
    log_info "    -H 'Content-Type: application/json' \\"
    log_info "    -d '{\"podName\":\"$POD_NAME\",\"namespace\":\"$NAMESPACE\"}'"
fi

echo ""
log_info "Checkpoint triggered. Waiting for hang or completion..."
log_info "Watch agent logs:  kubectl logs -n nvsnap-system daemonset/nvsnap-agent -f | grep -i cuda"
echo ""

# Wait for user to press Ctrl+C or timeout after 5 min
log_warn "Press Enter after cuda-checkpoint hangs (or after 60-120s) to collect trace..."
read -r -t 300 || true

log_step "5/5 Collecting trace"

# Stop bpftrace and collect output
kubectl exec -n nvsnap-system "$TRACE_POD" -- bash -c \
    'kill -INT $(cat /tmp/trace/bpftrace.pid) 2>/dev/null; sleep 2' || true

# Copy trace file locally
kubectl cp "nvsnap-system/$TRACE_POD:/tmp/trace/trace.log" "$TRACE_FILE" 2>/dev/null || true

# Cleanup trace pod
log_info "Cleaning up trace pod..."
kubectl delete pod "$TRACE_POD" -n nvsnap-system --grace-period=0 2>/dev/null || true

echo ""
echo "═══════════════════════════════════════════════════════════════════"
echo ""

if [ -f "$TRACE_FILE" ] && [ -s "$TRACE_FILE" ]; then
    log_info "Trace saved to: $TRACE_FILE"
    echo ""

    # Quick analysis
    log_info "=== Quick Analysis ==="
    echo ""

    # Find the last ENTER without a return (the blocking ioctl)
    log_info "Last 20 ioctl calls (look for ENTER without matching SLOW):"
    tail -30 "$TRACE_FILE" | grep -E 'ENTER|SLOW|cuda-checkpoint' | tail -20 || true
    echo ""

    # Count by command
    log_info "ioctl commands seen:"
    grep 'ENTER' "$TRACE_FILE" | awk '{print $4}' | sort | uniq -c | sort -rn | head -20 || true
    echo ""

    # Show blocked calls summary
    log_info "Blocked calls (>1s):"
    grep 'blocked' "$TRACE_FILE" || echo "  (check summary section at end of trace file)"
    echo ""

    log_info "Full trace: less $TRACE_FILE"
    log_info "Search for hangs: grep ENTER $TRACE_FILE | tail -5"
else
    log_error "No trace captured. Check if bpftrace ran successfully:"
    log_info "  kubectl logs $TRACE_POD -n nvsnap-system"
fi

echo ""
echo "═══════════════════════════════════════════════════════════════════"
echo ""
log_info "Next steps based on trace results:"
echo "  1. Identify the blocking ioctl command (0xNNNNNNNN)"
echo "  2. Cross-reference with NVIDIA driver source or nvidia-smi"
echo "  3. If it's IPC-related: our nvsnap_ipc_close_all() should fix it"
echo "  4. If it's NCCL proxy thread: need full CUDA interposition"
echo ""
