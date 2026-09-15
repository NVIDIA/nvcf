#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Empirical spike: validate that a fresh-pod cold start with the source
# pod's rootfs upperdir replayed is faster than a cache-cold cold start.
#
# This is the multi-GPU path forward (FLEET-FANOUT-DESIGN.md): we don't
# attempt to restore GPU state at all, just replay the rootfs (HF cache,
# torch.compile cache, Triton kernels) so the engine's normal startup
# hits the local cache.
#
# Steps:
#   1. Deploy GOLDEN pod (the workload yaml as-is).
#   2. Wait ready; do a pre-inference to warm caches fully.
#   3. Snapshot the source pod's overlayfs upperdir into a tar on the
#      agent's host, accessible via hostPath at /var/lib/nvsnap/golden/.
#   4. Delete the GOLDEN pod.
#   5. Deploy FRESH pod: same image, NO libnvsnap init containers, NO
#      LD_PRELOAD, with a hostPath volume mounted at /golden and a
#      `tar -xf /golden/<workload>.tar -C /` in the main container's
#      command before exec'ing the engine.
#   6. Time pod-create → first /v1/completions.
#   7. Compare against COLD-START baseline (same fresh-pod yaml minus
#      the tar hydration step).
#
# Usage:
#   ./scripts/spike-rootfs-only.sh vllm-small
#   ./scripts/spike-rootfs-only.sh vllm-8b      # TP=1 baseline
#   ./scripts/spike-rootfs-only.sh vllm-8b-tp2  # multi-GPU, the critical case
#
# Outputs a timing table at the end. Pass criteria: rootfs-only ready
# time < cold-start ready time, AND post-restore /v1/completions returns
# valid output.

set -euo pipefail

WORKLOAD="${1:-}"
if [ -z "$WORKLOAD" ]; then
    echo "usage: $0 <workload>" >&2
    exit 2
fi

NS="nvsnap-system"
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
WORKLOAD_YAML="$PROJECT_ROOT/deploy/k8s/${WORKLOAD}.yaml"
if [ ! -f "$WORKLOAD_YAML" ]; then
    echo "missing manifest: $WORKLOAD_YAML" >&2; exit 1
fi

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'
log()  { echo -e "${GREEN}[INFO]${RESET} $*"; }
warn() { echo -e "${YELLOW}[WARN]${RESET} $*"; }
err()  { echo -e "${RED}[ERROR]${RESET} $*"; }

# Resolve listen port + model id from the workload yaml (same logic as
# our agent-driven e2e driver).
PORT=$( { grep -oE -- '--port [0-9]+' "$WORKLOAD_YAML" || true; } | head -1 | awk '{print $2}')
PORT="${PORT:-8000}"
MODEL=$( { grep -oE -- '--model [^ ]+' "$WORKLOAD_YAML" || true; } | head -1 | awk '{print $2}')
[ -z "$MODEL" ] && MODEL=$( { grep -oE -- '--model-path [^ ]+' "$WORKLOAD_YAML" || true; } | head -1 | awk '{print $2}')
WORKLOAD_NODE=$(grep -E '^  nodeName:' "$WORKLOAD_YAML" | head -1 | awk '{print $2}')

AGENT_POD=$(kubectl -n "$NS" get pods -l app=nvsnap-agent --field-selector spec.nodeName="$WORKLOAD_NODE" -o jsonpath='{.items[0].metadata.name}')
[ -z "$AGENT_POD" ] && { err "no nvsnap-agent pod on node $WORKLOAD_NODE"; exit 1; }

log "Workload=$WORKLOAD  model=$MODEL  port=$PORT  node=$WORKLOAD_NODE  agent=$AGENT_POD"

# Re-use the agent's existing checkpoints hostPath to avoid adding a new mount.
# Inside the agent: /var/lib/nvsnap/checkpoints  ↔  Host: /var/lib/containerd/nvsnap-checkpoints
GOLDEN_DIR_AGENT="/var/lib/nvsnap/checkpoints/golden"
GOLDEN_TAR_AGENT="${GOLDEN_DIR_AGENT}/${WORKLOAD}.tar"
GOLDEN_DIR_HOST="/var/lib/containerd/nvsnap-checkpoints/golden"
GOLDEN_TAR_HOST="${GOLDEN_DIR_HOST}/${WORKLOAD}.tar"

POD_NAME="$WORKLOAD"
FRESH_NAME="${WORKLOAD}-rootfs-only"

# ─── Step 1: cleanup any prior run ───────────────────────────────────
# Modes:
#   SKIP_SNAPSHOT=1 — assume tars already on agent; only cleanup FRESH and
#                     GOLDEN, then jump straight to Step 5 (fastest).
#   REUSE_GOLDEN=1  — keep a ready GOLDEN; only cleanup FRESH; rerun snapshot.
#   default         — full pipeline: cleanup all, redeploy GOLDEN, capture,
#                     then FRESH.
if [ "${SKIP_SNAPSHOT:-0}" = "1" ]; then
    log "SKIP_SNAPSHOT=1 → cleaning FRESH + any stale GOLDEN; tars must be on disk"
    kubectl -n "$NS" delete pod "$POD_NAME" "$FRESH_NAME" --grace-period=10 --wait=true --ignore-not-found >/dev/null 2>&1 || true
    T_GOLDEN_START=$(date +%s)
    T_GOLDEN_READY=$T_GOLDEN_START
elif [ "${REUSE_GOLDEN:-0}" = "1" ] && \
    kubectl -n "$NS" get pod "$POD_NAME" >/dev/null 2>&1 && \
    [ "$(kubectl -n "$NS" get pod "$POD_NAME" -o jsonpath='{.status.containerStatuses[0].ready}')" = "true" ]; then
    log "REUSE_GOLDEN=1 → keeping existing $POD_NAME (already ready)"
    kubectl -n "$NS" delete pod "$FRESH_NAME" --grace-period=10 --wait=true --ignore-not-found >/dev/null 2>&1 || true
    kubectl -n "$NS" exec "$AGENT_POD" -- sh -c "mkdir -p $GOLDEN_DIR_AGENT && rm -f $GOLDEN_TAR_AGENT" >/dev/null 2>&1 || true
    T_GOLDEN_START=$(date +%s)
    T_GOLDEN_READY=$T_GOLDEN_START
else
    log "Cleaning prior pods + golden tar (waiting for actual deletion)"
    kubectl -n "$NS" delete pod "$POD_NAME" "$FRESH_NAME" --grace-period=10 --wait=true --ignore-not-found >/dev/null 2>&1 || true
    kubectl -n "$NS" exec "$AGENT_POD" -- sh -c "mkdir -p $GOLDEN_DIR_AGENT && rm -f $GOLDEN_TAR_AGENT" >/dev/null 2>&1 || true

    # ─── Step 2: deploy GOLDEN, wait ready, warm caches ──────────────────
    log "Step 2: deploy GOLDEN pod"
    T_GOLDEN_START=$(date +%s)
    kubectl apply -f "$WORKLOAD_YAML" >/dev/null
    if ! kubectl wait --for=condition=ready pod/"$POD_NAME" -n "$NS" --timeout=900s >/dev/null 2>&1; then
        err "golden pod did not become ready in 900s"; exit 1
    fi
    T_GOLDEN_READY=$(date +%s)
    log "  ready in $((T_GOLDEN_READY - T_GOLDEN_START))s"
fi

if [ "${SKIP_SNAPSHOT:-0}" != "1" ]; then
    POD_IP=$(kubectl -n "$NS" get pod "$POD_NAME" -o jsonpath='{.status.podIP}')
    log "Step 2.5: warm caches via /v1/completions"
    kubectl -n "$NS" exec "$AGENT_POD" -- curl -sS --retry 3 --retry-delay 10 --retry-all-errors -m 120 \
        -X POST "http://${POD_IP}:${PORT}/v1/completions" \
        -H 'Content-Type: application/json' \
        -d "{\"model\":\"$MODEL\",\"prompt\":\"warm\",\"max_tokens\":5}" >/dev/null
    log "  caches warm"
fi

# ─── Step 3: snapshot the rootfs upperdir to /var/lib/nvsnap/golden/X.tar ──
if [ "${SKIP_SNAPSHOT:-0}" = "1" ]; then
    log "SKIP_SNAPSHOT=1 → reusing existing tars on agent disk"
    kubectl -n "$NS" exec "$AGENT_POD" -- test -f "$GOLDEN_TAR_AGENT" || {
        err "SKIP_SNAPSHOT=1 but $GOLDEN_TAR_AGENT does not exist on agent"; exit 1
    }
fi

if [ "${SKIP_SNAPSHOT:-0}" != "1" ]; then
log "Step 3: snapshot source pod's overlay upperdir"
SOURCE_PID=$(kubectl -n "$NS" exec "$AGENT_POD" -- \
    sh -c "ps -ef | grep -E 'vllm serve|sglang.launch_server|trtllm-serve|start_server|/opt/nim' | grep -v grep | awk '{print \$2}' | head -1")
[ -z "$SOURCE_PID" ] && { err "could not resolve source PID"; exit 1; }
log "  source PID=$SOURCE_PID"

UPPERDIR=$(kubectl -n "$NS" exec "$AGENT_POD" -- \
    sh -c "awk '\$5==\"/\" && \$0 ~ /upperdir=/ {match(\$0, /upperdir=[^,]*/); print substr(\$0, RSTART+9, RLENGTH-9); exit}' /proc/$SOURCE_PID/mountinfo")
[ -z "$UPPERDIR" ] && { err "could not resolve overlay upperdir for PID $SOURCE_PID"; exit 1; }
log "  upperdir=$UPPERDIR"

# Exclude paths that are CDI / K8s / runtime bind-mounts on the destination
# pod — those are read-only RW-bound and `tar -x` would fail trying to
# overwrite them. Build the exclude list from the source pod's mountinfo
# (every non-"/" entry is a bind/runtime injection — same logic the agent
# uses for its rootfs mirror).
EXCLUDES=$(kubectl -n "$NS" exec "$AGENT_POD" -- \
    sh -c "awk -v pid=$SOURCE_PID '\$5 != \"/\" && \$5 != \"\" {print \$5}' /proc/$SOURCE_PID/mountinfo | sort -u")
EXCLUDE_ARGS=""
for mp in $EXCLUDES; do
    # tar archive is rooted at "."; entries inside are "./<mp>"
    EXCLUDE_ARGS+=" --exclude=.${mp} --exclude=.${mp}/*"
done

T_SNAPSHOT_START=$(date +%s)
kubectl -n "$NS" exec "$AGENT_POD" -- \
    sh -c "tar -C $UPPERDIR -cf $GOLDEN_TAR_AGENT --xattrs --xattrs-exclude=trusted.overlay.* --acls --numeric-owner --sparse --one-file-system $EXCLUDE_ARGS ."
T_SNAPSHOT_END=$(date +%s)
TAR_BYTES=$(kubectl -n "$NS" exec "$AGENT_POD" -- stat -c%s "$GOLDEN_TAR_AGENT")
log "  rootfs snapshot $((TAR_BYTES / 1024 / 1024)) MiB in $((T_SNAPSHOT_END - T_SNAPSHOT_START))s (excluded $(echo "$EXCLUDES" | wc -l) bind mounts)"
fi  # end SKIP_SNAPSHOT guard for Step 3

# ─── Step 3.5: capture user-data volumes (hostPath + emptyDir) ──────
# Some workloads use hostPath (vllm-70b: /root/.cache/huggingface) to share
# a cache across pod restarts on the same node. Others use emptyDir (NIM:
# /opt/nim/.cache) for an ephemeral per-pod cache. In BOTH cases, the
# cached content lives OUTSIDE the pod's overlayfs upperdir, so the rootfs
# tar above doesn't contain it. To get honest cross-node fan-out we
# capture each such volume separately and replay it into the FRESH pod.
#
# The host-side path of an emptyDir is well-known:
#     /var/lib/kubelet/pods/<pod-uid>/volumes/kubernetes.io~empty-dir/<volname>/
# We resolve <pod-uid> from kubectl after GOLDEN is ready.
#
# Heuristic for which volumes to capture: based on the MOUNT PATH in the
# container, not the volume type. NvSnap's tooling mounts at /checkpoints,
# /nvsnap-lib, /nvsnap. Workload caches live everywhere else. SHM/dev/sys/proc
# and runtime tooling are skipped.
if [ "${SKIP_SNAPSHOT:-0}" = "1" ]; then
    POD_UID="<skipped>"
else
    POD_UID=$(kubectl -n "$NS" get pod "$POD_NAME" -o jsonpath='{.metadata.uid}')
    [ -z "$POD_UID" ] && { err "could not resolve pod UID for $POD_NAME"; exit 1; }
fi

VOLUME_CAPTURES=$(python3 - "$WORKLOAD_YAML" <<'PYEOF'
import sys, yaml
with open(sys.argv[1]) as f:
    p = yaml.safe_load(f)
mounts = {m['name']: m['mountPath']
          for c in p['spec'].get('containers', [])
          for m in c.get('volumeMounts', [])}

SKIP_MOUNTS = ('/checkpoints', '/nvsnap-lib', '/nvsnap-system', '/nvsnap')
SKIP_PREFIXES = ('/dev/', '/sys', '/proc', '/run/', '/etc/')

for v in p['spec'].get('volumes', []):
    if 'hostPath' in v:
        vtype = 'hostPath'
    elif 'emptyDir' in v:
        # /dev/shm is typically an emptyDir(medium=Memory) — never a workload cache.
        if v.get('emptyDir', {}).get('medium') == 'Memory':
            continue
        vtype = 'emptyDir'
    else:
        continue
    mount_path = mounts.get(v['name'])
    if not mount_path:
        continue
    if mount_path in SKIP_MOUNTS or any(mount_path.startswith(s + '/') for s in SKIP_MOUNTS):
        continue
    if mount_path.startswith(SKIP_PREFIXES):
        continue
    # For hostPath, encode the host path. For emptyDir, the host path is
    # derived from pod UID at capture time — pass a placeholder.
    src = v['hostPath']['path'] if vtype == 'hostPath' else '<emptyDir>'
    print(f"{v['name']}\t{vtype}\t{src}\t{mount_path}")
PYEOF
)
declare -A VOLUME_TYPE         # volname -> hostPath | emptyDir
declare -A VOLUME_HOSTPATH     # volname -> host filesystem path (hostPath only)
declare -A VOLUME_MOUNT        # volname -> destination mountPath in container
declare -A VOLUME_TAR_AGENT    # volname -> path of tar inside agent
declare -A VOLUME_TAR_HOST     # volname -> path of tar on host

if [ -n "$VOLUME_CAPTURES" ]; then
    if [ "${SKIP_SNAPSHOT:-0}" != "1" ]; then
        log "Step 3.5: capture user-data volumes (hostPath + emptyDir)"
    else
        log "Step 3.5: SKIP_SNAPSHOT=1 → registering existing volume tars (no capture)"
    fi
    while IFS=$'\t' read -r volname vtype src_path mountpath; do
        [ -z "$volname" ] && continue
        SAFE=${volname//\//_}
        TAR_AGENT="${GOLDEN_DIR_AGENT}/${WORKLOAD}.${SAFE}.tar"
        TAR_HOST="${GOLDEN_DIR_HOST}/${WORKLOAD}.${SAFE}.tar"
        if [ "${SKIP_SNAPSHOT:-0}" = "1" ]; then
            kubectl -n "$NS" exec "$AGENT_POD" -- test -f "$TAR_AGENT" || {
                err "SKIP_SNAPSHOT=1 but $TAR_AGENT missing"; exit 1
            }
        else
            # Resolve the source filesystem path from the agent's view of the host.
            if [ "$vtype" = "hostPath" ]; then
                HOST_SRC="$src_path"
            else
                # emptyDir: kubelet stores at this canonical path
                HOST_SRC="/var/lib/kubelet/pods/${POD_UID}/volumes/kubernetes.io~empty-dir/${volname}"
            fi
            log "  capturing volume $volname ($vtype)  src=$HOST_SRC  mount=$mountpath"
            T_HP_START=$(date +%s)
            kubectl -n "$NS" exec "$AGENT_POD" -- \
                sh -c "test -d /proc/1/root${HOST_SRC} || { echo 'src dir missing: ${HOST_SRC}'; exit 1; }; \
                       tar -C /proc/1/root${HOST_SRC} -cf $TAR_AGENT --xattrs --acls --numeric-owner --sparse . 2>&1 | tail -5"
            T_HP_END=$(date +%s)
            HP_BYTES=$(kubectl -n "$NS" exec "$AGENT_POD" -- stat -c%s "$TAR_AGENT" 2>/dev/null || echo 0)
            log "    captured $((HP_BYTES / 1024 / 1024)) MiB in $((T_HP_END - T_HP_START))s"
        fi
        VOLUME_TYPE[$volname]=$vtype
        VOLUME_HOSTPATH[$volname]=$src_path
        VOLUME_MOUNT[$volname]=$mountpath
        VOLUME_TAR_AGENT[$volname]=$TAR_AGENT
        VOLUME_TAR_HOST[$volname]=$TAR_HOST
    done <<< "$VOLUME_CAPTURES"
fi

# ─── Step 4: delete GOLDEN, simulate cache-cold node, wait for GPU ──
if [ "${SKIP_SNAPSHOT:-0}" = "1" ]; then
    log "Step 4: SKIP_SNAPSHOT=1 → no GOLDEN to delete; checking GPU mem"
else
log "Step 4: delete GOLDEN pod (waiting for GPU memory to free)"
kubectl -n "$NS" delete pod "$POD_NAME" --grace-period=30 --wait=true >/dev/null 2>&1 || true

# Simulate cross-node fan-out by removing the hostPath cache contents we
# just captured. On a real fresh destination node, those paths would be
# empty. emptyDir volumes don't need wiping — kubelet creates a fresh
# emptyDir per pod, so they're already cache-cold by construction.
for volname in "${!VOLUME_TYPE[@]}"; do
    [ "${VOLUME_TYPE[$volname]}" = "hostPath" ] || continue
    HP="${VOLUME_HOSTPATH[$volname]}"
    log "  simulating fresh node: rm -rf ${HP}/* on source node"
    kubectl -n "$NS" exec "$AGENT_POD" -- \
        sh -c "rm -rf /proc/1/root${HP}/* /proc/1/root${HP}/.* 2>/dev/null || true"
done
fi  # end SKIP_SNAPSHOT guard for Step 4 GOLDEN delete
# Even after `kubectl delete --wait`, GPU drivers can take a few seconds
# to actually release the device memory. Poll until free memory ≥ what
# the FRESH pod will request (we use 0.6 utilization = ~47 GiB per GPU
# for 8B TP=2, ~71 GiB for 70B TP=4).
log "  waiting for GPU memory free..."
for try in $(seq 1 60); do
    FREE_MIB=$(kubectl -n "$NS" exec "$AGENT_POD" -- \
        nsenter -t 1 -m -- env LD_LIBRARY_PATH=/run/nvidia/driver/usr/lib/x86_64-linux-gnu \
        /run/nvidia/driver/usr/bin/nvidia-smi --query-gpu=memory.free \
        --format=csv,noheader,nounits 2>/dev/null | sort -n | head -1 | tr -d ' ')
    if [ -n "$FREE_MIB" ] && [ "$FREE_MIB" -gt 60000 ]; then
        log "  GPU min free = ${FREE_MIB} MiB after ${try}s"
        break
    fi
    sleep 1
done

# ─── Step 5: build + apply FRESH yaml ────────────────────────────────
# Generate a "rootfs-only" yaml by transforming the source workload yaml:
#   - Rename pod
#   - Drop libnvsnap init containers
#   - Drop LD_PRELOAD env var
#   - Add /golden hostPath volume mounted into main container
#   - Prepend `tar -xf /golden/<workload>.tar -C /` to main container command
log "Step 5: generate + apply FRESH (rootfs-only) yaml"
FRESH_YAML="/tmp/${FRESH_NAME}.yaml"

# Build a tab-separated list of "volname<TAB>tarpath<TAB>mountpath" for the
# Python generator to inject hydration init containers per captured volume.
HP_REPLAY_SPEC=""
for volname in "${!VOLUME_TAR_HOST[@]}"; do
    HP_REPLAY_SPEC+="${volname}\t${WORKLOAD}.${volname//\//_}.tar\t${VOLUME_MOUNT[$volname]}\n"
done

python3 - "$WORKLOAD_YAML" "$FRESH_NAME" "$WORKLOAD" "$FRESH_YAML" "$HP_REPLAY_SPEC" <<'PYEOF'
import sys, re, yaml
src, fresh_name, workload, out, hp_spec = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5]
with open(src) as f:
    docs = list(yaml.safe_load_all(f))
pod = docs[0]
pod['metadata']['name'] = fresh_name
labels = pod['metadata'].setdefault('labels', {})
labels['app'] = fresh_name
labels['nvsnap.io/mode'] = 'rootfs-only'

spec = pod['spec']
# Drop libnvsnap init containers (we don't want intercept).
spec['initContainers'] = [
    c for c in spec.get('initContainers', [])
    if c['name'] not in {'get-uvloop', 'get-libuv', 'get-libzmq', 'get-intercept-lib'}
]

# Add golden hostPath volume.
volumes = spec.setdefault('volumes', [])
volumes.append({'name': 'golden', 'hostPath': {'path': '/var/lib/containerd/nvsnap-checkpoints/golden', 'type': 'Directory'}})

# Parse hostPath replay spec — one init container per captured volume to
# hydrate that volume's hostPath from its tar BEFORE the main container
# starts. Each init mounts /golden + the destination volume + extracts
# the tar into the volume's mountPath.
replay_entries = []
for line in hp_spec.replace('\\n', '\n').strip().split('\n'):
    if not line or '\t' not in line:
        continue
    parts = line.split('\t')
    if len(parts) >= 3:
        replay_entries.append((parts[0], parts[1], parts[2]))

for volname, tar_basename, mount_path in replay_entries:
    init_name = f"hydrate-{volname[:40]}"
    spec['initContainers'].append({
        'name': init_name,
        'image': 'busybox:1.36',
        'imagePullPolicy': 'IfNotPresent',
        'command': ['/bin/sh', '-c'],
        'args': [
            f"echo '[spike-init] hydrating volume {volname} from /golden/{tar_basename}...';"
            f" T0=$(date +%s);"
            f" tar -C {mount_path} -xf /golden/{tar_basename} 2>&1 | tail -20 || {{ echo 'hostPath tar failed'; exit 1; }};"
            f" echo \"[spike-init] {volname} hydrated in $(($(date +%s)-T0))s\";"
        ],
        'volumeMounts': [
            {'name': 'golden', 'mountPath': '/golden', 'readOnly': True},
            {'name': volname, 'mountPath': mount_path},
        ],
    })

if not spec['initContainers']:
    del spec['initContainers']

# Modify main container.
main = spec['containers'][0]
main['env'] = [e for e in main.get('env', []) if e.get('name') != 'LD_PRELOAD']
mounts = main.setdefault('volumeMounts', [])
mounts.append({'name': 'golden', 'mountPath': '/golden', 'readOnly': True})

# Hydration command differs per engine:
# - vllm/sglang/trtllm: privileged: true + USER root by image default → can
#   `tar -C /` over the rootfs upperdir to restore Triton/torch.compile/TRT
#   caches that live outside named volumes.
# - NIM: image USER is 1000 (non-root). Even with privileged: true, that
#   process cannot chown/utime/setACL on root-owned mountpoints like
#   /run, /var/cache, /usr/lib/firmware. All NIM warm-start state already
#   lives in /opt/nim/.cache (a separately-captured volume), so skipping
#   the rootfs tar is correct AND necessary.
is_nim = 'nvcr.io/nim/' in main.get('image', '')
if is_nim:
    hydration = (
        f"echo '[spike] NIM detected — skipping rootfs tar (USER 1000 cannot restore root-owned paths);"
        f" warm-start state lives in /opt/nim/.cache volume which init container hydrates';"
        "\n"
    )
else:
    hydration = (
        f"echo '[spike] hydrating rootfs from /golden/{workload}.tar...';"
        f" T0=$(date +%s);"
        f" tar -C / -xf /golden/{workload}.tar --xattrs --xattrs-exclude=trusted.overlay.* --acls --numeric-owner --overwrite 2>/tmp/tar-stderr.log || {{ echo 'tar failed:'; cat /tmp/tar-stderr.log; exit 1; }};"
        f" echo \"[spike] rootfs hydration done in $(($(date +%s)-T0))s\";"
        "\n"
    )
if 'args' in main:
    main['args'] = [hydration + main['args'][0]]
else:
    # No args (NIM uses image ENTRYPOINT). Chain hydration → exec entrypoint.
    main['command'] = ['/bin/bash', '-lc']
    main['args'] = [hydration + " exec /opt/nim/start_server.sh"]

with open(out, 'w') as f:
    yaml.safe_dump(pod, f, default_flow_style=False)
PYEOF

T_FRESH_APPLY=$(date +%s)
kubectl apply -f "$FRESH_YAML" >/dev/null
log "  applied at t=0; waiting ready..."

if ! kubectl wait --for=condition=ready pod/"$FRESH_NAME" -n "$NS" --timeout=900s >/dev/null 2>&1; then
    err "FRESH pod did not become ready in 900s"
    kubectl -n "$NS" describe pod "$FRESH_NAME" 2>&1 | tail -30
    kubectl -n "$NS" logs "$FRESH_NAME" --tail=50 2>&1 | tail -50
    exit 1
fi
T_FRESH_READY=$(date +%s)
FRESH_READY_S=$((T_FRESH_READY - T_FRESH_APPLY))
log "  FRESH ready in ${FRESH_READY_S}s"

# ─── Step 6: post-restore inference ──────────────────────────────────
log "Step 6: post-restore inference"
FRESH_IP=$(kubectl -n "$NS" get pod "$FRESH_NAME" -o jsonpath='{.status.podIP}')
T_INFER_START=$(date +%s)
RESP=$(kubectl -n "$NS" exec "$AGENT_POD" -- curl -sS --retry 3 --retry-delay 5 -m 60 \
    -X POST "http://${FRESH_IP}:${PORT}/v1/completions" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"$MODEL\",\"prompt\":\"Capital of France is\",\"max_tokens\":15}")
T_INFER_END=$(date +%s)
if ! echo "$RESP" | grep -q '"text"'; then
    err "FRESH /v1/completions did not return valid text:"
    echo "$RESP" | head -20
    exit 1
fi
log "  inference OK ($((T_INFER_END - T_INFER_START))s)"
log "  response: $(echo "$RESP" | head -c 200)"

# ─── Summary ─────────────────────────────────────────────────────────
GOLDEN_S=$((T_GOLDEN_READY - T_GOLDEN_START))
SNAPSHOT_S=$(( ${T_SNAPSHOT_END:-0} - ${T_SNAPSHOT_START:-0} ))
TAR_MIB=$(( ${TAR_BYTES:-0} / 1024 / 1024 ))

echo
echo -e "${BOLD}${CYAN}[$WORKLOAD] rootfs-only spike results${RESET}"
echo -e "${CYAN}──────────────────────────────────────────────────${RESET}"
printf "%-32s %5d s\n" "GOLDEN cold start (cache-cold)" "$GOLDEN_S"
printf "%-32s %5d s   (%d MiB)\n" "Snapshot (tar)" "$SNAPSHOT_S" "$TAR_MIB"
printf "%-32s %5d s\n" "FRESH ready (rootfs-only)" "$FRESH_READY_S"
echo -e "${CYAN}──────────────────────────────────────────────────${RESET}"
SPEEDUP=$(awk -v g="$GOLDEN_S" -v f="$FRESH_READY_S" 'BEGIN{ if (f>0) printf "%.2f", g/f; else print "inf" }')
printf "%-32s %5sx\n" "FRESH vs GOLDEN speedup" "$SPEEDUP"
echo
log "PASS: $WORKLOAD rootfs-only restore worked"
