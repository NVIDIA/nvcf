# Helm chart cache reuse: one downloader per hash

Goal: when a chart deploys N model workers, exactly one downloads and
initializes; the rest start from its cache. If the cache already exists,
every worker starts from it. No change to the customer's chart.

Status: implemented on branch `nvsnap/helm-election`, verified on dev1 2026-09-25 (results below).
Supersedes the `nvsnap.io/restore-from: "auto"` annotation, which required
a template opt-in and could not be a capture source (issue #2099).

```mermaid
sequenceDiagram
    participant RS as ReplicaSet
    participant WH as webhook (agent)
    participant L as Lease nvsnap-capture-<hash>
    participant A as agent watcher
    participant S as nvsnap-server
    RS->>WH: create pod 1 (GPU + model)
    WH->>WH: hash = compose(spec); stamp nvsnap.io/hash
    WH->>L: create (holder = pod 1 UID)
    L-->>WH: created
    WH-->>RS: leader: capture decoration, capture label
    RS->>WH: create pod 2..N
    WH->>L: create
    L-->>WH: AlreadyExists
    WH-->>RS: follower: rox mount + schedulingGate, label nvsnap.io/gated
    A->>A: pod 1 Ready, capture, promote rox-<hash>
    A->>S: pvc-state ready
    S->>RS: remove gate on pods with label hash=<hash>
    RS->>RS: pods 2..N schedule, mount rox, start warm
```

## Decision at admission

For every pod created, the webhook runs this before any annotation logic.

1. Classify. A pod is a model worker when it requests `nvidia.com/gpu`
   and the main container has an inferable model identity: `--model` or
   `--model-path` in args, `HF_MODEL_ID`/`MODEL_ID`/`NIM_MODEL_NAME` env,
   or a NIM image. Anything else (routers, frontends, etcd, nats) is not
   touched. An explicit `nvsnap.io/restore-from` still takes the old path.
2. Hash once. Compose the hash from the incoming spec (image tag, model
   identity, command, args, cache-relevant env, driver major, format
   version). Stamp it as annotation `nvsnap.io/hash` (full) and label
   `nvsnap.io/hash` (short, selectable). The watcher reads the annotation
   instead of recomposing from the live pod, which removed the mismatch
   between admission-time and capture-time inputs (image digest, injected
   env).
3. Ready: a usable manifest exists and the rox PVC is Bound. Restore
   decoration, same as an explicit-hash restore today. Done.
4. Elect. Try to create Lease `nvsnap-capture-<hash>` in nvsnap-system
   with the pod UID as holder and a deadline. Create is atomic, so N
   simultaneous admissions produce one winner.
   - Winner (leader): capture decoration (cache env, `/opt/nvsnap`
     emptyDir), label `nvsnap.io/capture=true`, annotation
     `nvsnap.io/role=leader`. Runs at once.
   - Loser (follower): restore decoration against the deterministic claim
     name `rox-<hash>` even though it is not Bound yet, plus
     `schedulingGates: [nvsnap.io/wait-for-cache]`, label
     `nvsnap.io/gated=true`, annotation `nvsnap.io/role=follower`. Holds no
     node and no GPU.
5. Fail open. Any error in classify, compose, or the Lease call admits the
   pod unchanged. The webhook stays an optimization.

Followers need ReadOnlyMany storage (shared-volume or snapshot-clone with
`readOnlyMany: true`). On per-pod-clone storage there is no claim to name
ahead of time; followers are admitted unchanged and start cold.

## Release and failure

nvsnap-server already receives the promote state per hash.

- `ready`: list pods with labels `nvsnap.io/hash=<short>,nvsnap.io/gated=true`
  across namespaces, patch `spec.schedulingGates: []` and
  `nvsnap.io/gated=false`. The rox is Bound at this point, so the follower
  schedules, mounts it, and starts warm. The `nvsnap-l2-wait` init container
  stays as a second check and exits at once.
- `failed`, or the leader pod is gone or Failed before commit, or the Lease
  deadline passes: the server reconciler deletes the Lease and deletes the
  gated followers that have a controller owner. Their controller recreates
  them, admission runs again with no Lease, and a new leader is elected.
  Gated pods without an owner are left in place and logged. A follower's
  volumes reference a PVC that will never exist, so recreation is the only
  way to change its fate; pod volumes are immutable after creation.

The leader Lease is not renewed. Its deadline is the admission time plus a
configurable bound (default 60 minutes) that covers a cold start plus a
capture; the reconciler treats an expired Lease like a failed leader.

## What already exists

| Piece | Where |
|---|---|
| Hash composition | `internal/rootfsonly/composer.go` |
| Capture decoration | `internal/webhook/cachedir.go` `cacheDirCapturePatches` |
| Restore decoration | `internal/webhook/cachedir.go` `tryL2CacheDir` |
| Per-hash Lease pattern | `internal/checkpointstore/percapture_pvc.go` `acquireLease` |
| Promote state endpoint | `internal/server/sources.go` `updatePVCPromoteStateByHash` |
| Server reconcile loop | `internal/server/reconciler.go` |
| Wait init container | `internal/webhook/l2_mount.go` `buildL2WaitContainer` |

## What changes

- `internal/rootfsonly/composer.go`: export `InferModelID`, add the env
  sources.
- `internal/election` (new): classifier and `LeaseElector`.
- `internal/webhook`: the decision above, follower decoration built from a
  claim name rather than a `Mount` call, pod identity logged as
  `generateName+UID` because Deployment pods have no name at admission.
- `internal/rootfsonly/watcher.go`, `orchestrator.go`: honour the stamped
  hash (`CaptureRequest.Hash`).
- `internal/server`: ungate on `ready`, leader liveness in the reconciler.
- Helm: `agent.election.enabled` (default false until qualified),
  `agent.election.leaseTimeout`; server RBAC gains `leases` get/list/watch.

## Disaggregated workers share one cache

Dynamo prefill and decode workers run the same image and download the
same model; they differ only in a role flag (`--is-prefill-worker`,
`--is-decode-worker`, `--disaggregation-mode`) and in the KV-transfer and
discovery wiring (`--kv-transfer-config`, `DYN_*`, `ETCD_ENDPOINTS`,
`NATS_SERVER`). The hash leaves those out (`stripRoleFlags`,
`internal/rootfsonly/composer.go`), so both roles elect one leader and share
one rox. The model tree is identical across roles and mounts read-only;
compile caches live in the per-pod writable shadow, so a role that needs
different kernels recompiles into it. Flags that change the download
(`--model`, `--revision`, `--tokenizer`, quantization) stay in the hash,
because a pod restoring from a tree that lacks its files would fail on the
read-only mount.

## Verification

Unit: classifier matrix, election win/lose/error, follower patch shape
(gate, claim name, no GPU held), watcher uses the stamped hash, server
ungate and eviction against a fake clientset.

E2E: the TinyLlama TP=2 chart from the scale-up test, `replicas=1` then
`scale 2`, and `replicas=2` from the start. Expected: one capture, second
pod `SchedulingGated` until `ready`, then Ready without a download. Then
`helm uninstall` and reinstall: both pods restore.

## Results, dev1 2026-09-25

Stock Deployment chart, TinyLlama-1.1B on vLLM v0.20.0 TP=2, no nvsnap
labels or annotations in the template, agent v0.2.75-election2, server
v0.0.32-election2, NVMesh shared-volume storage.

```
helm install replicas=2   14:57:37  webhook (same second): leader + follower, hash c8bbf555
                                    follower: SchedulingGated, no node, claim rox-c8bbf555...
leader Ready              +70s
server: capture promoted; followers released   +124s   (released=1)
follower Ready            +53s after release, node different from the leader
                          0 download lines; inits nvsnap-l2-wait, nvsnap-seed-cache, nvsnap-prewarm
                          serves "The capital of France is" -> " Paris."
kubectl scale replicas=3  third pod admitted role=restore, no election; Ready +53s
helm uninstall + install  both pods role=restore; both Ready +50s
```

Cold start of the same pod on the same node without nvsnap: 94 s.

Two things the first runs taught:

- A pod has no UID and no name when a mutating webhook sees its CREATE.
  The Lease holder is an id the webhook mints and stamps on the leader as
  `nvsnap.io/election-id`; the unit fixture had carried a UID and hid this.
- A privileged test pod without `CUDA_VISIBLE_DEVICES` uses whichever GPUs
  it likes, so the device plugin hands "free" GPUs that are 70 GiB full to
  the next pod. The election leader landed on such a node once and vLLM
  refused to start. Node hygiene, not an election failure; the run was
  repeated with that node excluded.
