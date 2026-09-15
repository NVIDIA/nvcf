# Durable CFS Warm flip — controller sweep (nvca#104 follow-up)

## Problem

The `NvSnapFunctionState.status.localCacheState = Warm` flip is produced by
exactly one in-process reconcile goroutine that must survive the entire
Hook B flow:

```
pod-ready event → warmup buffer → POST /checkpoints
  → pollCheckpointTerminal (≤30 min)
  → pollPVCPromoteTerminal (≤15 min, Hyperdisk-ML snap+clone)
  → writeStatus(Warm)
```

If that goroutine is interrupted *after* the capture succeeds but *before*
`writeStatus` — controller restart, leader-election change, a poll that
silently never reaches terminal, or the source pod being scaled away
mid-flight — CFS stays not-Warm.

The only existing recovery is the `recoverExistingCapture` short-circuit
(step 0b in `Reconcile`). It runs at the **top of a reconcile**, so it only
fires when a *new pod stamped `nvsnap.io/checkpoint-on-warm`* appears for the
same function-version. A **restore** pod is not stamped that annotation, and
NVCF does not re-deploy a capture candidate once the function is warm — so
once the source pod is gone, **nothing ever re-evaluates** and CFS is stuck
not-Warm forever, silently cold-starting every restore until an operator
hand-patches it.

Observed on nvsnap-h100-a 2026-06-18 (DeepSeek, fvID `b96ac357`): checkpoint
`Completed 00:23:32`, L2 promote `ready 00:28:14`, source pod alive until
`00:29:30` — yet the reconcile emitted no terminal log and CFS never went
Warm. The next pod (`00:31:09`) was a restore attempt, so step 0b never ran.

## Fix

Add a **periodic CFS reconcile sweep** to the controller, independent of pod
events. The catalog (nvsnap-server) is the source of truth for "does a usable
capture exist"; the sweep reconciles every `NvSnapFunctionState` against it.

```
every SweepInterval (default 60s):
  list NvSnapFunctionState
  for each cfs where state != Warm && !optOut && spec.workloadLookup.imageRef != "":
    hash = findUsableCapture(imageRef, modelId)   # LookupCheckpoints + pvc-state usable
    if hash != "":
      writeStatus(cfs, Warm, hash, capturedHere=true)
```

`findUsableCapture` is the existing `recoverExistingCapture` body, refactored
to take `(imageRef, modelId)` instead of a `*Pod`, so the sweep and the
pod-triggered step-0b share one code path and one definition of "usable"
(L2 promote `ready`, or no-L2 → L1/peer-cascade).

### Persisting the lookup inputs

The sweep has no live pod, so it can't derive `imageRef`/`modelId` from a pod
spec. Hook B persists them onto `NvSnapFunctionState.spec.workloadLookup` right
after `getOrCreateCFS`, *before* the risky poll — so any capture that Hook B
starts is recoverable by the sweep even if the reconcile dies. The CRD uses
`x-kubernetes-preserve-unknown-fields: true`, so no CRD-schema change is
needed.

## Properties

- **Eventually consistent**: CFS goes Warm within one `SweepInterval` of the
  capture completing, regardless of pod/reconcile/controller lifecycle.
- **Idempotent**: a Warm CFS is skipped; re-running the sweep is a no-op.
- **Safe**: only flips Warm when nvsnap-server confirms a `Completed` capture
  with a terminal-usable L2 promote — same gate as the live reconcile.
- **No new RBAC**: list/get/update on `nvsnapfunctionstates` already granted.

## Files

- `pkg/apis/nvsnap/v1alpha1/nvsnapfunctionstate_types.go` — `WorkloadLookupSpec`
  on `Spec`.
- `pkg/nvca/nvsnap/reconciler/state.go` — read/write `spec.workloadLookup`.
- `pkg/nvca/nvsnap/reconciler/recover.go` — extract `findUsableCapture`.
- `pkg/nvca/nvsnap/reconciler/sweep.go` — `SweepOnce`.
- `pkg/nvca/nvsnap/reconciler/reconciler.go` — persist lookup inputs pre-poll.
- `pkg/nvca/nvsnap/controller/controller.go` — sweep ticker in `Run`.
- `pkg/nvca/nvsnap/reconciler/metrics.go` — `nvca_nvsnap_cfs_sweep_recovered_total`.
