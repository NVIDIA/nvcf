# Helm functions: every expensive artifact produced once per cluster

Goal: for any Helm chart of GPU model workers, on any registry, any
deployment shape and any scheduler, the model is downloaded once per cluster
and the compile artifacts (torch.compile, Triton, FlashInfer, DeepGEMM, CUDA
JIT) are built once per cluster, and every other pod consumes them. No chart
change. No pod is ever held back from scheduling.

Status: mechanisms 1 to 4 built and verified on dev1 (H100) and on an
18 node GB300 cluster (arm64), 2026-09-30. Mechanism 5 is redesigned
below as one cache-set volume per configuration and not yet built.
Supersedes the gate-and-promote path of `helm-chart-cache-election.md`
for Helm functions. Issue #2099. Diagrams are checked with
`scripts/check-mermaid.sh`.

```mermaid
sequenceDiagram
    participant WH as webhook (in agent)
    participant P as rank pods 0..N
    participant J as download Job (staging pod)
    participant A as agents (one per node)
    participant V as model volume
    participant CS as cache set volume

    Note over WH,CS: cold: no model volume, no cache set for this configuration
    WH->>J: create once per model (idempotent): chart download step into an emptyDir
    WH-->>P: admit as readers of the read-only model claim, cachedir emptyDir, cache stamps
    J->>J: download finishes, hold container keeps the emptyDir
    A->>V: size claim from the staged bytes, copy in, label complete, release
    A-->>P: mint read-only model claim in the namespace, pods bind and start
    P->>P: engines compile and tune, reach Ready
    A->>A: each agent: rank Ready and tree settled, annotate rank-ready and bytes
    A->>CS: one agent wins the election: create the set claim sized from all ranks
    A->>A: collector streams the other ranks over the agent API
    A->>CS: copy every rank into its ordinal directory, label complete, release

    Note over WH,CS: warm: model volume and cache set complete
    WH-->>P: admit with two claims: model volume and cache set, both read-only
    P->>P: seed init copies its ordinal directory into the cachedir, engine starts
    Note over P: no download, no compile, no tuning
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
   `HF_HOME`, `NIM_CACHE_PATH`, the init's dest, `/mnt/models`. A main
   container that names `NGC_MODEL_NAME` in its env (an NVCF chart whose
   own script downloads, kimi-k3 on GB300 2026-09-30) is an engine
   downloader landing at `NGC_MODEL_MOUNT`; the artifact name beats the
   local `MODEL_PATH` the engine is started from. If the volume there is
   a shared PVC, hostPath or OCI image, skip the pod. A per-replica claim
   from a StatefulSet volumeClaimTemplate (`<template>-<pod>`, owner
   StatefulSet) is the opposite of shared, one full download per pod, and
   is substituted like an emptyDir. Role flags and wiring env stay out of
   the hash (`stripRoleFlags`).

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

   When the engine downloads an artifact nvsnap has no recipe for (the
   chart's own NGC script in the main container) into a pod volume, there
   is no Job: pre-filling the volume would miss the script's markers and
   symlinks, and the script would try to write into a read-only mount. The
   first pod keeps its writable landing and is labelled
   `nvsnap.io/model-capture`; once it is Ready the agent on its node
   copies the finished volume, markers and all, into the primary
   (mechanism 3, the same path as the staging copy, with atomic claim
   creation choosing one source among concurrent pods). Later pods get the
   read-only copy at the same path and the script's own "already present"
   check passes. Pods of the same first deployment each download once;
   every deployment after that downloads nothing. Block mode with PVC
   readers only. A StatefulSet still creates the template claim for a
   reader even though the pod no longer mounts it; dropping that is the
   chart's to do.

   Why a Job and not the first pod: on NVMesh a volume attached read-write
   by a running pod cannot be attached read-only anywhere else (dev1,
   2026-09-26: `NVMesh Attach Failed` on the read-only PV while the writer
   pod held the primary). The download step has to exit and release the
   volume before readers attach, so it cannot live inside a pod that goes
   on to serve. A Job also decouples the download from the workload's
   scheduling: it runs on any node with the image, and the workload pods
   of a multi-node group or a gang all schedule as plain readers.

   Where the Job writes depends on the mode. Distributed filesystem: into
   the shared RWX claim, created at admission (the filesystem's quota
   makes its size nominal). Block storage: into a pod-local emptyDir on
   the node's disk, the same place the single-GPU cachedir capture
   downloads to. No claim exists yet, because the model's size is unknown
   until it has been downloaded, registries do not all expose it, and
   not every storage system can grow a volume. kubelet removes an emptyDir
   the moment its pod terminates (verified on dev1 2026-09-28), so the
   download is the pod's init container and a small hold container keeps
   the pod Running until the agent has copied the tree out (mechanism 3)
   and deletes the Job; a six-hour active deadline is the safety net. The
   pod carries no service-account token.

3. Model volume per identity, immutable after download (agent + storage
   profile). Distributed filesystem: one RWX volume; writer and readers
   mount it at admission; completion is a marker file the writer's init
   writes on exit 0. Block storage: when the staging pod's download init
   exits 0, the agent on its node measures the bytes on disk, creates a ReadWriteOnce
   claim of measured size plus ten percent, rounded up to a whole GiB
   (profile `modelVolume.minSize` is the floor), in the nvsnap namespace,
   attaches it through a mount-holder, copies the tree in with the
   agent's tree copier, labels the retained PV complete, releases the
   claim so the volume detaches, and deletes the Job so the emptyDir is
   freed. The PV is the artifact; it belongs to no function namespace.
   A copy that fails three times is given up: the Job and claim are
   dropped, a failure record (ConfigMap in the nvsnap namespace, one
   hour) makes the webhook leave new pods on their own download, and the
   readers pending on the read-only claim are deleted so their
   controllers recreate them on that path. A pod Pending on a claim
   cannot run a fallback init, so this is what keeps "never deadlock"
   true in `pvc` reader mode. Later deployments, other
   namespaces and new versions of the function all attach the same
   volume. There is no capture copy of the model anymore.

   Reaper (agent, every `agent.modelVolume.reapInterval`, default ten
   minutes, idempotent on every node), for model and cache volumes alike:
   read-only claims that no pod in their namespace has referenced for ten
   minutes are deleted together with their mount-holders (pvc-protection
   keeps a claim Terminating while a holder mounts it, and owner GC waits
   for the claim); read-only PVs whose claim is gone (Released, or bound
   in a namespace that no longer exists) are deleted as objects only,
   since the storage belongs to the primary; primaries Released without
   the complete label for fifteen minutes are switched to reclaim Delete
   and removed, freeing the capacity of an abandoned copy. A PV that still
   has a VolumeAttachment is left for the next sweep: deleting it wedges
   behind the attacher finalizer (dev1 2026-09-29). Complete primaries
   are kept; retention is a separate decision.

4. Readers reach the volume without help from inside the pod (webhook +
   agent). On a distributed filesystem the RWX claim exists and is bound
   at admission, so the pod schedules. On NVMesh the storage profile picks
   one of two reader modes (`modelVolume.readerMode`):
   - `pvc` (default). The reader references the read-only claim
     `nvsnap-model-<key>-ro` in its own namespace. Complete already: the
     webhook mints the claim at admission and the pod binds at once. Not
     yet: the pod stays Pending on volume binding; when the Job succeeds
     and the primary is detached, any agent mints the claim and kubelet
     starts the pod. No hostPath, so it passes Kyverno's
     `disallow-host-path` in NVCF function namespaces (enforced there).
     A pod Pending on a claim holds a gang scheduler, so this mode is for
     Deployments, StatefulSets and LWS, which is every NVCF chart today.
   - `hostPath`. The reader gets a hostPath landing under the agent's
     model root and schedules at once; the agent on its node attaches the
     read-only volume (mount-holder) and bind-mounts it over the landing,
     and the marker the wait init polls appears. For gang-scheduled
     workloads (Grove, kai-scheduler) where policy allows hostPath.
   In both modes the init waits for the marker and falls back to its own
   download at the deadline. No pod ever needs network access to nvsnap;
   function namespaces block it.

5. Compile caches (webhook env + agent). All caches are redirected to a
   cache location keyed by image digest plus identity plus role-neutral
   args. Distributed filesystem: `<volume>/cache/<key>/`, read-write for
   every pod; the engines' `filelock` and atomic replace make identical
   compiles converge; `cacheMode: shadow` in the profile keeps a per-pod
   writable copy seeded from it where the operator does not trust the
   filesystem's locks (Lustre needs `-o flock`; the agent checks).

   Block storage: each pod compiles into its local `/opt/nvsnap/cache`
   emptyDir; the caches of a tensor-parallel group are gathered once, as
   one volume per configuration, and every later pod of that
   configuration mounts it. The identity of the set is `cache://<config
   hash>`: the role-neutral configuration hash (image reference, model,
   arguments, cache env, driver major, GPU class, salt). Inside it, one
   directory per ordinal (LeaderWorkerSet worker index, else the
   StatefulSet ordinal, else 0): the ranks of a group write rank-specific
   directories, and the paths they share (Inductor and Triton autotune
   results) differ in content between ranks (measured 2026-09-29), so
   ranks are never merged, only placed side by side.

   Why one volume and not one per rank (the first design, ct1
   2026-09-30): per-rank volumes meant eight claims per namespace, eight
   read-only attaches per deployment on top of the model's, and on
   NVMesh a shared read-only attach can take 30 s under concurrent
   clients. On the cold deployment every rank's cache already sits in an
   emptyDir on local disk with no block storage involved, so the right
   moment to touch NVMesh is once, after all ranks exist.

   Cold deployment. The webhook finds no complete set for the key and
   stamps the pod `nvsnap.io/cache-capture`, `nvsnap.io/cache-set=<key>`,
   `nvsnap.io/cache-ordinal=<n>` and `nvsnap.io/cache-group-size=<N>`
   (the group size from the owning LeaderWorkerSet or StatefulSet, 1 for
   a lone pod). No claim, no volume, no Job. When a stamped pod is Ready
   and its tree has stopped changing (readiness is not "compiled": a
   worker reports Ready before its torch.compile finishes), the agent on
   its node measures it and annotates the pod `nvsnap.io/cache-rank-
   ready=true`, `nvsnap.io/cache-rank-bytes=<bytes>`. Nothing is copied;
   the pod keeps serving and its emptyDir keeps the tree.

   Election. An agent that marked a rank ready lists the group's pods by
   key. When every ordinal up to the group size is ready it creates the
   claim `nvsnap-cacheset-<key>` in the nvsnap namespace, sized from the
   sum of the ranks plus ten percent, rounded up to a GiB, floor from
   the profile's `minSize`. Create is atomic: one agent becomes the
   collector, the rest see AlreadyExists and stop. A group that never
   fully schedules never produces a partial set.

   Collection. The collector attaches the claim through a mount-holder,
   copies its own rank from the local emptyDir into `/<ordinal>/`, and
   for every other ordinal streams the tree from the agent hosting that
   pod (`GET /v1/cache-rank/<key>/<ordinal>`, a tar over the
   authenticated agent-to-agent client checkpoints already replicate
   over) into `/<ordinal>/`. Eight ranks of 0.85 GiB take under a minute
   on the node network. It labels the retained PV complete, releases the
   claim so the volume detaches, and clears any failure record. One
   attach, one volume, for the whole group.

   Warm deployment. The webhook finds the set complete, mints
   `nvsnap-cacheset-<key>-ro` in the pod's namespace from the primary as
   it does for the model, and the pod mounts two claims: the model
   volume and the cache set. A seed init on the engine image copies
   `/<ordinal>/` into the writable cachedir and exits 0. Two attaches per
   pod, the model's and the set's. An overlay of the read-only mount
   under the cachedir removes the copy as a follow-up and changes
   nothing above.

   Failures. A rank pod dies before collection: the group never reaches
   all-ready, no claim is created, and the next deployment collects
   again at the cost of one more compile round for that configuration.
   The collector fails mid-copy: it deletes the claim, the primary is
   reaped as abandoned, and another agent may win the next election;
   after three failures a failure record stops admissions from waiting
   on the key for an hour. A peer agent unreachable during collection:
   bounded retries for that rank, then the same give-up; a set is never
   marked complete with a rank missing. A seed copy that fails on a
   warm pod: the init exits 0 and the engine compiles locally.

   Retention. Every admission against a complete set touches
   `nvsnap.io/last-used` on its PV; the reaper retires a set unused for
   `agent.modelVolume.retention` together with its read-only views.

   The identity covers the image reference, model, arguments, cache
   env, driver major and GPU class, so a new engine image starts a new
   cache. It cannot see a change inside an unchanged image (a wheel
   installed at start from a mounted volume, a floating tag re-pointed):
   the pod then seeds the old cache, which the engines' own versioned
   layouts (FlashInfer per version directory, vLLM config hash, Triton
   and Inductor source hashes) ignore rather than misuse. For that case
   a chart sets `nvsnap.io/cache-salt` on the pod template and bumps it
   with the change; the salt is folded into the identity.

   Measured on dev1 (Qwen2.5-0.5B, TP=2 across two pods, H100): cold
   torch.compile 23 s per pod; seeded from the cache volume 2.9 s; Ready
   +131 s instead of +183 s. The 31 MB cache copies in a few seconds.
   CUDA graph capture (7 s) is not cacheable.

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
| M, NVMesh, first | 1; readers bind-mounted on completion; group forms | each rank compiles once; one collection into one cache-set volume after all ranks are Ready | no |
| any, NVMesh, later | 0 (ro claim at admission) | 0 (cache set ro, per-rank subdirectory seeded) | no |
| any, other namespace, later | 0 (`EnsureClaim`) | 0 | no |
| engine-script download (kimi-k3), NVMesh, first | each pod of the first deployment downloads; one is captured after Ready | as M, NVMesh, first | no |
| engine-script download, NVMesh, later | 0 (ro claim replaces the per-replica claim) | 0 | no |
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
| gang scheduler | readers always schedulable in RWX or hostPath mode; in `pvc` mode readers pend on binding until the download completes, so gang-scheduled charts use `hostPath` | profile `readerMode: hostPath` |
| function namespace with Kyverno enforced (`disallow-host-path`, requests and limits, no SA token) | `pvc` mode uses no hostPath; the download Job carries every mount the chart's init had (registry key secret, script ConfigMap), default requests and limits, seccomp, dropped capabilities and no token | none needed |
| identity deleted while a read-only PV is still Terminating (NVMesh) | the read-only PV name is deterministic per identity and namespace, so a re-download of the same identity cannot mint until the old PV finalizes; the attacher's detach timed out for minutes after the volume was gone | retention deletes read-only claims and PVs before the primary, and the controller retries minting; a stale VolumeAttachment on a deleted volume needs the finalizer cleared (seen on dev1 2026-09-26) |
| shared read-only attach is slow (NVMesh: 30 s IO-enable timeouts under 8 to 16 concurrent clients, ct1 2026-09-30) | every worker attached two shared volumes, its model and its rank cache, and the group attached eight distinct cache volumes | one cache-set volume per configuration (mechanism 5): the group's ranks are collected once into it, a warm pod attaches the model and the set, two attaches, and the cluster holds one cache volume per configuration instead of eight. |
| complete volume nobody uses any more | storage held forever | retention: every admission against a complete primary stamps `nvsnap.io/last-used`; the reaper retires a complete model or cache primary, and its read-only views, once nothing has used it for `agent.modelVolume.retention` (default 7 days) and no reader is bound in a live namespace; 0 keeps forever |
| last reader of an identity leaves a node (NVMesh) | the agent's bind mount keeps the volume published; kubelet cannot unmount and the attacher's detach times out (seen on dev1 2026-09-26 during cleanup) | the agent must unbind and drop its mount-holder when no pod on the node uses the identity; part of retention (follow-up) |

## What changes in the code

Stays: classifier (extended per mechanism 1), role-neutral hash,
`EnsureClaim`, storage profiles, cache env
injection and seed init, mount-holder and bind injection (L1), the server
reconciler, `vllm-workers` chart and runner.

New: identity from init containers and group inheritance; the download
Job derived from the chart's init or from `hf download`; init wrapping
for the wait; per-identity model volume (RWX on DFS created at admission;
on block storage staged in the Job's emptyDir and copied by the agent
into a claim sized from the download, in the nvsnap namespace); agent
completion handler (Job success -> marker / sized PV + read-only claims);
model volume reaper; cache volume capture (caches only) on NVMesh;
`cacheMode`; last-use labels.

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

## Results, dev1 2026-09-26 (NVMesh, block mode)

Stock `vllm-workers` chart, Qwen2.5-32B-Instruct TP=4, replicas=2 on two
nodes, no nvsnap markers in the chart, agent v0.2.76-mv5.

```
first deploy (nothing on the cluster)
  t+0       both pods admitted as readers (hostPath landing, wait init); one Job created
  t+354s    Job succeeded: 65 GB via `hf download` into the RWO claim; claim released
  t+400s    both readers un-pended (read-only PV minted per namespace, attached via
            mount-holder, bound under /var/lib/containerd/nvsnap-models/<key>)
  t+591s    both Ready; 0 downloads in either pod; serve " Paris. Correct!"
uninstall + reinstall
  t+12s     both un-pended (identity complete: no Job)
  t+136s    both Ready
earlier run, identity already complete on the cluster
  t+205s    both Ready, wait init 10-15 s, engine reads the volume directly
```

PVC reader mode (the default since the function-namespace fixes), same
chart, Qwen2.5-14B-Instruct TP=4, replicas=2, agent v0.2.76-mv6:

```
first deploy (fresh identity)
  t+0       both pods admitted as readers referencing nvsnap-model-<key>-ro; Pending on
            "persistentvolumeclaim not found"; one Job created (2 CPU / 4 Gi requests,
            no SA token, seccomp, dropped capabilities)
  t+135s    Job succeeded: 28 GB via `hf download`
  t+195s    read-only PV and claim minted in the pod namespace after the primary
            detached; both readers un-pended and scheduled; init found the marker
  t+344s    both Ready; 0 downloads; /root/.cache/huggingface is the NVMesh volume
            mounted ro,norecovery,nouuid; serve " Paris. The capital"
uninstall + reinstall
  t+118s    both Ready (claim minted at admission, no pending phase)
```

Real NVCF Helm function, 2026-09-27 (dev1 through the staging control
plane, QA org; stock `kimi-k3` 0.2.3 chart copied from the prod org with
values only: init-container NGC download into an emptyDir, two
StatefulSet replicas, Qwen2.5-0.5B-Instruct hosted on prod NGC, agent
v0.2.76-mv8, PVC reader mode). No chart change and no nvsnap marker.

```
first deploy (fresh identity, function namespace sr-848a0bc4-...)
  t+0       both mini-service pods admitted as readers referencing the ro claim; one Job;
            NVCA's own webhook mutated the Job pod too, so /var/secrets/secrets.json with
            the NGC key was present and the chart's download script ran unchanged
  t+150s    Job succeeded (NGC CLI download); claim released
  t+152s    ro claim minted in the function namespace; both readers scheduled; all three
            inits (two NVCA cert inits, the wrapped download-ngc-model) exited 0
  engine crashed on my override (TP=4 does not divide the model's 14 heads); NVCF
  marked the function ERROR and deleted the namespace; the primary volume stayed
redeploy (fixed override; identity already complete on the cluster)
  t+51s     namespace sr-4d3e0de5-... created; both readers admitted complete=true, ro claim
            minted at admission (no Job)
  t+153s    both Ready; init logs "model complete, skipping download"; 0 NGC downloads;
            /config/models is the NVMesh volume (ro,norecovery,nouuid); chat completion
            answered; function ACTIVE
```

Kyverno on dev1 is Audit; the audit failures on the function pods belong to
the chart (IPC_LOCK add, no capability drop, root) and to NVCA's cert inits,
not to the containers nvsnap creates. The namespace deletion left the first
run's read-only PV Released: retention must also cover PVs whose claim
namespace is gone.

No hostPath and no agent bind in this mode. Kyverno on dev1 is Audit, so
the remaining warnings were the stock chart's own containers plus, until
v0.2.76-mv8, the injected init (no resources, capabilities not dropped,
no runAsNonRoot); the enforced rejection itself is not testable on dev1.

Cold start of the same pod on the same node: 325 s. The reinstall number
is the engine's own load and compile from a read-only NVMesh mount with no
prewarm; compile caches on block storage are still the follow-up (step 4).

Findings that changed the design during these runs:

- NVMesh refuses a read-only attach on any node while the volume is
  attached read-write anywhere, including a Succeeded Job pod that still
  exists. Hence the download Job, its TTL, releasing the claim on
  completion, and the detach check before minting.
- The overlays root is swept by the L1 overlay GC; binds live under their
  own Bidirectional hostPath.
- Memory is not a mount table: binds are verified against the mounted
  device and the volume handle and redone when missing or stale.
- The agent's binds pin the volume on the node; unbinding when the last
  reader leaves is part of retention (open).
