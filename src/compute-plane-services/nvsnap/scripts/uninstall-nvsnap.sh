#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Remove NvSnap from a cluster, including the parts `helm uninstall` does not
# reach: cluster-scoped objects, per-capture volumes, and node-local state.
#
# Dry-run by default. Nothing is destroyed until --apply is passed.
#
#   ./scripts/uninstall-nvsnap.sh                 # report what would go
#   ./scripts/uninstall-nvsnap.sh --apply         # do it
#   ./scripts/uninstall-nvsnap.sh --apply --keep-node-state
#
# Why this exists rather than a wiki page of kubectl commands: a manual
# teardown selected persistent volumes by StorageClass, which on a shared class
# matched another team's volume and issued a delete against it. The finalizer
# saved the data, but the lesson is that ownership must be established from
# ownership metadata -- claimRef namespace, or our own naming -- never from a
# property the resource merely shares with unrelated workloads.
set -uo pipefail

NAMESPACE="${NVSNAP_NAMESPACE:-nvsnap-system}"
RELEASE="${NVSNAP_RELEASE:-nvsnap}"
APPLY=0
KEEP_NODE_STATE=0
for a in "$@"; do
    case "$a" in
        --apply)            APPLY=1 ;;
        --keep-node-state)  KEEP_NODE_STATE=1 ;;
        -h|--help)          sed -n '4,14p' "$0"; exit 0 ;;
        *) echo "unknown argument: $a" >&2; exit 2 ;;
    esac
done

# Node-local state. Listed explicitly: no globs, no prefix matching. These are
# passed to rm -rf on the host, so every entry is reviewed here rather than
# constructed at runtime.
NODE_PATHS=(
    /var/lib/nvsnap
    /var/lib/containerd/nvsnap-checkpoints
)

say()  { printf '%s\n' "$*"; }
step() { printf '\n=== %s ===\n' "$*"; }
run()  {
    if [ "$APPLY" = "1" ]; then "$@"
    else printf '  [dry-run] %s\n' "$*"
    fi
}

[ "$APPLY" = "1" ] || say "DRY RUN — nothing will be deleted. Re-run with --apply."
say "cluster: $(kubectl config current-context 2>/dev/null || echo unknown)"
say "namespace: $NAMESPACE   release: $RELEASE"

# 1. Admission webhooks first. A webhook whose backing service is gone either
#    silently stops mutating (failurePolicy=Ignore) or blocks pod admission
#    (Fail). Either way it must not outlive the service it points at.
step "admission webhooks"
for kind in mutatingwebhookconfiguration validatingwebhookconfiguration; do
    for w in $(kubectl get "$kind" -o name 2>/dev/null | grep -i nvsnap); do
        say "  $w"; run kubectl delete "$w" --ignore-not-found
    done
done

step "helm release"
if helm status "$RELEASE" -n "$NAMESPACE" >/dev/null 2>&1; then
    run helm uninstall "$RELEASE" -n "$NAMESPACE" --wait --timeout 5m
else
    say "  not installed"
fi

# 2. Per-capture volumes. Ownership comes from the claim's namespace and our
#    rox-/rwx- naming, NOT from the StorageClass -- the L2 class is routinely
#    shared with unrelated workloads.
step "per-capture PVCs"
# Ownership is established from OUR naming convention and labels, never from
# namespace membership alone -- NVSNAP_NAMESPACE is configurable and a shared
# namespace may hold volumes we do not own. Anything else here is left alone.
OWNED_PVCS=$(kubectl get pvc -n "$NAMESPACE" -o json 2>/dev/null | python3 -c '
import json,sys
for i in json.load(sys.stdin).get("items", []):
    n = i["metadata"]["name"]
    lbl = i["metadata"].get("labels") or {}
    ours = n.startswith("rox-") or n.startswith("rwx-") \
        or any(k.startswith("nvsnap.io/") for k in lbl) \
        or lbl.get("app.kubernetes.io/part-of") == "nvsnap"
    if ours:
        print(n)
')
if [ -z "$OWNED_PVCS" ]; then
    say "  none matching the nvsnap naming/label convention"
else
    for pvc in $OWNED_PVCS; do
        say "  $pvc"; run kubectl delete pvc "$pvc" -n "$NAMESPACE" --ignore-not-found --wait=false
    done
    # PVC deletion is asynchronous. Scanning PVs before the claims are gone
    # skips volumes still Bound; they go Released moments later, after this
    # script has moved on, leaving exactly the stranded capacity it exists to
    # reclaim. Wait for the claims to actually disappear.
    if [ "$APPLY" = "1" ]; then
        say "  waiting for claims to clear..."
        for _ in $(seq 1 60); do
            left=$(kubectl get pvc -n "$NAMESPACE" --no-headers 2>/dev/null \
                   | awk '{print $1}' | grep -cE '^(rox|rwx)-' || true)
            [ "${left:-0}" = "0" ] && break
            sleep 2
        done
        [ "${left:-0}" = "0" ] || say "  WARNING: $left claim(s) still present; their PVs are left untouched"
    fi
fi

# Released PVs whose claim was one of ours. Ownership is the claimRef name
# (our rox-/rwx- convention) plus the namespace -- never the StorageClass,
# which is routinely shared. A Retain policy means the backing volume outlives
# the PV, so flip to Delete first and let the CSI driver reclaim it; otherwise
# capacity is stranded silently, which is how ~1.2 TB accumulated unnoticed.
step "orphaned PVs previously claimed by $NAMESPACE"
for pv in $(kubectl get pv -o json 2>/dev/null \
        | python3 -c '
import json,sys
ns=sys.argv[1]
for i in json.load(sys.stdin)["items"]:
    cr=i["spec"].get("claimRef") or {}
    if cr.get("namespace")!=ns:            # ownership, not storage class
        continue
    if not (cr.get("name","").startswith("rox-") or cr.get("name","").startswith("rwx-")):
        continue                            # our per-capture naming only
    if i["status"]["phase"] not in ("Released","Available","Failed"):
        continue                            # never touch a Bound volume
    print(i["metadata"]["name"])
' "$NAMESPACE"); do
    pol=$(kubectl get pv "$pv" -o jsonpath='{.spec.persistentVolumeReclaimPolicy}' 2>/dev/null)
    say "  $pv (policy=$pol)"
    [ "$pol" = "Retain" ] && run kubectl patch pv "$pv" \
        -p '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}'
    run kubectl delete pv "$pv" --ignore-not-found --wait=false
done

step "CRDs and cluster RBAC"
for r in $(kubectl get crd -o name 2>/dev/null | grep -i 'nvsnap\.io'); do
    say "  $r"; run kubectl delete "$r" --ignore-not-found
done
for kind in clusterrole clusterrolebinding; do
    for r in $(kubectl get "$kind" -o name 2>/dev/null | grep -i nvsnap); do
        say "  $r"; run kubectl delete "$r" --ignore-not-found
    done
done

# 3. Node-local state. helm never owned these, so they survive every teardown
#    and accumulate. Reached with a short-lived privileged DaemonSet.
step "node-local state"
if [ "$KEEP_NODE_STATE" = "1" ]; then
    say "  skipped (--keep-node-state)"
else
    printf '  paths: %s\n' "${NODE_PATHS[*]}"
    CLEANER_NS="${NAMESPACE}"
    kubectl get ns "$CLEANER_NS" >/dev/null 2>&1 || CLEANER_NS=default
    # No double quotes inside: this string is embedded in a YAML flow-style
    # command array, and inner quoting breaks the parse. The paths are fixed
    # and contain no spaces, so word-splitting is not a concern here.
    ACTION='for p in PATHS; do if [ -e $p ]; then du -sh $p; rm -rf $p; echo removed $p; else echo absent $p; fi; done; echo DONE; sleep 3600'
    ACTION="${ACTION/PATHS/${NODE_PATHS[*]}}"
    if [ "$APPLY" = "1" ]; then
        kubectl apply -f - <<YAML
apiVersion: apps/v1
kind: DaemonSet
metadata: { name: nvsnap-cleanup, namespace: $CLEANER_NS }
spec:
  selector: { matchLabels: { app: nvsnap-cleanup } }
  template:
    metadata: { labels: { app: nvsnap-cleanup } }
    spec:
      tolerations: [{ operator: Exists }]
      hostPID: false
      containers:
        - name: cleanup
          image: busybox:1.36
          securityContext: { privileged: true }
          command: ["/bin/sh","-c","$ACTION"]
          volumeMounts:
            - { name: varlib, mountPath: /var/lib }
      volumes:
        - name: varlib
          hostPath: { path: /var/lib, type: Directory }
YAML
        kubectl rollout status ds/nvsnap-cleanup -n "$CLEANER_NS" --timeout=180s
        # rollout status only proves the pods started. rm -rf may still be
        # running, and deleting the DaemonSet now would kill it partway. Wait
        # for every pod to print DONE before tearing the cleaner down.
        want=$(kubectl get ds nvsnap-cleanup -n "$CLEANER_NS" \
               -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null)
        done_count=0
        for _ in $(seq 1 90); do
            done_count=$(kubectl logs -n "$CLEANER_NS" -l app=nvsnap-cleanup --tail=-1 2>/dev/null \
                         | grep -c '^DONE$' || true)
            [ "${done_count:-0}" -ge "${want:-1}" ] && break
            sleep 2
        done
        kubectl logs -n "$CLEANER_NS" -l app=nvsnap-cleanup --tail=20 --prefix 2>/dev/null | sed 's/^/    /'
        kubectl delete ds nvsnap-cleanup -n "$CLEANER_NS" --ignore-not-found
        if [ "${done_count:-0}" -lt "${want:-1}" ]; then
            say "  ERROR: only $done_count/$want nodes reported completion; node state may remain"
            exit 1
        fi
        say "  completed on $done_count/$want nodes"
    else
        say "  [dry-run] would run a privileged DaemonSet removing the paths above on every node"
    fi
fi

step "namespace"
kubectl get ns "$NAMESPACE" >/dev/null 2>&1 && { say "  $NAMESPACE"; run kubectl delete ns "$NAMESPACE" --wait=false; } || say "  absent"

step "verification"
for q in "ns/$NAMESPACE:kubectl get ns $NAMESPACE" \
         "crds:kubectl get crd" \
         "clusterroles:kubectl get clusterrole,clusterrolebinding" \
         "webhooks:kubectl get mutatingwebhookconfiguration,validatingwebhookconfiguration"; do
    label="${q%%:*}"; cmd="${q#*:}"
    n=$($cmd --no-headers 2>/dev/null | grep -ci nvsnap)
    printf '  %-16s %s remaining\n' "$label" "$n"
done
say ""
say "Bound PVs are never touched by this script. If a capture volume survives,"
say "its PVC still exists somewhere -- find it before forcing anything."
