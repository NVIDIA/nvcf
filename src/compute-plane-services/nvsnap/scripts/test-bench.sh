#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# PDF Testing Matrix benchmark runner.
#
# Distinct from scripts/test-e2e.sh (the nvsnap regression suite) — emits
# results in the format the customer-facing PDF expects:
#
#   Cloud | Storage | Hardware | Model | From-Cold (4 cols) |
#   From-Warm (4 cols) | Snapshot (s) | From-Restore (4 cols)
#
# Each From-* group breaks down into Container Download, Model Download,
# Model Initialization, Total — pulled from kubectl events and vLLM/NIM
# log timestamps.
#
# Usage:
#   ./test-bench.sh <workload> [--skip-cold|--skip-warm|--skip-restore]
#
# Workloads (single-GPU first; multi-GPU rows pending #25):
#   e5-mistral        Embedding (vllm v0.20.0, e5-mistral-7b)   1xH100
#   whisper           ASR/TTS (whisper-large-v3 NIM)            1xH100  [pending NGC validation]
#
# Output:
#   docs/PDF-BENCH-RESULTS.md  (markdown table, appended per workload)
#
# Exit codes:
#   0 = bench complete (each phase recorded, even partial PASS)
#   1 = setup error (missing manifest, kube unreachable)

set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
source "$SCRIPT_DIR/versions.sh"

# ─── Global temp-file cleanup ────────────────────────────────────────────────
# Single EXIT trap; each phase appends to TMP_FILES instead of installing its
# own trap (which would clobber any prior trap silently).
TMP_FILES=()
cleanup_tmp_files() {
    local f
    for f in "${TMP_FILES[@]:-}"; do
        [ -n "$f" ] && rm -f "$f"
    done
}
trap cleanup_tmp_files EXIT

# Verify cluster reachable.
if ! kubectl get nodes --no-headers --request-timeout=5s >/dev/null 2>&1; then
    echo "[ERROR] Cannot reach cluster. Check KUBECONFIG / tsh login." >&2
    exit 1
fi

# ─── Colors / logging ────────────────────────────────────────────────────────
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'
log_info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }

# ─── Args ────────────────────────────────────────────────────────────────────
WORKLOAD="${1:-}"
if [ -z "$WORKLOAD" ]; then
    cat <<EOF
Usage: $0 <workload>

PDF Testing Matrix workloads (in order):
  e5-mistral        Embedding: e5-mistral-7b on vllm:v0.20.0 (1xH100)
  whisper           ASR/TTS:   whisper-large-v3 NIM         (1xH100)  [requires ngc-api-key]
  gemma-4-31b-nim   LLM:       gemma-4-31B via NIM 1.7.1    (1xH100)  [requires ngc-api-key]

Results appended to docs/PDF-BENCH-RESULTS.md.
EOF
    exit 1
fi

SKIP_COLD=0; SKIP_WARM=0; SKIP_RESTORE=0
shift || true
for arg in "$@"; do
    case "$arg" in
        --skip-cold)    SKIP_COLD=1 ;;
        --skip-warm)    SKIP_WARM=1 ;;
        --skip-restore) SKIP_RESTORE=1 ;;
        *) log_error "unknown flag: $arg"; exit 1 ;;
    esac
done

# ─── Per-workload config ─────────────────────────────────────────────────────
# Each block sets: POD_NAME, RESTORE_POD_NAME, NAMESPACE, PORT, MODEL,
# READY_ENDPOINT, READY_PATTERN, INFER_ENDPOINT, INFER_DATA, INFER_VERIFY_PATTERN,
# SOURCE_MANIFEST, RESTORE_MANIFEST_TEMPLATE, MODEL_DL_BEGIN_PAT, MODEL_DL_END_PAT,
# MODEL_INIT_END_PAT, HARDWARE.
NAMESPACE="nvsnap-system"
# L2/cachedir storage class the deployed agent uses (from its --pod-cache-dir
# / NVSNAP_L2_STORAGE_CLASS). Non-empty → verify_capture_committed also
# checks the rox promote reached ready (the product/NVCA restore path).
L2_STORAGECLASS="$(kubectl get ds nvsnap-agent -n "$NAMESPACE" -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="NVSNAP_L2_STORAGE_CLASS")].value}' 2>/dev/null || true)"
# WEIGHTS_GB: safetensors/model weight size in GiB, used to compute the
# model-load GiB/s (storage-bandwidth figure). 0 = unknown → GiB/s omitted.
# Set per workload below where known.
WEIGHTS_GB=0
case "$WORKLOAD" in
    e5-mistral)
        POD_NAME="bench-e5-mistral"
        CONTAINER_NAME="vllm"
        RESTORE_POD_NAME="bench-e5-mistral-restored"
        RESTORE_CONTAINER_NAME="vllm"   # cachedir restore runs the workload container, not a CRIU 'restore' shim
        PORT=8000
        MODEL="intfloat/e5-mistral-7b-instruct"
        HARDWARE="1xH100"
        READY_ENDPOINT="/v1/models"
        INFER_ENDPOINT="/v1/embeddings"
        INFER_DATA='{"model":"intfloat/e5-mistral-7b-instruct","input":"Hello"}'
        INFER_VERIFY_PATTERN='"embedding"'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/benchmarks/e5-mistral.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/benchmarks/e5-mistral-restore.yaml"
        # Log-grep patterns to slice cold-start timing
        MODEL_DL_BEGIN_PAT="Downloading.*\\(safetensors\\|shard\\|model\\)"
        MODEL_DL_END_PAT="Loading weights took"
        MODEL_INIT_END_PAT="Application startup complete"
        ;;
    gemma-4-31b)
        # Matches PDF spec exactly: sglang v0.5.12.post1 + HF gemma-4-31B-it
        POD_NAME="bench-gemma-4-31b"
        CONTAINER_NAME="sglang"
        RESTORE_POD_NAME="bench-gemma-4-31b-restored"
        RESTORE_CONTAINER_NAME="sglang"   # cachedir restore runs the workload container
        PORT=30000
        MODEL="google/gemma-4-31B-it"
        HARDWARE="1xH100"
        READY_ENDPOINT="/v1/models"
        INFER_ENDPOINT="/v1/completions"
        INFER_DATA='{"model":"google/gemma-4-31B-it","prompt":"Hello","max_tokens":5}'
        POST_INFER_DATA='{"model":"google/gemma-4-31B-it","prompt":"The meaning of life is","max_tokens":10}'
        INFER_VERIFY_PATTERN='"choices"'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/benchmarks/gemma-4-31b.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/benchmarks/gemma-4-31b-restore.yaml"
        MODEL_DL_BEGIN_PAT="Downloading"
        MODEL_DL_END_PAT="model.weight_loader.*finished"
        MODEL_INIT_END_PAT="The server is fired up"
        ;;
    gemma-4-31b-nim)
        # NIM 1.7.1-variant of the same model — kept for parallel measurement.
        # Blocked on Riva ASR cudaHostUnregister post-restore failure (see
        # docs/PDF-BENCH-RESULTS.md whisper footnote — same class of bug).
        POD_NAME="bench-gemma-4-31b"
        CONTAINER_NAME="nim"
        RESTORE_POD_NAME="bench-gemma-4-31b-restored"
        RESTORE_CONTAINER_NAME="restore"
        PORT=8000
        MODEL="google/gemma-4-31b-it"
        HARDWARE="1xH100"
        READY_ENDPOINT="/v1/health/ready"
        INFER_ENDPOINT="/v1/completions"
        INFER_DATA='{"model":"google/gemma-4-31b-it","prompt":"Hello","max_tokens":5}'
        POST_INFER_DATA='{"model":"google/gemma-4-31b-it","prompt":"The meaning of life is","max_tokens":10}'
        INFER_VERIFY_PATTERN='"choices"'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/benchmarks/gemma-4-31b-nim.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/benchmarks/gemma-4-31b-nim-restore.yaml"
        MODEL_DL_BEGIN_PAT="Downloading"
        MODEL_DL_END_PAT="Loading.*complete"
        MODEL_INIT_END_PAT="Uvicorn running"
        ;;
    deepseek-v4-flash)
        # Args verbatim from PDF Testing Matrix: sglang TP=8.
        POD_NAME="bench-deepseek-v4-flash"
        CONTAINER_NAME="sglang"
        RESTORE_POD_NAME="bench-deepseek-v4-flash-restored"
        RESTORE_CONTAINER_NAME="sglang"
        PORT=8000
        MODEL="deepseek-ai/DeepSeek-V4-Flash"
        HARDWARE="8xH100"
        CAPTURE_PATH="rootfs"   # TP=8 multi-GPU → rootfs+OverlayFS per CLAUDE.md rule 20
        READY_ENDPOINT="/v1/models"
        INFER_ENDPOINT="/v1/completions"
        INFER_DATA='{"model":"deepseek-ai/DeepSeek-V4-Flash","prompt":"Hello","max_tokens":5}'
        POST_INFER_DATA='{"model":"deepseek-ai/DeepSeek-V4-Flash","prompt":"The capital of France is","max_tokens":10}'
        INFER_VERIFY_PATTERN='"choices"'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/benchmarks/deepseek-v4-flash.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/benchmarks/deepseek-v4-flash-restore.yaml"
        MODEL_DL_BEGIN_PAT="Downloading"
        MODEL_DL_END_PAT="model.weight_loader.*finished"
        MODEL_INIT_END_PAT="The server is fired up"
        ;;
    gpt-oss-120b)
        POD_NAME="bench-gpt-oss-120b"
        CONTAINER_NAME="vllm"
        RESTORE_POD_NAME="bench-gpt-oss-120b-restored"
        RESTORE_CONTAINER_NAME="vllm"
        PORT=8000
        MODEL="openai/gpt-oss-120b"
        HARDWARE="4xH100"
        WEIGHTS_GB=60           # safetensors total; for model-load GiB/s
        CAPTURE_PATH="rootfs"   # TP=4 multi-GPU → rootfs per CLAUDE.md rule 20
        READY_ENDPOINT="/v1/models"
        INFER_ENDPOINT="/v1/completions"
        INFER_DATA='{"model":"gpt-oss-120b","prompt":"Hello","max_tokens":5}'
        POST_INFER_DATA='{"model":"gpt-oss-120b","prompt":"The meaning of life is","max_tokens":10}'
        INFER_VERIFY_PATTERN='"choices"'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/benchmarks/gpt-oss-120b.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/benchmarks/gpt-oss-120b-restore.yaml"
        MODEL_DL_BEGIN_PAT="Downloading.*\\(safetensors\\|shard\\|model\\)"
        MODEL_DL_END_PAT="Loading weights took"
        MODEL_INIT_END_PAT="Application startup complete"
        ;;
    qwen-image)
        POD_NAME="bench-qwen-image"
        CONTAINER_NAME="vllm"
        RESTORE_POD_NAME="bench-qwen-image-restored"
        RESTORE_CONTAINER_NAME="vllm"   # cachedir restore runs the workload container
        PORT=8091
        MODEL="Qwen/Qwen-Image-2512"
        HARDWARE="1xH100"
        READY_ENDPOINT="/v1/models"
        # Image-gen API path. Different shape from /v1/embeddings — vLLM
        # omni's image-gen endpoint may be /v1/images/generations (TBD).
        # For pre/post inference probe we only need a healthy response;
        # the readiness probe is what gates phase transitions.
        INFER_ENDPOINT="/v1/models"
        INFER_VERIFY_PATTERN='"object"'
        INFER_DATA=""
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/benchmarks/qwen-image.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/benchmarks/qwen-image-restore.yaml"
        MODEL_DL_BEGIN_PAT="Downloading.*\\(safetensors\\|shard\\|model\\)"
        MODEL_DL_END_PAT="Loading weights took"
        MODEL_INIT_END_PAT="Application startup complete"
        ;;
    whisper)
        POD_NAME="bench-whisper"
        CONTAINER_NAME="whisper"
        RESTORE_POD_NAME="bench-whisper-rootfs-restored"
        RESTORE_CONTAINER_NAME="whisper"   # cachedir restore runs the workload container
        PORT=9000
        MODEL="openai/whisper-large-v3"
        HARDWARE="1xH100"
        READY_ENDPOINT="/v1/health/ready"
        INFER_ENDPOINT="/v1/audio/transcriptions"
        # NIM probe — the PDF doesn't include an inference body sample;
        # we use a small WAV (~1s) that the script will provide. Until
        # the manifest is wired, this workload errors at the deploy step.
        INFER_DATA=""
        INFER_VERIFY_PATTERN='"text"'
        SOURCE_MANIFEST="$PROJECT_ROOT/deploy/k8s/benchmarks/whisper-large-v3.yaml"
        RESTORE_MANIFEST_TEMPLATE="$PROJECT_ROOT/deploy/k8s/benchmarks/whisper-large-v3-rootfs-restore.yaml"
        MODEL_DL_BEGIN_PAT="Downloading model"
        MODEL_DL_END_PAT="Model loaded"
        MODEL_INIT_END_PAT="Uvicorn running"
        ;;
    *)
        log_error "unknown workload: $WORKLOAD"
        exit 1
        ;;
esac

if [ ! -f "$SOURCE_MANIFEST" ]; then
    log_error "missing source manifest: $SOURCE_MANIFEST"
    exit 1
fi

# Optional loader-strategy axis (storage experiment): NVSNAP_LOAD_STRATEGY makes
# the SOURCE vLLM start with a parallel/streamed weight loader, so the captured
# EntryArgv carries it and the B′ restore execs it. Lets us measure the
# model-load GiB/s win from parallel reads off rox (single-stream rox ~0.76
# GiB/s, parallel ceiling ~2.35 GiB/s — see docs/proposals/rootfs-overlay-mount-
# scale.md). Values: "prefetch" → --safetensors-load-strategy=prefetch;
# "runai_streamer" → --load-format runai_streamer. Default unset = no-op.
# Only applies to vLLM sources (the args block contains "vllm serve").
if [ -n "${NVSNAP_LOAD_STRATEGY:-}" ]; then
    if grep -q "vllm serve" "$SOURCE_MANIFEST"; then
        case "$NVSNAP_LOAD_STRATEGY" in
            runai_streamer) _LOAD_FLAG="--load-format runai_streamer" ;;
            *)              _LOAD_FLAG="--safetensors-load-strategy=${NVSNAP_LOAD_STRATEGY}" ;;
        esac
        _SRC_RENDERED=$(mktemp); TMP_FILES+=("$_SRC_RENDERED")
        # Insert the flag as a new continued arg right after "vllm serve \",
        # matching that line's indentation.
        awk -v flag="$_LOAD_FLAG" '
            { print }
            /vllm serve[[:space:]]*\\$/ {
                match($0, /^[[:space:]]*/); indent=substr($0,1,RLENGTH)
                print indent flag " \\"
            }' "$SOURCE_MANIFEST" > "$_SRC_RENDERED"
        SOURCE_MANIFEST="$_SRC_RENDERED"
        log_info "loader strategy: injected '${_LOAD_FLAG}' into source vLLM args (NVSNAP_LOAD_STRATEGY=${NVSNAP_LOAD_STRATEGY})"
    else
        log_info "NVSNAP_LOAD_STRATEGY set but source is not vLLM (no 'vllm serve') — ignoring"
    fi
fi
# CAPTURE_PATH defaults to "rootfs" — the agent itself defaults to rootfs
# since v0.0.48 (internal/agent/nim_backend.go), so the bench matches:
# rootfs for every workload unless a case block (or CAPTURE_PATH=criu in
# the env) explicitly opts into the CRIU + cuda-checkpoint path. Rootfs
# avoids the LD_PRELOAD quiesce intercept entirely (which breaks
# Riva/Triton NIM startup via SIGUSR1/2 hijack).
CAPTURE_PATH="${CAPTURE_PATH:-rootfs}"

# ─── Helpers ─────────────────────────────────────────────────────────────────
POD_READY_TIMEOUT=1800  # 30m — gemma-4-31b NIM from NGC takes ~20-25 min cold download
INFER_TIMEOUT=300
NODE=""

# Pull-time + model-load-time + init-time + total — extracted from kubectl
# events + container logs after the pod is fully ready. Single source of
# truth for the four-column triple the PDF asks for.
measure_phase() {
    local pod="$1" container="$2" phase_label="$3"
    local out
    out=$(kubectl -n "$NAMESPACE" get pod "$pod" -o json 2>/dev/null) || true
    if [ -z "$out" ]; then echo "0:0:0:0"; return; fi

    # Pod scheduled timestamp.
    local sched
    sched=$(printf '%s' "$out" | python3 -c '
import json,sys,datetime
d=json.load(sys.stdin)
for c in d.get("status",{}).get("conditions",[]):
    if c.get("type")=="PodScheduled" and c.get("status")=="True":
        print(c.get("lastTransitionTime","")); break
') || true

    # First containerd Pulled timestamp on the inference container.
    local pulled_end
    pulled_end=$(kubectl -n "$NAMESPACE" get events --field-selector "involvedObject.name=$pod" \
        -o json 2>/dev/null | python3 -c "
import json,sys
d=json.load(sys.stdin)
target='spec.containers{${container}}'
for e in d.get('items',[]):
    if e.get('reason')=='Pulled' and e.get('involvedObject',{}).get('fieldPath')==target:
        print(e.get('lastTimestamp') or e.get('eventTime') or ''); break
" 2>/dev/null) || true

    # Container started + ready timestamps.
    local started ready_at
    started=$(printf '%s' "$out" | python3 -c "
import json,sys
d=json.load(sys.stdin)
for cs in d.get('status',{}).get('containerStatuses',[]):
    if cs.get('name')=='$container':
        st=cs.get('state',{}).get('running',{}).get('startedAt') or cs.get('lastState',{}).get('terminated',{}).get('startedAt')
        if st: print(st); break
")
    ready_at=$(printf '%s' "$out" | python3 -c '
import json,sys
d=json.load(sys.stdin)
for c in d.get("status",{}).get("conditions",[]):
    if c.get("type")=="Ready" and c.get("status")=="True":
        print(c.get("lastTransitionTime","")); break
')

    # Slice model-download + init from container logs (best-effort; if patterns
    # don't match — e.g. fully cached — return 0 for that bucket and lump time
    # into Model Initialization).
    local logs
    logs=$(kubectl -n "$NAMESPACE" logs "$pod" -c "$container" --timestamps --tail=2000 2>/dev/null) || true
    local dl_begin dl_end init_end
    dl_begin=$(printf '%s' "$logs" | grep -oE "^[0-9T:.Z-]+" | head -1)
    dl_end=$(printf '%s' "$logs" | grep -E "$MODEL_DL_END_PAT" | head -1 | grep -oE "^[0-9T:.Z-]+")
    init_end=$(printf '%s' "$logs" | grep -E "$MODEL_INIT_END_PAT" | head -1 | grep -oE "^[0-9T:.Z-]+")

    # Compute deltas in seconds via python (handles RFC3339 with nanos).
    python3 -c "
import sys,datetime
def t(s):
    if not s: return None
    s=s.rstrip('Z').split('+')[0]
    if '.' in s: head,frac=s.split('.'); s=head+'.'+frac[:6]
    try: return datetime.datetime.fromisoformat(s)
    except: return None
sched, pulled, started, ready = t('$sched'), t('$pulled_end'), t('$started'), t('$ready_at')
dl_b, dl_e, init_e = t('$dl_begin'), t('$dl_end'), t('$init_end')
def d(a,b):
    if a is None or b is None: return 0
    return max(0,(b-a).total_seconds())
container_dl = d(sched, pulled) if pulled else d(sched, started)
model_dl     = d(dl_b, dl_e)    if dl_e else 0
model_init   = d(dl_e or started, ready) if ready else 0
total        = d(sched, ready)
print(f'{container_dl:.1f}:{model_dl:.1f}:{model_init:.1f}:{total:.1f}')
"
}

# measure_load extracts the storage-bound weight-load metrics from a pod's
# logs (vllm). Echoes "load_secs:download_secs:gbps":
#   load_secs     — vLLM "Loading weights took N seconds" (read weights → VRAM)
#   download_secs — vLLM "Time spent downloading weights ... : N seconds"
#                   (0/absent ⇒ CACHE HIT, no HF download — the warm-restore proof)
#   gbps          — WEIGHTS_GB / load_secs (only if WEIGHTS_GB>0), the
#                   storage-bandwidth figure the PDF storage case rests on.
# Non-vLLM workloads (NIM) don't emit these → returns 0:0:0 (harmless).
measure_load() {
    local pod="$1" container="$2"
    local logs
    logs=$(kubectl -n "$NAMESPACE" logs "$pod" -c "$container" --tail=4000 2>/dev/null) || true
    local load_secs dl_secs
    load_secs=$(printf '%s' "$logs" | grep -oE "Loading weights took [0-9.]+ seconds" | tail -1 | grep -oE "[0-9.]+" | head -1)
    dl_secs=$(printf '%s' "$logs" | grep -oE "Time spent downloading weights[^0-9]*[0-9.]+ seconds" | tail -1 | grep -oE "[0-9.]+ seconds" | grep -oE "[0-9.]+")
    awk -v l="${load_secs:-0}" -v d="${dl_secs:-0}" -v w="${WEIGHTS_GB:-0}" \
        'BEGIN{g=(w>0 && l>0)? w/l : 0; printf "%.1f:%.1f:%.2f\n", l, d, g}'
}

cleanup_workload_pods() {
    kubectl -n "$NAMESPACE" delete pod "$POD_NAME" "$RESTORE_POD_NAME" --ignore-not-found --wait=true >/dev/null 2>&1 || true
}

wait_ready() {
    local pod="$1" timeout="$2"
    # Fail fast: if the pod object never appears within 20s, `kubectl wait`
    # would otherwise block the full timeout on a non-existent resource
    # (admission rejection, wrong pod name, etc.). Surface the reason.
    local i
    for i in $(seq 1 20); do
        kubectl -n "$NAMESPACE" get pod "$pod" >/dev/null 2>&1 && break
        if [ "$i" -eq 20 ]; then
            log_error "pod/$pod was never created (admission rejected or name mismatch). Recent events:"
            kubectl -n "$NAMESPACE" get events --field-selector "involvedObject.name=$pod" \
                --sort-by=.lastTimestamp 2>/dev/null | tail -8 >&2
            return 1
        fi
        sleep 1
    done
    kubectl -n "$NAMESPACE" wait --for=condition=Ready pod/"$pod" --timeout="${timeout}s" >/dev/null
}

verify_infer() {
    local pod="$1" container="$2"
    # Match test-e2e.sh's pod_curl pattern: curl from inside the pod
    # against localhost so we don't deal with K8s Service routing.
    local out
    out=$(kubectl -n "$NAMESPACE" exec "$pod" -c "$container" -- \
        env LD_PRELOAD= curl -sf -m 30 -X POST "http://localhost:${PORT}${INFER_ENDPOINT}" \
        -H "Content-Type: application/json" -d "$INFER_DATA" 2>/dev/null || true)
    if echo "$out" | grep -q "$INFER_VERIFY_PATTERN"; then
        return 0
    fi
    return 1
}

# ─── Phase 1: Cold start ──────────────────────────────────────────────────────
COLD_CDL=0; COLD_MDL=0; COLD_INIT=0; COLD_TOTAL=0
if [ "$SKIP_COLD" -eq 0 ]; then
    log_info "Phase 1/4: cold start"
    cleanup_workload_pods
    # Best-effort image purge on the target node to make Container Download
    # measurement meaningful. crictl rmi runs on every agent.
    log_info "  wiping hostPath HF cache for true cold-model measurement..."
    # Agent has the HOST FS at /host (not /var/lib); the bench yamls'
    # hf-cache-bench hostPath is on the node, not in the agent container.
    # Wipe both candidate locations: boot-disk (legacy) + local SSD (preferred).
    for a in $(kubectl -n nvsnap-system get pods -l app=nvsnap-agent -o name 2>/dev/null); do
        kubectl -n nvsnap-system exec "$a" -c agent -- sh -c '
            rm -rf /host/var/lib/hf-cache-bench/* /host/var/lib/hf-cache-bench/.locks 2>/dev/null || true
            rm -rf /host/mnt/stateful_partition/kube-ephemeral-ssd/nvsnap/hf-cache-bench/* /host/mnt/stateful_partition/kube-ephemeral-ssd/nvsnap/hf-cache-bench/.locks 2>/dev/null || true
        ' 2>/dev/null || true
    done
    # Container image purge stays best-effort (crictl rarely in PATH);
    # accept Container DL = 0 on subsequent runs if image is cached.
    kubectl apply -f "$SOURCE_MANIFEST" >/dev/null
    wait_ready "$POD_NAME" "$POD_READY_TIMEOUT" || { log_error "cold pod didn't ready"; exit 1; }
    NODE=$(kubectl -n "$NAMESPACE" get pod "$POD_NAME" -o jsonpath='{.spec.nodeName}')
    log_info "  cold pod ready on $NODE"
    verify_infer "$POD_NAME" "$CONTAINER_NAME" || log_warn "  cold inference probe failed (continuing)"
    IFS=':' read -r COLD_CDL COLD_MDL COLD_INIT COLD_TOTAL <<<"$(measure_phase "$POD_NAME" "$CONTAINER_NAME" cold)"
    IFS=':' read -r COLD_LOAD COLD_DLW COLD_GBPS <<<"$(measure_load "$POD_NAME" "$CONTAINER_NAME")"
    log_info "  cold: container=${COLD_CDL}s model_dl=${COLD_MDL}s init=${COLD_INIT}s total=${COLD_TOTAL}s | load=${COLD_LOAD}s dlw=${COLD_DLW}s ${COLD_GBPS} GiB/s"
fi

# ─── Phase 2: Warm cache (same node, image+model cached) ─────────────────────
WARM_CDL=0; WARM_MDL=0; WARM_INIT=0; WARM_TOTAL=0
if [ "$SKIP_WARM" -eq 0 ]; then
    log_info "Phase 2/4: warm cache (redeploy on same node)"
    cleanup_workload_pods
    kubectl apply -f "$SOURCE_MANIFEST" >/dev/null
    wait_ready "$POD_NAME" "$POD_READY_TIMEOUT" || { log_error "warm pod didn't ready"; exit 1; }
    verify_infer "$POD_NAME" "$CONTAINER_NAME" || log_warn "  warm inference probe failed (continuing)"
    IFS=':' read -r WARM_CDL WARM_MDL WARM_INIT WARM_TOTAL <<<"$(measure_phase "$POD_NAME" "$CONTAINER_NAME" warm)"
    IFS=':' read -r WARM_LOAD WARM_DLW WARM_GBPS <<<"$(measure_load "$POD_NAME" "$CONTAINER_NAME")"
    log_info "  warm: container=${WARM_CDL}s model_dl=${WARM_MDL}s init=${WARM_INIT}s total=${WARM_TOTAL}s | load=${WARM_LOAD}s dlw=${WARM_DLW}s ${WARM_GBPS} GiB/s"
fi

# verify_capture_committed asserts the capture produced a RESTORABLE
# artifact — the same thing the product (NVCA restore) depends on — so the
# bench fails loudly instead of silently measuring a degraded capture.
# Catches the class of bug where the agent logs "capture failed; will
# retry" (e.g. an L2 promote RBAC Forbidden) but the partial hash still
# appears: the cluster-wide manifest ConfigMap is what the restore webhook
# Stats cross-node, and for L2/cachedir the rox promote must reach ready.
verify_capture_committed() {
    local hash="$1"
    local short="${hash:0:32}"
    # 1. Cluster-wide manifest CM (restore webhook resolves the hash from it).
    if ! kubectl get cm -n "$NAMESPACE" \
            -l "nvsnap.io/kind=rootfs-capture-manifest,nvsnap.io/short-hash=$short" \
            --no-headers 2>/dev/null | grep -q .; then
        log_error "capture $short committed NO cluster-wide manifest ConfigMap — restore cannot resolve the hash (the agent capture did not fully commit). Recent agent errors:"
        kubectl logs -n "$NAMESPACE" -l app=nvsnap-agent -c agent --tail=600 2>/dev/null \
            | grep -iE 'capture failed|forbidden|promote|backend put|error' | tail -8 >&2
        return 1
    fi
    # 2. L2/cachedir: the rox promote must reach ready, else restore can't
    #    mount the rox (no prewarm / no fan-out). Poll the catalog briefly.
    if [ -n "${L2_STORAGECLASS:-}" ]; then
        local sp i state
        sp=$(kubectl get pods -n "$NAMESPACE" --no-headers 2>/dev/null | awk '/nvsnap-server/&&/Running/{print $1;exit}')
        for i in $(seq 1 30); do
            state=$(kubectl exec -n "$NAMESPACE" "$sp" -- wget -qO- "http://localhost:8080/api/v1/checkpoints?source=db&limit=50" 2>/dev/null \
                | python3 -c "import sys,json
for c in json.load(sys.stdin).get('checkpoints',[]):
    if c.get('hash','').startswith('$short'): print(c.get('pvc_promote_state','')); break" 2>/dev/null)
            [ "$state" = "ready" ] && { log_info "  L2 rox promote: ready"; return 0; }
            sleep 10
        done
        log_warn "  L2 rox promote did not reach 'ready' (state=${state:-unknown}) within 300s — restore will fall back to node-local overlay (no rox fan-out)."
    fi
    return 0
}

# ─── Phase 3: Checkpoint ──────────────────────────────────────────────────────
SNAPSHOT_TIME=0
CHECKPOINT_ID=""
if [ "$SKIP_RESTORE" -eq 0 ]; then
    log_info "Phase 3/4: checkpoint ($CAPTURE_PATH path)"
    # If we skipped cold/warm, redeploy the workload first.
    if [ "$SKIP_COLD" -eq 1 ] && [ "$SKIP_WARM" -eq 1 ]; then
        cleanup_workload_pods
        kubectl apply -f "$SOURCE_MANIFEST" >/dev/null
        wait_ready "$POD_NAME" "$POD_READY_TIMEOUT" || { log_error "pod didn't ready"; exit 1; }
        NODE=$(kubectl -n "$NAMESPACE" get pod "$POD_NAME" -o jsonpath='{.spec.nodeName}')
        verify_infer "$POD_NAME" "$CONTAINER_NAME" || log_warn "  pre-checkpoint inference probe failed (continuing)"
    fi
    [ -z "$NODE" ] && NODE=$(kubectl -n "$NAMESPACE" get pod "$POD_NAME" -o jsonpath='{.spec.nodeName}')

    if [ "$CAPTURE_PATH" = "rootfs" ]; then
        # Rootfs path: the agent's rootfsonly.Watcher auto-captures pods
        # carrying nvsnap.io/capture=true after Ready + warmup (60s default).
        # We poll for the resulting ConfigMap (manifest.json + capture
        # hash) instead of POSTing to /v1/checkpoint.
        log_info "  rootfs: waiting for rootfsonly.Watcher to fire (Ready + 60s warmup)..."

        # Bash quirk: `python3 - <<'PY'` heredoc REPLACES the kubectl pipe
        # as python's stdin, so json.load(sys.stdin) reads the python
        # script source instead of the kubectl JSON and silently fails.
        # Write the script to a temp file once and pipe kubectl into it.
        ROOTFS_POLL_PY=$(mktemp -t nvsnap-rootfs-poll.XXXXXX.py)
        TMP_FILES+=("$ROOTFS_POLL_PY")
        cat > "$ROOTFS_POLL_PY" <<'PY'
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
if matches: print(matches[0][1])
PY

        local_start=$(date +%s)
        deadline=$(( local_start + POD_READY_TIMEOUT ))
        warned_empty=0
        while [ "$(date +%s)" -lt "$deadline" ]; do
            CHECKPOINT_ID=$(kubectl get cm -n "$NAMESPACE" \
                -l nvsnap.io/kind=rootfs-capture-manifest \
                -o json 2>/dev/null \
                | python3 "$ROOTFS_POLL_PY" "$POD_NAME" "$NAMESPACE" 2>/dev/null) || true
            if [ -n "$CHECKPOINT_ID" ]; then break; fi
            # Surface silent failure on first empty iteration so we
            # don't sit in the poll loop for 30 min if the python or
            # the kubectl call is broken.
            if [ "$warned_empty" -eq 0 ]; then
                elapsed=$(( $(date +%s) - local_start ))
                log_warn "  no rootfs capture CM yet at +${elapsed}s (will retry every 10s for ${POD_READY_TIMEOUT}s)"
                warned_empty=1
            fi
            sleep 10
        done
        SNAPSHOT_TIME=$(( $(date +%s) - local_start ))
        if [ -z "$CHECKPOINT_ID" ]; then
            log_error "rootfs capture didn't appear within ${POD_READY_TIMEOUT}s"
            exit 1
        fi
        log_info "  rootfs capture hash: $CHECKPOINT_ID (${SNAPSHOT_TIME}s wall-clock incl. warmup)"
        verify_capture_committed "$CHECKPOINT_ID" || exit 1
    else
        # CRIU path: POST to agent /v1/checkpoint via the helper script.
        local_start=$(date +%s)
        CHECKPOINT_OUT=$("$SCRIPT_DIR/checkpoint.sh" create "$POD_NAME" "$CONTAINER_NAME" "$NAMESPACE" 2>&1)
        CHECKPOINT_RC=$?

        # Exit 42 from checkpoint.sh = agent told us this workload's
        # backend (Riva or Triton) needs the rootfs path. Relabel the
        # source pod for the rootfsonly watcher and fall into the
        # rootfs poll loop. This lets us run test-bench against new
        # NIM workloads without having to know their backend in
        # advance.
        if [ "$CHECKPOINT_RC" -eq 42 ]; then
            BACKEND=$(printf '%s\n' "$CHECKPOINT_OUT" | sed -nE 's/.*backend=([a-z]+).*/\1/p' | head -1)
            log_warn "  agent redirected to rootfs path (backend=${BACKEND:-unknown}) — relabeling pod"
            kubectl -n "$NAMESPACE" label pod "$POD_NAME" nvsnap.io/capture=true --overwrite >/dev/null
            CAPTURE_PATH="rootfs"
            # Drop into the rootfs poll loop with the timer that's
            # already running. The poll is idempotent.
            log_info "  rootfs: waiting for rootfsonly.Watcher to fire (Ready + 60s warmup)..."
            ROOTFS_POLL_PY=$(mktemp -t nvsnap-rootfs-poll.XXXXXX.py)
            TMP_FILES+=("$ROOTFS_POLL_PY")
            cat > "$ROOTFS_POLL_PY" <<'PY'
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
if matches: print(matches[0][1])
PY
            deadline=$(( local_start + POD_READY_TIMEOUT ))
            while [ "$(date +%s)" -lt "$deadline" ]; do
                CHECKPOINT_ID=$(kubectl get cm -n "$NAMESPACE" \
                    -l nvsnap.io/kind=rootfs-capture-manifest \
                    -o json 2>/dev/null \
                    | python3 "$ROOTFS_POLL_PY" "$POD_NAME" "$NAMESPACE" 2>/dev/null) || true
                if [ -n "$CHECKPOINT_ID" ]; then break; fi
                sleep 10
            done
            SNAPSHOT_TIME=$(( $(date +%s) - local_start ))
            if [ -z "$CHECKPOINT_ID" ]; then
                log_error "rootfs capture didn't appear within ${POD_READY_TIMEOUT}s after redirect"
                exit 1
            fi
            log_info "  rootfs capture hash: $CHECKPOINT_ID (${SNAPSHOT_TIME}s wall-clock incl. warmup + redirect)"
            verify_capture_committed "$CHECKPOINT_ID" || exit 1
        else
            SNAPSHOT_TIME=$(( $(date +%s) - local_start ))
            CHECKPOINT_ID=$(printf '%s\n' "$CHECKPOINT_OUT" | grep "Checkpoint ID:" | awk '{print $NF}' || true)
            if [ -z "$CHECKPOINT_ID" ]; then
                log_error "checkpoint failed; output:"
                echo "$CHECKPOINT_OUT"
                exit 1
            fi
            log_info "  checkpoint: $CHECKPOINT_ID (${SNAPSHOT_TIME}s)"
        fi
    fi
fi

# ─── Phase 4: Restore ─────────────────────────────────────────────────────────
RESTORE_CDL=0; RESTORE_MDL=0; RESTORE_INIT=0; RESTORE_TOTAL=0
if [ "$SKIP_RESTORE" -eq 0 ] && [ -n "$CHECKPOINT_ID" ]; then
    log_info "Phase 4/4: restore ($CAPTURE_PATH path)"
    kubectl -n "$NAMESPACE" delete pod "$POD_NAME" --ignore-not-found --wait=true >/dev/null 2>&1 || true
    R_MANIFEST=$(mktemp); TMP_FILES+=("$R_MANIFEST")
    if [ "$CAPTURE_PATH" = "rootfs" ]; then
        # Rootfs restore: just substitute the capture hash; the nvsnap
        # webhook injects PVC mounts (and nodeAffinity for Local
        # backend) based on nvsnap.io/restore-from. No nodeName pin.
        sed -e "s|__CAPTURE_HASH__|$CHECKPOINT_ID|g" \
            "$RESTORE_MANIFEST_TEMPLATE" > "$R_MANIFEST"
    else
        # CRIU restore: substitute CHECKPOINT_ID + pin nodeName.
        sed -e "s|__CHECKPOINT_ID__|$CHECKPOINT_ID|g" \
            -e "/name: CHECKPOINT_ID/{n; s|value: \"__CHECKPOINT_ID__\"|value: \"$CHECKPOINT_ID\"|;}" \
            -e "s|nodeName: __NODE_NAME__|nodeName: $NODE|" \
            "$RESTORE_MANIFEST_TEMPLATE" > "$R_MANIFEST"
    fi
    kubectl apply -f "$R_MANIFEST" >/dev/null
    wait_ready "$RESTORE_POD_NAME" "$POD_READY_TIMEOUT" || { log_error "restore pod didn't ready"; exit 1; }
    verify_infer "$RESTORE_POD_NAME" "$RESTORE_CONTAINER_NAME" || log_warn "  post-restore inference probe failed (continuing)"
    IFS=':' read -r RESTORE_CDL RESTORE_MDL RESTORE_INIT RESTORE_TOTAL <<<"$(measure_phase "$RESTORE_POD_NAME" "$RESTORE_CONTAINER_NAME" restore)"
    IFS=':' read -r RESTORE_LOAD RESTORE_DLW RESTORE_GBPS <<<"$(measure_load "$RESTORE_POD_NAME" "$RESTORE_CONTAINER_NAME")"
    log_info "  restore: container=${RESTORE_CDL}s model_dl=${RESTORE_MDL}s init=${RESTORE_INIT}s total=${RESTORE_TOTAL}s | load=${RESTORE_LOAD}s dlw=${RESTORE_DLW}s ${RESTORE_GBPS} GiB/s"
fi

# ─── Emit PDF table row ───────────────────────────────────────────────────────
RESULTS="$PROJECT_ROOT/docs/PDF-BENCH-RESULTS.md"
if [ ! -f "$RESULTS" ]; then
    cat > "$RESULTS" <<EOF
# PDF Testing Matrix — NvSnap Benchmark Results

Generated by \`scripts/test-bench.sh\`. Each row follows the column layout
in \`NVCFCheckpoint_RestoreBenchmarks.pdf\` (Methodology section).

Cloud and Storage are derived per run from the cluster provider ID
and the L2 StorageClass provisioner.
NvSnap agent: see scripts/versions.sh at run time
Hardware: 1x H100 per row unless noted

| Cloud | Storage | Hardware | Model | Cold: Container DL (s) | Cold: Model DL (s) | Cold: Model Init (s) | Cold: Total (s) | Warm: Container DL (s) | Warm: Model DL (s) | Warm: Model Init (s) | Warm: Total (s) | Snapshot (s) | Restore: Container DL (s) | Restore: Model DL (s) | Restore: Model Init (s) | Restore: Total (s) | Cold: Wt-Load (s) | Cold: Load (GiB/s) | Cold: HF-DL (s) | Warm: Wt-Load (s) | Warm: Load (GiB/s) | Warm: HF-DL (s) | Restore: Wt-Load (s) | Restore: Load (GiB/s) | Restore: HF-DL (s) |
|-------|---------|----------|-------|------------------------|--------------------|----------------------|-----------------|------------------------|--------------------|----------------------|-----------------|--------------|---------------------------|-----------------------|-------------------------|--------------------|-------------------|--------------------|-----------------|-------------------|--------------------|-----------------|----------------------|-----------------------|--------------------|
EOF
fi
# Cloud and Storage are derived, not hardcoded. They were fixed at
# "GCP | Hyperdisk-ML", so every row claimed that regardless of where it ran --
# an NVMesh-on-AWS run was recorded as Hyperdisk-ML, making the storage
# comparison these rows exist for impossible to read.
BENCH_STORAGE="${BENCH_STORAGE:-}"
if [ -z "$BENCH_STORAGE" ]; then
    _prov=$(kubectl get sc "$L2_STORAGECLASS" -o jsonpath='{.provisioner}' 2>/dev/null || true)
    case "$_prov" in
        nvmesh-csi.excelero.com)      BENCH_STORAGE="NVMesh" ;;
        pd.csi.storage.gke.io)
            _t=$(kubectl get sc "$L2_STORAGECLASS" -o jsonpath='{.parameters.type}' 2>/dev/null || true)
            case "$_t" in hyperdisk-ml) BENCH_STORAGE="Hyperdisk-ML" ;; *) BENCH_STORAGE="PD-${_t:-unknown}" ;; esac ;;
        ebs.csi.aws.com)              BENCH_STORAGE="EBS" ;;
        efs.csi.aws.com)              BENCH_STORAGE="EFS" ;;
        filestore.csi.storage.gke.io) BENCH_STORAGE="Filestore" ;;
        "")                           BENCH_STORAGE="none" ;;
        *)                            BENCH_STORAGE="$_prov" ;;
    esac
fi
BENCH_CLOUD="${BENCH_CLOUD:-}"
if [ -z "$BENCH_CLOUD" ]; then
    _pid=$(kubectl get nodes -o jsonpath='{.items[0].spec.providerID}' 2>/dev/null || true)
    case "$_pid" in
        aws://*) BENCH_CLOUD="AWS" ;; gce://*) BENCH_CLOUD="GCP" ;;
        azure://*) BENCH_CLOUD="Azure" ;; *) BENCH_CLOUD="unknown" ;;
    esac
fi

# Defaults so skipped phases don't emit blank cells (storage/loader columns).
: "${COLD_LOAD:=0}" "${COLD_GBPS:=0}" "${COLD_DLW:=0}"
: "${WARM_LOAD:=0}" "${WARM_GBPS:=0}" "${WARM_DLW:=0}"
: "${RESTORE_LOAD:=0}" "${RESTORE_GBPS:=0}" "${RESTORE_DLW:=0}"
echo "| $BENCH_CLOUD | $BENCH_STORAGE | $HARDWARE | $MODEL | $COLD_CDL | $COLD_MDL | $COLD_INIT | $COLD_TOTAL | $WARM_CDL | $WARM_MDL | $WARM_INIT | $WARM_TOTAL | $SNAPSHOT_TIME | $RESTORE_CDL | $RESTORE_MDL | $RESTORE_INIT | $RESTORE_TOTAL | $COLD_LOAD | $COLD_GBPS | $COLD_DLW | $WARM_LOAD | $WARM_GBPS | $WARM_DLW | $RESTORE_LOAD | $RESTORE_GBPS | $RESTORE_DLW |" >> "$RESULTS"

log_info "=================================="
log_info "PDF bench complete: $WORKLOAD"
log_info "  Row appended to: $RESULTS"
log_info "=================================="
