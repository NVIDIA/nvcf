#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Progress-aware waiting for pod readiness.
#
# The suite used to wait a fixed number of seconds chosen from a table keyed on
# substrings of the workload name. That was wrong twice over. The patterns did
# not match the names -- only 3 of 13 workloads hit a branch, so a 64GB model
# got the 10 minute default -- and even a correct number cannot serve both a
# cold run that downloads 64GB and a warm run that does not. Every miss surfaced
# as "Pod ready FAIL", which reads like a capture bug and costs an hour to
# disprove.
#
# Wait on progress instead of on a deadline. A pod that is pulling an image,
# writing logs, or changing state is making progress and is allowed to keep
# going. A pod that has not changed in any observable way for stall_timeout has
# hung, and is failed then -- sooner than the old ceiling would have, and with
# the reason attached. Terminal states fail immediately rather than burning the
# clock.

# pod_failure_reason <pod> <ns>
# Echoes a human-readable reason when the pod is in a state no amount of waiting
# will fix, empty otherwise. Kept free of kubectl so it can be unit tested: it
# reads the JSON it is given on stdin.
pod_failure_reason_from_json() {
    python3 -c '
import json, sys
try:
    p = json.load(sys.stdin)
except Exception:
    sys.exit(0)
status = p.get("status", {})
if status.get("phase") == "Failed":
    print("pod phase Failed: " + str(status.get("reason", "unknown")))
    sys.exit(0)
allcs = (status.get("containerStatuses") or []) + (status.get("initContainerStatuses") or [])
for cs in allcs:
    name = str(cs.get("name"))
    st = cs.get("state") or {}
    w = st.get("waiting") or {}
    reason = w.get("reason", "")
    if reason in ("ImagePullBackOff", "ErrImagePull", "InvalidImageName",
                  "CreateContainerConfigError", "CrashLoopBackOff"):
        print(name + ": " + reason + ": " + str(w.get("message", "")).strip()[:200])
        sys.exit(0)
    t = st.get("terminated") or {}
    if t and t.get("exitCode", 0) != 0:
        print(name + ": exited " + str(t.get("exitCode")) + " (" + str(t.get("reason", "")) + ")")
        sys.exit(0)
    last = (cs.get("lastState") or {}).get("terminated") or {}
    if last.get("reason") == "OOMKilled":
        print(name + ": OOMKilled")
        sys.exit(0)
'
}

# progress_fingerprint_from_json
# A string that changes whenever the pod observably advances. Restart count is
# included deliberately: a crash-looping pod keeps changing it, and the terminal
# check above catches the loop, so this never masks a real failure.
progress_fingerprint_from_json() {
    python3 -c '
import json, sys
try:
    p = json.load(sys.stdin)
except Exception:
    print("unreadable"); sys.exit(0)
s = p.get("status", {})
parts = [str(s.get("phase", ""))]
for c in s.get("conditions") or []:
    parts.append(str(c.get("type")) + "=" + str(c.get("status")))
for cs in (s.get("initContainerStatuses") or []) + (s.get("containerStatuses") or []):
    st = cs.get("state") or {}
    which = ""
    for k in ("waiting", "running", "terminated"):
        if k in st:
            which = k
            break
    reason = ""
    if which:
        reason = str((st.get(which) or {}).get("reason", ""))
    parts.append(str(cs.get("name")) + ":" + which + ":" + reason + ":" +
                 str(cs.get("restartCount", 0)) + ":" + str(cs.get("ready")))
print("|".join(parts))
'
}

# wait_pod_ready <pod> <namespace> [stall_timeout_s] [max_wait_s]
# Returns 0 when ready, 1 on terminal failure or stall. Prints what it observed.
wait_pod_ready() {
    local pod="$1" ns="$2"
    local stall="${3:-${NVSNAP_STALL_TIMEOUT:-600}}"
    local max="${4:-${NVSNAP_MAX_WAIT:-5400}}"
    local interval="${NVSNAP_POLL_INTERVAL:-10}"

    local start last_change fingerprint prev logsize prevlog
    start=$(date +%s); last_change=$start; prev=""; prevlog=""

    while true; do
        local now elapsed json
        now=$(date +%s); elapsed=$((now - start))

        json=$(kubectl get pod "$pod" -n "$ns" -o json 2>/dev/null)
        if [ -z "$json" ]; then
            if [ "$elapsed" -gt 120 ]; then
                echo "wait_pod_ready: pod $ns/$pod not found after ${elapsed}s"
                return 1
            fi
            sleep "$interval"; continue
        fi

        if printf '%s' "$json" | python3 -c '
import json,sys
p=json.load(sys.stdin)
for c in p.get("status",{}).get("conditions",[]) or []:
    if c.get("type")=="Ready" and c.get("status")=="True":
        sys.exit(0)
sys.exit(1)'; then
            echo "wait_pod_ready: $ns/$pod ready after ${elapsed}s"
            return 0
        fi

        local reason
        reason=$(printf '%s' "$json" | pod_failure_reason_from_json)
        if [ -n "$reason" ]; then
            echo "wait_pod_ready: $ns/$pod FAILED after ${elapsed}s -- $reason"
            return 1
        fi

        # Log growth counts as progress: a model download or engine build emits
        # output steadily while producing no state transitions at all.
        logsize=$(kubectl logs "$pod" -n "$ns" --tail=-1 2>/dev/null | wc -c)
        fingerprint=$(printf '%s' "$json" | progress_fingerprint_from_json)

        if [ "$fingerprint" != "$prev" ] || [ "$logsize" != "$prevlog" ]; then
            if [ -n "$prev" ] && [ "$fingerprint" != "$prev" ]; then
                echo "  [${elapsed}s] progress: $fingerprint"
            fi
            prev="$fingerprint"; prevlog="$logsize"; last_change=$now
        fi

        local stalled_for=$((now - last_change))
        if [ "$stalled_for" -ge "$stall" ]; then
            echo "wait_pod_ready: $ns/$pod STALLED -- no observable progress for ${stalled_for}s (elapsed ${elapsed}s)"
            echo "  last state: $fingerprint"
            echo "  this is a startup stall, not a checkpoint failure"
            return 1
        fi

        if [ "$elapsed" -ge "$max" ]; then
            echo "wait_pod_ready: $ns/$pod exceeded the absolute ceiling ${max}s while still progressing"
            echo "  last state: $fingerprint"
            echo "  raise NVSNAP_MAX_WAIT if this workload is legitimately slower"
            return 1
        fi

        sleep "$interval"
    done
}
