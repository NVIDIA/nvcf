#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Fixture tests for restore-guard.sh.
#
# These guards decide whether a test run means anything: they are what stops a
# cold start being published as a restore time. A guard that silently stops
# guarding is worse than no guard, because the green result is still believed.
#
# Run: scripts/lib/restore-guard-test.sh

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
. "$HERE/restore-guard.sh"

PASS=0
FAIL=0
ok()   { PASS=$((PASS+1)); printf '  ok   %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  FAIL %s\n' "$1"; }
check(){ # check <name> <expected-rc> <actual-rc>
    if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (want rc=$2, got rc=$3)"; fi
}

TMP=$(mktemp -d); trap 'rm -rf "$TMP" "$TMP/bin" 2>/dev/null' EXIT
mkdir -p "$TMP/bin"

echo "assert_no_placeholders"

cat > "$TMP/clean.yaml" <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: vllm-small-restored
  annotations:
    nvsnap.io/restore-from: "abc123"
YAML
assert_no_placeholders "$TMP/clean.yaml" >/dev/null 2>&1
check "accepts a fully substituted manifest" 0 $?

cat > "$TMP/unsub.yaml" <<'YAML'
metadata:
  annotations:
    nvsnap.io/restore-from: "__CAPTURE_HASH__"
YAML
assert_no_placeholders "$TMP/unsub.yaml" >/dev/null 2>&1
check "rejects an unsubstituted __CAPTURE_HASH__" 1 $?

# The guard must reject ANY unresolved token, not only the one it was written
# for. A manifest carrying __NODE_NAME__ is equally unusable.
cat > "$TMP/othertoken.yaml" <<'YAML'
spec:
  nodeName: __NODE_NAME__
YAML
assert_no_placeholders "$TMP/othertoken.yaml" >/dev/null 2>&1
check "rejects any unresolved token, not just the hash" 1 $?

# Templates name their own placeholders in comments. Matching those fails a
# correctly substituted manifest, which is worse than the bug it guards.
cat > "$TMP/comment.yaml" <<'YAML'
# test-e2e.sh substitutes __NODE_NAME__ from the source pod's status.
spec:
  nodeName: ip-10-0-0-1
YAML
assert_no_placeholders "$TMP/comment.yaml" >/dev/null 2>&1
check "ignores a placeholder named only in a comment" 0 $?

echo "agent_pod_cache_dir"

mk_kubectl() { printf '#!/bin/sh\n%s\n' "$1" > "$TMP/bin/kubectl"; chmod +x "$TMP/bin/kubectl"; }

mk_kubectl "printf -- '--foo=1\n--pod-cache-dir=/opt/nvsnap\n--bar=2\n'"
got=$(PATH="$TMP/bin:$PATH" agent_pod_cache_dir)
if [ "$got" = "/opt/nvsnap" ]; then ok "extracts the value"; else bad "extracts the value (got '$got')"; fi

# The bug this replaced: a greedy match ran past the value into the next
# argument, yielding "/opt/nvsnap --other=2".
mk_kubectl "printf -- '--pod-cache-dir=/opt/nvsnap\n--other=2\n'"
got=$(PATH="$TMP/bin:$PATH" agent_pod_cache_dir)
if [ "$got" = "/opt/nvsnap" ]; then ok "stops at the argument boundary"; else bad "stops at the argument boundary (got '$got')"; fi

mk_kubectl "printf -- '--foo=1\n--bar=2\n'"
got=$(PATH="$TMP/bin:$PATH" agent_pod_cache_dir)
if [ -z "$got" ]; then ok "empty when the flag is absent"; else bad "empty when the flag is absent (got '$got')"; fi

# A cluster configured with a different cache dir must be followed, not
# second-guessed; that is the whole reason this reads the deployed value.
mk_kubectl "printf -- '--pod-cache-dir=/var/lib/containerd/nvsnap-cache\n'"
got=$(PATH="$TMP/bin:$PATH" agent_pod_cache_dir)
if [ "$got" = "/var/lib/containerd/nvsnap-cache" ]; then ok "follows a non-default cache dir"; else bad "follows a non-default cache dir (got '$got')"; fi

echo "assert_restore_admitted"

# Stubs kubectl to return a fixed pod. The guard reads the pod as JSON, so the
# fixture is the whole input -- no cluster is involved.
mk_pod() { printf '#!/bin/sh\ncat <<'"'"'JSON'"'"'\n%s\nJSON\n' "$1" > "$TMP/bin/kubectl"; chmod +x "$TMP/bin/kubectl"; }
admitted() { PATH="$TMP/bin:$PATH" assert_restore_admitted vllm-restored default "$1" "$2" >/dev/null 2>&1; }

VALID='{"spec":{"containers":[{"name":"vllm",
  "env":[{"name":"HF_HOME","value":"/opt/nvsnap/hf"}],
  "volumeMounts":[{"mountPath":"/opt/nvsnap"}]}]}}'

mk_pod "$VALID"
admitted vllm /opt/nvsnap
check "accepts a decorated restore pod" 0 $?

# An agent with no --pod-cache-dir means cachedir capture is not configured at
# all, so there is nothing a restore could have come from.
mk_pod "$VALID"
admitted vllm ""
check "rejects an empty cache dir" 1 $?

# Falling back to containers[0] would let a decorated sidecar vouch for a
# workload that is cold-starting, so a missing container fails closed.
mk_pod "$VALID"
admitted engine /opt/nvsnap
check "rejects a missing restore container" 1 $?

mk_pod '{"spec":{"containers":[{"name":"vllm",
  "env":[{"name":"HF_HOME","value":"/opt/nvsnap/hf"}],
  "volumeMounts":[{"mountPath":"/var/tmp"}]}]}}'
admitted vllm /opt/nvsnap
check "rejects an unmounted cache dir" 1 $?

# Absent cache env is a failure, not "nothing to check": a restore pod that
# inherited none of the stamped variables is not restoring from anything.
mk_pod '{"spec":{"containers":[{"name":"vllm",
  "env":[{"name":"PATH","value":"/usr/bin"}],
  "volumeMounts":[{"mountPath":"/opt/nvsnap"}]}]}}'
admitted vllm /opt/nvsnap
check "rejects a pod with no cache env" 1 $?

mk_pod '{"spec":{"containers":[{"name":"vllm",
  "env":[{"name":"HF_HOME","value":"/root/.cache/huggingface"}],
  "volumeMounts":[{"mountPath":"/opt/nvsnap"}]}]}}'
admitted vllm /opt/nvsnap
check "rejects cache env pointing outside the cache dir" 1 $?

# Prefix matching alone would accept a sibling directory whose name merely
# starts with the cache dir.
mk_pod '{"spec":{"containers":[{"name":"vllm",
  "env":[{"name":"HF_HOME","value":"/opt/nvsnap-other/hf"}],
  "volumeMounts":[{"mountPath":"/opt/nvsnap"}]}]}}'
admitted vllm /opt/nvsnap
check "rejects a sibling dir that shares the cache dir prefix" 1 $?

# NIM images stamp NIM_CACHE_PATH instead of HF_HOME; either satisfies the guard.
mk_pod '{"spec":{"containers":[{"name":"nim",
  "env":[{"name":"NIM_CACHE_PATH","value":"/opt/nvsnap/nim"}],
  "volumeMounts":[{"mountPath":"/opt/nvsnap"}]}]}}'
admitted nim /opt/nvsnap
check "accepts NIM_CACHE_PATH in place of HF_HOME" 0 $?

# A trailing slash is a spelling of the same path, not a different one.
mk_pod "$VALID"
admitted vllm /opt/nvsnap/
check "treats a trailing slash as the same cache dir" 0 $?

echo
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
