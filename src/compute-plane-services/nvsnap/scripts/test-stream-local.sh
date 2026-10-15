#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Local test for CRIU image streaming (no K8s, no GPU)
# Tests: checkpoint a sleep process with streaming → verify stream.lz4 → restore from it
#
# Usage: sudo ./scripts/test-stream-local.sh
#
# Requirements:
#   - CRIU binary (from agent image or local build)
#   - criu-image-streamer binary
#   - lz4 binary

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Binaries — try agent bundle first, then system PATH
CRIU="${CRIU:?CRIU must be set: path to a built criu binary, e.g. ../criu/criu/criu}"
STREAMER="${STREAMER:?STREAMER must be set: path to criu-image-streamer binary, e.g. ../criu-image-streamer/target/release/criu-image-streamer}"
LZ4="${LZ4:-$(which lz4 2>/dev/null || echo /usr/bin/lz4)}"

GREEN='\033[0;32m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m'

log_ok()   { echo -e "${GREEN}[OK]${NC} $*"; }
log_fail() { echo -e "${RED}[FAIL]${NC} $*"; }
log_info() { echo -e "${CYAN}[INFO]${NC} $*"; }

cleanup() {
    log_info "Cleaning up..."
    # Kill test process
    if [ -n "${TEST_PID:-}" ] && kill -0 "$TEST_PID" 2>/dev/null; then
        kill "$TEST_PID" 2>/dev/null || true
    fi
    # Kill streamer/lz4 if still running
    for pid in "${STREAMER_PID:-}" "${LZ4_PID:-}"; do
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null || true
        fi
    done
    # Remove temp dirs
    if [ -n "${WORK_DIR:-}" ] && [ -d "$WORK_DIR" ]; then
        rm -rf "$WORK_DIR"
    fi
}
trap cleanup EXIT

# --- Preflight checks ---

log_info "=== CRIU Streaming Local Test ==="
echo ""

if [ "$(id -u)" -ne 0 ]; then
    echo "This test requires root (CRIU needs ptrace). Run with sudo."
    exit 1
fi

for bin in "$CRIU" "$STREAMER" "$LZ4"; do
    if [ ! -x "$bin" ]; then
        log_fail "Binary not found or not executable: $bin"
        exit 1
    fi
done

log_ok "CRIU: $CRIU"
log_ok "Streamer: $STREAMER"
log_ok "lz4: $LZ4"
echo ""

# --- Setup ---

WORK_DIR=$(mktemp -d /tmp/criu-stream-test-XXXXXX)
DUMP_DIR="$WORK_DIR/dump"
RESTORE_DIR="$WORK_DIR/restore"
mkdir -p "$DUMP_DIR" "$RESTORE_DIR"

log_info "Work dir: $WORK_DIR"

# --- Step 1: Start test process ---

log_info "Step 1: Starting test process..."
sleep 999 &
TEST_PID=$!
log_ok "Test process PID: $TEST_PID"

# --- Step 2: Checkpoint WITHOUT streaming (baseline) ---

log_info "Step 2: Checkpoint WITHOUT streaming (baseline)..."
BASELINE_DIR="$WORK_DIR/baseline"
mkdir -p "$BASELINE_DIR"

BASELINE_START=$(date +%s%N)
if "$CRIU" dump -t "$TEST_PID" -D "$BASELINE_DIR" --shell-job -v0 --log-file dump.log 2>/dev/null; then
    BASELINE_END=$(date +%s%N)
    BASELINE_MS=$(( (BASELINE_END - BASELINE_START) / 1000000 ))
    BASELINE_SIZE=$(du -sh "$BASELINE_DIR" | awk '{print $1}')
    log_ok "Baseline checkpoint: ${BASELINE_SIZE} in ${BASELINE_MS}ms"
else
    log_fail "Baseline checkpoint failed"
    cat "$BASELINE_DIR/dump.log" 2>/dev/null | tail -20
    exit 1
fi

# --- Step 3: Restore from baseline (to get process back) ---

log_info "Step 3: Restoring from baseline..."
if "$CRIU" restore -D "$BASELINE_DIR" --shell-job -d -v0 --log-file restore.log 2>/dev/null; then
    # Get new PID
    sleep 1
    TEST_PID=$(pgrep -f "sleep 999" | head -1 || true)
    if [ -n "$TEST_PID" ]; then
        log_ok "Restored, new PID: $TEST_PID"
    else
        log_fail "Process not found after restore"
        exit 1
    fi
else
    log_fail "Baseline restore failed"
    cat "$BASELINE_DIR/restore.log" 2>/dev/null | tail -20
    exit 1
fi

# --- Step 4: Checkpoint WITH streaming ---

log_info "Step 4: Checkpoint WITH streaming..."

# Start streamer pipeline: criu-image-streamer capture | lz4 > stream.lz4
"$STREAMER" --images-dir "$DUMP_DIR" capture | "$LZ4" -1 - "$DUMP_DIR/stream.lz4" &
STREAMER_PID=$!

# Wait for capture socket
SOCKET="$DUMP_DIR/streamer-capture.sock"
DEADLINE=$((SECONDS + 10))
while [ ! -S "$SOCKET" ] && [ $SECONDS -lt $DEADLINE ]; do
    sleep 0.1
done

if [ ! -S "$SOCKET" ]; then
    log_fail "Streamer socket did not appear: $SOCKET"
    exit 1
fi
log_ok "Streamer socket ready: $SOCKET"

# Run CRIU dump with --stream
STREAM_START=$(date +%s%N)
if "$CRIU" dump -t "$TEST_PID" -D "$DUMP_DIR" --shell-job --stream -v0 --log-file dump.log 2>/dev/null; then
    # Wait for streamer to finish
    wait "$STREAMER_PID" 2>/dev/null || true
    STREAMER_PID=""
    STREAM_END=$(date +%s%N)
    STREAM_MS=$(( (STREAM_END - STREAM_START) / 1000000 ))

    STREAM_SIZE=$(ls -lh "$DUMP_DIR/stream.lz4" 2>/dev/null | awk '{print $5}')
    if [ -s "$DUMP_DIR/stream.lz4" ]; then
        log_ok "Streaming checkpoint: stream.lz4 = ${STREAM_SIZE} in ${STREAM_MS}ms"
    else
        log_fail "stream.lz4 is empty!"
        ls -la "$DUMP_DIR/"
        exit 1
    fi
else
    log_fail "Streaming checkpoint failed"
    cat "$DUMP_DIR/dump.log" 2>/dev/null | tail -20
    # Check if streamer had errors
    wait "$STREAMER_PID" 2>/dev/null || true
    STREAMER_PID=""
    exit 1
fi

# Verify no individual .img page files (streaming should capture everything)
PAGE_FILES=$(ls "$DUMP_DIR"/pages-*.img 2>/dev/null | wc -l || echo 0)
log_info "Individual page files in dump dir: $PAGE_FILES (should be 0 with streaming)"

# --- Step 5: Restore WITH streaming ---

log_info "Step 5: Restoring from stream.lz4..."

# Start streamer pipeline: lz4 -d stream.lz4 | criu-image-streamer serve
"$LZ4" -d "$DUMP_DIR/stream.lz4" -c | "$STREAMER" --images-dir "$DUMP_DIR" serve &
STREAMER_PID=$!

# Wait for serve socket
SOCKET="$DUMP_DIR/streamer-serve.sock"
DEADLINE=$((SECONDS + 10))
while [ ! -S "$SOCKET" ] && [ $SECONDS -lt $DEADLINE ]; do
    sleep 0.1
done

if [ ! -S "$SOCKET" ]; then
    log_fail "Streamer serve socket did not appear: $SOCKET"
    exit 1
fi
log_ok "Streamer serve socket ready"

RESTORE_START=$(date +%s%N)
if "$CRIU" restore -D "$DUMP_DIR" --shell-job -d --stream -v0 --log-file restore.log 2>/dev/null; then
    wait "$STREAMER_PID" 2>/dev/null || true
    STREAMER_PID=""
    RESTORE_END=$(date +%s%N)
    RESTORE_MS=$(( (RESTORE_END - RESTORE_START) / 1000000 ))

    sleep 1
    TEST_PID=$(pgrep -f "sleep 999" | head -1 || true)
    if [ -n "$TEST_PID" ]; then
        log_ok "Streaming restore: ${RESTORE_MS}ms, PID: $TEST_PID"
    else
        log_fail "Process not found after streaming restore"
        exit 1
    fi
else
    log_fail "Streaming restore failed"
    cat "$DUMP_DIR/restore.log" 2>/dev/null | tail -20
    wait "$STREAMER_PID" 2>/dev/null || true
    STREAMER_PID=""
    exit 1
fi

# --- Summary ---

echo ""
log_info "=== Results ==="
echo ""
printf "%-25s %s\n" "Baseline checkpoint:" "${BASELINE_SIZE} in ${BASELINE_MS}ms"
printf "%-25s %s\n" "Streaming checkpoint:" "${STREAM_SIZE} in ${STREAM_MS}ms"
printf "%-25s %s\n" "Streaming restore:" "${RESTORE_MS}ms"
printf "%-25s %s\n" "Page files in dump:" "$PAGE_FILES"
echo ""
log_ok "=== ALL TESTS PASSED ==="
