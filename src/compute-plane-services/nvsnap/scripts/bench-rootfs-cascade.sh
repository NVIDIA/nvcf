#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# bench-rootfs-cascade.sh — measure rootfs cascade fetch in parallel
# across 2 receivers, pulling from one peer that has the data.
#
# Mirrors what agent's EnsureCaptureLocal does at restore time but
# without the webhook layer: GET /v1/captures/{hash}/manifest from
# the peer, then 8-worker parallel GET /v1/captures/{hash}/file?path=
# for each file.
#
# Inputs:
#   $1 = capture hash (long form)
#   $2 = peer node short name (has the data)
#   args after = receiver node short names
#
# Usage:
#   bench-rootfs-cascade.sh aca2bd...e2cc9 3604 1sd6 hnst

set -u
HASH="$1"; shift
PEER_NODE="$1"; shift
RECEIVERS=("$@")
NS=nvsnap-system

agent_pod() {
    kubectl -n $NS get pods -l app=nvsnap-agent \
      --field-selector "spec.nodeName=gke-example-gpu-cluster-gpu-b6f5872c-$1" \
      -o name | head -1 | sed 's|pod/||'
}

PEER_POD=$(agent_pod "$PEER_NODE")
PEER_IP=$(kubectl -n $NS get pod "$PEER_POD" -o jsonpath='{.status.hostIP}')
PEER_URL="http://${PEER_IP}:8081"

echo "Hash:          ${HASH:0:32}..."
echo "Peer source:   $PEER_NODE ($PEER_URL)"
echo "Receivers:     ${RECEIVERS[*]}"
echo ""

# Pre-fetch the manifest from peer; receivers will see it.
MANIFEST_JSON=$(kubectl -n $NS exec "$PEER_POD" -- curl -sS "$PEER_URL/v1/captures/$HASH/manifest")
FILE_COUNT=$(echo "$MANIFEST_JSON" | python3 -c 'import sys, json; print(len(json.load(sys.stdin).get("files", [])))')
echo "Manifest:      $FILE_COUNT files"
echo ""

# Bench: for each receiver, in parallel:
#   1. Clean local cache for this hash
#   2. Fetch manifest
#   3. 8-worker parallel curl GET per file
#   4. Time wall-clock
RESULT_FILE=$(mktemp)
T0=$(date +%s.%N)

for NODE in "${RECEIVERS[@]}"; do
    POD=$(agent_pod "$NODE")
    (
        kubectl -n $NS exec "$POD" -- sh -c "rm -rf /var/lib/nvsnap/cache/$HASH; mkdir -p /var/lib/nvsnap/cache/$HASH/tree" >/dev/null
        START=$(date +%s.%N)
        # Stream the manifest and fan out 8 parallel curls.
        kubectl -n $NS exec "$POD" -- sh -c "
set -e
mkdir -p /var/lib/nvsnap/cache/$HASH
curl -sS '$PEER_URL/v1/captures/$HASH/manifest' > /var/lib/nvsnap/cache/$HASH/manifest.json
# Use jq if avail, otherwise python
python3 -c \"
import json, sys
m = json.load(open('/var/lib/nvsnap/cache/$HASH/manifest.json'))
for f in m.get('files', []):
    print(f['path'])
\" > /tmp/files.txt
WORKERS=8
xargs -n1 -P\$WORKERS -I{} sh -c '
  REL=\"\$1\"
  DST=\"/var/lib/nvsnap/cache/$HASH/\$REL\"
  mkdir -p \"\$(dirname \"\$DST\")\"
  curl -sS -o \"\$DST\" \"$PEER_URL/v1/captures/$HASH/file?path=\$REL\"
' _ {} < /tmp/files.txt
" 2>&1 | tail -2
        END=$(date +%s.%N)
        ELAPSED=$(awk -v a="$START" -v b="$END" 'BEGIN{printf "%.1f", b-a}')
        # Verify landed size
        SIZE=$(kubectl -n $NS exec "$POD" -- du -sB1 "/var/lib/nvsnap/cache/$HASH" 2>/dev/null | awk '{print $1}')
        SIZE_GIB=$(awk -v s="$SIZE" 'BEGIN{printf "%.1f", s/(1024*1024*1024)}')
        echo "$NODE|$ELAPSED|$SIZE_GIB" >> "$RESULT_FILE"
    ) &
done
wait

T1=$(date +%s.%N)
WALL=$(awk -v a="$T0" -v b="$T1" 'BEGIN{printf "%.1f", b-a}')

echo ""
echo "═══════════════════════════════════════════════"
echo " rootfs cascade fetch — vllm-70b (133 GiB on disk)"
echo "═══════════════════════════════════════════════"
printf "  %-10s %-12s %s\n" "Node" "Elapsed" "Landed"
printf "  %-10s %-12s %s\n" "────" "───────" "──────"
sort "$RESULT_FILE" | while IFS='|' read -r N E S; do
    TPUT=$(awk -v e="$E" -v s="$S" 'BEGIN{printf "%.2f GB/s", s/e}')
    printf "  %-10s %-12s %s GiB  (%s)\n" "$N" "${E} s" "$S" "$TPUT"
done
echo ""
printf "  %-10s %s\n" "Parallel wall-clock:" "${WALL} s"
rm -f "$RESULT_FILE"
