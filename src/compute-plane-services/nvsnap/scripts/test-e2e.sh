#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Automated E2E Checkpoint/Restore Test
# Tests the complete flow: deploy → wait → checkpoint → restore → verify inference
#
# Usage:
#   ./test-e2e.sh vllm-small
#   ./test-e2e.sh vllm-8b
#   ./test-e2e.sh sglang-small
#
# Uses kubectl exec + curl inside the pod for API access (reliable, no port-forward).
# Prints a structured timing summary at the end.
#
# Exit codes: 0 = PASS, 1 = FAIL (with which step failed)

set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Ensure all manifests use the current agent version before testing
"$SCRIPT_DIR/sync-versions.sh" >/dev/null 2>&1 || true

# Verify cluster connectivity before starting (catches expired tsh creds early)
if ! kubectl get nodes --no-headers --request-timeout=5s >/dev/null 2>&1; then
    echo -e "\033[0;31m[ERROR]\033[0m Cannot connect to cluster. Check tsh login / KUBECONFIG."
    exit 1
fi

# Verify deployed agent matches expected version
source "$SCRIPT_DIR/versions.sh"
DEPLOYED=$(kubectl get ds nvsnap-agent -n nvsnap-system -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null)
EXPECTED="${NVSNAP_REGISTRY}/nvsnap-agent:${NVSNAP_APP_VERSION}"
if [ "$DEPLOYED" != "$EXPECTED" ]; then
    echo -e "\033[0;31m[ERROR]\033[0m Deployed agent ($DEPLOYED) != expected ($EXPECTED)"
    echo "Run: ./scripts/build-agent.sh app && ./scripts/build-agent.sh push-app && ./scripts/build-agent.sh deploy"
    exit 1
fi

# Verify image exists in registry (catches failed pushes)
if ! docker manifest inspect "$EXPECTED" >/dev/null 2>&1; then
    echo -e "\033[0;31m[ERROR]\033[0m Image $EXPECTED not found in registry. Push failed?"
    exit 1
fi

# ─── Colors ───────────────────────────────────────────────────────────────────
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }

# ─── Workload configuration ──────────────────────────────────────────────────
WORKLOAD="${1:-}"
if [ -z "$WORKLOAD" ]; then
    echo "Usage: $0 <workload>"
    echo ""
    echo "Workloads (single-GPU CRIU):"
    echo "  vllm-small        TinyLlama 1.1B on vLLM"
    echo "  vllm-8b           Llama-3.1-8B on vLLM"
    echo "  gemma-sglang      gemma-4-31B on SGLang (NVCA benchmark)"
    echo "  sglang-small      TinyLlama 1.1B on SGLang"
    echo "  sglang-8b         Llama-3.1-8B on SGLang"
    echo "  trtllm-small      TinyLlama 1.1B on TensorRT-LLM"
    echo "  nim-llama-8b      Llama-3.1-8B on NVIDIA NIM"
    echo "  e5-mistral        e5-mistral-7B-instruct on vLLM (embedding)"
    echo ""
    echo "Workloads (multi-GPU rootfs):"
    echo "  vllm-70b          Llama-3.1-70B on vLLM (TP=4)"
    echo "  nim-qwen3-32b     Qwen3-32B on NVIDIA NIM (TP=2)"
    exit 1
fi

case "$WORKLOAD" in
    vllm-small)
        POD_NAME="vllm-small"
        CONTAINER_NAME="vllm"
        RESTORE_POD_NAME="vllm-small-restored"
        RESTORE_CONTAINER_NAME="restore"
        PORT=8000
        MODEL="TinyLlama/TinyLlama-1.1B-Chat-v1.0"
        INFER_ENDPOINT="/v1/completions"
        INFER_DATA='{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"Hello","max_tokens":5}'
        POST_INFER_DATA='{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"The meaning of life is","max_tokens":10}'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/workloads/vllm-small.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/workloads/vllm-small-restore.yaml"
        ;;
    vllm-mp)
        # E0 multi-GPU ladder: TinyLlama TP=1, multi-process EngineCore.
        POD_NAME="vllm-mp"
        CONTAINER_NAME="vllm"
        RESTORE_POD_NAME="vllm-mp-restored"
        RESTORE_CONTAINER_NAME="restore"
        PORT=8000
        MODEL="TinyLlama/TinyLlama-1.1B-Chat-v1.0"
        INFER_ENDPOINT="/v1/completions"
        INFER_DATA='{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"Hello","max_tokens":5}'
        POST_INFER_DATA='{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"The meaning of life is","max_tokens":10}'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/workloads/vllm-mp.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/workloads/vllm-mp-restore.yaml"
        ;;
    vllm-tp2)
        # E1 multi-GPU ladder: TinyLlama TP=2 eager. Force the CRIU engine
        # with CAPTURE_PATH=criu-v2 (multi-GPU otherwise defaults to rootfs).
        POD_NAME="vllm-tp2"
        CONTAINER_NAME="vllm"
        RESTORE_POD_NAME="vllm-tp2-restored"
        RESTORE_CONTAINER_NAME="restore"
        PORT=8000
        MODEL="TinyLlama/TinyLlama-1.1B-Chat-v1.0"
        INFER_ENDPOINT="/v1/completions"
        INFER_DATA='{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"Hello","max_tokens":5}'
        POST_INFER_DATA='{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"The meaning of life is","max_tokens":10}'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/workloads/vllm-tp2.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/workloads/vllm-tp2-restore.yaml"
        ;;
    vllm-8b)
        POD_NAME="vllm-8b"
        CONTAINER_NAME="vllm"
        RESTORE_POD_NAME="vllm-8b-restored"
        RESTORE_CONTAINER_NAME="restore"
        PORT=8000
        MODEL="meta-llama/Llama-3.1-8B-Instruct"
        INFER_ENDPOINT="/v1/completions"
        INFER_DATA='{"model":"meta-llama/Llama-3.1-8B-Instruct","prompt":"Hello","max_tokens":5}'
        POST_INFER_DATA='{"model":"meta-llama/Llama-3.1-8B-Instruct","prompt":"The meaning of life is","max_tokens":10}'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/workloads/vllm-8b.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/workloads/vllm-8b-restore.yaml"
        ;;
    vllm-qwen32b)
        POD_NAME="vllm-qwen32b"
        CONTAINER_NAME="vllm"
        RESTORE_POD_NAME="vllm-qwen32b-restored"
        RESTORE_CONTAINER_NAME="restore"
        PORT=8000
        MODEL="Qwen/Qwen2.5-32B-Instruct"
        INFER_ENDPOINT="/v1/completions"
        INFER_DATA='{"model":"Qwen/Qwen2.5-32B-Instruct","prompt":"Hello","max_tokens":5}'
        POST_INFER_DATA='{"model":"Qwen/Qwen2.5-32B-Instruct","prompt":"The meaning of life is","max_tokens":10}'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/workloads/vllm-qwen32b.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/workloads/vllm-qwen32b-restore.yaml"
        ;;
    gemma-sglang)
        # NVCA benchmark single-GPU SGLang model, criu-v2 path.
        POD_NAME="gemma-sglang"
        CONTAINER_NAME="sglang"
        RESTORE_POD_NAME="gemma-sglang-restored"
        RESTORE_CONTAINER_NAME="restore"
        PORT=30000
        MODEL="google/gemma-4-31B-it"
        INFER_ENDPOINT="/v1/completions"
        INFER_DATA='{"model":"google/gemma-4-31B-it","prompt":"Hello","max_tokens":5}'
        POST_INFER_DATA='{"model":"google/gemma-4-31B-it","prompt":"The meaning of life is","max_tokens":10}'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/workloads/gemma-sglang.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/workloads/gemma-sglang-restore.yaml"
        ;;
    sglang-small)
        POD_NAME="sglang-small"
        CONTAINER_NAME="sglang"
        RESTORE_POD_NAME="sglang-small-restored"
        RESTORE_CONTAINER_NAME="restore"
        PORT=30000
        MODEL="TinyLlama/TinyLlama-1.1B-Chat-v1.0"
        INFER_ENDPOINT="/v1/completions"
        INFER_DATA='{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"Hello","max_tokens":5}'
        POST_INFER_DATA='{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"The meaning of life is","max_tokens":10}'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/workloads/sglang-small.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/workloads/sglang-small-restore.yaml"
        ;;
    sglang-8b)
        POD_NAME="sglang-8b"
        CONTAINER_NAME="sglang"
        RESTORE_POD_NAME="sglang-8b-restored"
        RESTORE_CONTAINER_NAME="restore"
        PORT=30000
        MODEL="meta-llama/Llama-3.1-8B-Instruct"
        INFER_ENDPOINT="/v1/completions"
        INFER_DATA='{"model":"meta-llama/Llama-3.1-8B-Instruct","prompt":"Hello","max_tokens":5}'
        POST_INFER_DATA='{"model":"meta-llama/Llama-3.1-8B-Instruct","prompt":"The meaning of life is","max_tokens":10}'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/workloads/sglang-8b.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/workloads/sglang-8b-restore.yaml"
        ;;
    trtllm-small)
        POD_NAME="trtllm-small"
        CONTAINER_NAME="trtllm"
        RESTORE_POD_NAME="trtllm-small-restored"
        RESTORE_CONTAINER_NAME="restore"
        PORT=8000
        MODEL="TinyLlama/TinyLlama-1.1B-Chat-v1.0"
        INFER_ENDPOINT="/v1/completions"
        INFER_DATA='{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"Hello","max_tokens":5}'
        POST_INFER_DATA='{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"The meaning of life is","max_tokens":10}'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/workloads/trtllm-small.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/workloads/trtllm-small-restore.yaml"
        ;;
    nim-llama-8b)
        # 1-GPU NIM via CRIU + cuda-checkpoint. Inference endpoint matches
        # NIM's NGC model id (meta/llama-3.1-8b-instruct), not the HF id.
        POD_NAME="nim-llama-8b"
        CONTAINER_NAME="nim"
        RESTORE_POD_NAME="nim-llama-8b-restored"
        RESTORE_CONTAINER_NAME="restore"
        PORT=8000
        MODEL="meta/llama-3.1-8b-instruct"
        INFER_ENDPOINT="/v1/completions"
        INFER_DATA='{"model":"meta/llama-3.1-8b-instruct","prompt":"Hello","max_tokens":5}'
        POST_INFER_DATA='{"model":"meta/llama-3.1-8b-instruct","prompt":"The meaning of life is","max_tokens":10}'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/workloads/nim-llama-8b.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/workloads/nim-llama-8b-restore.yaml"
        # NIM uses /v1/health/ready (not /v1/models like vLLM/SGLang). The
        # script's wait-for-ready loop probes the readiness probe path
        # from the manifest, so this is a manifest concern not a script
        # concern — already correctly set in nim-llama-8b{,-restore}.yaml.
        ;;
    e5-mistral)
        POD_NAME="e5-mistral"
        CONTAINER_NAME="vllm"
        RESTORE_POD_NAME="e5-mistral-restored"
        RESTORE_CONTAINER_NAME="restore"
        PORT=8000
        MODEL="intfloat/e5-mistral-7b-instruct"
        # Embedding model — vLLM exposes /v1/embeddings instead of
        # /v1/completions. Response shape is "data":[{"embedding":[...]}],
        # NOT "choices" — override the default poll-success pattern so
        # the pre/post inference probes recognize a healthy response.
        INFER_ENDPOINT="/v1/embeddings"
        INFER_VERIFY_PATTERN='"embedding"'
        INFER_DATA='{"model":"intfloat/e5-mistral-7b-instruct","input":"Hello"}'
        POST_INFER_DATA='{"model":"intfloat/e5-mistral-7b-instruct","input":"The meaning of life is"}'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/workloads/e5-mistral.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/workloads/e5-mistral-restore.yaml"
        ;;
    nim-qwen3-32b)
        POD_NAME="nim-qwen3-32b"
        CONTAINER_NAME="nim"
        RESTORE_POD_NAME="nim-qwen3-32b-restored"
        RESTORE_CONTAINER_NAME="nim"
        PORT=8000
        MODEL="qwen/qwen3-32b"
        INFER_ENDPOINT="/v1/completions"
        INFER_DATA='{"model":"qwen/qwen3-32b","prompt":"Hello","max_tokens":5}'
        POST_INFER_DATA='{"model":"qwen/qwen3-32b","prompt":"The meaning of life is","max_tokens":10}'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/workloads/nim-qwen3-32b.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/workloads/nim-qwen3-32b-restore.yaml"
        ;;
    vllm-70b)
        POD_NAME="vllm-70b"
        CONTAINER_NAME="vllm"
        RESTORE_POD_NAME="vllm-70b-restored"
        RESTORE_CONTAINER_NAME="restore"
        PORT=8000
        MODEL="meta-llama/Llama-3.1-70B-Instruct"
        INFER_ENDPOINT="/v1/completions"
        INFER_DATA='{"model":"meta-llama/Llama-3.1-70B-Instruct","prompt":"Hello","max_tokens":5}'
        POST_INFER_DATA='{"model":"meta-llama/Llama-3.1-70B-Instruct","prompt":"The meaning of life is","max_tokens":10}'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/workloads/vllm-70b.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/workloads/vllm-70b-restore.yaml"
        ;;
    *)
        echo "Unknown workload: $WORKLOAD"
        echo "Supported: vllm-small, vllm-8b, gemma-sglang, sglang-small, sglang-8b, trtllm-small, nim-llama-8b, vllm-70b, nim-qwen3-32b"
        exit 1
        ;;
esac

# General configuration
NAMESPACE="nvsnap-system"

# Timeouts (seconds) — 70B needs longer for model download + GPU memory dump/restore
if [[ "$WORKLOAD" == *"70b"* ]]; then
    POD_READY_TIMEOUT=1800      # 30min: 70B model download + load
    MODELS_POLL_TIMEOUT=1200    # 20min
    INFERENCE_POLL_TIMEOUT=300
    RESTORE_READY_TIMEOUT=1200  # 20min: CRIU + 4x GPU memory restore
elif [[ "$WORKLOAD" == trtllm-* ]]; then
    POD_READY_TIMEOUT=1800      # 30min: ~25GB image pull + TRT engine compilation
    MODELS_POLL_TIMEOUT=1200    # 20min
    INFERENCE_POLL_TIMEOUT=300
    RESTORE_READY_TIMEOUT=600   # 10min
elif [[ "$WORKLOAD" == *"qwen32b"* ]]; then
    POD_READY_TIMEOUT=1800      # 30min: ~64GB fp16 model download + load
    MODELS_POLL_TIMEOUT=1200    # 20min
    INFERENCE_POLL_TIMEOUT=300
    RESTORE_READY_TIMEOUT=1200  # 20min: CRIU restore of ~64GB host-staged GPU memory
else
    POD_READY_TIMEOUT=600       # 10min
    MODELS_POLL_TIMEOUT=600
    INFERENCE_POLL_TIMEOUT=300
    RESTORE_READY_TIMEOUT=600   # 10min
fi
MODELS_POLL_INTERVAL=30
INFERENCE_POLL_INTERVAL=30
POST_MODELS_TIMEOUT=120
POST_MODELS_INTERVAL=10
POST_INFER_TIMEOUT=120
POST_INFER_INTERVAL=10

# ─── Timing infrastructure ──────────────────────────────────────────────────
declare -a STEP_NAMES=()
declare -a STEP_DURATIONS=()
declare -a STEP_RESULTS=()
TEST_START=$(date +%s)

step_start() {
    CURRENT_STEP_START=$(date +%s)
}

step_done() {
    local name="$1" result="$2"
    local elapsed=$(( $(date +%s) - CURRENT_STEP_START ))
    STEP_NAMES+=("$name")
    STEP_DURATIONS+=("$elapsed")
    STEP_RESULTS+=("$result")
}

fmt_duration() {
    local secs="$1"
    printf "%dm %02ds" $((secs / 60)) $((secs % 60))
}

print_summary() {
    local total_elapsed=$(( $(date +%s) - TEST_START ))
    local overall="PASS"

    echo ""
    echo -e "${BOLD}${CYAN}[$WORKLOAD] E2E Test Results${NC}"
    echo -e "${BOLD}${CYAN}Step                         Duration    Result${NC}"
    echo -e "${CYAN}──────────────────────────────────────────────────${NC}"
    for i in "${!STEP_NAMES[@]}"; do
        local name="${STEP_NAMES[$i]}"
        local dur=$(fmt_duration "${STEP_DURATIONS[$i]}")
        local res="${STEP_RESULTS[$i]}"
        local color="$GREEN"
        if [ "$res" = "FAIL" ]; then
            color="$RED"
            overall="FAIL"
        elif [ "$res" = "SKIP" ]; then
            color="$YELLOW"
        fi
        printf "%-28s %-11s ${color}%s${NC}\n" "$name" "$dur" "$res"
    done
    echo -e "${CYAN}──────────────────────────────────────────────────${NC}"
    local total_dur=$(fmt_duration "$total_elapsed")
    local total_color="$GREEN"
    if [ "$overall" = "FAIL" ]; then total_color="$RED"; fi
    printf "%-28s %-11s ${total_color}%s${NC}\n" "Total" "$total_dur" "$overall"
    echo ""

    if [ "$overall" = "FAIL" ]; then
        return 1
    fi
    return 0
}

# ─── Helper: call API via kubectl exec ────────────────────────────────────────
# Runs curl inside the pod — no port-forward needed, always reliable.
# Usage: pod_curl <pod> <container> <method> <path> [data] [timeout]
#
# Tries curl first; falls back to python3 urllib for containers that
# don't ship curl (e.g. NIM images). Clears LD_PRELOAD so the nvsnap
# intercept library doesn't load into the probe binary.
pod_curl() {
    local pod="$1" container="$2" method="$3" path="$4" data="${5:-}" timeout="${6:-10}"

    # Try curl first.
    local out
    if [ -n "$data" ]; then
        out=$(kubectl exec -n "$NAMESPACE" "$pod" -c "$container" -- \
            env LD_PRELOAD= curl -sf -m "$timeout" -X "$method" "http://localhost:${PORT}${path}" \
            -H "Content-Type: application/json" -d "$data" 2>/dev/null) && [ -n "$out" ] && { printf '%s' "$out"; return 0; }
    else
        out=$(kubectl exec -n "$NAMESPACE" "$pod" -c "$container" -- \
            env LD_PRELOAD= curl -sf -m "$timeout" -X "$method" "http://localhost:${PORT}${path}" 2>/dev/null) && [ -n "$out" ] && { printf '%s' "$out"; return 0; }
    fi

    # Python fallback (NIM has no curl). Base64-encode the JSON body
    # so we don't have to escape quotes through the shell+kubectl path.
    local py
    if [ -n "$data" ]; then
        local b64
        b64=$(printf '%s' "$data" | base64 -w0)
        py="
import base64,sys,urllib.request
req=urllib.request.Request('http://localhost:${PORT}${path}', method='${method}',
    headers={'Content-Type':'application/json'},
    data=base64.b64decode('${b64}'))
try:
    print(urllib.request.urlopen(req, timeout=${timeout}).read().decode())
except Exception as e:
    print('python probe error:', e, file=sys.stderr); sys.exit(1)
"
    else
        py="
import sys,urllib.request
try:
    print(urllib.request.urlopen('http://localhost:${PORT}${path}', timeout=${timeout}).read().decode())
except Exception as e:
    print('python probe error:', e, file=sys.stderr); sys.exit(1)
"
    fi
    kubectl exec -n "$NAMESPACE" "$pod" -c "$container" -- \
        env LD_PRELOAD= python3 -c "$py" 2>/dev/null
}

# ─── Helper: poll until API responds ──────────────────────────────────────────
# Usage: poll_api <pod> <container> <method> <path> <data> <grep_pattern> <timeout_secs> <interval_secs> <description>
# Returns: 0 on success (sets POLL_RESULT), 1 on timeout
POLL_RESULT=""
poll_api() {
    local pod="$1" container="$2" method="$3" path="$4" data="$5"
    local pattern="$6" timeout_secs="$7" interval="$8" desc="$9"

    local deadline=$(( $(date +%s) + timeout_secs ))
    local attempt=0
    while [ "$(date +%s)" -lt "$deadline" ]; do
        attempt=$((attempt + 1))
        POLL_RESULT=$(pod_curl "$pod" "$container" "$method" "$path" "$data" 30 || true)
        if echo "$POLL_RESULT" | grep -q "$pattern"; then
            log_info "  $desc OK (attempt $attempt)"
            return 0
        fi
        local remaining=$(( deadline - $(date +%s) ))
        if [ "$remaining" -le 0 ]; then break; fi
        log_info "  attempt $attempt: $desc not ready, retrying in ${interval}s (${remaining}s left)..."
        sleep "$interval"
    done
    log_error "  $desc not responding after ${timeout_secs}s"
    return 1
}

# ─── Fail handler ────────────────────────────────────────────────────────────
fail() {
    local step="$1"
    step_done "$step" "FAIL"
    log_error "FAILED at: $step"
    print_summary || true
    exit 1
}

log_info "=========================================="
log_info "E2E Checkpoint/Restore Test: $WORKLOAD"
log_info "=========================================="
echo ""

# ─── Step 1: Clean up ────────────────────────────────────────────────────────
# Delete every prior nvsnap demo pod, not just this workload's pair. Without
# this, *-restored pods from earlier runs keep holding GPUs and the next
# test fails with "Allocate failed ... Requested: N, Available: 0" even
# though the rest of the cluster has free H100s. nvsnap.io/demo=true is set
# on every source manifest; restored pods inherit it.
log_info "Step 1: Cleaning up existing pods..."
kubectl delete pod -l nvsnap.io/demo=true -n $NAMESPACE --ignore-not-found --wait=false
# Fallback by name (covers any pod started outside the demo-label flow).
kubectl delete pod $POD_NAME $RESTORE_POD_NAME -n $NAMESPACE --ignore-not-found --wait=false
# Wait for terminating GPU pods to actually release the device. kubelet's
# device-plugin accounting takes a few seconds after the pod transitions
# to Terminating; if we skip this we'll hit the same allocation race.
kubectl wait --for=delete pod -l nvsnap.io/demo=true -n $NAMESPACE --timeout=60s 2>/dev/null || true
sleep 3

# ─── Pre-flight: pick a GPU node with capacity ───────────────────────────────
# The scheduler's `nvidia.com/gpu.allocatable` count does NOT subtract live
# usage when other workloads (e.g. nvcf-backend) hold long-running pods; we
# can land on a node that's 100% full and hit `Allocate failed ... Requested:
# N, Available: 0` admission errors. Compute free GPUs per node ourselves
# from live pod requests and pin the source pod to the first node that fits.
GPU_REQ=$(yq -r '.spec.containers[0].resources.requests."nvidia.com/gpu" // "1"' "$SOURCE_MANIFEST")
SELECTED_NODE=$(python3 - "$GPU_REQ" <<'PY'
import json, subprocess, sys
need = int(sys.argv[1])
nodes = json.loads(subprocess.check_output(["kubectl","get","nodes","-o","json"]))["items"]
pods  = json.loads(subprocess.check_output(["kubectl","get","pods","-A","-o","json"]))["items"]
candidates = []
for n in nodes:
    if n["spec"].get("unschedulable"):
        continue  # skip cordoned nodes — pods with nodeName bypass the scheduler but cordon usually signals "do not place here"
    alloc_str = n["status"].get("allocatable",{}).get("nvidia.com/gpu","")
    if not alloc_str:
        continue
    alloc = int(alloc_str)
    used = 0
    for p in pods:
        if p["spec"].get("nodeName") != n["metadata"]["name"]:
            continue
        if p["status"].get("phase") not in ("Pending","Running"):
            continue
        for c in p["spec"].get("containers",[]) + p["spec"].get("initContainers",[]):
            v = c.get("resources",{}).get("requests",{}).get("nvidia.com/gpu")
            if v: used += int(v)
    free = alloc - used
    if free >= need:
        candidates.append((free, n["metadata"]["name"]))
# Most-free-GPUs first, not first-fit: the restore placeholder is created
# right after the source pod is deleted on the SAME node (nodeName pin). On a
# barely-fitting node that delete->recreate hits the kubelet device-release
# race (UnexpectedAdmissionError), and other tenants can eat the slack
# mid-test. Headroom makes both failure modes vanish.
if candidates:
    candidates.sort(reverse=True)
    print(candidates[0][1])
    sys.exit(0)
sys.exit(1)
PY
)
if [ -z "$SELECTED_NODE" ]; then
    echo -e "${RED}[ERROR]${NC} No GPU node with $GPU_REQ free GPUs."
    exit 1
fi
log_info "Pre-flight: pinning to node $SELECTED_NODE (needs $GPU_REQ GPU)"

# Multi-GPU pods (TP>=2) can't be checkpointed via CRIU + cuda-checkpoint
# (libcudart wall) — they use the rootfs-only path: agent's watcher
# captures the container's overlay diff after a warmup window, keyed by
# pod identity (image + mounts + env). Restore goes through the webhook
# via nvsnap.io/restore-from: <hash> annotation, not a hostPath checkpoint
# volume. Single-GPU keeps the CRIU + agent /v1/checkpoint flow.
# An explicit CAPTURE_PATH in the environment wins — needed for single-GPU
# workloads on a cachedir/rootfs-default agent, which redirects the CRIU
# /v1/checkpoint call to the rootfs path (HTTP 422) so the hardcoded
# default below would otherwise fail. Otherwise: multi-GPU → rootfs,
# single-GPU → criu.
if [ -n "${CAPTURE_PATH:-}" ]; then
    : # respect caller override
elif [ "$GPU_REQ" -ge 2 ]; then
    CAPTURE_PATH="rootfs"
else
    # criu-v2 is the default single-GPU engine (the legacy CRIU injection path
    # is retired). rootfs stays for multi-GPU / arm above.
    CAPTURE_PATH="criu-v2"
fi
log_info "Capture path: $CAPTURE_PATH"

# On the rootfs/cachedir path, prefer a dedicated <workload>-rootfs-restore.yaml
# if one exists: the CRIU restore template chains restore-entrypoint and waits
# for a CRIU dump that never appears on the rootfs path. The rootfs variant is a
# plain pod with a nvsnap.io/restore-from annotation the webhook acts on.
if [ "$CAPTURE_PATH" = "rootfs" ]; then
    _rootfs_tmpl="${RESTORE_MANIFEST_TEMPLATE%-restore.yaml}-rootfs-restore.yaml"
    if [ -f "$_rootfs_tmpl" ]; then
        RESTORE_MANIFEST_TEMPLATE="$_rootfs_tmpl"
        log_info "Using rootfs restore manifest: $(basename "$RESTORE_MANIFEST_TEMPLATE")"
        # The rootfs restore pod runs the plain engine container (named like
        # the source), not a CRIU 'restore' wrapper — post-restore exec/verify
        # must target it.
        RESTORE_CONTAINER_NAME="$CONTAINER_NAME"
    fi
fi

# ─── Step 2: Deploy source pod ───────────────────────────────────────────────
log_info "Step 2: Deploying $WORKLOAD pod..."
# Inject nodeName into a temp copy of the source manifest so we don't
# mutate the checked-in file.
SOURCE_RENDERED=$(mktemp --suffix=.yaml)
trap "rm -f $SOURCE_RENDERED" EXIT
yq ".spec.nodeName = \"$SELECTED_NODE\"" "$SOURCE_MANIFEST" > "$SOURCE_RENDERED"
# Rootfs path: the pod must carry nvsnap.io/capture=true at CREATE so the
# mutating webhook injects the cachedir volume (/opt/nvsnap) — the
# rootfsonly.Watcher's capture fails without it. The CRIU-oriented workload
# manifests don't set the label (only the bench manifests do), so add it here.
if [ "$CAPTURE_PATH" = "rootfs" ]; then
    yq -i '.metadata.labels."nvsnap.io/capture" = "true"' "$SOURCE_RENDERED"
fi
kubectl apply -f "$SOURCE_RENDERED"

# ─── Step 3: Wait for pod ready (readiness probe checks /v1/models) ──────────
step_start
log_info "Step 3: Waiting for pod ready (up to ${POD_READY_TIMEOUT}s)..."
if kubectl wait --for=condition=ready pod/$POD_NAME -n $NAMESPACE --timeout=${POD_READY_TIMEOUT}s; then
    step_done "Pod ready" "OK"
else
    kubectl logs $POD_NAME -n $NAMESPACE -c $CONTAINER_NAME --tail=20 || true
    fail "Pod ready"
fi

# ─── Step 4: Verify /v1/models ───────────────────────────────────────────────
step_start
log_info "Step 4: Verifying /v1/models responds..."
if poll_api "$POD_NAME" "$CONTAINER_NAME" GET /v1/models "" "$MODEL" \
    "$MODELS_POLL_TIMEOUT" "$MODELS_POLL_INTERVAL" "/v1/models"; then
    step_done "Models API ready" "OK"
else
    kubectl logs $POD_NAME -n $NAMESPACE -c $CONTAINER_NAME --tail=30 || true
    fail "Models API ready"
fi

# ─── Step 5: Verify inference ────────────────────────────────────────────────
# The success pattern depends on the API shape: completion-style
# returns "choices", embedding-style returns "embedding". Each
# workload sets INFER_VERIFY_PATTERN; default keeps the existing
# completion behavior so nothing breaks for callers that don't set it.
INFER_VERIFY_PATTERN="${INFER_VERIFY_PATTERN:-\"choices\"}"
step_start
log_info "Step 5: Verifying inference works before checkpoint..."
if poll_api "$POD_NAME" "$CONTAINER_NAME" POST "$INFER_ENDPOINT" \
    "$INFER_DATA" \
    "$INFER_VERIFY_PATTERN" "$INFERENCE_POLL_TIMEOUT" "$INFERENCE_POLL_INTERVAL" "$INFER_ENDPOINT"; then
    echo "$POLL_RESULT" | python3 -m json.tool 2>/dev/null || echo "$POLL_RESULT"
    step_done "Pre-checkpoint infer" "OK"
else
    kubectl logs $POD_NAME -n $NAMESPACE -c $CONTAINER_NAME --tail=30 || true
    fail "Pre-checkpoint infer"
fi

# ─── Step 6: Get node ────────────────────────────────────────────────────────
POD_NODE=$(kubectl get pod $POD_NAME -n $NAMESPACE -o jsonpath='{.spec.nodeName}')
log_info "Pod on node: $POD_NODE"

# ─── Step 7: Create checkpoint ───────────────────────────────────────────────
step_start
if [ "$CAPTURE_PATH" = "rootfs" ]; then
    # Rootfs path: the agent's rootfsonly.Watcher auto-captures pods
    # carrying nvsnap.io/capture=true once they're Ready + warmup window
    # passes (default 60s). It writes a ConfigMap manifest keyed by an
    # input-hash derived from pod identity (image + mounts + env).
    # Existing CMs with the same hash short-circuit the watcher — that's
    # the right behavior for production but means we need to wait for
    # the manifest to actually appear if we want this test deterministic.
    log_info "Step 7: Waiting for rootfs capture (watcher auto-fires post-Ready + warmup)..."
    POD_UID=$(kubectl get pod $POD_NAME -n $NAMESPACE -o jsonpath='{.metadata.uid}')
    HASH=""
    DEADLINE=$(( $(date +%s) + 900 ))  # 15 min — large captures take time
    while [ "$(date +%s)" -lt "$DEADLINE" ]; do
        # Pick the most-recent ConfigMap whose manifest.json source_pod_meta
        # matches this pod (by name + namespace). Multiple may exist if
        # the same workload has been captured before.
        # Capture the JSON into a var first: piping kubectl into
        # `python3 - <<'PY'` collides on stdin (the heredoc feeds the
        # program, leaving sys.stdin empty), so json.load reads nothing.
        # Pass the program via -c and pipe the data in.
        CM_JSON=$(kubectl get cm -n "$NAMESPACE" \
            -l nvsnap.io/kind=rootfs-capture-manifest -o json 2>/dev/null)
        HASH=$(printf '%s' "$CM_JSON" | python3 -c '
import sys, json
target_name, target_ns = sys.argv[1], sys.argv[2]
data = json.load(sys.stdin)
matches = []
for item in data.get("items", []):
    try:
        m = json.loads(item["data"]["manifest.json"])
        meta = m.get("source_pod_meta", {})
        if meta.get("name") == target_name and meta.get("namespace") == target_ns:
            matches.append((item["metadata"]["creationTimestamp"],
                            item["metadata"]["annotations"]["nvsnap.io/capture-hash"]))
    except Exception:
        pass
matches.sort(reverse=True)
if matches:
    print(matches[0][1])
' "$POD_NAME" "$NAMESPACE") || true
        if [ -n "$HASH" ]; then break; fi
        sleep 10
    done
    if [ -z "$HASH" ]; then
        fail "Rootfs capture (no manifest CM appeared within 15min)"
    fi
    CHECKPOINT_ID="$HASH"     # downstream uses this name uniformly
    log_info "Capture hash: $HASH"

    # Size: read from the manifest JSON (more reliable than du, since
    # data may live on a different node via cascade)
    CHECKPOINT_SIZE=$(kubectl get cm -n "$NAMESPACE" \
        "nvsnap-capture-${HASH:0:32}" -o jsonpath='{.data.manifest\.json}' 2>/dev/null \
        | python3 -c 'import sys,json; m=json.load(sys.stdin); b=int(m["total_size_bytes"]); print(f"{b/1024/1024/1024:.1f}G")' 2>/dev/null) || true
    if [ -n "$CHECKPOINT_SIZE" ]; then log_info "Capture size: $CHECKPOINT_SIZE"; fi
    step_done "Capture (rootfs)" "OK"
else
    log_info "Step 7: Creating checkpoint..."
    # criu-v2 must be requested explicitly per checkpoint — the agent only
    # takes the in-namespace engine via capturePath (or its own
    # NVSNAP_CRIU_V2=1 env). Plain criu keeps the no-override behavior.
    # The `|| CHECKPOINT_RC=$?` guard matters: under set -e a plain failing
    # command substitution kills the whole script before any diagnostics
    # below can print (first criu-v2 run died silently this way).
    CHECKPOINT_RC=0
    if [ "$CAPTURE_PATH" = "criu-v2" ]; then
        CHECKPOINT_OUTPUT=$(NVSNAP_CAPTURE_PATH=criu-v2 ${SCRIPT_DIR}/checkpoint.sh create $POD_NAME $CONTAINER_NAME $NAMESPACE 2>&1) || CHECKPOINT_RC=$?
    else
        CHECKPOINT_OUTPUT=$(${SCRIPT_DIR}/checkpoint.sh create $POD_NAME $CONTAINER_NAME $NAMESPACE 2>&1) || CHECKPOINT_RC=$?
    fi

    # The agent now refuses CRIU for Riva/Triton-backed NIMs (see
    # internal/agent/nim_backend.go). On redirect (exit 42), the
    # workload must run through the rootfs path instead. We don't
    # auto-rewire restore for that here — the manifest template,
    # webhook flow, and CHECKPOINT_ID semantics differ — so fail
    # loudly with a clear hint pointing at the test-bench rootfs
    # branch.
    if [ "$CHECKPOINT_RC" -eq 42 ]; then
        log_error "Agent refused CRIU for this workload (backend needs rootfs path)."
        log_error "Re-run with CAPTURE_PATH=rootfs or use scripts/test-bench.sh which auto-redirects."
        echo "$CHECKPOINT_OUTPUT"
        fail "Checkpoint (backend mismatch — use rootfs path)"
    fi

    # Pipefail kills the whole script if grep finds nothing (the typical
    # failure mode), which silently swallows checkpoint.sh's error
    # output. Guard the extraction so the diagnostic block below
    # actually runs and the user sees what checkpoint.sh said.
    CHECKPOINT_ID=$(printf '%s\n' "$CHECKPOINT_OUTPUT" | grep "Checkpoint ID:" | awk '{print $NF}' || true)

    if [ -z "$CHECKPOINT_ID" ]; then
        log_error "Failed to create checkpoint"
        echo "$CHECKPOINT_OUTPUT"
        fail "Checkpoint"
    fi
    log_info "Checkpoint: $CHECKPOINT_ID"
    step_done "Checkpoint" "OK"

    # Capture checkpoint size on disk
    AGENT_POD=$(kubectl get pods -n "$NAMESPACE" -l app=nvsnap-agent \
        --field-selector "spec.nodeName=$POD_NODE" \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
    CHECKPOINT_SIZE=""
    if [ -n "$AGENT_POD" ]; then
        CHECKPOINT_SIZE=$(kubectl exec -n "$NAMESPACE" "$AGENT_POD" -- \
            du -sh "/var/lib/nvsnap/checkpoints/$CHECKPOINT_ID" 2>/dev/null | awk '{print $1}') || true
        if [ -n "$CHECKPOINT_SIZE" ]; then
            log_info "Checkpoint size: $CHECKPOINT_SIZE"
        fi

        # Size gate: 70B+ workloads must have GPU data in checkpoint
        if [[ "$WORKLOAD" == *"70b"* ]]; then
            CHECKPOINT_SIZE_BYTES=$(kubectl exec -n "$NAMESPACE" "$AGENT_POD" -- \
                du -sb "/var/lib/nvsnap/checkpoints/$CHECKPOINT_ID" 2>/dev/null | awk '{print $1}') || true
            MIN_SIZE=$((100 * 1024 * 1024 * 1024))  # 100G
            if [ -n "$CHECKPOINT_SIZE_BYTES" ] && [ "$CHECKPOINT_SIZE_BYTES" -lt "$MIN_SIZE" ]; then
                log_error "Checkpoint too small for 70B workload: $CHECKPOINT_SIZE (need 100G+). GPU data likely missing."
                log_error "Verify NVSNAP_CUDA_INTERCEPT=1 and /checkpoints volume is mounted in source manifest."
                fail "Checkpoint size gate"
            fi
        fi
    fi
fi

# ─── Step 8: Delete original pod ─────────────────────────────────────────────
log_info "Step 8: Deleting original pod..."
kubectl delete pod $POD_NAME -n $NAMESPACE --wait=false
sleep 5

# ─── Step 9: Restore from checkpoint ─────────────────────────────────────────
step_start
log_info "Step 9: Restoring on node $POD_NODE..."
RESTORE_MANIFEST=$(mktemp)
trap "rm -f $RESTORE_MANIFEST" EXIT

# Substitute the CHECKPOINT_ID env value AND the nodeName.
#
# We use a literal placeholder __CHECKPOINT_ID__ in restore manifests
# (cf. vllm-small-restore.yaml etc.) rather than a "find name: then
# rewrite the next line" sed dance, because the latter only handles
# block-style YAML — flow-style entries like `- { name: FOO, value: "x" }`
# break it by clobbering the wrong line. Direct placeholder substitution
# is style-agnostic and matches the same pattern we use for __NODE_NAME__.
#
# For backwards compatibility we ALSO keep the old block-style rewrite
# for any restore manifest that hasn't yet adopted the placeholder.
if [ "$CAPTURE_PATH" = "rootfs" ]; then
    # Rootfs restore manifests use __CAPTURE_HASH__ in a nvsnap.io/restore-from
    # annotation; the webhook injects the storage. No nodeName pin —
    # webhook may add nodeAffinity based on backend.CapturedOnNodes
    # (Local) or pull via cascade (peer / blobstore) on any node.
    # __NODE_NAME__ is shared with the CRIU template; pin it to the
    # capture node (the local copy lives there — the fast path; the
    # webhook can still cascade-fetch if scheduled elsewhere).
    sed -e "s|__CAPTURE_HASH__|$CHECKPOINT_ID|g" \
        -e "s|__NODE_NAME__|$POD_NODE|g" \
        "$RESTORE_MANIFEST_TEMPLATE" > "$RESTORE_MANIFEST"
else
    sed -e "s|__CHECKPOINT_ID__|$CHECKPOINT_ID|g" \
        -e "/name: CHECKPOINT_ID/{n; s|value: \"__CHECKPOINT_ID__\"|value: \"$CHECKPOINT_ID\"|;}" \
        -e "s|nodeName: .*|nodeName: $POD_NODE|" \
        "$RESTORE_MANIFEST_TEMPLATE" > "$RESTORE_MANIFEST"
fi

# Phase 5d: restore goes back to the simple hostPath mount on the
# capture-source node (or the agent's EnsureLocal cascade materializes
# it locally on a different target node before the placeholder reads
# /checkpoints). The gpdrox PVC patching block lived here previously
# and was removed when 5d landed peer-fanout + nvsnap-blobstore.

kubectl apply -f "$RESTORE_MANIFEST"

# criu-v2: the restore pod is a dumb reaper placeholder — the agent drives
# the restore. Wait for Running (never Ready on its own), then POST
# /v1/restore to the agent on the checkpoint node; the readiness probe
# flips once the restored engine serves /v1/models again.
if [ "$CAPTURE_PATH" = "criu-v2" ]; then
    log_info "criu-v2: waiting for placeholder pod Running..."
    kubectl wait --for=jsonpath='{.status.phase}'=Running \
        pod/$RESTORE_POD_NAME -n $NAMESPACE --timeout=600s || fail "criu-v2 placeholder Running"
    AGENT_POD=$(kubectl get pods -n $NAMESPACE -l app=nvsnap-agent \
        --field-selector "spec.nodeName=$POD_NODE" -o jsonpath='{.items[0].metadata.name}')
    [ -n "$AGENT_POD" ] || fail "criu-v2 restore (no agent pod on $POD_NODE)"
    log_info "criu-v2: agent-driven restore via $AGENT_POD (synchronous, up to 21min)..."
    RESTORE_RESP=$(kubectl exec -n $NAMESPACE "$AGENT_POD" -c agent -- \
        curl -s --max-time 1260 -X POST "http://localhost:8081/v1/restore" \
        -H 'Content-Type: application/json' \
        -d "{\"checkpointId\":\"$CHECKPOINT_ID\",\"placeholderPodName\":\"$RESTORE_POD_NAME\",\"placeholderNamespace\":\"$NAMESPACE\"}") || true
    if ! printf '%s' "$RESTORE_RESP" | grep -q '"newContainerId"'; then
        log_error "agent /v1/restore failed: $RESTORE_RESP"
        kubectl logs -n $NAMESPACE "$AGENT_POD" -c agent --tail=40 || true
        fail "criu-v2 agent restore"
    fi
    log_info "criu-v2: agent restore returned: $RESTORE_RESP"
fi

log_info "Waiting for restore pod ready (up to ${RESTORE_READY_TIMEOUT}s)..."
log_info "  (readiness probe polls /v1/models — succeeds only when serving)"
if kubectl wait --for=condition=ready pod/$RESTORE_POD_NAME -n $NAMESPACE --timeout=${RESTORE_READY_TIMEOUT}s; then
    step_done "Restore pod ready" "OK"
else
    log_warn "Restore pod not ready, checking status..."
    kubectl get pod $RESTORE_POD_NAME -n $NAMESPACE -o wide || true
    kubectl logs $RESTORE_POD_NAME -n $NAMESPACE -c $RESTORE_CONTAINER_NAME --tail=30 || true
    fail "Restore pod ready"
fi

# ─── Step 10: Post-restore /v1/models ────────────────────────────────────────
step_start
log_info "Step 10: Verifying /v1/models after restore..."
if poll_api "$RESTORE_POD_NAME" "$RESTORE_CONTAINER_NAME" GET /v1/models "" "$MODEL" \
    "$POST_MODELS_TIMEOUT" "$POST_MODELS_INTERVAL" "post-restore /v1/models"; then
    step_done "Post-restore models" "OK"
else
    kubectl logs $RESTORE_POD_NAME -n $NAMESPACE -c $RESTORE_CONTAINER_NAME --tail=30 || true
    fail "Post-restore models"
fi

# ─── Step 11: Post-restore inference ─────────────────────────────────────────
step_start
log_info "Step 11: Verifying $INFER_ENDPOINT after restore..."
if poll_api "$RESTORE_POD_NAME" "$RESTORE_CONTAINER_NAME" POST "$INFER_ENDPOINT" \
    "$POST_INFER_DATA" \
    "$INFER_VERIFY_PATTERN" "$POST_INFER_TIMEOUT" "$POST_INFER_INTERVAL" "post-restore $INFER_ENDPOINT"; then
    echo "$POLL_RESULT" | python3 -m json.tool 2>/dev/null || echo "$POLL_RESULT"
    step_done "Post-restore infer" "OK"
else
    log_warn "Post-restore $INFER_ENDPOINT not working"
    step_done "Post-restore infer" "FAIL"
fi

# ─── Diagnostics ─────────────────────────────────────────────────────────────
echo ""
log_info "Key restore events:"
kubectl logs $RESTORE_POD_NAME -n $NAMESPACE -c $RESTORE_CONTAINER_NAME 2>&1 | \
    grep -E "wakeRestoredThreads|uv_loop_fork|libzmq.*CRIU|ETERM|reinit completed|RESTORE_COMPLETE" | head -10 || true

# ─── Summary ─────────────────────────────────────────────────────────────────
echo ""
log_info "=========================================="
if print_summary; then
    log_info "TEST PASSED: $WORKLOAD"
else
    log_error "TEST FAILED: $WORKLOAD"
fi
log_info "=========================================="
echo ""
log_info "Checkpoint: ${CHECKPOINT_ID:-<none>}"
log_info "Checkpoint size: ${CHECKPOINT_SIZE:-unknown}"
log_info "Restored pod: $RESTORE_POD_NAME"
log_info "Cleanup: kubectl delete pod $RESTORE_POD_NAME -n $NAMESPACE"
