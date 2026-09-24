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

echo "wait-progress: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
