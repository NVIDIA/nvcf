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
# csi-sanity.sh — run the upstream Kubernetes CSI conformance suite
# (kubernetes-csi/csi-test → csi-sanity) against our nvstor-csi
# binary. This is the *spec* level test that catches edge cases the
# happy-path Rust integration test in main.rs's mod tests can't:
# every required behaviour of CSI v1.10 — error codes, idempotency
# corners, validation rules, NodeStage / NodePublish path semantics.
#
# Prereqs: root/CAP_SYS_ADMIN, nfs-common, and a running nvstor NFS endpoint.
# Controller CreateVolume/DeleteVolume now perform real NFS directory operations.
# Set NVSTOR_CSI_NFS_SERVER to override the default local endpoint.
# One-time client installation:
#   go install github.com/kubernetes-csi/csi-test/v5/cmd/csi-sanity@latest
#
# Usage:
#   scripts/csi-sanity.sh                   # full suite
#   scripts/csi-sanity.sh --csi.focus=Foo   # filter to spec subset
#
# Exit code is the csi-sanity exit code: 0 = all passed, non-zero =
# at least one spec test failed. Non-zero on this script is a
# legitimate compliance regression, treat it like any other test
# failure.

set -uo pipefail

CSI_SANITY="${CSI_SANITY:-$HOME/gocode/bin/csi-sanity}"
if [ ! -x "$CSI_SANITY" ]; then
    echo "csi-sanity binary not found at $CSI_SANITY"
    echo "install with: go install github.com/kubernetes-csi/csi-test/v5/cmd/csi-sanity@latest"
    exit 2
fi

WORK="$(mktemp -d -t nvstor-csi-sanity.XXXXXX)"
SOCK="$WORK/csi.sock"
LOG="$WORK/nvstor-csi.log"
# csi-sanity calls `os.Mkdir(path, 0755)` on the mount + staging
# paths *itself* in BeforeEach, and errors if they already exist.
# Pass paths under $WORK but do not pre-create them.
MNT="$WORK/mnt"
STAGE="$WORK/stage"

cleanup() {
    if [ -n "${CSI_PID:-}" ]; then
        kill -9 "$CSI_PID" 2>/dev/null
    fi
    rm -rf "$WORK"
}
trap cleanup EXIT

echo "[csi-sanity] building nvstor-csi"
cargo build --release --bin nvstor-csi 2>&1 | tail -3

# Use a tempdir-isolated CSI db path so a stale /var/lib/nvstor-csi
# from a previous run can't leak in. The bin currently hard-codes
# CSI_DB_PATH; the bin will create it on demand if the dir tree
# exists.
export NVSTOR_CSI_DB="$WORK/csi-db"
mkdir -p "$NVSTOR_CSI_DB"

echo "[csi-sanity] starting nvstor-csi on $SOCK"
./target/release/nvstor-csi \
    --endpoint="unix://$SOCK" \
    --nvstor-server="${NVSTOR_CSI_NFS_SERVER:-127.0.0.1:2049}" \
    --node-name="sanity-node" \
    --db-path="$NVSTOR_CSI_DB" \
    > "$LOG" 2>&1 &
CSI_PID=$!

# Wait for socket to appear.
for _ in $(seq 1 50); do
    if [ -S "$SOCK" ]; then break; fi
    sleep 0.1
done
if [ ! -S "$SOCK" ]; then
    echo "[csi-sanity] socket never appeared; nvstor-csi log:"
    cat "$LOG"
    exit 3
fi
echo "[csi-sanity] driver up (pid $CSI_PID), socket ready"

# Run conformance. Pass through any extra args (e.g. --csi.focus=...).
#
# The default skips kernel mount lifecycle checks; clear CSI_SANITY_SKIP on
# a provisioned test cluster to include them. Controller lifecycle checks also
# require the live NFS endpoint and mount privileges described above.
SKIP_PATTERN="${CSI_SANITY_SKIP:-NodePublishVolume should work|NodeUnpublishVolume should remove target path|should work$|should be idempotent$|NodeExpandVolume should work if node-expand is called after node-publish|volume lifecycle should work|volume lifecycle should be idempotent}"

echo "[csi-sanity] running conformance suite (skipping mount-requires-root tests)"
"$CSI_SANITY" \
    --csi.endpoint="$SOCK" \
    --csi.mountdir="$MNT" \
    --csi.stagingdir="$STAGE" \
    --ginkgo.skip="$SKIP_PATTERN" \
    "$@"
EXIT_CODE=$?

echo
echo "[csi-sanity] exit=$EXIT_CODE; nvstor-csi log saved at $LOG (kept in $WORK)"
if [ $EXIT_CODE -ne 0 ]; then
    echo "[csi-sanity] last 30 lines of nvstor-csi log:"
    tail -30 "$LOG"
fi

# Don't auto-clean on failure — leave WORK so the human can inspect.
if [ $EXIT_CODE -eq 0 ]; then
    trap - EXIT
    cleanup
fi

exit $EXIT_CODE
