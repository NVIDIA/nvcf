# Helm functions: one download per model, on the customer's filesystem

Goal: a Helm chart with N GPU workers, on any model registry, downloads the
model once per cluster and reuses compile caches, without a template change
and without holding any pod back from scheduling.

Status: design, supersedes the gate-and-promote follower path of
`helm-chart-cache-election.md` for clusters with a distributed filesystem.
Issue #2099. Decisions taken 2026-09-25 with the product owner are marked
"decided".

```mermaid
sequenceDiagram
    participant C as chart (LWS / StatefulSet / Deployment)
    participant WH as webhook (agent)
    participant L as Lease nvsnap-model-<id>
    participant V as RWX volume nvsnap-model-<id>
    participant A as agent (node)
    C->>WH: create pod-0
    WH->>WH: identity = hf://org/repo ; landing volume = emptyDir at HF_HOME
    WH->>L: create (holder = election id)
    WH-->>C: writer: emptyDir -> claim on V, cache env -> V/cache/<image>
    C->>WH: create pod-1..N-1 (same second, other nodes, other namespaces)
    WH->>L: create -> AlreadyExists
    WH-->>C: reader: same claim, init nvsnap-wait-model (marker or deadline)
    Note over C: every pod schedules now; nothing is gated
    C->>V: writer downloads straight into V
    A->>V: writer Ready -> agent writes V/.nvsnap-complete
    C->>V: readers' init sees marker, engine starts from V
```

## Four questions, one answer each

Every deployment pattern in the field (plain Deployment, LWS or StatefulSet
multi-node, init-container download from NGC, HF or S3, KServe
`storageUri`, NIM, Dynamo and llm-d disaggregation, Ray Serve) reduces to
four questions. Scheduling is not one of them: no pod is ever held, because
multi-node groups and gang schedulers break if one member is.

### 1. Identity

Derived at admission from the pod alone, normalized to a URI:

| Source | Example | URI |
|---|---|---|
| engine args `--model`, `--model-path`, `--model=`, positional `vllm serve <x>` | `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` | `hf://Qwen/...` (`@rev` when `--revision`) |
| engine env `HF_MODEL_ID`, `MODEL_ID`, `MODEL_PATH` | `/config/models/nemotron3-ultra-genrm` | resolved through the init that fills that path |
| init container env or args | `NGC_MODEL_NAME=org/team/model:ver`, `huggingface-cli download <repo>`, `aws s3 sync s3://b/p` | `ngc://org/team/model:ver`, `hf://repo`, `s3://b/p` |
| KServe `storageUri` annotation | `hf://`, `s3://`, `pvc://` | as given (`pvc://` means skip) |
| NIM image | `nvcr.io/nim/...:tag` + `NIM_MODEL_PROFILE` | `nim://image@profile` |
| group member with no identity (LWS `--headless` worker) | | inherited from the group's leader template via owner references |

Role flags and wiring env stay out of the identity (`stripRoleFlags`, done).
Compile caches key on image digest plus identity plus the role-neutral args.

### 2. Landing volume

The volume mounted at the path the download writes: `HF_HOME` (default
`/root/.cache/huggingface`), `NIM_CACHE_PATH`, the init's `--dest` /
`NGC_MODEL_MOUNT`, KServe's `/mnt/models`. Backed by an emptyDir in every
stock chart. If it is already a PVC, hostPath or an OCI model image, the
customer solved this themselves: skip.

### 3. Sharing strategy (decided)

Chosen by the storage profile of the cluster.

- shared-fs: the customer has a distributed filesystem (Weka, VAST, Lustre,
  FSS, EFS, Filestore). One RWX volume per identity per cluster,
  `nvsnap-model-<id>`, created by the webhook on first sight; a claim per
  namespace through `EnsureClaim`. The writer downloads straight into it;
  readers mount the same claim. No copy, no promote, no gate. This is the
  product path.
- snapshot: NVMesh only. The existing capture-after-Ready, promote to ROX,
  restore path, unchanged. A multi-node instance downloads N times on its
  first deploy and restores on every later one.
- anything else without a distributed filesystem: nvsnap does nothing for
  Helm functions.

### 4. Completion signal

- init-container download: the webhook wraps the init. Writer:
  `<original> && touch <vol>/.nvsnap-complete`. Readers:
  `nvsnap-wait-model` waits for the marker, then skips the download. Charts
  that already carry marker logic (the NVCF NGC pattern) see no change in
  behaviour.
- engine-internal download: the agent writes the marker when the writer pod
  turns Ready, through the pod-volume path it already resolves for capture.
  Readers carry the same wait init before the engine. No pod-to-server
  network is needed; function namespaces block it.
- Writer dies before the marker (decided: always fall back): the Lease
  expires at the deadline, readers stop waiting and download into the same
  volume themselves. HF and NGC downloads are per-file atomic, so
  concurrent writers converge; the next deployment finds a complete volume.

## Compile caches (decided: shared, profile can switch to shadow)

All caches (`VLLM_CACHE_ROOT`, `TORCHINDUCTOR_CACHE_DIR`, `TRITON_CACHE_DIR`,
FlashInfer, DeepGEMM, `CUDA_CACHE_PATH`, `HOME`) point at
`<vol>/cache/<image-digest>/`, read-write for every pod. Entries are
content-addressed and written atomically with `flock`, which is how shared
`HF_HOME` runs in the field, and the mtime-sensitive ninja caches are served
better by a shared filesystem than by a copy. Measured on 70B: 208 MB of
caches against 131 GB of model; compile 65 s cold, 3 s from cache.

`cacheMode: shared | shadow` in the storage profile. `shadow` keeps today's
per-pod writable copy seeded from the writer's cache; default for SMB
(CIFS lock semantics) and any filesystem the operator does not trust. On
Lustre `flock` needs the `-o flock` client mount option; the agent checks the
L2 volume's mount options at startup and logs when `shared` is configured
without it.

## Namespaces and lifecycle

One volume per identity per cluster; a claim per namespace, minted on
demand by `EnsureClaim` (done for static PVs and snapshots; shared-fs adds
the RWX filesystem case, which is a second claim on the same volume).
Volumes carry identity, image and last-use labels; retention is a later
change and is not needed for correctness.

## What stays, what goes

Stays: classifier (extended per the identity table), role-neutral hash,
Lease election (elects the writer), `EnsureClaim`, storage profiles, cache
env injection, the server reconciler, `vllm-workers` chart and runner.

Goes for shared-fs: `schedulingGates`, promote-to-ROX, `nvsnap-l2-wait`,
the capture copy, `restore-from`.

## Build order

1. Identity and landing-volume detection for every source in the table,
   including group inheritance for headless workers. Unit tests from real
   specs (prd11 function, Dynamo sample, LWS example).
2. shared-fs substitution: claim creation from the profile's RWX class,
   emptyDir replacement, cache env, per-namespace claim.
3. Completion: init wrapping, `nvsnap-wait-model`, agent marker on Ready,
   deadline fallback.
4. `cacheMode` in the profile; Lustre mount-option check.
5. Retire the gate on shared-fs profiles.
6. dev1 reproduction of the prd11 shape (StatefulSet, init download, two
   pods per instance) on the SMB class: today, shared-fs, redeploy.
   Then the same chart in a second namespace.
