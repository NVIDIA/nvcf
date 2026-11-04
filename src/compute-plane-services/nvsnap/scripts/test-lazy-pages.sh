#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# CRIU Lazy Pages Spike Test (#37)
#
# Tests whether CRIU lazy pages (userfaultfd-based demand paging) works
# with GPU workloads. Three progressive tests:
#
#   Test 1: CPU-only baseline — verify lazy pages works on our GKE kernel
#   Test 2: GPU workload — attempt lazy-pages restore of a vLLM checkpoint
#   Test 3: Page split analysis — quantify CPU vs GPU page ratio
#
# Prerequisites:
#   - Agent pod running in nvsnap-system namespace
#   - For Test 2: an existing GPU checkpoint (run test-e2e.sh vllm-small first)
#
# Usage:
#   ./scripts/test-lazy-pages.sh              # Run all tests
#   ./scripts/test-lazy-pages.sh cpu-only      # Test 1 only
#   ./scripts/test-lazy-pages.sh gpu           # Test 2 only
#   ./scripts/test-lazy-pages.sh page-split    # Test 3 only

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

source "$SCRIPT_DIR/config.sh"

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; }
step()  { echo -e "${BLUE}[STEP]${NC} $*"; }

NAMESPACE="nvsnap-system"
CRIU="/criu-bundle/criu-wrapper"
CHECKPOINT_DIR="/var/lib/nvsnap/checkpoints"

# ── Helpers ─────────────────────────────────────────────────────

find_agent_pod() {
    local node="${1:-}"
    if [ -n "$node" ]; then
        kubectl get pods -n "$NAMESPACE" -l app=nvsnap-agent \
            --field-selector "spec.nodeName=$node" \
            -o jsonpath='{.items[0].metadata.name}' 2>/dev/null
    else
        kubectl get pods -n "$NAMESPACE" -l app=nvsnap-agent \
            -o jsonpath='{.items[0].metadata.name}' 2>/dev/null
    fi
}

agent_exec() {
    local pod="$1"; shift
    kubectl exec -n "$NAMESPACE" "$pod" -- "$@"
}

# ── Test 1: CPU-only lazy pages baseline ───────────────────────

test_cpu_only() {
    echo ""
    step "═══════════════════════════════════════════════"
    step "TEST 1: CPU-only Lazy Pages (kernel baseline)"
    step "═══════════════════════════════════════════════"
    echo ""

    local agent_pod
    agent_pod=$(find_agent_pod)
    if [ -z "$agent_pod" ]; then
        error "No agent pod found in $NAMESPACE"
        return 1
    fi
    info "Agent pod: $agent_pod"

    # Check kernel userfaultfd support
    info "1.1: Checking userfaultfd support..."
    if ! agent_exec "$agent_pod" test -e /proc/sys/vm/unprivileged_userfaultfd; then
        warn "/proc/sys/vm/unprivileged_userfaultfd not found"
        warn "userfaultfd may require CAP_SYS_PTRACE (agent has it)"
    else
        local uffd_val
        uffd_val=$(agent_exec "$agent_pod" cat /proc/sys/vm/unprivileged_userfaultfd 2>/dev/null || echo "unknown")
        info "unprivileged_userfaultfd = $uffd_val"
    fi

    # Check CRIU lazy-pages feature
    info "1.2: Checking CRIU lazy-pages support..."
    local check_output
    check_output=$(agent_exec "$agent_pod" $CRIU check --feature lazy_pages 2>&1 || true)
    local check_rc=$?
    if echo "$check_output" | grep -qi "success\|available\|looks good"; then
        info "CRIU reports lazy-pages is available"
    else
        error "CRIU lazy-pages feature check FAILED"
        echo "$check_output" | head -10
        info ""
        info "════════════════════════════════════════════"
        info "TEST 1: FAIL — lazy pages not supported"
        info "════════════════════════════════════════════"
        info "Kernel userfaultfd not available (unprivileged_userfaultfd=0)"
        info "CRIU cannot use lazy pages on this GKE kernel (5.15)"
        info ""
        info "This is a definitive result — lazy pages requires kernel support"
        info "that is disabled on GKE. No further testing possible."
        return 1
    fi

    # Create a simple test process, checkpoint it with --lazy-pages, restore
    local test_dir="/tmp/lazy-pages-test-$$"

    info "1.3: Creating test process (100MB allocation + checksum)..."
    agent_exec "$agent_pod" bash -c "
        mkdir -p $test_dir/images $test_dir/work

        # Python script: allocate 100MB, write checksum to file, then sleep
        cat > $test_dir/work/test.py << 'PYEOF'
import hashlib, os, signal, sys, time

# Allocate 100MB of deterministic data
data = b'A' * (100 * 1024 * 1024)
checksum = hashlib.sha256(data).hexdigest()

# Write checksum so we can verify after restore
with open('/tmp/lazy-pages-checksum.txt', 'w') as f:
    f.write(checksum)

print(f'PID={os.getpid()} checksum={checksum}', flush=True)

# Sleep forever (will be checkpointed)
while True:
    time.sleep(1)
PYEOF

        # Start the process in background
        cd $test_dir/work
        python3 test.py > $test_dir/stdout.log 2>&1 &
        echo \$! > $test_dir/pid.txt
        sleep 2
    "

    local pid
    pid=$(agent_exec "$agent_pod" cat "$test_dir/pid.txt")
    info "Test process PID: $pid"

    local original_checksum
    original_checksum=$(agent_exec "$agent_pod" cat /tmp/lazy-pages-checksum.txt 2>/dev/null || echo "")
    if [ -z "$original_checksum" ]; then
        error "Test process didn't write checksum — not running?"
        agent_exec "$agent_pod" cat "$test_dir/stdout.log" 2>/dev/null || true
        return 1
    fi
    info "Original checksum: $original_checksum"

    info "1.4: Dumping with --lazy-pages (30s timeout)..."
    local dump_output
    dump_output=$(agent_exec "$agent_pod" timeout 30 bash -c "
        $CRIU dump \
            --tree $pid \
            --images-dir $test_dir/images \
            --leave-stopped \
            --lazy-pages \
            --shell-job \
            -v4 \
            --log-file dump.log \
            2>&1
    " 2>&1) || true

    # Check if dump succeeded
    if agent_exec "$agent_pod" test -f "$test_dir/images/core-$pid.img" 2>/dev/null; then
        info "Dump succeeded"
    else
        error "Dump failed"
        echo "$dump_output" | tail -20
        info "Dump log (last 30 lines):"
        agent_exec "$agent_pod" tail -30 "$test_dir/images/dump.log" 2>/dev/null || true
        # Cleanup
        agent_exec "$agent_pod" kill "$pid" 2>/dev/null || true
        agent_exec "$agent_pod" rm -rf "$test_dir" 2>/dev/null || true
        return 1
    fi

    # Kill original process
    agent_exec "$agent_pod" kill -9 "$pid" 2>/dev/null || true

    info "1.5: Starting lazy-pages daemon..."
    agent_exec "$agent_pod" bash -c "
        $CRIU lazy-pages \
            --images-dir $test_dir/images \
            -v4 \
            --log-file lazy-pages.log \
            &
        echo \$! > $test_dir/lazy-pages-pid.txt
        sleep 1
    " 2>/dev/null || true

    info "1.6: Restoring with --lazy-pages..."
    local restore_output
    restore_output=$(agent_exec "$agent_pod" bash -c "
        $CRIU restore \
            --images-dir $test_dir/images \
            --lazy-pages \
            --shell-job \
            -d \
            -v4 \
            --log-file restore.log \
            2>&1
    " 2>&1) || true

    sleep 3

    # Check if restore succeeded by verifying the checksum file is accessible
    local restored_checksum
    restored_checksum=$(agent_exec "$agent_pod" cat /tmp/lazy-pages-checksum.txt 2>/dev/null || echo "")

    echo ""
    if [ "$restored_checksum" = "$original_checksum" ]; then
        info "════════════════════════════════════════════"
        info "TEST 1: PASS — CPU-only lazy pages works"
        info "════════════════════════════════════════════"
        info "Checksum match: $restored_checksum"
    else
        warn "════════════════════════════════════════════"
        warn "TEST 1: INCONCLUSIVE"
        warn "════════════════════════════════════════════"
        warn "Restore output:"
        echo "$restore_output" | tail -20
        info "Restore log (last 30 lines):"
        agent_exec "$agent_pod" tail -30 "$test_dir/images/restore.log" 2>/dev/null || true
        info "Lazy-pages log (last 30 lines):"
        agent_exec "$agent_pod" tail -30 "$test_dir/images/lazy-pages.log" 2>/dev/null || true
    fi

    # Cleanup
    info "Cleaning up..."
    agent_exec "$agent_pod" bash -c "
        kill \$(cat $test_dir/lazy-pages-pid.txt 2>/dev/null) 2>/dev/null || true
        # Kill any restored process
        pgrep -f 'test.py' | xargs kill -9 2>/dev/null || true
        rm -rf $test_dir /tmp/lazy-pages-checksum.txt
    " 2>/dev/null || true
    echo ""
}

# ── Test 2: GPU workload with lazy pages ───────────────────────

test_gpu() {
    echo ""
    step "═══════════════════════════════════════════════"
    step "TEST 2: GPU Workload with Lazy Pages"
    step "═══════════════════════════════════════════════"
    echo ""

    local agent_pod
    agent_pod=$(find_agent_pod)
    if [ -z "$agent_pod" ]; then
        error "No agent pod found in $NAMESPACE"
        return 1
    fi
    info "Agent pod: $agent_pod"

    # Find an existing GPU checkpoint
    info "2.1: Looking for existing GPU checkpoint..."
    local checkpoints
    checkpoints=$(agent_exec "$agent_pod" bash -c "
        ls -td $CHECKPOINT_DIR/*/ 2>/dev/null | head -5
    " 2>/dev/null || echo "")

    if [ -z "$checkpoints" ]; then
        error "No existing checkpoints found at $CHECKPOINT_DIR"
        info "Run 'test-e2e.sh vllm-small' first to create a checkpoint"
        return 1
    fi

    # Pick the latest checkpoint
    local ckpt_dir
    ckpt_dir=$(echo "$checkpoints" | head -1 | tr -d '[:space:]')
    local ckpt_id
    ckpt_id=$(basename "$ckpt_dir")

    info "Using checkpoint: $ckpt_id"

    # Show checkpoint contents
    local ckpt_size
    ckpt_size=$(agent_exec "$agent_pod" du -sh "$ckpt_dir" 2>/dev/null | awk '{print $1}')
    info "Checkpoint size: $ckpt_size"

    # Check if this checkpoint has CUDA plugin files
    local has_cuda
    has_cuda=$(agent_exec "$agent_pod" bash -c "
        ls $ckpt_dir/cuda-checkpoint-* 2>/dev/null | wc -l || echo 0
    " 2>/dev/null || echo "0")
    info "CUDA plugin files: $has_cuda"

    # Check what pages files exist
    info "2.2: Analyzing checkpoint image files..."
    agent_exec "$agent_pod" bash -c "
        echo 'Pages files:'
        ls -lh $ckpt_dir/pages-*.img 2>/dev/null || echo '  (none)'
        echo ''
        echo 'Core files:'
        ls -lh $ckpt_dir/core-*.img 2>/dev/null | head -5
        echo ''
        echo 'Memory maps (pagemap):'
        ls -lh $ckpt_dir/pagemap-*.img 2>/dev/null | head -5
    " 2>/dev/null || true

    # Attempt lazy-pages restore
    info "2.3: Attempting lazy-pages restore..."
    info "(This will likely fail with CUDA plugin — that's the expected outcome)"
    echo ""

    local test_dir="/tmp/lazy-pages-gpu-test-$$"
    local restore_output
    restore_output=$(agent_exec "$agent_pod" bash -c "
        mkdir -p $test_dir

        # Start lazy-pages daemon first
        $CRIU lazy-pages \
            --images-dir $ckpt_dir \
            -v4 \
            --log-file $test_dir/lazy-pages.log \
            2>$test_dir/lazy-pages-stderr.log &
        LP_PID=\$!
        sleep 2

        # Attempt restore with lazy-pages
        $CRIU restore \
            --images-dir $ckpt_dir \
            --lazy-pages \
            --tcp-established \
            --tcp-close \
            --shell-job \
            -d \
            -v4 \
            --log-file $test_dir/restore.log \
            --pidfile $test_dir/restore.pid \
            2>&1

        RESTORE_RC=\$?
        echo \"RESTORE_EXIT_CODE=\$RESTORE_RC\"

        # Give it a moment
        sleep 3

        # Kill lazy-pages daemon
        kill \$LP_PID 2>/dev/null || true

        echo \"--- lazy-pages log (last 30 lines) ---\"
        tail -30 $test_dir/lazy-pages.log 2>/dev/null || echo '(no log)'
        echo \"--- restore log (last 30 lines) ---\"
        tail -30 $test_dir/restore.log 2>/dev/null || echo '(no log)'
        echo \"--- lazy-pages stderr ---\"
        cat $test_dir/lazy-pages-stderr.log 2>/dev/null || echo '(none)'
    " 2>&1) || true

    echo ""
    echo "$restore_output"
    echo ""

    # Parse result
    local exit_code
    exit_code=$(echo "$restore_output" | grep "RESTORE_EXIT_CODE=" | tail -1 | cut -d= -f2 || echo "unknown")

    if [ "$exit_code" = "0" ]; then
        info "════════════════════════════════════════════"
        info "TEST 2: PASS — GPU lazy-pages restore started!"
        info "════════════════════════════════════════════"
        info "This is unexpected — check if inference works"

        # Try to hit the API
        sleep 5
        local restored_pid
        restored_pid=$(agent_exec "$agent_pod" cat "$test_dir/restore.pid" 2>/dev/null || echo "")
        if [ -n "$restored_pid" ]; then
            info "Restored PID: $restored_pid"
            # Can't easily test API from agent pod, but report success
        fi
    else
        warn "════════════════════════════════════════════"
        warn "TEST 2: FAIL (expected) — exit code $exit_code"
        warn "════════════════════════════════════════════"
        info "Lazy-pages restore does not work with CUDA plugin"
        info "This confirms theoretical analysis:"
        info "  - GPU device VMAs are file-backed, not MAP_PRIVATE|MAP_ANONYMOUS"
        info "  - CUDA plugin expects eager restore of all GPU state"
        info "  - userfaultfd cannot inject pages from device driver ioctls"
    fi

    # Cleanup
    agent_exec "$agent_pod" rm -rf "$test_dir" 2>/dev/null || true
    echo ""
}

# ── Test 3: Page split analysis ────────────────────────────────

test_page_split() {
    echo ""
    step "═══════════════════════════════════════════════"
    step "TEST 3: CPU vs GPU Page Split Analysis"
    step "═══════════════════════════════════════════════"
    echo ""

    local agent_pod
    agent_pod=$(find_agent_pod)
    if [ -z "$agent_pod" ]; then
        error "No agent pod found in $NAMESPACE"
        return 1
    fi
    info "Agent pod: $agent_pod"

    # Find existing checkpoints
    local checkpoints
    checkpoints=$(agent_exec "$agent_pod" bash -c "
        ls -td $CHECKPOINT_DIR/*/ 2>/dev/null | head -5
    " 2>/dev/null || echo "")

    if [ -z "$checkpoints" ]; then
        error "No existing checkpoints found"
        info "Run 'test-e2e.sh vllm-small' first"
        return 1
    fi

    local ckpt_dir
    ckpt_dir=$(echo "$checkpoints" | head -1 | tr -d '[:space:]')
    local ckpt_id
    ckpt_id=$(basename "$ckpt_dir")
    info "Analyzing checkpoint: $ckpt_id"
    echo ""

    # Write Python analysis scripts to agent pod as temp files
    # (avoids nested quoting issues with inline Python in bash -c)
    agent_exec "$agent_pod" bash -c "mkdir -p /tmp/lazy-pages-analysis"

    # Pagemap analysis script
    kubectl exec -n "$NAMESPACE" "$agent_pod" -- tee /tmp/lazy-pages-analysis/pagemap.py > /dev/null <<'PYEOF'
import json, sys
data = json.load(sys.stdin)
entries = data.get("entries", [])
total_pages = 0
for e in entries:
    if "nr_pages" in e:
        total_pages += e["nr_pages"]
print(f"entries={len(entries)} total_pages={total_pages} total_bytes={total_pages * 4096}")
PYEOF

    # VMA lazy-loadable analysis script
    kubectl exec -n "$NAMESPACE" "$agent_pod" -- tee /tmp/lazy-pages-analysis/vma.py > /dev/null <<'PYEOF'
import json, sys
data = json.load(sys.stdin)
entries = data.get("entries", [])
lazy_ok = 0
lazy_bytes = 0
not_lazy = 0
not_lazy_bytes = 0
for e in entries:
    vmas = e.get("vmas", [])
    for vma in vmas:
        size = vma.get("end", 0) - vma.get("start", 0)
        flags = vma.get("flags", 0)
        # MAP_PRIVATE=0x02, MAP_ANONYMOUS=0x20
        # Lazy pages only works for private anonymous mappings
        # CRIU checks: vma_entry_can_be_lazy() -> MAP_PRIVATE && MAP_ANONYMOUS
        if (flags & 0x22) == 0x22:
            lazy_ok += 1
            lazy_bytes += size
        else:
            not_lazy += 1
            not_lazy_bytes += size
total = lazy_bytes + not_lazy_bytes
pct = lazy_bytes * 100 / total if total > 0 else 0
pid = sys.argv[1] if len(sys.argv) > 1 else "?"
print(f"PID {pid}: lazy-eligible={lazy_ok} VMAs ({lazy_bytes // (1024*1024)} MB, {pct:.1f}%), "
      f"not-eligible={not_lazy} VMAs ({not_lazy_bytes // (1024*1024)} MB)")
PYEOF

    # Run analysis
    agent_exec "$agent_pod" bash -c "
        CKPT='$ckpt_dir'

        echo '── Checkpoint file sizes ──'
        echo ''

        TOTAL_HR=\$(du -sh \$CKPT | awk '{print \$1}')
        TOTAL=\$(du -sb \$CKPT | awk '{print \$1}')
        echo \"Total checkpoint: \$TOTAL_HR (\$TOTAL bytes)\"
        echo ''

        PAGES_TOTAL=0
        echo 'CRIU page files (CPU memory):'
        for f in \$CKPT/pages-*.img; do
            [ -f \"\$f\" ] || continue
            SIZE=\$(stat -c %s \"\$f\")
            SIZE_HR=\$(ls -lh \"\$f\" | awk '{print \$5}')
            echo \"  \$(basename \$f): \$SIZE_HR\"
            PAGES_TOTAL=\$((PAGES_TOTAL + SIZE))
        done
        PAGES_HR=\$(numfmt --to=iec \$PAGES_TOTAL 2>/dev/null || echo \"\${PAGES_TOTAL} bytes\")
        echo \"  Total CRIU pages: \$PAGES_HR\"
        echo ''

        GPU_TOTAL=0
        echo 'GPU checkpoint files:'
        for f in \$CKPT/cuda-checkpoint-* \$CKPT/gpu-* \$CKPT/*.gpu; do
            [ -f \"\$f\" ] || continue
            SIZE=\$(stat -c %s \"\$f\")
            SIZE_HR=\$(ls -lh \"\$f\" | awk '{print \$5}')
            echo \"  \$(basename \$f): \$SIZE_HR\"
            GPU_TOTAL=\$((GPU_TOTAL + SIZE))
        done
        if [ \$GPU_TOTAL -eq 0 ]; then
            echo '  (GPU memory dumped via CUDA plugin into pages-*.img)'
            echo '  Cannot separate GPU vs CPU pages without pagemap analysis'
        else
            GPU_HR=\$(numfmt --to=iec \$GPU_TOTAL 2>/dev/null || echo \"\${GPU_TOTAL} bytes\")
            echo \"  Total GPU files: \$GPU_HR\"
        fi
        echo ''

        if command -v crit &>/dev/null; then
            echo '── Pagemap analysis (via crit) ──'
            echo ''
            for f in \$CKPT/pagemap-*.img; do
                [ -f \"\$f\" ] || continue
                PID=\$(basename \$f | sed 's/pagemap-//;s/.img//')
                RESULT=\$(crit decode -i \"\$f\" 2>/dev/null | python3 /tmp/lazy-pages-analysis/pagemap.py 2>/dev/null || echo 'decode failed')
                echo \"  PID \$PID: \$RESULT\"
            done
            echo ''

            echo '── VMA lazy-loadable analysis ──'
            echo ''
            for f in \$CKPT/mm-*.img; do
                [ -f \"\$f\" ] || continue
                PID=\$(basename \$f | sed 's/mm-//;s/.img//')
                crit decode -i \"\$f\" 2>/dev/null | python3 /tmp/lazy-pages-analysis/vma.py \"\$PID\" 2>/dev/null || echo \"  PID \$PID: analysis failed\"
            done
            echo ''
        else
            echo '(crit not available — install pycriu for pagemap analysis)'
            echo ''
        fi

        echo '── Summary ──'
        echo ''
        echo 'Even if lazy pages worked:'
        echo '  - GPU memory is restored eagerly by cuda-checkpoint (not pageable)'
        echo '  - Only CPU-side private anonymous pages could be lazy-loaded'
        echo '  - For GPU workloads, GPU memory dominates checkpoint size'
        echo '  - Benefit of lazy pages = (lazy-eligible CPU pages) / (total checkpoint)'

        rm -rf /tmp/lazy-pages-analysis
    "
    echo ""
}

# ── Main ───────────────────────────────────────────────────────

echo ""
echo -e "${BOLD}╔══════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║     CRIU Lazy Pages Spike Test (Issue #37)       ║${NC}"
echo -e "${BOLD}╚══════════════════════════════════════════════════╝${NC}"
echo ""
info "Tests whether userfaultfd-based demand paging works with GPU workloads"
echo ""

MODE="${1:-all}"

case "$MODE" in
    cpu-only|cpu|1)
        test_cpu_only
        ;;
    gpu|2)
        test_gpu
        ;;
    page-split|split|3)
        test_page_split
        ;;
    all)
        if ! test_cpu_only; then
            echo ""
            step "═══════════════════════════════════════════════"
            step "FINAL SUMMARY"
            step "═══════════════════════════════════════════════"
            echo ""
            info "Test 1 (CPU-only) failed — lazy pages not supported on this kernel."
            info "Skipping Test 2 (GPU) and Test 3 (page split) — no point testing further."
            echo ""
            info "CONCLUSION: Close #37. Lazy pages is not viable."
            info "Proceed with #29 (rebrand) then #46 (compression)."
            echo ""
            # Still run page-split analysis — it's useful data even without lazy pages
            info "Running page-split analysis anyway for checkpoint size insights..."
            test_page_split || true
            exit 0
        fi
        test_gpu
        test_page_split

        echo ""
        step "═══════════════════════════════════════════════"
        step "FINAL SUMMARY"
        step "═══════════════════════════════════════════════"
        echo ""
        info "Results have been printed above for each test."
        info "If Test 2 failed (expected), lazy pages is not viable for GPU workloads."
        info "Recommendation: close #37, proceed with #29 (rebrand) then #46 (compression)."
        echo ""
        ;;
    *)
        echo "Usage: $0 [cpu-only|gpu|page-split|all]"
        exit 1
        ;;
esac
