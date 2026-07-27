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
  *) ;;
esac
exit 0
EOF
cat > "$FAKE/helm" <<'EOF'
#!/usr/bin/env bash
echo "helm $*" >> "$CALLS"
[ "${1:-}" = "status" ] && exit 1   # not installed
exit 0
EOF
chmod +x "$FAKE/kubectl" "$FAKE/helm"
export PATH="$FAKE:$PATH" CALLS

echo "== dry run makes no changes =="
: > "$CALLS"
out="$("$SCRIPT" 2>&1)"
check   "announces dry run"            "DRY RUN" "$out"
refute  "issues no delete"             "kubectl delete" "$(cat "$CALLS")"
refute  "issues no patch"              "kubectl patch"  "$(cat "$CALLS")"

echo "== ownership selection (--apply) =="
: > "$CALLS"
out="$("$SCRIPT" --apply --keep-node-state 2>&1)"
calls="$(cat "$CALLS")"
check  "deletes our rox- claim"        "delete pvc rox-abc123" "$calls"
check  "deletes our rwx- claim"        "delete pvc rwx-abc123" "$calls"
refute "spares an unrelated claim"     "someone-elses-data"    "$calls"
check  "reclaims our released PV"      "delete pv pv-ours"     "$calls"
refute "spares a PV from another claim" "pv-theirs"            "$calls"
refute "never touches a Bound PV"      "pv-bound-ours"         "$calls"
check  "flips Retain before deleting"  "patch pv pv-ours"      "$calls"

echo "== node state is opt-out =="
refute "skipped with --keep-node-state" "nvsnap-cleanup" "$calls"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
