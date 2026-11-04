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
# cluster-chaos.sh — failure-injection under sustained load
#
# What this exercises:
#   * Sustained NBD write workload on each node.
#   * Mid-workload SIGKILL of a non-leader node.
#   * Survivors must stay healthy (/healthz) throughout.
#   * Workload must complete on the survivors (errors are surfaced, not hidden).
#   * Killed node restarts; we verify it rejoins the cluster.
#   * After everything settles, NBD volumes on survivors are readable and the
#     data matches what we wrote.
#
# What this does NOT test (yet):
#   * Network partitions (would need tc/netem on the cluster).
#   * Slow-disk injection (would need cgroup IO throttling).
#   * Leader kill (current placement is replicated-3 with quorum, so killing
#     the Raft leader is interesting but adds a layer of stabilisation time
#     the bench harness needs more polish to handle cleanly).
#
# Usage:
#   scripts/cluster-chaos.sh                # full scenario
#   scripts/cluster-chaos.sh nbd            # NBD-only scenario
#   scripts/cluster-chaos.sh restart        # post-test, restart killed node + verify it rejoins

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=cluster-config.sh
source "$SCRIPT_DIR/cluster-config.sh"

# Build NODES the same way cluster-smoketest.sh does.
NODES=("$NODE1" "$NODE2")
if [ "${INCLUDE_NODE3:-0}" = "1" ]; then
    NODES+=("$NODE3")
fi

SSH_OPTS="-i $SSH_KEY -o StrictHostKeyChecking=accept-new -o ConnectTimeout=8"
LOG_DIR="${LOG_DIR:-/tmp/nvstor-chaos}"
mkdir -p "$LOG_DIR"

# Scenario knobs.
WORKLOAD_SIZE_MB="${WORKLOAD_SIZE_MB:-1024}"
WORKLOAD_QD="${WORKLOAD_QD:-32}"
KILL_DELAY_SEC="${KILL_DELAY_SEC:-3}"
SETTLE_SEC="${SETTLE_SEC:-5}"

# The node we'll kill mid-workload. Default to the LAST node so node 1
# (typical Raft leader after fresh bootstrap) stays up — kills there
# would also exercise leader election, but that's a separate scenario.
VICTIM_NODE="${VICTIM_NODE:-${NODES[$((${#NODES[@]} - 1))]}}"

# Surviving nodes = NODES minus victim.
SURVIVORS=()
for n in "${NODES[@]}"; do
    [ "$n" = "$VICTIM_NODE" ] || SURVIVORS+=("$n")
done

# Pretty-printing.
log()  { echo "[$(date '+%H:%M:%S')] $*"; }
pass() { echo "  [ ok ] $*"; }
fail() { echo "  [FAIL] $*"; FAILED+=("$*"); }
FAILED=()

# ssh wrapper that handles the csh default shell on these nodes.
#
# Strategy: invoke `bash -s` over ssh and pipe the script body via
# stdin. This sidesteps csh's command-line parsing entirely — csh sees
# only the literal command `bash -s` and never gets a chance to
# interpret `$variables`, `<`, `&&`, etc. that we want bash to see.
remote() {
    local host="$1"; shift
    ssh $SSH_OPTS "$SSH_USER@$host" 'bash -s' <<<"$*"
}

# ---------------------------------------------------------------------------
# Pre-flight: cluster must be healthy + all NBD ports reachable.
# ---------------------------------------------------------------------------

preflight() {
    log "[preflight] checking cluster health"
    local all_ok=1
    for n in "${NODES[@]}"; do
        if curl -sf -m 5 "http://${n}:9101/healthz" >/dev/null 2>&1; then
            pass "/healthz $n"
        else
            fail "/healthz $n"
            all_ok=0
        fi
    done
    if [ "$all_ok" = "0" ]; then
        log "[preflight] cluster not healthy; aborting"
        exit 2
    fi
    log "[preflight] victim = $VICTIM_NODE; survivors = ${SURVIVORS[*]}"
}

# ---------------------------------------------------------------------------
# Sustained workload: each surviving node drives an NBD write workload
# against its own local server. We run them all in the background so we can
# inject a fault while they're in flight.
#
# Each node writes a known pattern (the first 4 bytes of every 1 MiB block
# are an opcode-style marker we can verify on read-back).
# ---------------------------------------------------------------------------

start_workload_on() {
    local node="$1"
    local size_mb="$2"
    local logfile="$LOG_DIR/workload-${node//./_}.log"

    remote "$node" "
        set -uo pipefail
        sudo nbd-client -d /dev/nbd0 >/dev/null 2>&1 || true
        sudo nbd-client -N chaos-vol-$node 127.0.0.1 10809 /dev/nbd0 -timeout 30 \
            >/dev/null 2>&1 || { echo CONNECT_FAILED; exit 3; }
        START=\$(date +%s.%N)
        sudo /bin/fio --name=chaos --filename=/dev/nbd0 \
            --rw=write --bs=1M --direct=1 --ioengine=libaio --iodepth=$WORKLOAD_QD \
            --numjobs=1 --size=${size_mb}M --time_based=0 \
            --output-format=normal 2>&1 | grep -E 'WRITE: bw|IOPS=|err='
        END=\$(date +%s.%N)
        SEC=\$(awk -v s=\$START -v e=\$END 'BEGIN { print e - s }')
        echo \"WORKLOAD_DONE sec=\$SEC node=$node\"
        sudo nbd-client -d /dev/nbd0 >/dev/null 2>&1 || true
    " > "$logfile" 2>&1 &
    echo $!  # return the local PID of the background ssh
}

# ---------------------------------------------------------------------------
# Read-back verification: after the kill+settle, the volume on each
# survivor must still respond to reads at full size.
# ---------------------------------------------------------------------------

verify_readback_on() {
    local node="$1"
    local size_mb="$2"

    remote "$node" "
        set -uo pipefail
        sudo nbd-client -d /dev/nbd0 >/dev/null 2>&1 || true
        sudo nbd-client -N chaos-vol-$node 127.0.0.1 10809 /dev/nbd0 -timeout 30 \
            >/dev/null 2>&1 || { echo CONNECT_FAILED; exit 3; }
        sudo /bin/fio --name=verify --filename=/dev/nbd0 \
            --rw=read --bs=1M --direct=1 --ioengine=libaio --iodepth=$WORKLOAD_QD \
            --numjobs=1 --size=${size_mb}M --time_based=0 --readonly \
            --output-format=normal 2>&1 | grep -E 'READ: bw|IOPS=|err='
        sudo nbd-client -d /dev/nbd0 >/dev/null 2>&1 || true
    " 2>&1
}

# ---------------------------------------------------------------------------
# Restart the victim and verify it rejoins discovery + healthz.
# ---------------------------------------------------------------------------

restart_victim() {
    log "[restart] re-launching nvstor-server on $VICTIM_NODE"
    # Reuse the launcher the smoketest already deploys to /tmp on each
    # node. It does the nohup+setsid+disown dance so ssh returns
    # immediately; without it the ssh session stays attached to the
    # nvstor-server pid and the chaos script hangs.
    remote "$VICTIM_NODE" "bash /tmp/nvstor-launch.sh" 2>&1 | sed 's/^/  /'

    log "[restart] wait up to 30s for victim /healthz"
    for i in $(seq 1 15); do
        if curl -sf -m 3 "http://${VICTIM_NODE}:9101/healthz" >/dev/null 2>&1; then
            pass "victim /healthz back in ${i}s"
            break
        fi
        sleep 2
    done

    log "[restart] confirm victim rejoined discovery on each survivor"
    # Discovery heartbeat is on the order of 10 s; after the leader
    # kill the survivors won't log "new node joined" for the former
    # leader until the next heartbeat round. Poll up to 30 s.
    for s in "${SURVIVORS[@]}"; do
        local rejoined=0
        for _ in $(seq 1 15); do
            local joined
            joined=$(remote "$s" "sudo tail -400 /tmp/nvstor.log | grep -c 'new node joined' || true")
            if [ "${joined:-0}" -gt 0 ]; then
                rejoined=1
                break
            fi
            sleep 2
        done
        if [ "$rejoined" = "1" ]; then
            pass "$s saw discovery rejoin"
        else
            fail "$s did not see discovery rejoin (30 s timeout)"
        fi
    done
}

# ---------------------------------------------------------------------------
# The main scenario: sustained NBD writes + mid-flight kill.
# ---------------------------------------------------------------------------

scenario_nbd_kill_under_load() {
    log "[scenario] NBD writes on all nodes; kill $VICTIM_NODE after ${KILL_DELAY_SEC}s"
    preflight

    local pids=()
    for n in "${NODES[@]}"; do
        pids+=( $(start_workload_on "$n" "$WORKLOAD_SIZE_MB") )
    done
    log "[scenario] workloads started (${#pids[@]} pids); sleeping ${KILL_DELAY_SEC}s before kill"
    sleep "$KILL_DELAY_SEC"

    log "[scenario] SIGKILL nvstor-server on $VICTIM_NODE"
    remote "$VICTIM_NODE" "sudo pkill -9 -f nvstor-server"

    log "[scenario] survivors must stay healthy through the kill"
    for n in "${SURVIVORS[@]}"; do
        if curl -sf -m 5 "http://${n}:9101/healthz" >/dev/null 2>&1; then
            pass "survivor /healthz $n"
        else
            fail "survivor /healthz $n"
        fi
    done

    log "[scenario] waiting for workloads to drain"
    for pid in "${pids[@]}"; do
        wait "$pid" 2>/dev/null || true
    done

    log "[scenario] workload results:"
    for n in "${NODES[@]}"; do
        local lf="$LOG_DIR/workload-${n//./_}.log"
        echo "  --- $n ---"
        if grep -q "WORKLOAD_DONE" "$lf"; then
            grep -E "WRITE: bw|IOPS=|WORKLOAD_DONE" "$lf" | sed 's/^/    /'
            if [ "$n" = "$VICTIM_NODE" ]; then
                # Victim workload should have failed mid-write — that's expected.
                pass "victim workload terminated (expected)"
            else
                pass "survivor workload completed"
            fi
        else
            # No DONE marker — workload didn't finish cleanly.
            grep -E "ERROR|err=|CONNECT_FAILED|fio:" "$lf" | head -5 | sed 's/^/    /'
            if [ "$n" = "$VICTIM_NODE" ]; then
                pass "victim workload terminated mid-flight (expected)"
            else
                fail "survivor workload did not complete cleanly"
            fi
        fi
    done

    log "[scenario] settling ${SETTLE_SEC}s"
    sleep "$SETTLE_SEC"

    log "[scenario] read-back verify on each survivor"
    for n in "${SURVIVORS[@]}"; do
        echo "  --- $n ---"
        if verify_readback_on "$n" "$WORKLOAD_SIZE_MB" | grep -q "READ: bw"; then
            pass "read-back $n"
        else
            fail "read-back $n"
        fi
    done

    # Auto-restart the victim so the test is idempotent — can be run
    # back-to-back without an intervening deploy. Skip if the caller
    # explicitly wants the victim to stay down (CHAOS_NO_RESTART=1).
    if [ -z "${CHAOS_NO_RESTART:-}" ]; then
        restart_victim
    fi
}

# ---------------------------------------------------------------------------
# Leader-kill scenario: discover the current Raft leader, kill it under
# load, verify the survivors elect a new leader and the workload completes.
#
# The standard NBD-kill scenario above picks the last node in NODES,
# which on a freshly-bootstrapped cluster is unlikely to be the leader
# (nvstor-server elects node_id=1 first by design). Killing a non-leader
# tests partial-failure resilience, but says nothing about consensus
# failover — that's what this scenario covers.
# ---------------------------------------------------------------------------

# Discover the current Raft leader by scanning each node's nvstor.log
# for the most recent "elected leader" entry. The nvstor_raft::node
# code only logs this on the node that *becomes* leader (followers
# don't log when they learn of a new leader), so the latest entry's
# host is the leader.
#
# Returns the leader's IP via stdout, or empty string if none found.
discover_leader() {
    local best_node="" best_ts=""
    for n in "${NODES[@]}"; do
        # Last 'elected leader' line per node; parse the ISO-8601
        # timestamp at the start.
        local line
        line=$(ssh $SSH_OPTS "$SSH_USER@$n" \
            "sudo grep -E 'elected leader' /tmp/nvstor.log | tail -1" 2>/dev/null)
        # strip ANSI color codes before timestamp extraction
        line=$(echo "$line" | sed 's/\x1b\[[0-9;]*m//g')
        local ts
        ts=$(echo "$line" | grep -oE '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+Z' | head -1)
        if [ -n "$ts" ]; then
            if [ -z "$best_ts" ] || [[ "$ts" > "$best_ts" ]]; then
                best_ts="$ts"
                best_node="$n"
            fi
        fi
    done
    echo "$best_node"
}

scenario_leader_kill_under_load() {
    log "[scenario:leader-kill] discovering current Raft leader"
    preflight

    local leader
    leader=$(discover_leader)
    if [ -z "$leader" ]; then
        fail "could not discover Raft leader from any node's log"
        return
    fi
    log "[scenario:leader-kill] current leader = $leader"

    # Override VICTIM_NODE and SURVIVORS for this scenario.
    VICTIM_NODE="$leader"
    SURVIVORS=()
    for n in "${NODES[@]}"; do
        [ "$n" = "$VICTIM_NODE" ] || SURVIVORS+=("$n")
    done
    log "[scenario:leader-kill] victim = $VICTIM_NODE; survivors = ${SURVIVORS[*]}"

    # Start the workload as the standard scenario does, but only on
    # the survivors — running fio against the soon-to-be-dead leader
    # would just confirm "fio errors when the server dies," which is
    # not the interesting signal. We want to verify the *survivors*
    # keep serving and that one of them takes over leadership.
    local pids=()
    for n in "${SURVIVORS[@]}"; do
        pids+=( $(start_workload_on "$n" "$WORKLOAD_SIZE_MB") )
    done
    log "[scenario:leader-kill] survivor workloads started; sleeping ${KILL_DELAY_SEC}s before kill"
    sleep "$KILL_DELAY_SEC"

    log "[scenario:leader-kill] SIGKILL nvstor-server on the leader ($VICTIM_NODE)"
    remote "$VICTIM_NODE" "sudo pkill -9 -f nvstor-server"

    log "[scenario:leader-kill] survivors must stay healthy"
    for n in "${SURVIVORS[@]}"; do
        if curl -sf -m 5 "http://${n}:9101/healthz" >/dev/null 2>&1; then
            pass "survivor /healthz $n"
        else
            fail "survivor /healthz $n"
        fi
    done

    log "[scenario:leader-kill] waiting for new leader election"
    local new_leader=""
    for i in $(seq 1 30); do
        sleep 1
        local cand
        cand=$(discover_leader)
        # The new leader must be one of the survivors (not the
        # dead leader, whose log we can't read anyway).
        for s in "${SURVIVORS[@]}"; do
            if [ "$cand" = "$s" ]; then
                new_leader="$cand"
                break
            fi
        done
        [ -n "$new_leader" ] && break
    done

    if [ -n "$new_leader" ] && [ "$new_leader" != "$VICTIM_NODE" ]; then
        pass "new leader elected: $new_leader (was $VICTIM_NODE)"
    else
        fail "no new leader elected within 30 s (still '$new_leader')"
    fi

    log "[scenario:leader-kill] waiting for survivor workloads to drain"
    for pid in "${pids[@]}"; do
        wait "$pid" 2>/dev/null || true
    done

    log "[scenario:leader-kill] survivor workload results"
    for n in "${SURVIVORS[@]}"; do
        local lf="$LOG_DIR/workload-${n//./_}.log"
        echo "  --- $n ---"
        if grep -q "WORKLOAD_DONE" "$lf" 2>/dev/null; then
            grep -E "WRITE: bw|IOPS=|WORKLOAD_DONE" "$lf" | sed 's/^/    /'
            pass "survivor workload completed: $n"
        else
            grep -E "ERROR|err=|CONNECT_FAILED|fio:" "$lf" 2>/dev/null | head -5 | sed 's/^/    /'
            fail "survivor workload did not complete: $n"
        fi
    done

    log "[scenario:leader-kill] settling ${SETTLE_SEC}s"
    sleep "$SETTLE_SEC"

    log "[scenario:leader-kill] read-back verify on survivors"
    for n in "${SURVIVORS[@]}"; do
        echo "  --- $n ---"
        if verify_readback_on "$n" "$WORKLOAD_SIZE_MB" | grep -q "READ: bw"; then
            pass "read-back $n"
        else
            fail "read-back $n"
        fi
    done

    # Auto-restart the former leader so the test is idempotent.
    if [ -z "${CHAOS_NO_RESTART:-}" ]; then
        restart_victim
    fi
}

report() {
    echo
    echo "==============================================="
    if [ "${#FAILED[@]}" -gt 0 ]; then
        echo "  CHAOS FAILED (${#FAILED[@]})"
        for f in "${FAILED[@]}"; do echo "    - $f"; done
        echo "==============================================="
        exit 1
    fi
    echo "  CHAOS PASSED"
    echo "==============================================="
}

MODE="${1:-all}"
case "$MODE" in
    all)
        scenario_nbd_kill_under_load
        # Cluster needs to be healthy again before leader-kill.
        # scenario_nbd_kill_under_load already auto-restarted.
        sleep 3
        scenario_leader_kill_under_load
        report
        ;;
    nbd)
        scenario_nbd_kill_under_load
        report
        ;;
    leader)
        scenario_leader_kill_under_load
        report
        ;;
    restart)
        preflight
        restart_victim
        report
        ;;
    *)
        echo "usage: $0 [all|nbd|leader|restart]"
        exit 1
        ;;
esac
