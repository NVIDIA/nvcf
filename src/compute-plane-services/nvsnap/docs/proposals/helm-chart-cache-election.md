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

## Claims across namespaces

NVCF runs every chart in its own namespace, so the capture and the
restores usually live apart, and a PVC is namespaced. The promoter grows
`EnsureClaim(hash, ns)`: make `rox-<hash>` exist in `ns` from the promoted
artifact. Two strategies, the same shape NVCA uses for one model volume
across tenant namespaces (`pkg/storage/modelcache.go`):

- shared-volume (NVMesh, EFS, Filestore): one more secondary static PV,
  `nvsnap-ro-pv-<hash>-<ns8>`, with the CSI handle rewritten for that
  namespace, pre-bound to a `rox-<hash>` claim there. Zero copy.
- snapshot-clone with ReadOnlyMany (Hyperdisk ML): a VolumeSnapshot is
  namespaced and a clone must name one in its own namespace, so the
  promote's snapshot handle is re-exposed there through a pre-provisioned
  VolumeSnapshotContent + VolumeSnapshot pair (Retain), then cloned.
- per-pod clone: no shared claim exists to reproduce; `ErrUnsupported`,
  the pod starts cold.

Two call sites. `Mount` on a claim miss mints the claim and retries, which
covers a restore admitted after the promote in any namespace. The promote
itself, before publishing `ready`, mints the claim in every namespace that
already holds pods stamped with the hash (gated followers admitted before
the promote), so they find it bound when the server releases them. All
minted objects carry `nvsnap.io/hash-short` and `nvsnap.io/namespace`;
`Delete` lists and reaps them.

A restore namespace also needs the agent token Secret and the restore-pod
NetworkPolicy, both already fanned out by `agent.l2.restoreNamespaces`.
That policy selects every pod in the namespace and allows egress only to
nvsnap-server, so in a namespace with no other egress allows it becomes
default-deny for everything else, DNS included, and a restored vLLM cannot
resolve huggingface.co (it lists the repo even with a warm cache). NVCF
function namespaces carry NVCA's egress allows; a bare test namespace does
not. Seen on dev1 2026-09-25; the chart should allow DNS in that policy or
document the requirement.

## Grove and the scheduling gate

Under the Dynamo operator, Grove gang scheduling owns `schedulingGates` and
rewrote the follower's list at creation (managedFields: `grove-operator`,
same second), removing `nvsnap.io/wait-for-cache`. The follower then sits
`Unschedulable` on volume binding instead, because `rox-<hash>` does not
exist yet; it still holds no node and no GPU, and schedules when the promote
creates the claim. Same outcome, noisier events. Stock charts keep the gate.

## Verification

Unit: classifier matrix, election win/lose/error, follower patch shape
(gate, claim name, no GPU held), watcher uses the stamped hash, server
ungate and eviction against a fake clientset.

E2E: `scripts/test-election-e2e.sh` drives `deploy/k8s/charts/vllm-workers`
(a stock vLLM Deployment with no nvsnap markers; model, TP and replicas are
values). Historically the TinyLlama TP=2 chart from the scale-up test, `replicas=1` then
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

### Cross-namespace, dev1 2026-09-25

Capture only in `nvsnap-system` (hash c8bbf555); the same stock chart
installed in `nvsnap-xns`, replicas=2, admitted in the same second, agent
v0.2.75-election6, NVMesh shared-volume:

```
webhook   "L2 claim minted in restore namespace" namespace=nvsnap-xns, then both
          "election: promoted capture exists; restoring"
claim     nvsnap-xns/rox-c8bbf555... Bound to nvsnap-ro-pv-c8bbf555...-1cfa3cd7
PV        handle single-zone-cluster:csi-...:nvsnap-xns, ro, [ro norecovery nouuid]
mount     /dev/nvmesh/csi-... on /opt/nvsnap type xfs (ro,nouuid,norecovery), 2.1G model tree
pods      both role=restore, Ready +60s, 0 download lines, serve " Paris."
```

The first attempt exposed a create-then-label race between the two
admissions ("the object has been modified"); the per-namespace PV is now
created already labelled and the fresh concurrent mint passed.

### Dynamo disaggregated, dev1 2026-09-25

NVCF Dynamo sample (vllm-runtime 1.1.1, Qwen3-0.6B, frontend + prefill +
decode). With the role-neutral hash both workers compose 18313f93: prefill
elected leader, decode follower, frontend ignored. The gang did not
schedule: `kai-scheduler` places the podgang `myllm-0` as a unit and the
follower is unschedulable until `rox-<hash>` exists, so the leader is held
with it. See "Grove and the scheduling gate"; the resolution for gang
scheduling is a design decision recorded in issue #2099.

### Qwen2.5-32B-Instruct TP=4 chart, dev1 2026-09-25

Stock Deployment, vLLM v0.20.0, torch.compile on (no enforce-eager),
replicas=2 on one node, agent v0.2.75-election6, NVMesh. Capture 65.7 GB,
805 files.

```
first deploy, replicas=2
  t+0        leader + follower admitted (same second); follower gated, no GPU
  t+325s     leader Ready (cold)
  t+439s     capture copied + promoted, follower released
  t+600s     follower Ready (161 s after release), 0 downloads, serves
             one download of 65.7 GB instead of two
uninstall + reinstall, replicas=2
  t+170s     both Ready (prewarm init 83 s each), both role=restore

engine phases            cold (leader)   warm (follower)
  model load + download  247 s           6.4 s
  torch.compile          57 s            8.5 s   (graphs loaded from cache, 2.7 s each range)
  init engine total      77 s            22 s
  CUDA graph capture     10 s            10 s    (not cacheable)
```

### Qwen3-235B-A22B-Instruct-2507-FP8 TP=8 chart, two nodes, dev1 2026-09-25

Stock chart (`deploy/k8s/charts/vllm-workers`, `--enable-expert-parallel`;
the FP8 block quantization does not split the MoE intermediate eight ways
without it), replicas=2, one worker per 8x H100 node, agent v0.2.75-election6,
NVMesh. Capture 237.0 GB, 2006 files.

```
first deploy, replicas=2
  t+0        leader + follower admitted; follower gated, no GPU
  t+870s     leader Ready (cold: 641 s download+load, 94 s compile, 292 s init, 42 s graphs)
  t+~1165s   capture committed + promoted, follower released
  t+1524s    follower Ready (359 s after release), 0 downloads, serves
             one download of 237 GB instead of two
uninstall + reinstall, replicas=2, both nodes reading the same rox at once
  t+367s     both Ready; prewarm init 230 s each (237 GB), engine 17 s load, 15 s compile, 48 s init

engine phases            cold (leader)   warm (follower / reinstall)
  model load + download  641 s           16-17 s
  torch.compile          94 s            15 s
  init engine total      292 s           48 s
  CUDA graph capture     42 s            28 s
```

At this size the warm start is the volume read: 230 s of the 367 s is the
prewarm sweep at ~1 GB/s, shared by two readers. The engine work that the
cache removes went from 641+94 s to 17+15 s.
