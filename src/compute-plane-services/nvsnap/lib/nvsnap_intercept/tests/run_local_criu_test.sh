#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

#
# Run this script from a terminal OUTSIDE Cursor to avoid inherited FDs
#
# Usage: ./run_local_criu_test.sh
#

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$(dirname "$(dirname "$SCRIPT_DIR")")")"
CRIU="$PROJECT_ROOT/bin/criu"
LIB="$PROJECT_ROOT/lib/nvsnap_intercept/libnvsnap_intercept.so"

CKPT_DIR="/tmp/nvsnap-local-test-$$"
mkdir -p "$CKPT_DIR"

cleanup() {
    pkill -f "local_test_app" 2>/dev/null || true
    rm -rf "$CKPT_DIR" /tmp/local_test_app.py /tmp/local_test_pid 2>/dev/null || true
}
trap cleanup EXIT

# Build intercept library if needed
if [ ! -f "$LIB" ]; then
    echo "Building intercept library..."
    cd "$PROJECT_ROOT/lib/nvsnap_intercept" && make
fi

cat > /tmp/local_test_app.py << 'PYTHON'
#!/usr/bin/env python3
import os, sys, time, signal

counter = 0
restored = os.environ.get("NVSNAP_RESTORED") == "1"

def handler(sig, frame):
    print(f"[APP] Signal {sig}, counter={counter}")
    sys.stdout.flush()

signal.signal(signal.SIGUSR1, handler)
signal.signal(signal.SIGUSR2, handler)

with open("/tmp/local_test_pid", "w") as f:
    f.write(str(os.getpid()))

print(f"[APP] Started PID={os.getpid()} restored={restored}")
sys.stdout.flush()

while counter < 100:
    counter += 1
    if counter % 10 == 0:
        print(f"[APP] counter={counter}")
        sys.stdout.flush()
    time.sleep(0.3)

print("[APP] Finished")
PYTHON
chmod +x /tmp/local_test_app.py

echo "========================================"
echo "  LOCAL CHECKPOINT/RESTORE TEST"
echo "========================================"
echo ""
echo "CRIU: $CRIU"
echo "LIB:  $LIB"
echo "CKPT: $CKPT_DIR"
echo ""

echo "=== Step 1: Start application ==="
NVSNAP_LOG_LEVEL=3 LD_PRELOAD="$LIB" python3 /tmp/local_test_app.py &
APP_PID=$!
sleep 2

if [ -f /tmp/local_test_pid ]; then
    APP_PID=$(cat /tmp/local_test_pid)
fi

echo "App PID: $APP_PID"
echo ""

echo "=== Step 2: Send quiesce signal (SIGUSR1) ==="
kill -USR1 $APP_PID 2>/dev/null || true
sleep 1

echo ""
echo "=== Step 3: CRIU Checkpoint ==="
sudo "$CRIU" dump \
    --tree $APP_PID \
    --images-dir "$CKPT_DIR" \
    --shell-job \
    -v2

if [ $? -eq 0 ]; then
    echo "Checkpoint SUCCESS!"
    ls "$CKPT_DIR"/*.img | wc -l
else
    echo "Checkpoint FAILED!"
    exit 1
fi

echo ""
echo "=== Step 4: CRIU Restore ==="
cd "$CKPT_DIR"
sudo NVSNAP_RESTORED=1 NVSNAP_LOG_LEVEL=3 LD_PRELOAD="$LIB" \
    "$CRIU" restore \
    --images-dir "$CKPT_DIR" \
    --shell-job \
    -d \
    -v2

sleep 2

echo ""
echo "=== Step 5: Verify restored process ==="
if pgrep -f local_test_app > /dev/null; then
    RPID=$(pgrep -f local_test_app)
    echo "Restored process running at PID=$RPID"
    sleep 3
    
    if ps -p $RPID > /dev/null 2>&1; then
        echo ""
        echo "========================================"
        echo "  SUCCESS: CHECKPOINT/RESTORE WORKS!"
        echo "========================================"
        sudo kill $RPID 2>/dev/null || true
    else
        echo "Process died after restore"
        exit 1
    fi
else
    echo "FAILED: Restored process not running"
    exit 1
fi
