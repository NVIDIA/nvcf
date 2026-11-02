#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Full CRIU checkpoint/restore of a vLLM model (default Qwen2.5-72B, TP=4) on one node,
# with cold-start and restore timing breakdowns.
#   tests/gpushare/k8s/vllm_criu_bench.sh <namespace> <node>
# env: MODEL, TP, UTIL; CYCLES (checkpoint+restore rounds, pods alternate);
#   PVC: claim mounted at /store for checkpoints, or DUMP_HOSTPATH: node dir
#   (e.g. local NVMe) mounted at /dump; default /ckpt (vllm-criu.yaml).
#   CKPT_PVC: claim used as /ckpt itself (model, caches, libnvsnap_gpushare,
#   CRIU, checkpoints) instead of the node's hostPath, so a restore can run
#   on another node: RESTORE_NODE.
#   CACHE_HOSTPATH: node dir (local NVMe) mounted at /cache on every node, the
#   chunk store's node cache (--cache); PREFETCH=1 fills the restore node's
#   cache with the checkpoint's chunks first (timed apart: done ahead of time).
# GPU memory saved by libnvsnap_gpushare goes to the chunk store <base>/store
# (shared by every checkpoint: weights are stored once); each checkpoint's
# CRIU image goes to <base>/ckpt-*. Restores read cold (page cache evicted).
# Prerequisites: CRIU in /var/lib/nvsnap-gpushare/criu on <node>
# (criu-build.yaml) and nvsnap-pull-secret in <namespace>: libnvsnap_gpushare
# and nvsnap-gpu-suspend come from the agent base image (see vllm-criu.yaml).
# A ready source pod "vllm-criu" is reused (cold start then not measured).
# Any failed step stops the run; the source pod is deleted only after its
# checkpoint is complete.
set -euo pipefail
TMP=$(mktemp -d)
NS=$1; NODE=$2
MODEL=${MODEL:-Qwen/Qwen2.5-72B-Instruct}; TP=${TP:-4}; UTIL=${UTIL:-0.2}
SRC=vllm-criu; DST=vllm-restored
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
T=/ckpt/wc/nvsnap-gpu-suspend
BASEDIR=/ckpt
[ -n "${DUMP_HOSTPATH:-}" ] && BASEDIR=/dump
[ -n "${PVC:-}" ] && BASEDIR=/store
CRIU="env LD_LIBRARY_PATH=/ckpt/criu/lib /ckpt/criu/criu"
CRIU_OPTS="--shell-job --tcp-established --skip-in-flight --network-lock nftables --file-locks"
x() { local pod=$1; shift; kubectl exec -n "$NS" "$pod" -c vllm -- "$@"; }
fail() { echo "FAIL: $*"; exit 1; }
now() { date +%s.%N; }
dt() { printf "%.1f" "$(echo "$2 - $1" | bc)"; }
ts() { "$@" 2>&1 | while IFS= read -r l; do printf "%s %s\n" "$(date +%s.%N)" "$l"; done; }
query() { x "$1" python3 /ckpt/vllm_query.py; }
manifest() {  # $1 pod name, $2 mode, [$3 node]
    local ckpt_vol="s#^#&#"  # no-op unless CKPT_PVC
    [ -n "${CKPT_PVC:-}" ] && ckpt_vol="s#^    hostPath: {path: /var/lib/nvsnap-gpushare, type: DirectoryOrCreate}#    persistentVolumeClaim: {claimName: $CKPT_PVC}#"
    sed -e "s/^spec:\$/spec:\n  nodeName: ${3:-$NODE}/" -e "$ckpt_vol" \
        -e "s/^  name: vllm-criu\$/  name: $1/" -e "s/app: vllm-criu/app: $1\n    nvsnap.io\/inject: \"false\"/" \
        -e 's#image: vllm/#image: docker.io/vllm/#' \
        -e "s#Qwen/Qwen2.5-7B-Instruct#$MODEL#g" \
        -e "s/--tensor-parallel-size=2/--tensor-parallel-size=$TP/" \
        -e "s/--gpu-memory-utilization=0.3/--gpu-memory-utilization=$UTIL/" \
        -e "s/requests: {nvidia.com\/gpu: 2, cpu: \"8\", memory: 64Gi}/requests: {nvidia.com\/gpu: $TP, cpu: \"32\", memory: 64Gi}/" \
        -e "s/limits: {nvidia.com\/gpu: 2, memory: 200Gi}/limits: {nvidia.com\/gpu: $TP, memory: 800Gi}/" \
        -e "s/{name: MODE, value: serve}/{name: MODE, value: $2}/" "$ROOT/tests/gpushare/k8s/vllm-criu.yaml" |
    if [ -n "${PVC:-}" ]; then
        sed -e 's#^    - {name: dshm, mountPath: /dev/shm}$#&\n    - {name: store, mountPath: /store}#' \
            -e "s#^  volumes:\$#&\n  - name: store\n    persistentVolumeClaim: {claimName: $PVC}#"
    elif [ -n "${DUMP_HOSTPATH:-}" ]; then
        sed -e 's#^    - {name: dshm, mountPath: /dev/shm}$#&\n    - {name: dump, mountPath: /dump}#' \
            -e "s#^  volumes:\$#&\n  - name: dump\n    hostPath: {path: $DUMP_HOSTPATH, type: DirectoryOrCreate}#"
    else cat; fi |
    if [ -n "${CACHE_HOSTPATH:-}" ]; then
        sed -e 's#^    - {name: dshm, mountPath: /dev/shm}$#&\n    - {name: cache, mountPath: /cache}#' \
            -e "s#^  volumes:\$#&\n  - name: cache\n    hostPath: {path: $CACHE_HOSTPATH, type: DirectoryOrCreate}#"
    else cat; fi
}
kts() { date -d "$1" +%s.%N; }  # k8s RFC3339 -> epoch

echo "### model=$MODEL TP=$TP util=$UTIL node=$NODE checkpoints in $BASEDIR${PVC:+ (PVC $PVC)}"

# ── Cold start ──────────────────────────────────────────────────────────
if [ "$(kubectl get pod -n $NS $SRC -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null)" = true ]; then
    echo "reusing running $SRC (cold start measured earlier)"
    BASE=$(query $SRC | python3 -c 'import json,sys; print(json.load(sys.stdin)["text"])')
    echo "baseline: ${BASE:0:60}..."
else
t_create=$(now)
manifest $SRC serve | kubectl apply -n "$NS" -f - >/dev/null
until [ "$(kubectl get pod -n $NS $SRC -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null)" = true ]; do
    ph=$(kubectl get pod -n $NS $SRC -o jsonpath='{.status.phase}' 2>/dev/null)
    [ "$ph" = Failed ] && { echo "FAIL: source pod failed"; kubectl logs -n $NS $SRC --tail=30; exit 1; }
    sleep 5
done
t_ready=$(now)
J=$(kubectl get pod -n $NS $SRC -o json)
fetch() { echo "$J" | python3 -c 'import json,sys; print([c for c in json.load(sys.stdin)["status"]["initContainerStatuses"] if c["name"] == "fetch-model"][0]["state"]["terminated"][sys.argv[1]])' "$1"; }
i_start=$(kts "$(fetch startedAt)")
i_end=$(kts "$(fetch finishedAt)")
c_start=$(kts "$(echo "$J" | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"]["containerStatuses"][0]["state"]["running"]["startedAt"])')")
kubectl cp -n "$NS" "$ROOT/tests/gpushare/k8s/vllm_query.py" "$SRC:/ckpt/vllm_query.py" -c vllm
kubectl cp -n "$NS" "$ROOT/tests/gpushare/k8s/dump_evict.py" "$SRC:/ckpt/dump_evict.py" -c vllm
s=$(now); R1=$(query $SRC); t_first=$(now); first=$(dt $s $t_first)
BASE=$(query $SRC | python3 -c 'import json,sys; print(json.load(sys.stdin)["text"])')
LOG=$(kubectl logs -n $NS $SRC -c vllm --timestamps)
lt() { echo "$LOG" | grep -m1 -E "$1" | awk '{print $1}'; }
lv() { echo "$LOG" | grep -m1 -oE "$1" | grep -oE '[0-9]+\.[0-9]+' | head -1; }
v_first=$(kts "$(echo "$LOG" | grep -m1 -E 'vLLM API server version|non-default args' | awk '{print $1}')")
v_up=$(kts "$(lt 'Application startup complete')")
echo
echo "=== COLD START (model $MODEL, TP=$TP, util $UTIL)"
echo "  pod scheduled -> init start        $(dt $t_create $i_start)s"
echo "  model download (init container)    $(dt $i_start $i_end)s  ($(x $SRC du -sh /ckpt/home/.cache/huggingface 2>/dev/null | cut -f1))"
echo "  init end -> vllm container start   $(dt $i_end $c_start)s"
echo "  container start -> vllm first log  $(dt $c_start $v_first)s  (python import)"
echo "  vllm: weights loaded               $(lv 'Loading weights took [0-9.]+')s"
echo "  vllm: model loading total          $(lv 'Model loading took [0-9.]+ GiB memory and [0-9.]+' | tail -1)  ($(echo "$LOG" | grep -m1 -oE 'Model loading took [0-9.]+ GiB memory and [0-9.]+ seconds'))"
echo "  vllm: torch.compile                $(echo "$LOG" | grep -m1 -oE 'torch.compile takes [0-9.]+ s in total')"
echo "  vllm: CUDA graph capture           $(echo "$LOG" | grep -m1 -oE 'Graph capturing finished in [0-9.]+ secs')"
echo "  vllm: engine init total            $(echo "$LOG" | grep -m1 -oE 'took [0-9.]+ seconds' | tail -1) ($(echo "$LOG" | grep -m1 -oE 'init engine \(profile, create kv cache, warmup model\) took [0-9.]+ seconds'))"
echo "  vllm first log -> API up           $(dt $v_first $v_up)s"
echo "  API up -> pod ready (probe)        $(dt $v_up $t_ready)s"
echo "  first request (cold)               ${first}s"
echo "  TOTAL pod create -> first token     $(dt $t_create $t_first)s"
echo "  TOTAL excl. download                $(echo "$(dt $t_create $t_first) - $(dt $i_start $i_end)" | bc)s"
echo "baseline: ${BASE:0:60}..."
fi

# The tool is in the shared node dir (/ckpt/wc, from the gpushare init container).
ROOTPID=$(x $SRC pgrep -x vllm)
PIDS="$ROOTPID $(x $SRC pgrep -f 'VLLM::' | xargs)"  # CRIU restores the same pids
echo "pids: $PIDS"
STORE=$BASEDIR/store
CACHE=${CACHE_HOSTPATH:+--cache /cache}

# One checkpoint of pod $1 into the store, then a restore into new pod $2.
cycle() {
    local SRC=$1 DST=$2 c=$3 D=$BASEDIR/ckpt-$(date +%Y%m%d-%H%M%S)-$3
    x_src() { kubectl exec -n "$NS" $SRC -c vllm -- "$@"; }
    x_dst() { kubectl exec -n "$NS" $DST -c vllm -- "$@"; }
    x_src mkdir -p $D/img $STORE
    x_src $T gpus | kubectl exec -i -n "$NS" $SRC -c vllm -- tee $D/gpus >/dev/null
    local before=$(x_src du -sb $STORE | cut -f1)
    echo
    echo "=== CHECKPOINT $c ($SRC -> $D)"
    s=$(now); ts x_src stdbuf -oL $T --timeout-ms 120000 --store $STORE --ckpt-dir $D $CACHE suspend $PIDS > $TMP/suspend.log ||
        { cat $TMP/suspend.log; fail "suspend (the source pod is left as is)"; }
    e=$(now)
    { grep -E "release[ :]|checkpoint=|suspend|err|fail" $TMP/suspend.log | awk -v s=$s '{printf "  +%6.1fs %s\n", $1-s, substr($0, index($0,$2))}' | grep -vE "checkpoint=CUDA_SUCCESS" | sed -E "s#release [^:]*: ok released [0-9]+ mapping.*saved#release: saved#" | head -12; } || true
    local after=$(x_src du -sb $STORE | cut -f1)
    echo "  GPU suspend (quiesce+release[save]+checkpoint)  $(dt $s $e)s  (store grew $(( (after - before) >> 30 )) GiB, now $(( after >> 30 )) GiB)"
    s=$(now); x_src $T stop $PIDS >/dev/null || fail "stop"; e=$(now); echo "  stop (hand off to CRIU)            $(dt $s $e)s"
    s=$(now); x_src $CRIU dump -t "$ROOTPID" -D $D/img $CRIU_OPTS --ghost-limit 1G --link-remap -v4 -o $D/dump.log || { echo "FAIL: criu dump"; x_src tail -30 $D/dump.log; exit 1; }
    e=$(now); echo "  criu dump                          $(dt $s $e)s  (image $(x_src du -sh $D/img | cut -f1))"
    x_src tar -C /dev/shm -cf $D/shm.tar . || fail "save /dev/shm"
    s=$(now); x_src sync -f $D || fail "sync"; e=$(now); echo "  sync (flush checkpoint fs)         $(dt $s $e)s"
    kubectl delete pod -n "$NS" $SRC --wait=true >/dev/null
    echo "  source pod deleted"

    echo
    echo "=== RESTORE $c (new pod $DST)"
    t0=$(now)
    manifest $DST restore ${RESTORE_NODE:-} | sed -e '/initContainers:/,/^  containers:/{/^  containers:/!d}' | kubectl apply -n "$NS" -f - >/dev/null
    until kubectl get pod -n "$NS" $DST -o jsonpath='{.status.containerStatuses[0].state}' 2>/dev/null | grep -q running; do
        [ "$(echo "$(now) - $t0 > 600" | bc)" = 1 ] && fail "restore pod not running after 600s"
        sleep 1
    done
    t1=$(now); echo "  pod create -> container running    $(dt $t0 $t1)s"
    x_dst python3 /ckpt/dump_evict.py $BASEDIR | sed "s/^/  /"
    if [ -n "${CACHE_HOSTPATH:-}" ]; then
        x_dst python3 /ckpt/dump_evict.py /cache | sed "s/^/  node cache: /"
        if [ "${PREFETCH:-0}" = 1 ]; then
            s=$(now); x_dst $T cache-prefetch $STORE $D /cache | sed "s/^/  (ahead of time) /"; e=$(now)
            echo "  (ahead of time) prefetch to node cache  $(dt $s $e)s"
            x_dst python3 /ckpt/dump_evict.py /cache >/dev/null  # warm on disk, not in page cache
        fi
    fi
    s=$(now); x_dst tar -C /dev/shm -xf $D/shm.tar || fail "restore /dev/shm"; e=$(now); echo "  /dev/shm restore                   $(dt $s $e)s"
    s=$(now); x_dst $CRIU restore -D $D/img $CRIU_OPTS -d -v4 -o $D/restore.log || { echo "FAIL: criu restore"; x_dst tail -30 $D/restore.log; exit 1; }
    e=$(now); echo "  criu restore (processes + host RAM) $(dt $s $e)s"
    s=$(now); ts x_dst stdbuf -oL $T --gpu-map $D/gpus $CACHE resume $PIDS > $TMP/resume.log ||
        { cat $TMP/resume.log; fail "resume"; }
    e=$(now)
    { awk -v s=$s '{printf "    +%6.1fs %s\n", $1-s, substr($0, index($0,$2))}' $TMP/resume.log | grep -E "restore=|unlock=|load[ :]|remap:|resume:|thawed|err|fail" | sed -E 's/remapped ([0-9]+) mapping.*/remapped \1 mappings .../' | head -24; } || true
    echo "  GPU resume (restore+unlock+load+remap+resume) $(dt $s $e)s"
    until x_dst python3 -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/health', timeout=2)" 2>/dev/null; do
        [ "$(echo "$(now) - $e > 300" | bc)" = 1 ] && fail "no health 300s after resume"
        sleep 0.5
    done
    t_h=$(now); echo "  -> health OK                       $(dt $e $t_h)s"
    s=$(now); AFTER=$(query $DST | python3 -c 'import json,sys; print(json.load(sys.stdin)["text"])'); t_f=$(now)
    echo "  first request                      $(dt $s $t_f)s"
    echo "  TOTAL pod create -> first token     $(dt $t0 $t_f)s"
    if [ "$AFTER" != "$BASE" ]; then diff <(echo "$BASE") <(echo "$AFTER") || true; fail "output differs"; fi
    echo "=== PASS $c: restored pod output matches baseline"
}

a=$SRC b=$DST
for c in $(seq 1 "${CYCLES:-1}"); do
    cycle $a $b $c
    t=$a; a=$b; b=$t
done
