#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# One-downloader election e2e against a stock vLLM chart
# (deploy/k8s/charts/vllm-workers): install N replicas, expect one leader
# and gated followers, the followers released after the promote and Ready
# with no downloads, then uninstall + reinstall with every pod restoring.
# Prints the cold and warm engine phases from the vLLM logs.
#
#   KUBECONFIG=... ./scripts/test-election-e2e.sh [helm --set overrides...]
#   NS=nvsnap-system REL=qwen MODEL=Qwen/Qwen2.5-32B-Instruct ./scripts/test-election-e2e.sh --set tensorParallel=4
#
# Design and recorded results: docs/proposals/helm-chart-cache-election.md
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
CHART=${CHART:-$HERE/../deploy/k8s/charts/vllm-workers}
NS=${NS:-nvsnap-system}; AGENT_NS=${AGENT_NS:-nvsnap-system}; REL=${REL:-vllm-workers}; MODEL=${MODEL:-Qwen/Qwen2.5-32B-Instruct}; REPLICAS=${REPLICAS:-2}
HELM_ARGS=("$@")
k() { timeout 60 kubectl "$@"; }
roles() { k get pods -n $NS -l app=$REL -o json | python3 -c "
import json,sys
for p in json.load(sys.stdin)['items']:
    a=p['metadata'].get('annotations',{}); l=p['metadata'].get('labels',{}); st=p['status']
    ready=any(c['type']=='Ready' and c['status']=='True' for c in st.get('conditions',[]))
    print('   %-22s role=%-8s gated=%-5s gates=%d phase=%-16s ready=%s node=%s hash=%s' % (p['metadata']['name'], a.get('nvsnap.io/role','-'), l.get('nvsnap.io/gated','-'), len(p['spec'].get('schedulingGates',[])), st.get('phase'), ready, p['spec'].get('nodeName','-'), (a.get('nvsnap.io/hash') or '-')[:8]))"; }
agentlog() { for a in $(k get pods -n $AGENT_NS -o name | grep nvsnap-agent); do k logs -n $AGENT_NS $a -c agent --since=${1:-30m} 2>/dev/null | grep -E "$2" | cut -c1-240; done; }
waitfor() { local desc=$1 secs=$2; shift 2; for i in $(seq 1 $((secs/5))); do if eval "$@" >/dev/null 2>&1; then echo "   [$desc] after $((i*5))s"; return 0; fi; sleep 5; done; echo "   [$desc] TIMEOUT ${secs}s"; return 1; }

echo "=== 0. preconditions"; k get ds nvsnap-agent -n $AGENT_NS -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'; k get deploy nvsnap-server -n $AGENT_NS -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
helm uninstall $REL -n $NS >/dev/null 2>&1; sleep 5
echo "=== 1. install replicas=2 at $(date -u +%H:%M:%SZ)"; T0=$(date +%s)
helm install $REL $CHART -n $NS --set replicas=$REPLICAS --set model="$MODEL" "${HELM_ARGS[@]}" >/dev/null || exit 1
sleep 12; roles
echo "--- webhook decisions"; agentlog 5m "election:" | tail -6
echo "--- lease"; k get lease -n $AGENT_NS -l nvsnap.io/kind=capture-election -o custom-columns=NAME:.metadata.name,HOLDER:.spec.holderIdentity,DEADLINE:.metadata.annotations.nvsnap\\.io/deadline --no-headers
LEADER=$(k get pods -n $NS -l app=$REL -o json | python3 -c "import json,sys; print(next((p['metadata']['name'] for p in json.load(sys.stdin)['items'] if p['metadata'].get('annotations',{}).get('nvsnap.io/role')=='leader'),''))")
FOLLOWER=$(k get pods -n $NS -l app=$REL -o json | python3 -c "import json,sys; print(next((p['metadata']['name'] for p in json.load(sys.stdin)['items'] if p['metadata'].get('annotations',{}).get('nvsnap.io/role')=='follower'),''))")
H=$(k get pod $LEADER -n $NS -o jsonpath='{.metadata.annotations.nvsnap\.io/hash}' | cut -c1-8); echo "leader=$LEADER follower=$FOLLOWER hash=$H"; [ -n "$LEADER" ] && [ -n "$FOLLOWER" ] || { echo "ELECTION DID NOT HAPPEN"; exit 1; }
echo "=== 2. leader cold start"; waitfor "leader Ready" 1800 "[ \"\$(k get pod $LEADER -n $NS -o jsonpath='{.status.containerStatuses[0].ready}')\" = true ]"; echo "   leader Ready at +$(( $(date +%s)-T0 ))s"
echo "=== 3. capture + promote"; waitfor "capture committed" 600 "agentlog 20m 'capture committed' | grep -q '$LEADER'"; agentlog 20m "capture committed|L2 promote complete|pvc-state" | grep "$LEADER|$H" | tail -3
echo "=== 4. follower release"; waitfor "follower ungated" 1500 "[ \"\$(k get pod $FOLLOWER -n $NS -o jsonpath='{.spec.schedulingGates}')\" = '' ]"; echo "   follower ungated at +$(( $(date +%s)-T0 ))s"; T1=$(date +%s)
k logs -n $AGENT_NS deploy/nvsnap-server --since=20m 2>/dev/null | grep -E "election" | tail -3 | cut -c1-200
waitfor "follower Ready" 1200 "[ \"\$(k get pod $FOLLOWER -n $NS -o jsonpath='{.status.containerStatuses[0].ready}')\" = true ]"; echo "   follower Ready $(( $(date +%s)-T1 ))s after ungate (leader cold: $(( T1-T0 ))s incl capture)"
roles
echo "--- follower downloaded? (0 expected)"; k logs -n $NS $FOLLOWER 2>/dev/null | grep -ciE "downloading|Fetching .* files"
echo "--- follower inits"; k get pod $FOLLOWER -n $NS -o jsonpath='{range .spec.initContainers[*]}{.name} {end}{"\n"}'
echo "--- follower serves"; k exec -n $NS $FOLLOWER -c vllm -- curl -s -m 60 http://127.0.0.1:8000/v1/completions -H 'Content-Type: application/json' -d '{"model":"'"$MODEL"'","prompt":"The capital of France is","max_tokens":4,"temperature":0}' 2>/dev/null | python3 -c "import json,sys; print('  ', repr(json.load(sys.stdin)['choices'][0]['text']))"
phases() { k logs -n $NS $1 2>/dev/null | grep -E "Starting to load model|Loading weights took|Model loading took|torch.compile takes|compiled graph|Graph capturing finished|init engine|Application startup complete" | sed -E 's/^\(([A-Za-z_0-9]+) pid=[0-9]+\) //' | cut -c1-150 | sed 's/^/      /'; }
echo "--- leader (cold) phases"; phases $LEADER; echo "--- follower (warm) phases"; phases $FOLLOWER
echo "--- capture size"; k get cm -n $NS -l nvsnap.io/kind -o json 2>/dev/null | python3 -c "
import json,sys
for cm in json.load(sys.stdin)['items']:
    for v in cm['data'].values():
        try: d=json.loads(v)
        except Exception: continue
        if d.get('capture_method')=='cachedir' and d.get('hash','').startswith('$H'): print('      hash', d['hash'][:8], 'bytes', d.get('total_size_bytes'), 'files', d.get('file_count'))" 2>/dev/null | head -2
echo "=== 6. reinstall (both restore)"; helm uninstall $REL -n $NS >/dev/null; sleep 20; T3=$(date +%s); helm install $REL $CHART -n $NS --set replicas=$REPLICAS --set model="$MODEL" "${HELM_ARGS[@]}" >/dev/null; sleep 12; roles
waitfor "both Ready" 1200 "[ \"\$(k get pods -n $NS -l app=$REL -o jsonpath='{.items[*].status.containerStatuses[0].ready}')\" = 'true true' ]"; echo "   both Ready $(( $(date +%s)-T3 ))s after reinstall"
echo "=== done $(date -u +%H:%M:%SZ)"
