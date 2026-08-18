# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Guards that stop a restore test from measuring a cold start.
#
# A pod the webhook declined still starts, still serves, and still passes every
# functional check -- it just fetches its model again. The timings then describe
# a cold start wearing a restore label, and nothing in the run says so. That is
# worse than a failure, because the number is plausible and gets quoted: in
# test-bench.sh it is written straight into the published results table.
#
# Sourced by test-e2e.sh and test-bench.sh so both enforce the same contract.

# assert_no_placeholders <rendered-manifest>
#
# Restore templates carry nvsnap.io/restore-from: "__CAPTURE_HASH__". If a
# placeholder survives substitution the webhook has nothing to resolve, injects
# nothing, and the pod cold-starts.
assert_no_placeholders() {
    local manifest="$1" hits
    # Comments are excluded deliberately. Templates name their own placeholders
    # in explanatory comments ("test-e2e.sh substitutes __NODE_NAME__ from ..."),
    # and matching those fails a correctly substituted manifest -- a guard that
    # blocks good runs is worse than the problem it was added for. sed keeps the
    # line count, so grep -n still reports true line numbers.
    hits=$(sed 's/#.*//' "$manifest" | grep -nE '__[A-Z_]+__')
    if [ -n "$hits" ]; then
        printf '%s\n' "$hits" >&2
        echo "ERROR: unsubstituted placeholder(s) above in $manifest" >&2
        echo "ERROR: the webhook would ignore this pod and it would COLD START, not restore" >&2
        return 1
    fi
    return 0
}

# agent_pod_cache_dir
#
# The cache path is the agent's, not ours to guess. Echoes the deployed
# --pod-cache-dir so callers follow a cluster configured differently instead of
# asserting a hardcoded default. Empty output means cachedir capture is not
# configured, which callers should treat as fatal for a restore test.
agent_pod_cache_dir() {
    kubectl get ds nvsnap-agent -n nvsnap-system \
        -o jsonpath='{.spec.template.spec.containers[0].args}' 2>/dev/null \
        | tr ',' '\n' | sed -n 's|.*--pod-cache-dir=\([^"]*\).*|\1|p' | head -1
}

# assert_restore_admitted <pod> <namespace> <container> <pod-cache-dir>
#
# Proves the webhook decorated the pod as a restore: the configured cache dir is
# mounted, and the cache env points into it. Returns non-zero with the reasons
# on stderr otherwise.
assert_restore_admitted() {
    local pod="$1" ns="$2" container="$3" cache_dir="$4"
    local json rc

    if [ -z "$cache_dir" ]; then
        echo "ERROR: agent has no --pod-cache-dir; cachedir capture is not configured" >&2
        return 1
    fi

    json=$(mktemp -t nvsnap-restore-pod.XXXXXX.json) || return 1
    local i
    for i in $(seq 1 30); do
        kubectl get pod "$pod" -n "$ns" -o json >"$json" 2>/dev/null && break
        sleep 2
    done

    python3 - "$json" "$container" "$cache_dir" <<'PY'
import json, sys, posixpath

pod_json, want, cache_dir = sys.argv[1], sys.argv[2], sys.argv[3].rstrip("/")
pod = json.load(open(pod_json))
containers = pod["spec"]["containers"]

# Fail closed on the container: falling back to containers[0] would let a
# decorated sidecar vouch for a workload that is cold-starting.
c = next((x for x in containers if x["name"] == want), None)
if c is None:
    print(f"  container {want!r} not found (have: {[x['name'] for x in containers]})", file=sys.stderr)
    sys.exit(1)

env = {e["name"]: e.get("value", "") for e in (c.get("env") or [])}
mounts = {m["mountPath"].rstrip("/") for m in (c.get("volumeMounts") or [])}

def at_or_under(path, root):
    # Exact match or a genuine child. Prefix matching alone would accept
    # "/opt/nvsnap-other" for root "/opt/nvsnap".
    path = path.rstrip("/")
    return path == root or path.startswith(root + posixpath.sep)

problems = []
if cache_dir not in mounts:
    problems.append(f"cache dir {cache_dir} is not mounted (mounts: {sorted(mounts)})")
# A restore pod that inherited none of the stamped cache env is not restoring
# from anything, so absent counts as a failure rather than "nothing to check".
if not any(v in env for v in ("HF_HOME", "NIM_CACHE_PATH")):
    problems.append("no cache env (HF_HOME / NIM_CACHE_PATH) injected")
for var in ("HF_HOME", "NIM_CACHE_PATH"):
    val = env.get(var)
    if val and not at_or_under(val, cache_dir):
        problems.append(f"{var}={val!r} points outside {cache_dir}")

for p in problems:
    print(f"  {p}", file=sys.stderr)
sys.exit(1 if problems else 0)
PY
    rc=$?
    rm -f "$json"
    if [ $rc -ne 0 ]; then
        echo "ERROR: restore pod was NOT decorated by the webhook - it will COLD START" >&2
        echo "ERROR: any timing from this run would be a cold start labelled as a restore" >&2
        kubectl get pod "$pod" -n "$ns" \
            -o jsonpath='{.metadata.annotations.nvsnap\.io/restore-from}{"\n"}' 2>/dev/null \
            | sed 's/^/  restore-from: /' >&2
    fi
    return $rc
}
