#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Reproduce concurrent shared read-only attaches of one NVMesh volume, the
# way nvsnap's model and cache volumes are consumed, without nvsnap.
#
#   1. one ReadWriteOnce PVC is provisioned, filled by a Job, and released
#      (PV kept with Retain, claim deleted, volume detached);
#   2. N static PersistentVolumes are created over the same NVMesh volume,
#      read-only, ReadOnlyMany, one per reader namespace suffix in the
#      volumeHandle, each bound to its own PVC;
#   3. N pods, one per node, mount their PVC at the same time;
#   4. per pod: time from creation to Running, and MountDevice timeouts.
#
# Usage:
#   repro.sh run      [READERS=8] [SC=nvcf-sc] [NS=nvmesh-ro-repro] [SIZE=2Gi] [FILL_MB=900] [NODE_SELECTOR=key=value] [IMAGE=busybox:1.36]
#   repro.sh cleanup  [NS=nvmesh-ro-repro]
set -euo pipefail
cmd=${1:-run}
: "${READERS:=8}" "${SC:=nvcf-sc}" "${NS:=nvmesh-ro-repro}" "${SIZE:=2Gi}" "${FILL_MB:=900}" "${NODE_SELECTOR:=}" "${IMAGE:=busybox:1.36}"
k() { kubectl "$@"; }
say() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*"; }

cleanup() {
  say "cleanup: pods, claims, read-only PVs, source PV in namespace $NS"
  k delete pods -n "$NS" -l app=nvmesh-ro-repro --ignore-not-found --wait=false >/dev/null 2>&1 || true
  k wait --for=delete pods -n "$NS" -l app=nvmesh-ro-repro --timeout=180s >/dev/null 2>&1 || true
  k delete pvc -n "$NS" -l app=nvmesh-ro-repro --ignore-not-found >/dev/null 2>&1 || true
  for pv in $(k get pv -l app=nvmesh-ro-repro -o name 2>/dev/null); do
    k patch "$pv" -p '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}' >/dev/null 2>&1 || true
    k delete "$pv" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  done
  k delete ns "$NS" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  say "cleanup requested; the NVMesh volume is freed when the source PV finishes deleting"
}
[ "$cmd" = cleanup ] && { cleanup; exit 0; }

selector=""
[ -n "$NODE_SELECTOR" ] && selector="      nodeSelector: {\"${NODE_SELECTOR%%=*}\": \"${NODE_SELECTOR#*=}\"}"

say "1/4 source volume: ReadWriteOnce PVC $SIZE on $SC, filled with ${FILL_MB} MB"
k create ns "$NS" --dry-run=client -o yaml | k apply -f - >/dev/null
cat <<Y | k apply -f - >/dev/null
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: repro-src, namespace: $NS, labels: {app: nvmesh-ro-repro}}
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: $SC
  resources: {requests: {storage: $SIZE}}
---
apiVersion: batch/v1
kind: Job
metadata: {name: repro-writer, namespace: $NS, labels: {app: nvmesh-ro-repro}}
spec:
  backoffLimit: 2
  template:
    metadata: {labels: {app: nvmesh-ro-repro}}
    spec:
      restartPolicy: Never
$selector
      containers:
      - name: w
        image: $IMAGE
        command: ["sh","-c","dd if=/dev/urandom of=/data/blob bs=1M count=$FILL_MB status=none && echo ok > /data/marker && sync && ls -la /data"]
        volumeMounts: [{name: d, mountPath: /data}]
      volumes: [{name: d, persistentVolumeClaim: {claimName: repro-src}}]
Y
k wait --for=condition=complete job/repro-writer -n "$NS" --timeout=15m >/dev/null
pv=$(k get pvc -n "$NS" repro-src -o jsonpath='{.spec.volumeName}')
handle=$(k get pv "$pv" -o jsonpath='{.spec.csi.volumeHandle}')
attrs=$(k get pv "$pv" -o json | python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["spec"]["csi"]["volumeAttributes"]))')
capacity=$(k get pv "$pv" -o jsonpath='{.spec.capacity.storage}')
say "    source PV $pv handle $handle"

say "2/4 release the source: keep the PV (Retain), delete the claim and the Job, wait for detach"
k label pv "$pv" app=nvmesh-ro-repro --overwrite >/dev/null
k patch pv "$pv" -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}' >/dev/null
k delete job -n "$NS" repro-writer --wait=true >/dev/null
k delete pvc -n "$NS" repro-src --wait=true >/dev/null
for i in $(seq 1 60); do
  n=$(k get volumeattachments -o json | python3 -c "import json,sys; print(sum(1 for v in json.load(sys.stdin)['items'] if v['spec']['source'].get('persistentVolumeName')=='$pv'))")
  [ "$n" = 0 ] && break; sleep 5
done
say "    released; VolumeAttachments for $pv: $n"

say "3/4 $READERS read-only views over the same volume, one pod per node, all at once"
base=${handle%:*}   # zone:name:uuid without the namespace suffix
for i in $(seq 0 $((READERS-1))); do
cat <<Y
---
apiVersion: v1
kind: PersistentVolume
metadata: {name: repro-ro-$i, labels: {app: nvmesh-ro-repro}}
spec:
  capacity: {storage: $capacity}
  accessModes: [ReadOnlyMany]
  persistentVolumeReclaimPolicy: Retain
  storageClassName: $SC
  mountOptions: [ro, norecovery, nouuid]
  claimRef: {namespace: $NS, name: repro-ro-$i}
  csi:
    driver: nvmesh-csi.excelero.com
    fsType: xfs
    readOnly: true
    volumeHandle: "$base:reader-$i"
    volumeAttributes: $attrs
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: repro-ro-$i, namespace: $NS, labels: {app: nvmesh-ro-repro}}
spec:
  accessModes: [ReadOnlyMany]
  storageClassName: $SC
  volumeName: repro-ro-$i
  resources: {requests: {storage: $capacity}}
Y
done | k apply -f - >/dev/null
t0=$(date -u +%s)
for i in $(seq 0 $((READERS-1))); do
cat <<Y
---
apiVersion: v1
kind: Pod
metadata: {name: repro-reader-$i, namespace: $NS, labels: {app: nvmesh-ro-repro, role: reader}}
spec:
  restartPolicy: Never
$selector
  affinity:
    podAntiAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
      - labelSelector: {matchLabels: {role: reader}}
        topologyKey: kubernetes.io/hostname
  containers:
  - name: r
    image: $IMAGE
    command: ["sh","-c","cat /data/marker && sleep 3600"]
    volumeMounts: [{name: d, mountPath: /data, readOnly: true}]
  volumes: [{name: d, persistentVolumeClaim: {claimName: repro-ro-$i, readOnly: true}}]
Y
done | k apply -f - >/dev/null
say "    pods created at $(date -u -d @$t0 +%H:%M:%S); waiting up to 10 min for all to run"
for s in $(seq 1 120); do
  running=$(k get pods -n "$NS" -l role=reader -o json | python3 -c "import json,sys; print(sum(1 for p in json.load(sys.stdin)['items'] if p['status'].get('phase')=='Running'))")
  [ "$running" = "$READERS" ] && break; sleep 5
done

say "4/4 results"
k get pods -n "$NS" -l role=reader -o json | python3 -c "
import json,sys,datetime as dt,subprocess
f=lambda s: dt.datetime.fromisoformat(s.replace('Z','+00:00'))
ev=json.loads(subprocess.run(['kubectl','get','events','-n','$NS','-o','json'],capture_output=True,text=True).stdout)['items']
to={}
for e in ev:
    if e.get('reason')=='FailedMount' and 'IO Enabled' in e.get('message',''): to[e['involvedObject']['name']]=e.get('count',1)
print('%-16s %-30s %8s %8s %s' % ('pod','node','sched_s','run_s','30s-timeouts'))
rows=[]
for p in json.load(sys.stdin)['items']:
    c=f(p['metadata']['creationTimestamp']); cond={x['type']:x['lastTransitionTime'] for x in p['status'].get('conditions',[]) if x['status']=='True'}
    sched=(f(cond['PodScheduled'])-c).total_seconds() if 'PodScheduled' in cond else None
    run=(f(cond['Ready'])-c).total_seconds() if 'Ready' in cond else None
    rows.append((p['metadata']['name'], p['spec'].get('nodeName','').split('.')[0], sched, run, to.get(p['metadata']['name'],0)))
for r in sorted(rows): print('%-16s %-30s %8s %8s %s' % (r[0], r[1], '%.0f'%r[2] if r[2] is not None else '-', '%.0f'%r[3] if r[3] is not None else 'not yet', r[4]))
runs=[r[3] for r in rows if r[3] is not None]
if runs: print('created->running: min %.0fs  median %.0fs  max %.0fs  (%d/%d running); pods with a 30 s IO-enable timeout: %d' % (min(runs), sorted(runs)[len(runs)//2], max(runs), len(runs), len(rows), sum(1 for r in rows if r[4])))
"
say "pod events with the driver's message, if any:"
k get events -n "$NS" --field-selector reason=FailedMount -o json | python3 -c "
import json,sys
for e in json.load(sys.stdin)['items'][:3]: print('  ', e['involvedObject']['name'], '|', e['message'][:220])"
say "done. Re-run 'repro.sh run' to attach again (the views already exist: delete them with cleanup first) or 'repro.sh cleanup' to remove everything."
