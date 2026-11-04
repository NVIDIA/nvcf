#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Tests uninstall-nvsnap.sh against fake kubectl/helm. The script issues
# irreversible deletes, so what matters is which objects it selects: a
# selection bug here destroys someone else's data. That already happened once
# with a StorageClass-based filter, so ownership selection is tested directly.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/../uninstall-nvsnap.sh"
FAKE="$(mktemp -d)"; trap 'rm -rf "$FAKE"' EXIT
CALLS="$FAKE/calls"; : > "$CALLS"
pass=0; fail=0

check() { # check <desc> <expected-substring> <actual>
    if printf '%s' "$3" | grep -qF -- "$2"; then
        pass=$((pass+1)); printf '  ok   %s\n' "$1"
    else
        fail=$((fail+1)); printf '  FAIL %s\n       want substring: %s\n' "$1" "$2"
    fi
}
refute() {
    if printf '%s' "$3" | grep -qF -- "$2"; then
        fail=$((fail+1)); printf '  FAIL %s\n       must NOT contain: %s\n' "$1" "$2"
    else
        pass=$((pass+1)); printf '  ok   %s\n' "$1"
    fi
}

# Fake kubectl: a shared namespace holding two nvsnap per-capture claims and
# one belonging to an unrelated team, plus PVs for each.
cat > "$FAKE/kubectl" <<'EOF'
#!/usr/bin/env bash
echo "kubectl $*" >> "$CALLS"
case "$*" in
  *"get pvc"*"-o json"*)
    cat <<'JSON'
{"items":[
 {"metadata":{"name":"rox-abc123","labels":{}}},
 {"metadata":{"name":"rwx-abc123","labels":{}}},
 {"metadata":{"name":"someone-elses-data","labels":{}}}
]}
JSON
    ;;
  *"get pvc"*"--no-headers"*) ;;                      # claims cleared
  *"get pv -o json"*)
    cat <<'JSON'
{"items":[
 {"metadata":{"name":"pv-ours"},"spec":{"claimRef":{"namespace":"nvsnap-system","name":"rox-abc123"},
  "persistentVolumeReclaimPolicy":"Retain"},"status":{"phase":"Released"}},
 {"metadata":{"name":"pv-theirs"},"spec":{"claimRef":{"namespace":"nvsnap-system","name":"someone-elses-data"},
  "persistentVolumeReclaimPolicy":"Retain"},"status":{"phase":"Released"}},
 {"metadata":{"name":"pv-bound-ours"},"spec":{"claimRef":{"namespace":"nvsnap-system","name":"rox-live"},
  "persistentVolumeReclaimPolicy":"Retain"},"status":{"phase":"Bound"}}
]}
JSON
    ;;
  *"get pv"*"persistentVolumeReclaimPolicy"*) echo "Retain" ;;   # policy probe
  *"get ds"*"desiredNumberScheduled"*) echo 1 ;;
  *"get ns"*) exit 1 ;;                                # namespace absent
  *"config current-context"*) echo fake-cluster ;;
  # Cluster-scoped inventory: nothing of ours exists in this fixture, so the
  # script should find nothing to delete.
  *"get crd"*|*"get clusterrole"*|*"get mutatingwebhookconfiguration"*|*"get validatingwebhookconfiguration"*) ;;
  # The mutations under test. Listed explicitly so the assertions below are
  # checking commands this fixture actually sanctions.
  *"delete pvc "*|*"delete pv "*|*"patch pv "*|*"delete ns "*) ;;
  *"rollout status"*|*" logs "*|*"apply -f"*|*"delete ds "*) ;;
  # Anything else is a command this test has never reviewed. Falling through
  # to success would let a regression introduce an extra mutation -- a stray
  # delete, a namespace-wide wipe -- and still pass.
  *) echo "UNMODELED kubectl $*" >> "$CALLS"; exit 97 ;;
esac
exit 0
EOF
cat > "$FAKE/helm" <<'EOF'
#!/usr/bin/env bash
echo "helm $*" >> "$CALLS"
case "${1:-}" in
  status) exit 1 ;;                 # not installed
  *) echo "UNMODELED helm $*" >> "$CALLS"; exit 97 ;;
esac
EOF
chmod +x "$FAKE/kubectl" "$FAKE/helm"
export PATH="$FAKE:$PATH" CALLS

# expect_rc <desc> <want> <got>
expect_rc() {
    if [ "$3" = "$2" ]; then pass=$((pass+1)); printf '  ok   %s\n' "$1"
    else fail=$((fail+1)); printf '  FAIL %s\n       exit %s, want %s\n' "$1" "$3" "$2"
    fi
}

echo "== dry run makes no changes =="
: > "$CALLS"
out="$("$SCRIPT" 2>&1)"; rc=$?
expect_rc "exits 0"                    0 "$rc"
refute  "issues no unmodeled command"  "UNMODELED" "$(cat "$CALLS")"
check   "announces dry run"            "DRY RUN" "$out"
refute  "issues no delete"             "kubectl delete" "$(cat "$CALLS")"
refute  "issues no patch"              "kubectl patch"  "$(cat "$CALLS")"
# apply is a mutation too: the node-state step creates a privileged cleanup
# DaemonSet with it. Dry run must not create that either.
refute  "creates no cleanup DaemonSet" "kubectl apply"  "$(cat "$CALLS")"
refute  "uninstalls no helm release"   "helm uninstall" "$(cat "$CALLS")"

echo "== ownership selection (--apply) =="
: > "$CALLS"
out="$("$SCRIPT" --apply --keep-node-state 2>&1)"; rc=$?
calls="$(cat "$CALLS")"
expect_rc "exits 0"                    0 "$rc"
refute  "issues no unmodeled command"  "UNMODELED" "$calls"
# Full argument strings, not fragments: a regression that drops the namespace
# would still satisfy a "delete pvc rox-abc123" substring search and delete a
# claim of the same name in whatever namespace kubectl defaults to.
check  "deletes our rox- claim in-namespace" \
       "delete pvc rox-abc123 -n nvsnap-system --ignore-not-found --wait=false" "$calls"
check  "deletes our rwx- claim in-namespace" \
       "delete pvc rwx-abc123 -n nvsnap-system --ignore-not-found --wait=false" "$calls"
refute "spares an unrelated claim"     "someone-elses-data"    "$calls"
check  "reclaims our released PV"      "delete pv pv-ours"     "$calls"
refute "spares a PV from another claim" "pv-theirs"            "$calls"
refute "never touches a Bound PV"      "pv-bound-ours"         "$calls"
check  "flips Retain to Delete"        \
       'patch pv pv-ours -p {"spec":{"persistentVolumeReclaimPolicy":"Delete"}}' "$calls"
# Ordering matters: deleting a Retain PV first strands the backing volume,
# which is the exact leak this step exists to stop. Substring checks cannot
# see order, so compare line numbers.
patch_line=$(printf '%s\n' "$calls" | grep -n "patch pv pv-ours" | head -1 | cut -d: -f1)
del_line=$(printf '%s\n'   "$calls" | grep -n "delete pv pv-ours" | head -1 | cut -d: -f1)
if [ -n "$patch_line" ] && [ -n "$del_line" ] && [ "$patch_line" -lt "$del_line" ]; then
    pass=$((pass+1)); printf '  ok   %s\n' "patches reclaim policy BEFORE deleting the PV"
else
    fail=$((fail+1)); printf '  FAIL %s\n       patch@%s delete@%s\n' \
        "patches reclaim policy BEFORE deleting the PV" "${patch_line:-none}" "${del_line:-none}"
fi
# helm uninstall must not run when the release is absent.
refute "no helm uninstall when not installed" "helm uninstall" "$calls"

echo "== node state is opt-out =="
refute "skipped with --keep-node-state" "nvsnap-cleanup" "$calls"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
