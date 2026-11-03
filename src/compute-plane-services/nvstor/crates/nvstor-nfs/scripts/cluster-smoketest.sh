#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

#
# cluster-smoketest.sh — runs ON a remote node that already has the
# nvstor-nfs-export binary deployed. Used by deploy-cluster-smoketest.sh
# but also callable directly if you've scp'd the binary yourself.
#
# Assumes:
#   BIN          path to an nvstor-nfs-export binary              (default /tmp/nvstor-nfs-export)
#   PORT         port the server should listen on                 (default 12049)
#   BIND         address to bind                                  (default 127.0.0.1)
#
# What it does:
#   - Populates a temp export directory with sample files.
#   - Starts the server in the background.
#   - Runs `mount -t nfs4 -o port=<PORT> <BIND>:/ <mnt>` (needs sudo).
#   - Exercises: ls, cat, stat, nested lookup, 4 MiB sha256 round-trip,
#     mid-file pread.
#   - Tears everything down.
#
# Exit 0 on success, non-zero on any check failing.

set -euo pipefail

BIN="${BIN:-/tmp/nvstor-nfs-export}"
PORT="${PORT:-12049}"
# Pick a metrics port deterministically from the NFS port so concurrent
# smoketests on the same host don't collide on a fixed 9101.
METRICS_PORT="${METRICS_PORT:-$((PORT + 10000))}"
BIND="${BIND:-127.0.0.1}"
LOG_DIR="${LOG_DIR:-/tmp}"

[ -x "$BIN" ] || { echo "error: BIN=$BIN is not executable" >&2; exit 1; }
command -v mount.nfs4 >/dev/null 2>&1 \
    || { echo "error: mount.nfs4 not installed (apt install nfs-common)" >&2; exit 1; }

EXPORT_DIR=$(mktemp -d -t nvstor-export-smoke.XXXXXX)
MNT=$(mktemp -d -t nvstor-mnt.XXXXXX)
SERVER_STDOUT="$LOG_DIR/nvstor-nfs-export.stdout"
SERVER_STDERR="$LOG_DIR/nvstor-nfs-export.stderr"

cleanup() {
    set +e
    if mountpoint -q "$MNT"; then sudo umount "$MNT" 2>/dev/null; fi
    if [ -n "${SERVER_PID:-}" ]; then
        kill "$SERVER_PID" 2>/dev/null
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    rmdir "$MNT" 2>/dev/null || true
    rm -rf "$EXPORT_DIR"
}
trap cleanup EXIT

echo "==> Preparing export at $EXPORT_DIR"
echo "hello from nfs" > "$EXPORT_DIR/hello.txt"
mkdir "$EXPORT_DIR/sub"
echo "nested" > "$EXPORT_DIR/sub/nested.txt"
dd if=/dev/urandom of="$EXPORT_DIR/random.bin" bs=1M count=4 status=none
RANDOM_SHA=$(sha256sum "$EXPORT_DIR/random.bin" | cut -d' ' -f1)

echo "==> Starting nvstor-nfs-export on ${BIND}:${PORT} (metrics on ${METRICS_PORT})"
RUST_LOG=info "$BIN" --port "$PORT" --metrics-port "$METRICS_PORT" --allow-client "$BIND" "$EXPORT_DIR" \
    > "$SERVER_STDOUT" 2> "$SERVER_STDERR" &
SERVER_PID=$!

# Wait up to 5 s for the socket to bind.
for _ in $(seq 1 50); do
    if ss -tln 2>/dev/null | grep -q ":${PORT} "; then break; fi
    sleep 0.1
done
ss -tln 2>/dev/null | grep -q ":${PORT} " || {
    echo "error: server failed to bind on port $PORT" >&2
    echo "--- server stderr ---"; tail -20 "$SERVER_STDERR"
    exit 2
}

echo "==> mount -t nfs4 -o port=${PORT} ${BIND}:/ $MNT"
# Default (no vers= override) — kernel negotiates v4.2.
sudo mount -t nfs4 -o "port=${PORT},proto=tcp,ro,soft,timeo=50" "${BIND}:/" "$MNT"

echo "==> ls"
ls "$MNT" | sort | diff - <(printf 'hello.txt\nrandom.bin\nsub\n') \
    || { echo "ls output mismatch" >&2; exit 3; }

echo "==> cat hello.txt"
[ "$(cat "$MNT/hello.txt")" = "hello from nfs" ] \
    || { echo "hello.txt content mismatch" >&2; exit 4; }

echo "==> stat hello.txt"
stat --printf='%s %F\n' "$MNT/hello.txt" | grep -q 'regular file' \
    || { echo "stat failed" >&2; exit 5; }

echo "==> nested lookup + read"
[ "$(cat "$MNT/sub/nested.txt")" = "nested" ] \
    || { echo "nested read mismatch" >&2; exit 6; }

echo "==> 4 MiB random.bin sha256 round-trip (pread across many rsize RPCs)"
SEEN_SHA=$(sha256sum "$MNT/random.bin" | cut -d' ' -f1)
[ "$SEEN_SHA" = "$RANDOM_SHA" ] \
    || { echo "random.bin sha mismatch: expected $RANDOM_SHA got $SEEN_SHA" >&2; exit 7; }

echo "==> Mid-file pread"
dd if="$MNT/random.bin" bs=1024 count=1 skip=2048 status=none of=/dev/null \
    || { echo "offset read failed" >&2; exit 8; }

echo "==> Prometheus /metrics scrape"
METRICS_URL="http://${BIND}:${METRICS_PORT}/metrics"
METRICS_BODY=$(curl -sf "$METRICS_URL") || { echo "metrics scrape failed: $METRICS_URL" >&2; exit 9; }
# Verify a histogram exists and has non-zero observations after our reads.
echo "$METRICS_BODY" | grep -q "# TYPE nvstor_nfs_op_duration_seconds histogram" \
    || { echo "metrics: missing op_duration_seconds histogram" >&2; exit 10; }
READ_COUNT=$(echo "$METRICS_BODY" \
    | awk -F'} ' '/op_duration_seconds_count{category="read"}/ {print $2}' \
    | head -1)
[ -n "$READ_COUNT" ] && [ "$READ_COUNT" -gt 0 ] \
    || { echo "metrics: read category count is zero (got: '$READ_COUNT')" >&2; exit 11; }
echo "    nvstor_nfs_op_duration_seconds_count{category=\"read\"} = $READ_COUNT"

echo ""
echo "==> ALL CHECKS PASSED"
