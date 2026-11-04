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
# cluster-smoketest.sh — reusable multi-node smoketest for nvstor-server.
# Builds, deploys, runs each protocol against a real client, kills a node,
# verifies cluster survives, tears down.
#
# Usage
# -----
#   scripts/cluster-smoketest.sh                 # full run (deploy + test + teardown)
#   scripts/cluster-smoketest.sh deploy          # build + deploy + start, leave running
#   scripts/cluster-smoketest.sh test            # assume cluster up, run tests only
#   scripts/cluster-smoketest.sh failover        # run failover scenario only
#   scripts/cluster-smoketest.sh clean           # stop nvstor-server everywhere + remove state
#   scripts/cluster-smoketest.sh status          # snapshot of each node (Raft, ports, log tail)
#
# Per-protocol toggles (any subset; default = all):
#   PROTOCOLS=nfs,s3,nbd,health   scripts/cluster-smoketest.sh test
#
# Cluster: export NODE1=...  NODE2=...  NODE3=...  INCLUDE_NODE3=1  SSH_USER=...
#   SSH_KEY=..., or put them in a file named by NVSTOR_CLUSTER_ENV
#   (see scripts/cluster-config.sh).
#
# Output: pass/fail per protocol section. Exit 0 = all green.

set -uo pipefail

# Source the shared cluster definition (scripts/cluster-config.sh), which
# reads the NODE1..3 environment (or the NVSTOR_CLUSTER_ENV file).
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=cluster-config.sh
source "$SCRIPT_DIR/cluster-config.sh"

SSH_OPTS="-i $SSH_KEY -o StrictHostKeyChecking=accept-new -o ConnectTimeout=8"
REMOTE_BIN="/tmp/nvstor-server"
REMOTE_DATA="/tmp/nvstor-data"
REMOTE_CONFIG="/tmp/nvstor.toml"
REMOTE_LOG="/tmp/nvstor.log"
REMOTE_LAUNCH="/tmp/nvstor-launch.sh"

# Two nodes by default. A binary built on a newer glibc (e.g. Ubuntu 24,
# glibc 2.39) does not run on an older host (Ubuntu 22, glibc 2.35), so a
# third node is opt-in with INCLUDE_NODE3=1.
NODES=("$NODE1" "$NODE2")
if [ "${INCLUDE_NODE3:-0}" = "1" ]; then
    NODES+=("$NODE3")
fi

PROTOCOLS_REQ="${PROTOCOLS:-nfs,s3,nbd,health}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LOG_DIR="${LOG_DIR:-/tmp/nvstor-smoketest}"
mkdir -p "$LOG_DIR"

MODE="${1:-all}"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

log()  { echo "[$(date '+%H:%M:%S')] $*"; }
fail() { echo "  [FAIL] $*" >&2; FAILED+=("$*"); }
pass() { echo "  [ ok ] $*"; }
skip() { echo "  [skip] $*"; }

want() {
    case ",$PROTOCOLS_REQ," in
        *,$1,*) return 0 ;;
        *)      return 1 ;;
    esac
}

# Run a command on a remote node. The bash script is piped over stdin,
# so it works whatever the remote login shell is. Local-side variable
# expansion happens before piping (so $REMOTE_LOG etc. resolve in this
# script); the remote login shell never sees the script body.
remote() {
    local node="$1"; shift
    printf '%s\n' "$*" | ssh $SSH_OPTS "$SSH_USER@$node" 'bash -s'
}

# Daemonize a single command on a remote node so ssh returns immediately.
remote_start() {
    local node="$1"; shift
    printf 'setsid %s < /dev/null > /dev/null 2>&1 &\n' "$*" \
        | ssh $SSH_OPTS "$SSH_USER@$node" 'bash -s' < /dev/null
}

declare -a FAILED=()

# ---------------------------------------------------------------------------
# Phases
# ---------------------------------------------------------------------------

phase_build() {
    log "[build] cargo build --release --bin nvstor-server"
    cd "$REPO_ROOT"
    cargo build --release --bin nvstor-server > "$LOG_DIR/build.log" 2>&1 \
        || { echo "build failed; see $LOG_DIR/build.log"; exit 2; }
    BINARY="$REPO_ROOT/target/release/nvstor-server"
    [ -x "$BINARY" ] || { echo "binary not at $BINARY"; exit 2; }
    log "  built: $(ls -lh "$BINARY" | awk '{print $5}')"
}

phase_clean() {
    log "[clean] stop nvstor-server + remove state on each node"
    for n in "${NODES[@]}"; do
        # Aggressive cleanup. Two non-obvious bits:
        # 1. Kill stuck mount.nfs4 processes — they pin kernel-side NFS
        #    client state to the (host, port) tuple. A subsequent mount
        #    against the same tuple silently never establishes a new TCP
        #    connection while one is still pending, so the next mount
        #    hangs.
        # 2. Drop dentry/inode caches so any remembered FH handles from a
        #    prior failed mount get released.
        remote "$n" "
            sudo pkill -9 -f nvstor-server 2>/dev/null
            sudo pkill -9 -f 'mount\.nfs' 2>/dev/null
            for m in /mnt/nvstor-test /tmp/nfs-test /tmp/nfs-test2 \
                     /tmp/nvstor-comp-mnt.* /tmp/nvstor-mnt.*; do
                mountpoint -q \$m 2>/dev/null && sudo umount -lf \$m 2>/dev/null
                sudo rmdir \$m 2>/dev/null
            done
            sudo bash -c 'echo 3 > /proc/sys/vm/drop_caches' 2>/dev/null || true
            sleep 1
            sudo rm -rf $REMOTE_DATA $REMOTE_CONFIG $REMOTE_LOG $REMOTE_LAUNCH /tmp/nvstor-debug.log
            mkdir -p $REMOTE_DATA
            echo cleaned $(hostname)
        " 2>&1 | tail -2
    done
}

phase_deploy() {
    log "[deploy] scp binary + per-node configs to ${#NODES[@]} nodes"
    for n in "${NODES[@]}"; do
        scp $SSH_OPTS -q "$BINARY" "$SSH_USER@$n:$REMOTE_BIN" \
            || { echo "scp to $n failed"; exit 2; }
        scp $SSH_OPTS -q "$REPO_ROOT/scripts/volume.py" "$SSH_USER@$n:/tmp/nvstor-volume.py" \
            || { echo "volume tool copy failed"; exit 2; }
    done

    seeds_for() {
        local self_idx="$1"
        for i in "${!NODES[@]}"; do
            [ "$i" = "$self_idx" ] && continue
            echo "    \"${NODES[$i]}:9102\","
        done
    }

    for i in "${!NODES[@]}"; do
        node_id=$((i + 1))
        node_ip="${NODES[$i]}"
        local is_bootstrap="false"
        [ "$node_id" = "1" ] && is_bootstrap="true"
        local cfg="$LOG_DIR/node${node_id}.toml"
        cat > "$cfg" <<EOF
[node]
id = ${node_id}
name = "nvstor-${node_id}"

[cluster]
name = "smoketest-cluster"
bootstrap = ${is_bootstrap}
seeds = [
$(seeds_for $i)
]

[storage]
data_path = "${REMOTE_DATA}"
chunk_size = 4194304
direct_io = true
verify_checksums = true
gc_enabled = false

[network]
grpc_port = 9002
http_port = 9101
raft_port = 9102
s3_port = 9000
nbd_port = 10809
nfs_port = 12049

[nfs]
# The smoketest mounts the export on NODE1 over 127.0.0.1; the node
# addresses are listed too. NODE* must be IP addresses here.
allow_clients = [$(for n in "${NODES[@]}"; do printf '"%s", ' "$n"; done)"127.0.0.1"]

[auth]
# The s3-rdma / s3-tcp / RPC transports authenticate every request.
# Without a credential here the server refuses all of them, so the
# smoketest and perf runs would fail with STATUS_UNAUTHORIZED.
access_key = "${NVSTOR_ACCESS_KEY}"
secret_key = "${NVSTOR_SECRET_KEY}"

[logging]
level = "info"
format = "json"
EOF
        scp $SSH_OPTS -q "$cfg" "$SSH_USER@$node_ip:$REMOTE_CONFIG"
    done

    log "[deploy] start nvstor-server on each node"
    # Launcher is a wrapper that double-forks via setsid and writes startup
    # markers so we can tell whether nvstor-server actually came up vs. the
    # daemon channel detaching cleanly but the process dying.
    local launcher="$LOG_DIR/nvstor-launch.sh"
    cat > "$launcher" <<EOF
#!/usr/bin/env bash
# Run as 'bash nvstor-launch.sh' or invoked via systemd-style daemon.
# This wrapper backgrounds nvstor-server and returns immediately; the
# server logs to $REMOTE_LOG.
#
# ulimit -n 1048576: the server holds many file descriptors at once
# (concurrent objects and volumes, client connections, RocksDB files),
# more than a default limit of 1024 allows.
echo "\$(date '+%H:%M:%S') launcher start (pid \$\$)" >> $REMOTE_LOG
ulimit -n 1048576
nohup setsid $REMOTE_BIN --config $REMOTE_CONFIG >> $REMOTE_LOG 2>&1 < /dev/null &
disown \$!
sleep 0.2
EOF
    chmod +x "$launcher"
    for i in "${!NODES[@]}"; do
        node_ip="${NODES[$i]}"
        scp $SSH_OPTS -q "$launcher" "$SSH_USER@$node_ip:$REMOTE_LAUNCH"
        # Run the launcher synchronously (it backgrounds the actual server).
        remote "$node_ip" "bash $REMOTE_LAUNCH"
    done
    sleep 3

    log "[deploy] wait for /healthz on each node (up to 30 s)"
    for i in "${!NODES[@]}"; do
        node_ip="${NODES[$i]}"
        local up=0
        for try in $(seq 1 30); do
            if curl -sf -o /dev/null -m 2 "http://${node_ip}:9101/healthz" 2>/dev/null; then
                pass "node $((i+1)) (${node_ip}) healthy"
                up=1
                break
            fi
            sleep 1
        done
        [ "$up" = "1" ] || fail "node $((i+1)) (${node_ip}) never became healthy"
    done
}

phase_status() {
    log "[status] cluster snapshot"
    for i in "${!NODES[@]}"; do
        node_ip="${NODES[$i]}"
        echo "  --- node $((i+1)) (${node_ip}) ---"
        remote "$node_ip" "
            ss -tn | awk '/(9000|9002|9101|9102|10809|12049)/ {print \"    listen \"\$0}' | head -8
            echo '    --- /healthz ---'
            curl -sf -m 2 http://${node_ip}:9101/healthz 2>&1 | head -3 | sed 's/^/      /'
            echo '    --- /metrics raft+nfs subset ---'
            curl -sf -m 2 http://${node_ip}:9101/metrics 2>/dev/null \
                | grep -E '^nvstor_(raft|nfs|node)' | head -10 | sed 's/^/      /'
            echo '    --- log tail ---'
            tail -5 $REMOTE_LOG 2>/dev/null | sed 's/^/      /'
        " 2>&1 | tail -25
    done
}

phase_test_health() {
    log "[test:health] /healthz on every node"
    for i in "${!NODES[@]}"; do
        node_ip="${NODES[$i]}"
        if curl -sf -m 3 "http://${node_ip}:9101/healthz" >/dev/null 2>&1; then
            pass "/healthz node $((i+1))"
        else
            fail "/healthz node $((i+1))"
        fi
    done
}

phase_test_nfs() {
    log "[test:nfs] mount + write + read + umount on ${NODE1}"
    # Use a fresh mountpoint each run so leftover kernel-side state on a
    # path we've used before doesn't poison this attempt. nfsvers=4.1 is
    # explicit because some kernels otherwise probe v4.2 → fall back, and
    # the fallback can leave a wedged in-progress mount on the same path.
    remote "$NODE1" "
        sudo pkill -9 -f 'mount\.nfs' 2>/dev/null
        MNT=\$(mktemp -d -t nvstor-nfstest.XXXXXX)
        sudo timeout 12 mount -t nfs4 \
            -o port=12049,proto=tcp,rw,soft,timeo=50,sec=sys,nfsvers=4.1 \
            127.0.0.1:/ \$MNT
        rc=\$?
        echo \"mount exit=\$rc mountpoint=\$MNT\"
        if [ \$rc -eq 0 ] && mountpoint -q \$MNT; then
            sudo bash -c \"echo cluster-test > \$MNT/smoke.txt\"
            sudo cat \$MNT/smoke.txt 2>&1 | head -1
            sudo rm -f \$MNT/smoke.txt
            sudo umount -lf \$MNT
        fi
        rmdir \$MNT 2>/dev/null
    " 2>&1 | tee /tmp/nvstor-smoketest/nfs-attempt.log | grep -E "mount exit|cluster-test" | while read line; do
        if echo "$line" | grep -q "mount exit=0" ; then pass "NFS mount"
        elif echo "$line" | grep -q "mount exit=" ; then fail "NFS mount: $line"
        elif echo "$line" | grep -q "cluster-test" ; then pass "NFS read-after-write"
        fi
    done
    # Surface mount failures by re-checking the marker file in the
    # captured output — `while read` runs in a subshell so its FAILED+=
    # additions don't propagate.
    if grep -q 'mount exit=0' /tmp/nvstor-smoketest/nfs-attempt.log; then
        :
    else
        FAILED+=("NFS mount did not return exit 0")
    fi
}

phase_test_s3() {
    log "[test:s3] real PUT/GET/DELETE round-trip + replication fanout"
    # The smoketest config disables S3 auth (no IAM users, no creds in
    # config), so plain curl works. Real production deploys would use
    # signed requests via aws-cli or boto3.
    local bucket="smoketest-bucket"
    local key="smoke.txt"
    local body="cluster-replication-proof"

    # 1. Endpoint up?
    local code
    code=$(curl -sf -m 5 -o /dev/null -w '%{http_code}' "http://${NODE1}:9000/" 2>/dev/null || echo "fail")
    case "$code" in
        200|403|404) pass "S3 endpoint responds (${code})" ;;
        *)           fail "S3 endpoint returned ${code}"; return ;;
    esac

    # 2. Create bucket on node 1.
    if curl -sf -m 5 -X PUT -o /dev/null "http://${NODE1}:9000/${bucket}"; then
        pass "S3 bucket PUT on node 1"
    else
        fail "S3 bucket PUT failed"; return
    fi

    # 3. Bucket fanout: with replication wired, the bucket
    # should exist on every node. The TCP-only RPC path's
    # replicate_bucket_put is what we're proving here.
    local fanout_ok=1
    for i in "${!NODES[@]}"; do
        local n="${NODES[$i]}"
        if curl -sf -m 5 -o /dev/null "http://${n}:9000/${bucket}"; then
            pass "S3 bucket visible on node $((i+1)) (${n})"
        else
            fail "S3 bucket NOT visible on node $((i+1)) — replicate_bucket_put didn't fan out"
            fanout_ok=0
        fi
    done
    [ "$fanout_ok" = "1" ] || return

    # 4. PUT object on node 1.
    if curl -sf -m 5 -X PUT -o /dev/null --data-binary "$body" "http://${NODE1}:9000/${bucket}/${key}"; then
        pass "S3 object PUT on node 1"
    else
        fail "S3 object PUT failed"; return
    fi

    # 5. GET back, verify body matches.
    local got
    got=$(curl -sf -m 5 "http://${NODE1}:9000/${bucket}/${key}" 2>/dev/null || echo "")
    if [ "$got" = "$body" ]; then
        pass "S3 object GET matches"
    else
        fail "S3 object GET mismatch (got=${got:0:80})"
    fi

    # 6. DELETE — exercises replicate_delete.
    if curl -sf -m 5 -X DELETE -o /dev/null "http://${NODE1}:9000/${bucket}/${key}"; then
        pass "S3 object DELETE on node 1"
    else
        fail "S3 object DELETE failed"
    fi

    # 7. After DELETE, GET on every node must 404. If any node still
    # has the object, replicate_delete didn't fan out — a node would
    # keep serving a deleted object. This assertion catches that.
    sleep 0.5  # give async fanout a moment
    for i in "${!NODES[@]}"; do
        local n="${NODES[$i]}"
        local code
        code=$(curl -s -m 5 -o /dev/null -w '%{http_code}' "http://${n}:9000/${bucket}/${key}" 2>/dev/null || echo "fail")
        if [ "$code" = "404" ]; then
            pass "S3 object gone on node $((i+1)) after DELETE"
        else
            fail "S3 object still on node $((i+1)) after DELETE (${code}) — replicate_delete didn't fan out"
        fi
    done

    # 8. Bucket cleanup.
    curl -sf -m 5 -X DELETE -o /dev/null "http://${NODE1}:9000/${bucket}" 2>/dev/null || true
}

phase_test_nbd() {
    log "[test:nbd] handshake magic on ${NODE1}:10809"
    # Python probe — reliable across ssh stdin piping. Reads the full
    # 18-byte fixed-newstyle greeting and validates NBDMAGIC + IHAVEOPT.
    local out
    out=$(remote "$NODE1" "
        python3 - <<'PY'
import socket, struct, sys
s = socket.create_connection(('127.0.0.1', 10809), timeout=5)
data = b''
while len(data) < 18:
    chunk = s.recv(18 - len(data))
    if not chunk: sys.exit(2)
    data += chunk
nbdmagic, ihaveopt, _ = struct.unpack('>QQH', data)
ok = nbdmagic == 0x4E42444D41474943 and ihaveopt == 0x49484156454F5054
print('OK' if ok else 'BAD')
sys.exit(0 if ok else 1)
PY
" 2>&1)
    if echo "$out" | grep -q "^OK$"; then
        pass "NBD greeting (NBDMAGIC + IHAVEOPT)"
    else
        fail "NBD greeting (got: ${out})"
    fi

    log "[test:nbd-client] real nbd-client round-trip on ${NODE1}"
    # Skip if nbd-client not installed — it's not on every box.
    local has_client
    has_client=$(remote "$NODE1" 'which nbd-client 2>/dev/null || true' 2>&1 \
        | grep -E '^/.+/nbd-client$' || true)
    if [ -z "$has_client" ]; then
        skip "nbd-client not installed on ${NODE1}; install with apt to enable"
        return
    fi

    # Run the round-trip remotely. We connect a free /dev/nbdN, write a
    # small payload, disconnect, reconnect, verify the bytes are still
    # there. This tests the nvstor-server NBD persistence path
    # (handshake → option negotiation → block read/write → disconnect).
    local script
    script=$(cat <<'NBDSH'
set -uo pipefail
DEV=""
for i in 0 1 2 3 4 5 6 7; do
    if ! grep -q "^nbd${i} " /proc/devices 2>/dev/null; then continue; fi
    if [ -e /dev/nbd${i} ] && ! sudo nbd-client -c /dev/nbd${i} >/dev/null 2>&1; then
        DEV=/dev/nbd${i}; break
    fi
done
if [ -z "$DEV" ]; then
    sudo modprobe nbd 2>/dev/null || true
    DEV=/dev/nbd0
fi
echo "device=$DEV"

VOL=$(python3 /tmp/nvstor-volume.py create --server 127.0.0.1:9002 --size 1073741824) || exit 2
# NBD exports use the volume provisioned by the native API.
sudo nbd-client -N "$VOL" 127.0.0.1 10809 "$DEV" -timeout 10 2>&1 | tail -3
echo "connected=$?"

# Quick sanity: device exists and reports a non-zero size.
SIZE=$(sudo blockdev --getsize64 "$DEV" 2>/dev/null || echo 0)
echo "size=$SIZE"
[ "$SIZE" -gt 0 ] || { echo "BAD: zero size"; sudo nbd-client -d "$DEV"; exit 2; }

# Write a small magic at offset 0; flush; disconnect; reconnect; read back.
echo -n "NVSTOR-NBD-SMOKE-OK" | sudo dd of="$DEV" bs=1 count=20 conv=notrunc status=none
sudo blockdev --flushbufs "$DEV"
sudo nbd-client -d "$DEV" >/dev/null 2>&1
sleep 0.5
sudo nbd-client -N "$VOL" 127.0.0.1 10809 "$DEV" -timeout 10 >/dev/null 2>&1
GOT=$(sudo dd if="$DEV" bs=1 count=20 status=none 2>/dev/null)
echo "readback=$GOT"
sudo nbd-client -d "$DEV" >/dev/null 2>&1
[ "$GOT" = "NVSTOR-NBD-SMOKE-OK" ] && echo "ROUND_TRIP=OK" || echo "ROUND_TRIP=BAD"
NBDSH
)
    local out
    out=$(remote "$NODE1" "$script" 2>&1)
    if echo "$out" | grep -q "ROUND_TRIP=OK"; then
        pass "NBD nbd-client round-trip (write → disconnect → reconnect → read)"
    else
        fail "NBD nbd-client round-trip: $(echo "$out" | tr '\n' ' ' | head -c 200)"
    fi
}

# (Second copy of phase_test_nbd was a stale duplicate; removed.)

phase_failover() {
    if [ "${#NODES[@]}" -lt 2 ]; then
        skip "failover requires ≥2 nodes"
        return
    fi
    local last_idx=$((${#NODES[@]} - 1))
    local last_node="${NODES[$last_idx]}"
    log "[failover] kill node $((last_idx+1)) (${last_node})"
    remote "$last_node" "sudo pkill -9 -f nvstor-server"
    sleep 3

    log "[failover] survivors must stay healthy"
    for i in $(seq 0 $((last_idx - 1))); do
        node_ip="${NODES[$i]}"
        if curl -sf -m 3 "http://${node_ip}:9101/healthz" >/dev/null 2>&1; then
            pass "survivor /healthz node $((i+1))"
        else
            fail "survivor /healthz node $((i+1))"
        fi
    done
}

phase_test() {
    want health && phase_test_health
    want nfs    && phase_test_nfs
    want s3     && phase_test_s3
    want nbd    && phase_test_nbd
}

phase_report() {
    echo
    echo "============================================="
    if [ "${#FAILED[@]}" -gt 0 ]; then
        echo "  FAILED (${#FAILED[@]})"
        for f in "${FAILED[@]}"; do echo "    - $f"; done
        echo "============================================="
        exit 1
    fi
    echo "  ALL CHECKS PASSED"
    echo "============================================="
}

# ---------------------------------------------------------------------------
# Mode dispatch
# ---------------------------------------------------------------------------

case "$MODE" in
    all)
        phase_build
        phase_clean
        phase_deploy
        phase_test
        phase_failover
        phase_clean   # tear down
        phase_report
        ;;
    deploy)
        phase_build
        phase_clean
        phase_deploy
        ;;
    test)
        phase_test
        phase_report
        ;;
    failover)
        phase_failover
        phase_report
        ;;
    clean)
        phase_clean
        ;;
    status)
        phase_status
        ;;
    *)
        echo "unknown mode: $MODE"
        echo "usage: $0 [all|deploy|test|failover|clean|status]"
        exit 2
        ;;
esac
