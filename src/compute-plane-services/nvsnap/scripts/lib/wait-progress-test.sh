#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Unit tests for the pure JSON helpers in wait-progress.sh. No cluster needed.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/wait-progress.sh"

PASS=0; FAIL=0
check() { # check <name> <expected-substring-or-EMPTY> <actual>
    local name="$1" want="$2" got="$3"
    if [ "$want" = "EMPTY" ]; then
        if [ -z "$got" ]; then PASS=$((PASS+1)); else FAIL=$((FAIL+1)); echo "FAIL $name: wanted empty, got '$got'"; fi
    elif [[ "$got" == *"$want"* ]]; then PASS=$((PASS+1))
    else FAIL=$((FAIL+1)); echo "FAIL $name: wanted '*$want*', got '$got'"; fi
}

fail_reason() { printf '%s' "$1" | pod_failure_reason_from_json; }
fingerprint() { printf '%s' "$1" | progress_fingerprint_from_json; }

# --- terminal states must fail fast -----------------------------------------
check "imagepullbackoff" "ImagePullBackOff" "$(fail_reason '{"status":{"phase":"Pending","containerStatuses":[{"name":"c","state":{"waiting":{"reason":"ImagePullBackOff","message":"no pull secret"}}}]}}')"
check "crashloop" "CrashLoopBackOff" "$(fail_reason '{"status":{"phase":"Running","containerStatuses":[{"name":"c","state":{"waiting":{"reason":"CrashLoopBackOff","message":"back-off"}}}]}}')"
check "nonzero exit" "exited 1" "$(fail_reason '{"status":{"phase":"Running","containerStatuses":[{"name":"c","state":{"terminated":{"exitCode":1,"reason":"Error"}}}]}}')"
check "oomkilled" "OOMKilled" "$(fail_reason '{"status":{"phase":"Running","containerStatuses":[{"name":"c","state":{"running":{}},"lastState":{"terminated":{"reason":"OOMKilled"}}}]}}')"
check "phase failed" "pod phase Failed" "$(fail_reason '{"status":{"phase":"Failed","reason":"Evicted"}}')"
check "init container failure counts" "ImagePullBackOff" "$(fail_reason '{"status":{"phase":"Pending","initContainerStatuses":[{"name":"i","state":{"waiting":{"reason":"ImagePullBackOff","message":"x"}}}]}}')"

# --- healthy startup must NOT be called terminal -----------------------------
check "pulling is not failure" "EMPTY" "$(fail_reason '{"status":{"phase":"Pending","containerStatuses":[{"name":"c","state":{"waiting":{"reason":"ContainerCreating"}}}]}}')"
check "running-not-ready is not failure" "EMPTY" "$(fail_reason '{"status":{"phase":"Running","containerStatuses":[{"name":"c","ready":false,"state":{"running":{}}}]}}')"
check "clean exit 0 is not failure" "EMPTY" "$(fail_reason '{"status":{"phase":"Succeeded","containerStatuses":[{"name":"c","state":{"terminated":{"exitCode":0}}}]}}')"
check "garbage json is not failure" "EMPTY" "$(fail_reason 'not json at all')"

# --- fingerprint must move when the pod moves, and only then -----------------
A='{"status":{"phase":"Pending","conditions":[{"type":"Ready","status":"False"}],"containerStatuses":[{"name":"c","ready":false,"restartCount":0,"state":{"waiting":{"reason":"ContainerCreating"}}}]}}'
B='{"status":{"phase":"Running","conditions":[{"type":"Ready","status":"False"}],"containerStatuses":[{"name":"c","ready":false,"restartCount":0,"state":{"running":{}}}]}}'
C='{"status":{"phase":"Running","conditions":[{"type":"Ready","status":"False"}],"containerStatuses":[{"name":"c","ready":false,"restartCount":1,"state":{"running":{}}}]}}'
[ "$(fingerprint "$A")" = "$(fingerprint "$A")" ] && PASS=$((PASS+1)) || { FAIL=$((FAIL+1)); echo "FAIL fingerprint not stable"; }
[ "$(fingerprint "$A")" != "$(fingerprint "$B")" ] && PASS=$((PASS+1)) || { FAIL=$((FAIL+1)); echo "FAIL fingerprint blind to phase change"; }
[ "$(fingerprint "$B")" != "$(fingerprint "$C")" ] && PASS=$((PASS+1)) || { FAIL=$((FAIL+1)); echo "FAIL fingerprint blind to restart"; }

pull() { printf '%s' "$1" | pull_in_flight_from_json; }

# --- image pull in flight must extend the budget, not look like a stall -------
check "containercreating is a pull" "yes" "$(pull '{"status":{"phase":"Pending","containerStatuses":[{"name":"c","state":{"waiting":{"reason":"ContainerCreating"}}}]}}')"
check "podinitializing is a pull" "yes" "$(pull '{"status":{"phase":"Pending","containerStatuses":[{"name":"c","state":{"waiting":{"reason":"PodInitializing"}}}]}}')"
check "init container pull counts" "yes" "$(pull '{"status":{"phase":"Pending","initContainerStatuses":[{"name":"i","state":{"waiting":{"reason":"ContainerCreating"}}}]}}')"
check "running container is not a pull" "EMPTY" "$(pull '{"status":{"phase":"Running","containerStatuses":[{"name":"c","state":{"running":{}}}]}}')"
check "crashloop is not a pull" "EMPTY" "$(pull '{"status":{"phase":"Running","containerStatuses":[{"name":"c","state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}')"
check "succeeded is not a pull" "EMPTY" "$(pull '{"status":{"phase":"Succeeded","containerStatuses":[{"name":"c","state":{"terminated":{"exitCode":0}}}]}}')"
check "garbage json is not a pull" "EMPTY" "$(pull 'not json')"

# A pod mid-pull must not also be reported terminal, or the budget never applies.
check "pull is not terminal" "EMPTY" "$(fail_reason '{"status":{"phase":"Pending","containerStatuses":[{"name":"c","state":{"waiting":{"reason":"ContainerCreating"}}}]}}')"

# ---------------------------------------------------------------------------
# Loop-level tests.
#
# The predicate tests above all passed while wait_pod_ready contained a defect
# that failed every cold image pull: the predicates were right, the logic that
# composed them was not. Nothing here called wait_pod_ready, so nothing could
# have caught it. These drive the loop itself through the seams, with a virtual
# clock and a scripted pod sequence.
# ---------------------------------------------------------------------------

# Replay harness: SEQ holds one JSON doc per poll, LOGS one size per poll. The
# last entry repeats once the script runs out.
SEQ=(); LOGS=(); VNOW=0; TICK=10
_wp_now()   { echo "$VNOW"; }
_wp_sleep() { VNOW=$((VNOW + TICK)); IDX=$((IDX + 1)); }
_wp_pod_json() { local i=$IDX; [ "$i" -ge "${#SEQ[@]}" ] && i=$(( ${#SEQ[@]} - 1 )); printf '%s' "${SEQ[$i]}"; }
_wp_log_size() { local i=$IDX; [ "$i" -ge "${#LOGS[@]}" ] && i=$(( ${#LOGS[@]} - 1 )); printf '%s' "${LOGS[$i]}"; }
run_loop() { IDX=0; VNOW=0; wait_pod_ready testpod testns "$1" "$2" >/dev/null 2>&1; }

CREATING='{"status":{"phase":"Pending","conditions":[{"type":"Ready","status":"False"}],"containerStatuses":[{"name":"c","ready":false,"restartCount":0,"state":{"waiting":{"reason":"ContainerCreating"}}}]}}'
RUNNING='{"status":{"phase":"Running","conditions":[{"type":"Ready","status":"False"}],"containerStatuses":[{"name":"c","ready":false,"restartCount":0,"state":{"running":{}}}]}}'
READY='{"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"name":"c","ready":true,"restartCount":0,"state":{"running":{}}}]}}'
PULLFAIL='{"status":{"phase":"Pending","containerStatuses":[{"name":"c","state":{"waiting":{"reason":"ImagePullBackOff","message":"no secret"}}}]}}'

loopcheck() { # loopcheck <name> <expected rc>
    local name="$1" want="$2"
    if [ "$RC" = "$want" ]; then PASS=$((PASS+1)); else FAIL=$((FAIL+1)); echo "FAIL $name: wanted rc=$want got rc=$RC"; fi
}

# THE REGRESSION TEST: a cold pull emits no logs and changes no state. Under the
# plain 600s stall budget that looked identical to a hang and was failed. It must
# now survive past 600s. It is still bounded -- see the next case -- just not by
# the short budget.
SEQ=("$CREATING"); LOGS=(0); IDX=0; VNOW=0
out=$(wait_pod_ready testpod testns 600 1000 2>&1) || true
case "$out" in
  *"no observable progress"*) FAIL=$((FAIL+1)); echo "FAIL cold pull stalled at the short budget (the bug)";;
  *"ceiling"*)                PASS=$((PASS+1));;
  *)                          FAIL=$((FAIL+1)); echo "FAIL cold pull: unexpected outcome: $out";;
esac

# ...but a pull that never progresses is still bounded, by the pull budget, not
# deferred to the ceiling. Kubernetes gives no byte-level progress, so this is
# the only lever available.
SEQ=("$CREATING"); LOGS=(0); IDX=0; VNOW=0
out=$(NVSNAP_PULL_STALL_TIMEOUT=1800 wait_pod_ready testpod testns 600 100000 2>&1) || true
case "$out" in *"budget 1800s"*) PASS=$((PASS+1));; *) FAIL=$((FAIL+1)); echo "FAIL stuck pull not bounded by the pull budget: $out";; esac

# A Running pod with no logs and no state change IS a stall, on the short budget.
SEQ=("$RUNNING"); LOGS=(0); IDX=0; VNOW=0
out=$(wait_pod_ready testpod testns 600 100000 2>&1) || true
case "$out" in *"no observable progress"*) PASS=$((PASS+1));; *) FAIL=$((FAIL+1)); echo "FAIL running+unchanging was not called a stall";; esac

# Readiness returns 0.
SEQ=("$CREATING" "$RUNNING" "$READY"); LOGS=(0 10 20); run_loop 600 100000; RC=$?
loopcheck "becomes ready" 0

# A terminal state fails immediately, not after any budget.
SEQ=("$PULLFAIL"); LOGS=(0); IDX=0; VNOW=0
out=$(wait_pod_ready testpod testns 600 100000 2>&1) && RC=0 || RC=$?
loopcheck "terminal state fails" 1
[[ "$out" == *"ImagePullBackOff"* ]] && PASS=$((PASS+1)) || { FAIL=$((FAIL+1)); echo "FAIL terminal reason not reported"; }
[ "$VNOW" -lt 120 ] && PASS=$((PASS+1)) || { FAIL=$((FAIL+1)); echo "FAIL terminal state waited $VNOW virtual seconds"; }

# An API blip after the pod has been seen must not be reported as not-found.
SEQ=("$RUNNING" "" "" "$READY"); LOGS=(0 0 0 30); run_loop 600 100000; RC=$?
loopcheck "api blip after sighting is survivable" 0

# A pod that never appears does fail.
SEQ=(""); LOGS=(0); run_loop 600 100000; RC=$?
loopcheck "never-seen pod fails" 1

echo "wait-progress: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
