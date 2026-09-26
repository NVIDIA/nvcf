# Helm functions: every expensive artifact produced once per cluster

Goal: for any Helm chart of GPU model workers, on any registry, any
deployment shape and any scheduler, the model is downloaded once per cluster
and the compile artifacts (torch.compile, Triton, FlashInfer, DeepGEMM, CUDA
JIT) are built once per cluster, and every other pod consumes them. No chart
change. No pod is ever held back from scheduling.

Status: design, 2026-09-26. Supersedes the gate-and-promote path of
`helm-chart-cache-election.md` for Helm functions. Issue #2099.

```mermaid
sequenceDiagram
    participant J as Job nvsnap-model-dl-<key>
    participant P as pods 0..N (readers, any node, any namespace)
    participant WH as webhook
    participant A as agent
    participant V as model volume nvsnap-model-<key>
    WH->>WH: identity, landing path, group
    WH->>J: create (idempotent): the chart's download step, into V
    WH-->>P: landing path -> V (DFS) or hostPath + wait init (NVMesh)
    Note over P: all pods schedule immediately
    J->>V: download; touch .nvsnap-complete; exit 0, volume released
    A->>V: complete: label claim; NVMesh: ro PV + bind into P's hostPath
    P->>P: wait init sees marker, engine starts
    Note over P: engines start together; multi-node groups form as today
```

## Two artifacts, two lifecycles

| Artifact | Producer | Written when | Consumers may run concurrently with producer? |
|---|---|---|---|
| Model tree | one download step | before any engine starts; immutable afterwards | no: it is complete before consumers need it |
| Compile caches | every rank of every pod | during engine init | yes: ranks of one multi-node instance compile at the same time |

This split is the whole design. The model is a write-once file set, so it
can be produced by one pod and attached read-only by all others on any
storage, including block. Compile caches are produced concurrently by
ranks that cannot wait for each other, so sharing them within one first
start needs a shared writable filesystem; sharing them across starts does
not.

## The five mechanisms

1. Identity and landing path (webhook). Identity is a URI derived from the
   pod: `hf://org/repo[@rev]` from `--model`, `--model-path`, `--model=`,
   positional `vllm serve <x>`, `HF_MODEL_ID`, `MODEL_ID`; `ngc://org/team/
   model:ver` from an init's `NGC_MODEL_NAME` or `ngc registry model
   download-version`; `s3://` from `aws s3 sync`; KServe `storageUri`;
   `nim://image@profile`. Members of a group with no identity of their own
   (LWS `--headless` workers) inherit from the group's template via owner
   references (`leaderworkerset.sigs.k8s.io/group-key`, StatefulSet,
   `grove.io/podgang`). Landing path is where the download writes:
   `HF_HOME`, `NIM_CACHE_PATH`, the init's dest, `/mnt/models`. If the
   volume there is already a PVC, hostPath or OCI image, skip the pod.
   Role flags and wiring env stay out of the hash (`stripRoleFlags`).

2. One download step per identity per cluster, as a Job (webhook). The
   webhook creates Job `nvsnap-model-dl-<key>` in the pod's namespace on
   first sight; create is idempotent, so concurrent admissions need no
   election. The Job's pod is the chart's own download init (image,
   command, env, secrets, pull secrets, tolerations copied from the
   admitted pod) wrapped to touch `<volume>/.nvsnap-complete` on success;
   when the engine downloads itself the Job runs `hf download <repo>`
   with the engine image and credentials. Every workload pod is a reader:
   its download init becomes a wait for the marker, and an engine that
   downloaded itself is started offline.

   Why a Job and not the first pod: on NVMesh a volume attached read-write
   by a running pod cannot be attached read-only anywhere else (dev1,
   2026-09-26: `NVMesh Attach Failed` on the read-only PV while the writer
   pod held the primary). The download step has to exit and release the
   volume before readers attach, so it cannot live inside a pod that goes
   on to serve. The Job's pod must also be removed after success
   (`ttlSecondsAfterFinished`): a Succeeded pod keeps its volumes attached,
   and on dev1 the read-only attach worked on the Job's node but failed on
   every other node until that pod was deleted. A Job also decouples the download from the workload's
   scheduling: it runs on any node with the image, and the workload pods
   of a multi-node group or a gang all schedule as plain readers.

3. Model volume per identity, immutable after download (agent + storage
   profile). Distributed filesystem: one RWX volume; writer and readers
   mount it at admission; completion is a marker file the writer's init
   writes on exit 0. NVMesh: the writer's PVC is the artifact; on the
   init's exit 0 the agent creates the read-only static PV and marks it
   complete; readers attach it read-only. Later deployments, other
   namespaces (`EnsureClaim`, done) and new versions of the function all
   attach the same volume. There is no capture copy of the model anymore.

4. Readers never block scheduling (webhook + agent). On a distributed
   filesystem the RWX claim exists and is bound at admission, so the pod
   schedules. On NVMesh, before the download is complete, a reader cannot
   reference a bindable claim, so it gets an emptyDir at the landing path
   plus `nvsnap-wait-model`; once complete, the agent on the reader's node
   attaches the read-only volume (mount-holder, existing) and bind-mounts
   it over the emptyDir, then drops the marker the wait init is polling.
   After completion, NVMesh readers reference the read-only claim directly.
   No pod ever needs network access to nvsnap; function namespaces block it.

5. Compile caches (webhook env + agent). All caches are redirected to a
   cache location keyed by image digest plus identity plus role-neutral
   args. Distributed filesystem: `<volume>/cache/<key>/`, read-write for
   every pod; the engines' `filelock` and atomic replace make identical
   compiles converge; `cacheMode: shadow` in the profile keeps a per-pod
   writable copy seeded from it where the operator does not trust the
   filesystem's locks (Lustre needs `-o flock`; the agent checks). NVMesh:
   each pod compiles into a local emptyDir on the first start; after the
   writer is Ready the agent captures its cache dir into a read-only cache
   volume `nvsnap-cache-<key>` (the existing capture path, now caches
   only, hundreds of MB); every later pod mounts it read-only with a
   writable shadow (today's seed init).

## Every scenario, same mechanisms

Shapes: D = single-pod Deployment replicas; M = multi-pod instance (LWS,
StatefulSet, Dynamo podgang, prefill+decode). Storage: DFS, NVMesh. State:
first = nothing exists; concurrent = download in flight; later = complete.

| Shape / storage / state | Download | Compile | Pods held? |
|---|---|---|---|
| D, DFS, first | 1 (writer init); readers wait on marker then start | once, shared cache, engines lock | no |
| M, DFS, first | 1; readers wait on marker; group forms when all engines start | ranks share cache; duplicates limited to races within one start | no |
| any, DFS, concurrent or later | 0 | 0 | no |
| D, NVMesh, first | 1; readers get bind-mounted ro volume on completion | writer compiles; readers compile locally once, then cache volume exists | no |
| M, NVMesh, first | 1; readers bind-mounted on completion; group forms | each pod compiles once (concurrent ranks, no shared fs) | no |
| any, NVMesh, later | 0 (ro claim at admission) | 0 (cache volume ro + shadow) | no |
| any, other namespace, later | 0 (`EnsureClaim`) | 0 | no |
| neither storage | nvsnap does nothing for Helm | | no |

The one row that does not reach "once per cluster" is M on NVMesh on the
first start of a model, for compile only: each pod of that instance builds
its kernels once, because its ranks need the binaries while the other
pod's ranks are still building them and there is no shared filesystem
between them. Everything after that first start is a full hit.

## Failure behaviour (decided: always fall back, never deadlock)

| Failure | Effect | Recovery |
|---|---|---|
| download Job fails or never completes | Job retries with backoff; readers' wait reaches the deadline | readers download locally (NVMesh) or into the volume (DFS; per-file atomic); the next admission recreates a missing Job |
| volume full | writer's download fails, init restarts | same as above; retention by last use with a size budget is part of this design's follow-up, since a full volume fails every writer |
| agent down on a reader node (NVMesh) | no bind arrives | wait deadline, local download |
| writer pod restarts after complete | volume immutable, unaffected | none needed |
| identity changes (revision, quantization, image) | different URI or cache key | separate volume; old one ages out |
| gang scheduler | readers always schedulable (RWX bound, or hostPath); download claim binds in seconds on Immediate storage classes | none needed |
| identity deleted while a read-only PV is still Terminating (NVMesh) | the read-only PV name is deterministic per identity and namespace, so a re-download of the same identity cannot mint until the old PV finalizes; the attacher's detach timed out for minutes after the volume was gone | retention deletes read-only claims and PVs before the primary, and the controller retries minting; a stale VolumeAttachment on a deleted volume needs the finalizer cleared (seen on dev1 2026-09-26) |
| last reader of an identity leaves a node (NVMesh) | the agent's bind mount keeps the volume published; kubelet cannot unmount and the attacher's detach times out (seen on dev1 2026-09-26 during cleanup) | the agent must unbind and drop its mount-holder when no pod on the node uses the identity; part of retention (follow-up) |

## What changes in the code

Stays: classifier (extended per mechanism 1), role-neutral hash,
`EnsureClaim`, storage profiles, cache env
injection and seed init, mount-holder and bind injection (L1), the server
reconciler, `vllm-workers` chart and runner.

New: identity from init containers and group inheritance; the download
Job derived from the chart's init or from `hf download`; init wrapping
for the wait; per-identity model volume created at admission from the profile's
class (RWX on DFS, RWO writer PVC on NVMesh); agent completion handler
(init exit 0 -> marker / ro PV + bind); cache volume capture (caches only)
on NVMesh; `cacheMode`; last-use labels.

Removed for Helm: `schedulingGates`, promote-to-ROX of the whole tree,
`nvsnap-l2-wait`, `restore-from`.

## Build order and verification

1. Identity, landing path, group inheritance; tests from real specs
   (prd11 NGC function, Dynamo sample, LWS example, plain Deployment).
2. Model volume and writer path on both storage classes; readers on DFS
   (marker wait).
3. NVMesh readers: agent completion, ro attach, bind into emptyDir.
4. Compile caches: shared on DFS, cache-volume capture on NVMesh.
5. Retire the gate; e2e on dev1 in all matrix rows that dev1 can host
   (NVMesh; DFS stands in with an NFS class), each measured cold, first
   deploy with two pods per instance, redeploy, second namespace.
