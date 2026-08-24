# NVCA × NvSnap — checkpoint/restore integration design

**Status:** Proposed, 2026-05-12
**Branch:** `feat/nvsnap-integration`
**NvSnap project:** `github.com/balaji-g/nvsnap` (currently @ `main` 1cc2410)

## What this delivers

Today: NVCA creates an inference pod for a function-version. Cold start
takes 1–13 min depending on the framework (HF model download, engine
compile, NCCL handshake). Every pod for the same function-version pays
the same cost. Scale-out of N replicas = N cold starts.

After this integration:

1. NVCA creates the first pod cold. Once it's warm (inference ready,
   passed N seconds), NVCA calls nvsnap to **checkpoint** the `inference`
   container's running state into a node-local cache + the cluster's
   nvsnap-blobstore.
2. NVCA stores the resulting **checkpoint hash** on the function-version
   metadata.
3. For every subsequent pod creation for that function-version, NVCA
   stamps the pod with `nvsnap.io/restore-from: <hash>`. NvSnap's
   admission webhook injects bind-mounts of the warmed cache onto the
   inference container. Pod reaches Ready in tens of seconds instead
   of minutes.

Measured speedups on nvsnap's own test cluster (see `docs/ARCHITECTURE.md`
in the nvsnap repo for the bench table):

- vLLM Llama-3.1-8B: **2.1×** (cold 2m → restore 56s)
- NIM Llama-3.1-8B: **1.5×** (cold 1m40s → restore 1m05s)
- NIM Qwen3-32B TP=2: **6×** (cold 3m16s → restore 33s) — flagship
- vLLM Llama-3.1-70B TP=4: **~4×** (cold ~13m → restore 3m29s)

## Non-goals

- NvSnap's internals — agent, CRIU patches, sitecustomize injection.
  This integration treats nvsnap as a service.
- Cross-cluster blob replication mechanics — covered by the
  nvsnap-blobstore design (S3/GCS-backed content-addressed store).
  This doc assumes any cluster's nvsnap-agent can resolve a hash to
  bytes via the blobstore.
- Replacing utils sidecar / init containers. NvSnap only checkpoints the
  `inference` container.

## Architecture

```
                       ┌───────────────────────────────────────────┐
                       │  Function-version object (NGC catalog)    │
                       │    + nvsnap_checkpoint_hash (new field)     │
                       └──────────────┬────────────────────────────┘
                                      │
                                      │ NVCA reads/writes per version
                                      ▼
        ┌─────────────────────────────────────────────────────────┐
        │  NVCA agent                                              │
        │  pkg/nvca/k8scomputebackend.go : CreatePodArtifactInstances
        │    1. for function-version V → fetch hash from NGC       │
        │    2. if hash exists & feature flag on → annotate pod    │
        │         metadata.annotations.nvsnap.io/restore-from = hash │
        │    3. apply pod                                          │
        │    4. when pod is Ready + warm window elapses → call     │
        │         nvsnap.Checkpoint(pod, container="inference")      │
        │    5. on success, write hash back to NGC metadata        │
        └─────────────────────────────────────────────────────────┘
                                      │
                                      │ HTTP
                                      ▼
        ┌─────────────────────────────────────────────────────────┐
        │  nvsnap-server (nvsnap-system namespace)                     │
        │  POST /checkpoint/pod                                    │
        │    { pod, namespace, container } → { hash, size, ... }   │
        │  GET  /api/v1/checkpoints/<id>  status / metadata        │
        │  ←   admission webhook handles nvsnap.io/restore-from      │
        └─────────────────────────────────────────────────────────┘
```

## Integration point inside NVCA

`pkg/nvca/k8scomputebackend.go` is the only file that needs deep
wiring. `CreatePodArtifactInstances` (line 956) is where workload
pods are constructed and applied. Two hooks:

### Hook A — restore-on-create (pre-apply)

Just before `podClient.Create(ctx, pod, ...)`, look up the nvsnap hash
for this function-version. If found and the feature flag is on, add
the annotation:

```go
pod.Annotations["nvsnap.io/restore-from"] = checkpointHash
```

That's the entirety of the restore path NVCA needs. NvSnap's
`MutatingWebhookConfiguration nvsnap-rootfs-restore` runs at admission,
reads the annotation, resolves the manifest from
`nvsnap-capture-<shortHash>` ConfigMap, injects nodeAffinity +
hostPath volume mounts on the inference container. Customer image
+ args unchanged.

### Hook B — checkpoint-after-warm (post-health-OK)

A separate reconcile loop watches for newly-Ready pods bearing the
`nvsnap.io/checkpoint-on-warm: "true"` annotation that NVCA stamped at
creation. Once `PodReady` fires, the loop polls the inference
container's health endpoint directly. When `/health` returns HTTP
200, the engine is provably warm (model loaded, lazy compile done,
ready to serve real requests). Then the loop:

1. POSTs to `nvsnap-server.nvsnap-system.svc.cluster.local:8080/api/v1/checkpoint/pod`
   with `{namespace, pod, container: "inference"}`.
2. Polls status until terminal (success / fail).
3. On success, writes the hash to the function-version object via the
   NGC API.
4. Removes the `checkpoint-on-warm` annotation so the loop is idempotent.

Health-check details:
- NVCA reads the health endpoint URL from the function-version's
  inference spec (already there for traffic routing). Default
  contract: `GET <host>:<port>/v1/health/ready` returns 200 when the
  engine is ready to serve.
- Poll interval: 5s. Total budget: `NvSnapWarmupTimeoutSeconds` (default
  900, 15 min) — TRT-LLM 70B engine compile can take 10+ min.
- After the first 200, wait `NvSnapWarmupBufferSeconds` (default 10s)
  to let any post-first-response setup land (CUDA graph capture, JIT
  warmup), then trigger checkpoint.
- If the health endpoint never returns 200 within the budget, log +
  metric, give up on this pod (no checkpoint, no retry — operator
  investigates).

The same reconcile loop can re-checkpoint if the function-version's
`nvsnap_refresh_interval` has elapsed (handles model updates without
operator intervention).

Failure modes (fail-open):

- nvsnap-server unreachable → log + retry with exponential backoff;
  pod stays running, just no future restore speedup.
- Checkpoint fails (CRIU error, GPU state issue) → log + report metric;
  function-version's hash isn't updated; subsequent pods cold-start.

## NvSnap APIs NVCA consumes

```
POST /api/v1/checkpoint/pod
  body: {"namespace": "nvcf-backend", "pod": "0-sr-...", "container": "inference"}
  → 202 Accepted {"checkpoint_id": "...", "status_url": "/api/v1/checkpoints/..."}

GET  /api/v1/checkpoints/<id>
  → { "id": "...", "hash": "<sha256>", "size_bytes": N, "status": "succeeded|failed", ... }

DELETE /api/v1/checkpoints/<id>
  → 204 (cleanup when a function-version is removed)
```

The `nvsnap.io/restore-from: <hash>` annotation is the only restore-side
contract — no new API. Webhook does the rest.

## What NVCA needs from nvsnap that isn't yet there

1. **Container selector on `POST /checkpoint/pod`.** Today the nvsnap
   handler in `internal/server/demo.go` keys off pod-name only. Need
   to take `container` and pass it through to the agent's process
   discovery. **Tracked: nvsnap issue (new).**
2. **Multi-cluster registration of nvsnap-server URL.** NVCA's agent
   needs to know where nvsnap lives. Today it's
   `nvsnap-server.nvsnap-system.svc.cluster.local:8080` in-cluster — fine
   as a default, configurable via NVCA cluster config key
   `NvSnapServerURL` (override for off-cluster deployments).
3. **Idempotent checkpoint calls.** If NVCA retries a checkpoint
   request that succeeded but the response was lost, nvsnap should
   return the existing checkpoint, not start a new one. NvSnap computes
   the manifest hash from the pod spec deterministically (see
   `internal/rootfsonly/composer.go`), so this is mostly already true
   for the rootfs path — needs verification.

## Configuration surface (new)

### Cluster-level config keys (set via NGC during cluster registration)

| Key | Default | Meaning |
|---|---|---|
| `NvSnapIntegrationEnabled` | `false` | Master switch for the entire integration. Pod creation behaves identically to today when off. |
| `NvSnapServerURL` | `http://nvsnap-server.nvsnap-system.svc.cluster.local:8080` | Where nvsnap-server lives. |
| `NvSnapWarmupTimeoutSeconds` | `900` (15 min) | Max time to wait for `/health` 200 after `PodReady` before giving up. |
| `NvSnapWarmupBufferSeconds` | `10` | After first `/health` 200, wait this long before triggering checkpoint (lets post-response setup land). |
| `NvSnapRestoreEnabled` | `false` | Independent toggle for the restore side. Lets us roll out checkpoint-only first. |

### Per-function-version (NGC API)

| Field | Default | Meaning |
|---|---|---|
| `nvsnap_checkpoint_hash` | empty | Set by NVCA after a successful checkpoint. Used as the value of `nvsnap.io/restore-from`. |
| `nvsnap_opt_out` | `false` | Function-version opt-out (e.g., for workloads with state that can't be safely restored). |

## Feature flag

`pkg/featureflag/featureflag.go` gets one new flag:

```go
NvSnapCheckpointRestore = newFeatureFlag("NvSnapCheckpointRestore", newBool(false))
```

Wraps both `NvSnapIntegrationEnabled` and `NvSnapRestoreEnabled` for
emergency cluster-wide disable. Per-cluster + per-function-version
overrides via the config keys above.

## Rollout plan

1. **Stage 1 — checkpoint-only.** Flag ON, restore OFF. Every pod
   gets checkpointed after warmup; no pod uses a restore. NVCA verifies
   the checkpoint flow works end-to-end and gathers timing data on
   nvsnap's checkpoint duration without affecting customer pod boot time.
2. **Stage 2 — restore for new functions.** Restore ON for
   function-versions deployed after a cut-over date. Existing
   function-versions stay cold-start until they're re-deployed.
3. **Stage 3 — backfill.** A maintenance job walks existing
   function-versions, kicks one cold-start pod each, checkpoints
   after warmup, then enables restore for that version.

Each stage rolls per-cluster via the config keys.

## Pieces of work

Filed as nvca tasks (this design doc + the items below):

- A. NvSnap HTTP client package `pkg/nvca/nvsnap/`. Thin Go bindings for
  the four endpoints. Mocked in unit tests.
- B. Feature flag definition. One-line addition.
- C. Cluster config keys (read at NVCA startup, similar pattern to
  existing `AgentNodeSelectorLabelKey` etc.).
- D. Restore-on-create hook in `CreatePodArtifactInstances`. ~30 LOC.
- E. Checkpoint-after-warm reconcile loop. New file
  `pkg/nvca/nvsnap/checkpoint_reconciler.go`. ~200 LOC.
- F. Function-version metadata field `nvsnap_checkpoint_hash`. NGC API
  schema update + NVCA read/write paths.
- G. Cluster-wide disable: respect `NvSnapCheckpointRestore` feature flag
  in both A and D.
- H. Metrics — `nvca_nvsnap_checkpoint_duration_seconds`,
  `nvca_nvsnap_checkpoint_total`, `nvca_nvsnap_restore_attempted_total` —
  for ops visibility.
- I. E2E test — apply a function-version, wait for warmup, verify
  checkpoint hash recorded, scale up to N replicas, verify N-1 of
  them came up via restore (timing assertion).

NvSnap-side dependencies (tracked in the nvsnap repo):

- Container selector on `/api/v1/checkpoint/pod` (nvsnap issue TBD).
- Idempotent checkpoint replay verified.

## Open questions

1. **Where exactly does the hash live?** **Decided 2026-05-12: split
   across two stores.**

   - **NGC function-version object** holds `nvsnap_checkpoint_hash`
     (global, authoritative). One cluster checkpoints a
     function-version; NGC records the hash; *every* cluster can then
     stamp the `nvsnap.io/restore-from: <hash>` annotation on its pods
     without re-checkpointing. This is what makes checkpoints scale
     across clusters — the hash is content-addressed, the blobs live
     in the nvsnap-blobstore (S3/GCS), and any cluster's nvsnap-agent
     pulls bytes via the standard tier-3 cascade.
   - **Per-cluster CRD `NvSnapFunctionState`** (cluster-scoped) tracks
     local state that NGC shouldn't carry: which cluster *first*
     captured this hash, local cache freshness, per-cluster opt-outs,
     last-attempted timestamps, retry backoff, error history. This is
     the working state of the checkpoint-after-warm reconciler.

   ```yaml
   apiVersion: nvsnap.nvidia.com/v1alpha1
   kind: NvSnapFunctionState
   metadata:
     name: <functionVersionID>          # 1:1 with NGC function-version
   spec:
     functionVersionID: <uuid>
     optOut: false                       # nvsnap_opt_out
   status:
     # Mirror of NGC for fast local reads (canonical source = NGC):
     checkpointHash: <sha256>
     # Per-cluster operational state:
     capturedHere: false                 # did THIS cluster do the capture
     capturedAt: <RFC3339>
     localCacheState: warm|cold|fetching|failed
     lastAttemptAt: <RFC3339>
     lastError: ""
     attemptCount: 0
   ```

   Reasons this split wins for production:
   - **Cross-cluster scaling**: hash is global, so cluster #2..N skip
     the cold-start checkpoint entirely. Capturing once in any
     cluster benefits all of them.
   - **Per-cluster operational state stays per-cluster**: a fetch
     failure on AWS-us-east-2 doesn't pollute NGC; an opt-out on
     a single cluster doesn't need NGC permissions.
   - **CRD gives typed schema, RBAC, watch semantics, status
     subresource, audit** — required for production-grade operator.
   - **Authoritative read path**: NVCA pre-apply hook reads NGC (one
     network hop, cached); falls back to local CRD if NGC is
     transiently unreachable.

2. **Does NVCA call nvsnap, or does nvsnap watch NVCA?** **Decided
   2026-05-12: NVCA calls nvsnap (push).** NvSnap stays NVCA-agnostic; any
   compute backend or scheduler that wants C/R can drive nvsnap's HTTP
   API the same way. NVCA owns the lifecycle decisions; nvsnap executes
   the C/R primitives.

3. **Per-pod-template hash, or per-pod hash?** NvSnap's
   `internal/rootfsonly/composer.go` already canonicalizes the pod
   spec (TP size, image, CUDA driver major, etc.) into a deterministic
   hash. NVCA can use that hash as the function-version key
   automatically — if two function-versions happen to produce the same
   canonical pod spec, they SHARE a checkpoint, which is exactly the
   semantics we want.

4. **What's the warm-window heuristic?** **Decided 2026-05-12: poll
   the inference container's health endpoint directly.** K8s
   `PodReady` is too lenient (readiness probes often just check that
   the HTTP server answers, not that the engine is loaded). All
   supported inference frameworks expose a `/health` (or
   `/v1/health/ready`) endpoint that returns 200 only when the model
   is fully loaded and ready to serve. NVCA polls this endpoint at 5s
   intervals starting from PodReady, waits `NvSnapWarmupBufferSeconds`
   (default 10s) after the first 200 to absorb post-first-response
   setup, then triggers checkpoint. Total polling budget is
   `NvSnapWarmupTimeoutSeconds` (default 15 min) — long enough for
   TRT-LLM 70B engine compile.

   Why this beats a fixed timeout:
   - Workload-aware: vLLM warm in 30s, TRT-LLM 70B in ~10min — both
     handled by the same logic.
   - Deterministic: checkpoint fires when the engine *says* it's
     ready, not when a clock says it should be.
   - Zero per-workload tuning: no `nvsnap_warmup_seconds` per
     function-version to maintain.

   Health endpoint URL comes from the function-version's inference
   spec (already there for traffic routing).

---

*Next:* skeleton `pkg/nvca/nvsnap/client.go` + the feature flag entry,
without wiring into `CreatePodArtifactInstances` yet. Get the client +
flag merged as a no-op baseline, then layer hooks on top.
