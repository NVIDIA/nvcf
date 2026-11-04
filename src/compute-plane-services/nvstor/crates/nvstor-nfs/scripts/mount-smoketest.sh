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
# Manual end-to-end smoke test for nvstor-nfs-export.
#
# Requires:
#   - Linux with nfs-common installed (provides mount.nfs4)
#   - sudo (for mount / umount)
#   - cargo + a debug or release build of the repo
#
# What it does:
#   1. Builds nvstor-nfs-export.
#   2. Populates a temp export directory with sample files.
#   3. Launches the server on 127.0.0.1:12049.
#   4. Runs `mount -t nfs4 -o port=12049 127.0.0.1:/ <mnt>`.
#   5. Exercises ls / stat / cat / read-at-offset against the mount.
#   6. Unmounts, kills the server, reports pass/fail.
#
# Exit codes:
#   0 = all checks passed
#   >0 = a check failed or setup problem
#
# NOTE: This cannot run in a rootless sandbox — it needs real `mount` +
# sudo. It is the real kernel client test for nvstor-nfs-export. CI covers the protocol layer via
# `cargo test -p nvstor-nfs`.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
PORT="${PORT:-12049}"
BIND="127.0.0.1"

cleanup() {
    set +e
    if [ -n "${MNT:-}" ] && mountpoint -q "$MNT"; then
        sudo umount "$MNT" 2>/dev/null
    fi
    if [ -n "${SERVER_PID:-}" ]; then
        kill "$SERVER_PID" 2>/dev/null
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    if [ -n "${MNT:-}" ]; then rmdir "$MNT" 2>/dev/null || true; fi
    if [ -n "${EXPORT_DIR:-}" ]; then rm -rf "$EXPORT_DIR"; fi
}
trap cleanup EXIT

echo "==> Building nvstor-nfs-export"
cd "$REPO_ROOT"
cargo build -p nvstor-nfs --bin nvstor-nfs-export --release >/dev/null 2>&1
BIN="$REPO_ROOT/target/release/nvstor-nfs-export"
[ -x "$BIN" ] || { echo "missing binary: $BIN" >&2; exit 1; }

echo "==> Preparing export directory"
EXPORT_DIR="$(mktemp -d -t nvstor-nfs-export.XXXXXX)"
echo "hello from nfs" > "$EXPORT_DIR/hello.txt"
mkdir "$EXPORT_DIR/sub"
echo "nested" > "$EXPORT_DIR/sub/nested.txt"
dd if=/dev/urandom of="$EXPORT_DIR/random.bin" bs=1M count=4 status=none
RANDOM_SHA=$(sha256sum "$EXPORT_DIR/random.bin" | cut -d' ' -f1)

echo "==> Launching server on ${BIND}:${PORT} (export=${EXPORT_DIR})"
"$BIN" --port "$PORT" --allow-client "$BIND" "$EXPORT_DIR" \
    > /tmp/nvstor-nfs-export.stdout 2> /tmp/nvstor-nfs-export.stderr &
SERVER_PID=$!

# Wait up to 5 s for the socket to bind.
for _ in $(seq 1 50); do
    if ss -tlnp 2>/dev/null | grep -q ":${PORT}\b"; then
        break
    fi
    sleep 0.1
done
ss -tlnp 2>/dev/null | grep -q ":${PORT}\b" \
    || { echo "server failed to bind; see /tmp/nvstor-nfs-export.stderr" >&2; exit 2; }

MNT="$(mktemp -d -t nvstor-nfs-mnt.XXXXXX)"
echo "==> Mounting at ${MNT}"
sudo mount -t nfs4 \
    -o "port=${PORT},vers=4.2,proto=tcp,ro" \
    "${BIND}:/" "$MNT"

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

echo "==> 4 MiB random file integrity (pread across many rsize-sized RPCs)"
SEEN_SHA=$(sha256sum "$MNT/random.bin" | cut -d' ' -f1)
[ "$SEEN_SHA" = "$RANDOM_SHA" ] \
    || { echo "random.bin sha mismatch: expected $RANDOM_SHA got $SEEN_SHA" >&2; exit 7; }

echo "==> Read at a mid-file offset (exercises pread window)"
dd if="$MNT/random.bin" bs=1024 count=1 skip=2048 status=none of=/dev/null \
    || { echo "offset read failed" >&2; exit 8; }

echo "==> All checks passed"
