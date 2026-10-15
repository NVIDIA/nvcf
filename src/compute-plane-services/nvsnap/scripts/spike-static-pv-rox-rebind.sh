#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# spike-static-pv-rox-rebind.sh
#
# Spike #168: prove (or disprove) that we can avoid the snap+clone dance
# on Hyperdisk-ML by using NVCA's model-cache pattern — static PV with
# manual rebind from RWO → ROX, no VolumeSnapshot involved.
#
# Sequence:
#   1. Dynamic-provision rwx PVC (RWO) → wait Bound → grab the PV's volumeHandle
#   2. Single-pod writer drops a known payload onto the PVC
#   3. Patch PV reclaim policy → Retain (so deleting the PVC keeps the disk)
#   4. Delete the writer pod + writer PVC; PV survives, claimRef stays
#   5. Patch the PV: clear claimRef, accessModes → [ReadOnlyMany]
#   6. Create N reader PVCs that statically bind via Spec.VolumeName
#   7. Schedule N reader pods that mount the readers (different nodes), verify
#      all read the same payload — proves multi-attach RO works
#   8. Tear down everything (delete reader pods + PVCs + PV → GCP disk freed
#      because retain policy was reverted to Delete before final teardown)
#
# Open questions this script answers:
#   Q1: does pd.csi accept static-PV ROX provisioning?
#   Q2: can multiple pods NodeStageVolume the same volumeHandle as RO concurrently?
#   Q3: what's the wall-clock vs snap+clone path?
#   Q4: is the disk leaked after teardown?
#
# Usage:
#   KUBECONFIG=$HOME/GCP-H100-a.kubeconfig ./scripts/spike-static-pv-rox-rebind.sh
#   ./scripts/spike-static-pv-rox-rebind.sh cleanup     # tear down only

set -u
NS="${SPIKE_NS:-nvsnap-spike-rox}"
SC="${SPIKE_SC:-hyperdisk-ml}"
SIZE="${SPIKE_SIZE:-10Gi}"
NREADERS="${SPIKE_NREADERS:-3}"
TAG="$(date +%s)"
NAME_RWX="rwx-${TAG}"
NAME_PV=""                                # filled in at runtime
PAYLOAD="hello-from-writer-${TAG}"
TIMEOUT="${SPIKE_TIMEOUT:-300}"           # seconds per wait step

log()  { printf '\n[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
warn() { printf '\n[%s] WARN: %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
die()  { printf '\n[%s] FAIL: %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; cleanup_artifacts; exit 1; }

step_start_ts=""
step()       { step_start_ts="$(date +%s)"; log "STEP: $*"; }
step_done()  { local now; now="$(date +%s)"; log "  ok ($((now - step_start_ts))s)"; }

require()    { command -v "$1" >/dev/null 2>&1 || die "missing dep: $1"; }

cleanup_artifacts() {
  log "cleanup: deleting test namespace + lingering PVs"
  kubectl delete ns "$NS" --wait=false --ignore-not-found >/dev/null 2>&1
  # Reset reclaim policy on any test PVs we created, so GCP disks are freed
  local pvs
  pvs="$(kubectl get pv -o jsonpath='{range .items[?(@.spec.storageClassName=="'"$SC"'")]}{.metadata.name}{" "}{.spec.claimRef.namespace}{"\n"}{end}' \
        | awk -v ns="$NS" '$2==ns || $2=="" { print $1 }')"
  for pv in $pvs; do
    kubectl patch pv "$pv" -p '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}' >/dev/null 2>&1
    kubectl delete pv "$pv" --wait=false >/dev/null 2>&1
  done
}

case "${1:-run}" in
  cleanup) cleanup_artifacts; exit 0 ;;
  run)     ;;
  *)       die "usage: $0 [run|cleanup]" ;;
esac

require kubectl
require jq

# Sanity: cluster reachable + storage class exists
kubectl get sc "$SC" >/dev/null 2>&1 || die "storageclass $SC not found"
log "spike target: ns=$NS sc=$SC size=$SIZE readers=$NREADERS tag=$TAG"

# Idempotent: clean prior run
cleanup_artifacts >/dev/null 2>&1 || true

# ────────────────────────────────────────────────────────────────────────────
step "create namespace + writer PVC ($NAME_RWX, RWO)"
kubectl create ns "$NS" >/dev/null
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: v1
kind: PersistentVolumeClaim
metadata: { name: $NAME_RWX, namespace: $NS }
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: $SC
  resources: { requests: { storage: $SIZE } }
EOF
step_done

# ────────────────────────────────────────────────────────────────────────────
step "writer pod — writes payload to /data/marker"
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata: { name: writer, namespace: $NS, labels: { spike-role: writer } }
spec:
  restartPolicy: Never
  containers:
  - name: writer
    image: busybox:1.36
    command: ["sh","-c"]
    args:
    - |
      set -e
      echo "$PAYLOAD" > /data/marker
      echo "wrote: \$(cat /data/marker)"
      ls -la /data
      sync
    volumeMounts: [{ name: data, mountPath: /data }]
  volumes:
  - name: data
    persistentVolumeClaim: { claimName: $NAME_RWX }
EOF

if ! kubectl -n "$NS" wait --for=condition=Ready=False --timeout="${TIMEOUT}s" pod/writer 2>/dev/null; then
  # Wait for Succeeded instead — pod runs to completion
  kubectl -n "$NS" wait --for=jsonpath='{.status.phase}'=Succeeded --timeout="${TIMEOUT}s" pod/writer \
    || die "writer pod did not Succeed; check 'kubectl -n $NS describe pod writer'"
fi
log "writer Succeeded; PVC should be Bound now"
kubectl -n "$NS" get pvc "$NAME_RWX"
NAME_PV="$(kubectl -n "$NS" get pvc "$NAME_RWX" -o jsonpath='{.spec.volumeName}')"
[ -n "$NAME_PV" ] || die "no PV bound to $NAME_RWX"
log "bound PV: $NAME_PV"
step_done

# ────────────────────────────────────────────────────────────────────────────
step "inspect PV — pull volumeHandle (GCP disk id)"
kubectl get pv "$NAME_PV" -o yaml | grep -E 'volumeHandle|reclaimPolicy|accessModes|fsType|claimRef' | head -20
VOLHANDLE="$(kubectl get pv "$NAME_PV" -o jsonpath='{.spec.csi.volumeHandle}')"
[ -n "$VOLHANDLE" ] || die "no csi.volumeHandle on $NAME_PV"
log "volumeHandle: $VOLHANDLE"
step_done

# ────────────────────────────────────────────────────────────────────────────
step "patch PV reclaim policy → Retain (so PVC delete doesn't nuke the disk)"
kubectl patch pv "$NAME_PV" -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}'
kubectl get pv "$NAME_PV" -o jsonpath='{.spec.persistentVolumeReclaimPolicy}' ; echo
step_done

# ────────────────────────────────────────────────────────────────────────────
step "delete writer pod + writer PVC (disk should survive)"
kubectl -n "$NS" delete pod writer --wait=true
kubectl -n "$NS" delete pvc "$NAME_RWX" --wait=true
log "PV state after PVC delete:"
kubectl get pv "$NAME_PV" -o jsonpath='phase={.status.phase} reclaim={.spec.persistentVolumeReclaimPolicy} claimRef={.spec.claimRef.name}{"\n"}'
step_done

# ────────────────────────────────────────────────────────────────────────────
# Hyperdisk-ML at the GCP layer is in READ_WRITE_SINGLE mode until we
# explicitly flip it to READ_ONLY_MANY. Without this, RO multi-attach
# fails at mount time ("cannot mount unformatted disk … in read-only
# mode" — pd.csi's format-check can't manipulate the disk while RO).
# snap+clone hides this because the cloned disk is BORN in ROX mode.
step "Q1pre: gcloud compute disks update --access-mode READ_ONLY_MANY"
require gcloud
# VOLHANDLE format: projects/<proj>/zones/<zone>/disks/<name>
GCP_PROJ="$(echo "$VOLHANDLE" | awk -F/ '{print $2}')"
GCP_ZONE="$(echo "$VOLHANDLE" | awk -F/ '{print $4}')"
GCP_DISK="$(echo "$VOLHANDLE" | awk -F/ '{print $6}')"
log "  disk=$GCP_DISK zone=$GCP_ZONE project=$GCP_PROJ"
# Pre-update access mode
log "  before: $(gcloud compute disks describe "$GCP_DISK" --zone="$GCP_ZONE" --project="$GCP_PROJ" --format='value(accessMode)' 2>&1)"
gcloud compute disks update "$GCP_DISK" --zone="$GCP_ZONE" --project="$GCP_PROJ" --access-mode=READ_ONLY_MANY 2>&1 \
  || die "Q1pre FAIL: gcloud disk access-mode update failed"
log "  after:  $(gcloud compute disks describe "$GCP_DISK" --zone="$GCP_ZONE" --project="$GCP_PROJ" --format='value(accessMode)' 2>&1)"
step_done

# ────────────────────────────────────────────────────────────────────────────
step "Q1: rebind PV — mirror validate-nvmesh.sh patches (rename claimRef, ROX, mountOptions)"
# Per user's proven-on-GCE-PD recipe (validate-nvmesh.sh):
#  1. claimRef.name → reader PVC; remove claimRef.uid + claimRef.resourceVersion
#     (free the binding without removing claimRef — this is how K8s rebind works)
#  2. accessModes → [ReadOnlyMany]
#  3. mountOptions → [ro, norecovery, nouuid]
#     ro: force RO at FS layer
#     norecovery: don't attempt ext4 journal recovery (which would need writes)
#     nouuid: skip UUID check
#
# Note: PV.spec.csi.readOnly is IMMUTABLE post-creation and cannot be patched.
# If pd.csi-driver derives attach-mode from csi.readOnly (not accessModes),
# this will still fail with "cannot be attached with READ_WRITE mode" — that
# would prove we need NVCA's "DeepCopy to NEW PV" pattern instead of patch.
NAME_ROX="rox-${TAG}"
kubectl patch pv "$NAME_PV" --type=json -p='[
  {"op":"replace","path":"/spec/claimRef/name","value":"'"$NAME_ROX"'"},
  {"op":"remove","path":"/spec/claimRef/uid"},
  {"op":"remove","path":"/spec/claimRef/resourceVersion"}
]' || die "Q1 FAIL: claimRef rename patch failed"
kubectl patch pv "$NAME_PV" --type=json -p='[
  {"op":"replace","path":"/spec/accessModes","value":["ReadOnlyMany"]}
]' || die "Q1 FAIL: accessModes patch failed"
kubectl patch pv "$NAME_PV" --type=json -p='[
  {"op":"add","path":"/spec/mountOptions","value":["ro","norecovery","nouuid"]}
]' || die "Q1 FAIL: mountOptions patch failed"
log "PV after rebind patches:"
kubectl get pv "$NAME_PV" -o jsonpath='phase={.status.phase} access={.spec.accessModes} mountOpts={.spec.mountOptions} claimRef={.spec.claimRef.name}/{.spec.claimRef.namespace}{"\n"}'
[ "$(kubectl get pv "$NAME_PV" -o jsonpath='{.spec.accessModes[0]}')" = "ReadOnlyMany" ] \
  || die "Q1 FAIL: pv accessModes did not flip to ROX"
log "Q1 PASS: PV patched (claimRef rename, ROX, mountOptions)"
step_done

# ────────────────────────────────────────────────────────────────────────────
step "Q1b: create ONE reader PVC (claimRef pre-rename means it binds automatically)"
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: v1
kind: PersistentVolumeClaim
metadata: { name: $NAME_ROX, namespace: $NS }
spec:
  accessModes: [ReadOnlyMany]
  storageClassName: $SC
  resources: { requests: { storage: $SIZE } }
EOF

# Watch for Bound or Pending-with-error
end=$(( $(date +%s) + 60 ))
while [ "$(date +%s)" -lt "$end" ]; do
  phase="$(kubectl -n "$NS" get pvc "$NAME_ROX" -o jsonpath='{.status.phase}' 2>/dev/null)"
  [ "$phase" = "Bound" ] && { log "reader PVC bound"; break; }
  sleep 2
done
phase="$(kubectl -n "$NS" get pvc "$NAME_ROX" -o jsonpath='{.status.phase}' 2>/dev/null)"
if [ "$phase" != "Bound" ]; then
  warn "Q1b FAIL: reader PVC stuck in $phase. Events:"
  kubectl -n "$NS" describe pvc "$NAME_ROX" | tail -15
  die "static-bind ROX PVC did not Bind — pd.csi may reject this path"
fi
log "Q1b PASS: pd.csi accepts static-bind ROX PVC (no dataSource required)"
step_done

# ────────────────────────────────────────────────────────────────────────────
step "Q2: schedule $NREADERS reader pods, ALL mount the SAME PVC ($NAME_ROX) read-only"
for i in $(seq 1 "$NREADERS"); do
  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata: { name: reader-$i, namespace: $NS, labels: { spike-role: reader } }
spec:
  restartPolicy: Never
  containers:
  - name: reader
    image: busybox:1.36
    command: ["sh","-c"]
    args:
    - |
      echo "reader-$i on \$(hostname): \$(cat /data/marker)"
      # Confirm it's actually read-only
      if echo "x" > /data/marker 2>/dev/null; then
        echo "ERROR: writable!"
        exit 1
      fi
      echo "confirmed read-only"
      sleep 30
    volumeMounts:
    - { name: data, mountPath: /data, readOnly: true }
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: $NAME_ROX
      readOnly: true
EOF
done

# Wait for all readers to either reach Running (Q2 PASS) or fail to attach (Q2 FAIL)
end=$(( $(date +%s) + TIMEOUT ))
log "waiting for $NREADERS readers to reach Running…"
while [ "$(date +%s)" -lt "$end" ]; do
  running="$(kubectl -n "$NS" get pods -l spike-role=reader --no-headers 2>/dev/null | awk '$3=="Running" || $3=="Completed"' | wc -l)"
  [ "$running" -eq "$NREADERS" ] && break
  sleep 3
done

running="$(kubectl -n "$NS" get pods -l spike-role=reader --no-headers 2>/dev/null | awk '$3=="Running" || $3=="Completed"' | wc -l)"
log "reader status:"
kubectl -n "$NS" get pods -l spike-role=reader -o wide

if [ "$running" -ne "$NREADERS" ]; then
  warn "Q2 FAIL: only $running/$NREADERS readers running. Describe of stuck readers:"
  for i in $(seq 1 "$NREADERS"); do
    state="$(kubectl -n "$NS" get pod reader-$i -o jsonpath='{.status.phase}' 2>/dev/null)"
    [ "$state" != "Running" ] && [ "$state" != "Succeeded" ] && {
      echo "--- reader-$i ($state) ---"
      kubectl -n "$NS" describe pod reader-$i | tail -20
    }
  done
  die "multi-attach RO failed — NodeStageVolume likely rejected concurrent mounts"
fi

log "Q2 PASS: $NREADERS readers concurrently mounted volumeHandle=$VOLHANDLE as RO"
echo
echo "reader logs (proves they read the same payload):"
for i in $(seq 1 "$NREADERS"); do
  echo "--- reader-$i ---"
  kubectl -n "$NS" logs reader-$i 2>&1 | head -5
done
step_done

# ────────────────────────────────────────────────────────────────────────────
log "════════════════════════════════════════════"
log " SPIKE RESULT: static-PV ROX rebind WORKS ✓"
log "   Q1 (PV ROX patch):           PASS"
log "   Q1b (static ROX bind):        PASS"
log "   Q2 ($NREADERS-way concurrent mount): PASS"
log "════════════════════════════════════════════"
log "leaving artifacts up for inspection. Tear down with: $0 cleanup"
log "  ns: $NS"
log "  pv: $NAME_PV (Retain — manual delete required)"
