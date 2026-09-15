#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# bench-gpd-vs-cascade.sh — measure single-receiver GPD throughput,
# compare to peer cascade. Question: should beta use GPD multi-attach
# ROX as the primary fan-out path, or stick with peer cascade?
#
# Bench:
#   1. Provision PD-SSD PVC of size SIZE_GIB, RWO.
#   2. Schedule a "writer" pod on $WRITER_NODE that mounts the PVC RW
#      and writes 30 GiB of random data into it. Measure write time.
#   3. Terminate writer pod → PD detaches.
#   4. Schedule a "reader" pod on $READER_NODE (different node) that
#      mounts the SAME PVC. Measure read time of the 30 GiB file.
#
# We use RWO + node-different rescheduling, not RWX→ROX multi-attach,
# because PD-SSD per-VM throughput is the same whether 1 or N nodes
# read concurrently. The multi-attach dance adds K8s plumbing without
# changing the per-receiver number. If GPD beats cascade per-receiver,
# multi-attach ROX is plumbing we'd add. If it doesn't, we save effort.
#
# Inputs:
#   $1 = SIZE_GIB (PVC size in GiB, e.g. 100 or 1000)
#   $2 = WRITER_NODE (full node name)
#   $3 = READER_NODE (full node name, must differ from writer)

set -euo pipefail

if [ $# -ne 3 ]; then
    echo "Usage: $0 <SIZE_GIB> <WRITER_NODE> <READER_NODE>"
    exit 1
fi
SIZE_GIB="$1"
WRITER_NODE="$2"
READER_NODE="$3"
NS="nvsnap-system"
PVC_NAME="gpd-bench-pvc"
DATA_GIB=30  # how much we actually write/read

GREEN='\033[0;32m'; CYAN='\033[0;36m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
log() { echo -e "${CYAN}[gpd]${NC}  $*"; }
ok()  { echo -e "${GREEN}[ok]${NC}   $*"; }
warn(){ echo -e "${YELLOW}[warn]${NC} $*"; }

cleanup() {
    log "Cleanup…"
    kubectl -n $NS delete pod gpd-bench-writer gpd-bench-reader --wait=true --timeout=60s 2>/dev/null || true
    kubectl -n $NS delete pvc $PVC_NAME --wait=true --timeout=120s 2>/dev/null || true
}
trap cleanup EXIT
# Pre-cleanup in case prior run left pods behind.
kubectl -n $NS delete pod gpd-bench-writer gpd-bench-reader --wait=true --timeout=60s 2>/dev/null || true
kubectl -n $NS delete pvc $PVC_NAME --wait=true --timeout=60s 2>/dev/null || true

log "Provisioning ${SIZE_GIB} GiB PD-SSD PVC…"
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $PVC_NAME
  namespace: $NS
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: premium-rwo
  resources:
    requests:
      storage: ${SIZE_GIB}Gi
EOF

log "Writer pod on $WRITER_NODE…"
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: gpd-bench-writer
  namespace: $NS
spec:
  nodeSelector:
    kubernetes.io/hostname: $WRITER_NODE
  tolerations:
    - { key: "nvidia.com/gpu", operator: "Exists", effect: "NoSchedule" }
  restartPolicy: Never
  containers:
    - name: w
      image: ubuntu:22.04
      command: ["/bin/sh","-c"]
      # dd from /dev/urandom is CPU-bound; use /dev/zero for max PD write rate.
      # Then explicit sync to flush to PD (so timing reflects durable write).
      args:
        - |
          set -e
          echo "writer starting on \$(hostname)"
          dd if=/dev/zero of=/data/test.bin bs=1M count=${DATA_GIB}000 conv=fdatasync status=progress 2>&1
          ls -lh /data/test.bin
          echo "WRITER_DONE"
      volumeMounts:
        - { name: pvc, mountPath: /data }
  volumes:
    - name: pvc
      persistentVolumeClaim:
        claimName: $PVC_NAME
EOF

log "Waiting up to 5 min for PVC to bind (PD provisioning can take ~1-2 min)…"
for i in $(seq 1 60); do
    phase=$(kubectl -n $NS get pvc $PVC_NAME -o jsonpath='{.status.phase}' 2>/dev/null || echo "Pending")
    if [ "$phase" = "Bound" ]; then
        ok "PVC bound after ${i}×5s"
        break
    fi
    sleep 5
done
log "Waiting for writer pod to start running…"
for i in $(seq 1 60); do
    phase=$(kubectl -n $NS get pod gpd-bench-writer -o jsonpath='{.status.phase}' 2>/dev/null || echo "Pending")
    if [ "$phase" = "Running" ] || [ "$phase" = "Succeeded" ] || [ "$phase" = "Failed" ]; then
        break
    fi
    sleep 5
done
log "Streaming writer logs…"
kubectl -n $NS logs -f gpd-bench-writer 2>&1 | tee /tmp/gpd-writer.log || true
# dd's own final summary line is authoritative; e.g.
#   "31457280000 bytes (31 GB, 29 GiB) copied, 90.1372 s, 349 MB/s"
WRITE_LINE=$(grep -E "bytes .* copied," /tmp/gpd-writer.log | tail -1)
WRITE_RATE=$(echo "$WRITE_LINE" | sed -nE 's/.*, ([0-9.]+ [KMG]B\/s).*/\1/p')
WRITE_ELAPSED=$(echo "$WRITE_LINE" | sed -nE 's/.* copied, ([0-9.]+) s,.*/\1/p')

log "Deleting writer (forces PD detach)…"
kubectl -n $NS delete pod gpd-bench-writer --wait=true --timeout=120s

log "Reader pod on $READER_NODE…"
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: gpd-bench-reader
  namespace: $NS
spec:
  nodeSelector:
    kubernetes.io/hostname: $READER_NODE
  tolerations:
    - { key: "nvidia.com/gpu", operator: "Exists", effect: "NoSchedule" }
  restartPolicy: Never
  containers:
    - name: r
      image: ubuntu:22.04
      command: ["/bin/sh","-c"]
      args:
        - |
          set -e
          echo "reader starting on \$(hostname)"
          ls -lh /data/test.bin
          dd if=/data/test.bin of=/dev/null bs=1M status=progress 2>&1
          echo "READER_DONE"
      volumeMounts:
        - { name: pvc, mountPath: /data, readOnly: true }
  volumes:
    - name: pvc
      persistentVolumeClaim:
        claimName: $PVC_NAME
EOF

log "Waiting for reader pod to start (PD must detach from writer first)…"
for i in $(seq 1 60); do
    phase=$(kubectl -n $NS get pod gpd-bench-reader -o jsonpath='{.status.phase}' 2>/dev/null || echo "Pending")
    if [ "$phase" = "Running" ] || [ "$phase" = "Succeeded" ] || [ "$phase" = "Failed" ]; then
        break
    fi
    sleep 5
done
log "Streaming reader logs…"
kubectl -n $NS logs -f gpd-bench-reader 2>&1 | tee /tmp/gpd-reader.log || true
READ_LINE=$(grep -E "bytes .* copied," /tmp/gpd-reader.log | tail -1)
READ_RATE=$(echo "$READ_LINE" | sed -nE 's/.*, ([0-9.]+ [KMG]B\/s).*/\1/p')
READ_ELAPSED=$(echo "$READ_LINE" | sed -nE 's/.* copied, ([0-9.]+) s,.*/\1/p')

echo ""
log "==== Summary (${SIZE_GIB} GiB PD-SSD, ${DATA_GIB} GiB data) ===="
echo ""
printf "  %-20s %s\n" "Phase" "Elapsed (s)"
printf "  %-20s %s\n" "$(printf '%.0s-' {1..20})" "-----------"
printf "  %-20s %-12s %s\n" "Write (capture)" "${WRITE_ELAPSED:-?} s" "${WRITE_RATE:-?}"
printf "  %-20s %-12s %s\n" "Read (restore)"  "${READ_ELAPSED:-?} s"  "${READ_RATE:-?}"
echo ""
log "Compare to peer cascade: ~30 s for 30 GiB = ~1 GB/s per receiver"
log "Cascade aggregate scales with peers; GPD per-VM bandwidth is fixed by PD size."
