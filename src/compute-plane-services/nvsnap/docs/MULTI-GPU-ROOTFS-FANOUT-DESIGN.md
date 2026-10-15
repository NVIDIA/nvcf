# Multi-GPU Rootfs-Only Restore — Design

**Status**: Phase 1 production-grade end-to-end on the GKE test cluster
(2026-05-04). vllm-8b TP=2: capture (16 GB / 6398 files) + restore via
mutating webhook with zero customer yaml change → FRESH ready in 79s vs
cold-start ~5 min, model loaded from cache (no HF download), inference
verified.
> **Status:** Phase 1 (rootfs-only "warm cold-start") shipped. This is the
> original Phase-1 design; the current end-state design is in
> [ROOTFS-EVERYWHERE](design/ROOTFS-EVERYWHERE.md).
**Related**: NCCL-MULTI-GPU-CHECKLIST.md, CUDA-INTERPOSITION-DESIGN.md, MEMORY/multi_gpu_libcudart_wall.md.

## 1. Problem

Single-GPU CRIU + cuda-checkpoint already works (5/5 single-GPU workloads
PASS). Multi-GPU CRIU is blocked on the **libcudart wall**: cuda-checkpoint
hangs on `cuMemUnregisterGpu` for TP>1 because libcudart's per-process
state can't be reset from userspace post-CRIU. Fix is upstream NVIDIA.

We can't gate the platform on an upstream NVIDIA bug, and the cluster
serves generic workloads — pre-warmed engine pools aren't viable. We
need a multi-GPU restore path that:

- Works without GPU-state preservation.
- Saves real time vs cold-start, *especially* under fan-out (N pods
  scheduled simultaneously across N cache-cold GPU nodes).
- Is engine-agnostic (vLLM, SGLang, TRT-LLM, NIM, future engines).
- Is transparent to customer pod yaml (one annotation, no surgery).

## 2. Approach

Three phases, layered. Each is independently shippable.

| Phase | Mechanism | Preserves | Saves | Implementation cost |
|---|---|---|---|---|
| 1 | Rootfs-only "warm cold-start" | On-disk caches: model weights, compiled engines, JIT kernel caches, tokenizer/config, NIM workspace | Per-pod download (tens of GB from HF/NGC) and engine compile (TRT-LLM C++ profiles only) | Medium |
| 2 | CRIU CPU-dump + rootfs | All of Phase 1 + Python interpreter state + imported modules + tokenizer in-memory tables + JIT-compiled CPU code + open FDs/sockets | Python import chain (~10-30s) + module init + scheduler setup | High (engine cooperation needed) |
| 3 | CRIU + cuda-checkpoint + rootfs | All of Phase 2 + GPU VRAM (weights, KV cache, CUDA graphs) + NCCL communicators | Weight H2D upload + engine bind + NCCL handshake + KV cache alloc | Blocked on NVIDIA upstream |

This document focuses on **Phase 1** (production-ready). Phase 2/3 are
sketched at the end.

## 2.1 Project-wide convention: swappable backends

Every cross-cutting infrastructure dependency in nvsnap (storage, catalog
database, metrics sink, audit log, future components) is an **interface
in `internal/` Go code** with at least one concrete implementation, and
a wiring/factory layer that picks the impl from cluster config.
Non-negotiable because:

- Customers run on different clouds (GKE, EKS, AKS, on-prem) with
  different primitives available.
- Any GCP API can be disabled in a customer's project (we hit this with
  Filestore in the test cluster).
- Compliance / cost / scale concerns drive different choices for the
  same logical role (SQLite vs Postgres for catalog DB; GPD-ROX vs
  Filestore vs GCS for cache).

For storage specifically, see §3.1.5. The catalog DB refactor is tracked
in a separate issue; concrete API:

```go
// Storage abstraction
type Backend interface {
    Store    // capture/restore (agent's POV)
    Mounter  // pod-volume rendering (webhook's POV)
}

// One-line backend selection at startup:
backend := storage.New(cfg)  // returns gpdrox.Backend, local.Backend, ...
```

## 3. Phase 1 architecture

### 3.1 Capture

Triggered when a workload is "warm" (engine ready + first inference
served). The agent on the source node:

1. Resolves the workload's main process via cgroup → namespace → root
   process. (Generic; replaces the regex hack in the spike.)
2. Reads `/proc/<pid>/mountinfo` to find the overlay upperdir + bind-mount
   excludes.
3. Captures **two artifact types**:
   - **Rootfs upperdir** — files the engine wrote at root level (Triton
     cache, torch.compile cache, compiled engines for TRT-LLM C++
     profiles, tokenizer pre-built tables). Excluded: every non-`/`
     mountpoint from mountinfo, plus `/dev/shm`, `/run`, `/var/cache`,
     `/usr/lib/firmware`, `/proc`, `/sys`.
   - **Named user-data volumes** — hostPath and emptyDir (medium != Memory)
     volumes whose container mountPath isn't in the nvsnap-tooling skip set
     (`/checkpoints`, `/nvsnap-lib`, `/nvsnap-system`, `/nvsnap`) and doesn't
     start with `/dev/`, `/sys`, `/proc`, `/run/`, `/etc/`. emptyDir host
     path resolves via `/var/lib/kubelet/pods/<uid>/volumes/kubernetes.io~empty-dir/<volname>`.
4. **No tar**. Captures use `rsync`-style file-level copy to a
   content-addressed object key in shared storage. (See §3.4 for the
   rationale.)

For NIM (image USER 1000): rootfs upperdir is **skipped**. All NIM
warm-start state lives in `/opt/nim/.cache` which is captured as a
named volume.

### 3.2 Content addressing

Each capture is identified by a stable hash over:

```
sha256(
  image_digest,                      # exact container content
  model_id,                          # e.g. meta-llama/Llama-3.1-70B-Instruct
  engine_compat_flags,               # ordered tuple — see below
  cuda_driver_major_version,         # 580 vs 555 etc
  capture_format_version             # increment when capture schema changes
)
```

`engine_compat_flags` is per-engine — flags that affect what's in the
cache:
- vLLM: `--tensor-parallel-size`, `--dtype`, `--max-model-len`,
  `--gpu-memory-utilization`, `--quantization`
- SGLang: `--tp-size`, `--mem-fraction-static`, `--dtype`
- TRT-LLM: `--tp_size`, `--kv_cache_free_gpu_memory_fraction`
- NIM: `NIM_TAGS_SELECTOR` profile id

Layout (backend-specific URL scheme; conceptually identical):

```
<root>/<hash[:16]>/manifest.json
<root>/<hash[:16]>/tree/                          # captured directory tree
                       rootfs/...                 # (skipped for NIM)
                       volumes/<name>/...
```

For Local backend `<root>` is a filesystem path (e.g. `/var/lib/nvsnap/cache`
or a mounted PVC); for GPD-ROX it's a per-capture PVC; for GCS it's
`gs://<bucket>/nvsnap/v1/`.

`manifest.json` records: full hash, capture timestamp, source pod
metadata, total size, file count, list of volumes captured. Used for
cache invalidation and audit.

Human-readable CLI alias: `nvsnap capture list` shows
`<engine>-<model-shortname>-<hash[:12]>` rows; the hash prefix is the
canonical id.

### 3.3 Storage backend (pluggable)

Per §2.1, storage is an interface — `Backend = Store + Mounter`. Concrete
backends:

| Backend | First-byte latency | Sustained throughput | Multi-pod concurrent reads | Implementation cost | When to pick |
|---|---|---|---|---|---|
| **Local** (any RWX FS or single-node) | filesystem-native | filesystem-native | depends on mount | trivial — already shipped | dev, single-node, or when mounted on top of a shared RWX PVC (Filestore, etc) |
| **GPD-ROX** (per-capture PVC, [RWO,ROX]) | ~1 ms | ~1.5 GB/s per attached node | ~16 attach limit per disk | medium — K8s client + per-capture lifecycle | **default for Phase 1 on GKE** (works without Filestore API enabled) |
| **Filestore RWX** (Local + Filestore mount) | ~5 ms | ~2-3 GB/s shared | unlimited | low — config flip if Filestore API enabled | when Filestore API is enabled in the GCP project |
| **GCS Fuse** (gcsfuse CSI inline volume) | ~50 ms | lazy, per-pod ~1-1.5 GB/s | unlimited | low | cross-cluster portability or no PVC quota |
| **GCS prefetch** (download then bind-mount) | upfront download | local-disk after fetch | unlimited but pay once | medium — init container | when GCS Fuse latency hurts engine startup |

**Phase 1 default**: GPD-ROX backend (no API enables required, works
today on the test cluster). **Phase 1.5 path forward**: Filestore RWX
when API is enabled — switch backends via config, no code change.

**Restore-side behavior** is fully encapsulated in `Mounter.Mount(hash, path)`
which returns the pod-yaml fragment to inject. The webhook is
backend-agnostic.

### 3.4 Restore — the critical path

The spike used `tar -xf` of a 69 GB tar on FRESH start, taking ~80s. This
is the single biggest restore-time win available in Phase 1.

Three options, ordered by performance:

**Option A — bind-mount pre-fetched directory tree (recommended)**:

1. Mutating webhook adds an init container that ensures the captured
   tree is present at `/var/lib/nvsnap/cache/<hash>/<volname>/`.
2. The init container is a no-op if the tree exists locally (cache hit).
3. Otherwise it does parallel directory-tree download from GCS (gcsfuse
   read-through or `gcloud storage cp -r` with `--continue-on-error`).
4. The webhook adds a hostPath bind-mount mapping
   `/var/lib/nvsnap/cache/<hash>/<volname>/` → the engine's expected mount
   point (`/opt/nim/.cache`, `/root/.cache/huggingface`, etc).

Restore time on cache hit: ~1s. On cache miss: limited by GCS download
bandwidth (typically 1-2 GB/s sustained per VM) ≈ 35-70s for 70 GB.
Either way, no extraction step. Compares to ~80s tar -x in the spike.

**Option B — overlayfs lower layer (Phase 1.5)**:

The captured tree gets converted to an immutable read-only filesystem
image once, then every pod mounts it as overlayfs lowerdir with a
per-pod writable upper. Engine sees the cache via overlayfs; per-pod
writes don't mutate the lower.

Three concrete backings (ranked by simplicity → flexibility):

1. **erofs** — Linux-native read-only filesystem (kernel ≥ 5.4). Convert
   the captured tree to a single `.erofs` image with `mkfs.erofs`,
   distribute via GCS, mount with `mount -t erofs ...`. Mount time is
   sub-second; reads go through the kernel page cache. Best when the
   image fits on local NVMe. Operationally simplest — no CSI driver, no
   FUSE process. (B. has prior production experience with this pattern.)
2. **nydus / stargz** — lazy-pull container-image-like format with
   per-chunk demand fetch from the registry. Pod start doesn't block on
   downloading the whole image. Better at fleet scale where node-local
   NVMe can't hold every (model, profile) image. Higher infra cost
   (snapshotter daemon).
3. **erofs + EROFS-over-fscache lazy fetch** — kernel 6.1+ supports
   on-demand fetch of erofs blocks via fscache. Combines erofs's mount
   speed with lazy pulling. Newest of the three; least mature.

Restore time on cache miss: TTFT (time-to-first-token) unchanged from
cold start, but the engine never blocks on a "load all weights" step.

Phase 1 ships Option A (no FS layer). Phase 1.5 evaluates erofs first
(lowest cost, proven elsewhere), then nydus if fleet scale demands it.

**Option C — fall back to tar**: if neither A nor B is available
(unsupported storage class), tar -x as in the spike. Slow but always
works.

Phase 1 ships with Option A. Phase 1.5 adds Option B as a perf upgrade.

### 3.5 Customer transparency

Customer's pod yaml is unchanged except for one annotation:

```yaml
metadata:
  annotations:
    nvsnap.io/restore-from: "auto"          # or explicit: "<hash>"
```

`auto` means: webhook computes the hash from the pod spec (image, env,
resources) and looks up the latest matching capture. Explicit hash is
for pinning to a specific capture (rollback, debug).

**Mutating admission webhook** (`cmd/nvsnap-server/webhook.go`):

1. On pod create with `nvsnap.io/restore-from`: compute or resolve hash.
2. If no capture exists: pass through (cold start; capture on warm).
3. If capture exists: inject:
   - One hostPath volume `nvsnap-cache` → `/var/lib/nvsnap/cache/<hash>/`
   - One init container `nvsnap-cache-warm` that ensures the tree is
     present locally (downloads from GCS if missing).
   - Per captured volume: replace the user's volume with a hostPath
     pointing into the cache, OR add an init container that bind-copies
     the cache subtree into the user's existing emptyDir (when
     hostPath isn't acceptable, e.g. when the user expects an empty
     directory at startup).

The webhook never touches the main container's command/args, except to
add init containers. NIM, vLLM, SGLang, TRT-LLM all work with the same
machinery.

### 3.6 Capture lifecycle

- **Trigger**: agent watches pods labeled `nvsnap.io/capture: "true"`
  (or globally via cluster config). When a labeled pod hits ready +
  serves N successful inferences (configurable), capture runs once.
- **Idempotent**: capture is content-addressed; second capture of the
  same hash is a no-op.
- **Eviction**: existing retention policies (issue #42) apply by hash
  prefix. Operator chooses retention by `nvsnap.io/cache: <model-class>`.
- **Audit**: every capture logs to the audit table (issue #45).

## 4. What's preserved vs lost (generic)

Rootfs-only preserves on-disk state only. Lost on every restore:

| Layer | Examples |
|---|---|
| CPU process memory | Python interpreter + modules, JIT-compiled code, tokenizer in-memory tables, scheduler state, pinned host buffers |
| GPU VRAM | Weights uploaded H2D, KV cache, activation buffers, CUDA graph buffers, allocator free-lists, compiled CUDA kernels |
| CUDA driver state | CUDA contexts (one per GPU), UVM page tables, GPU virtual address space, stream queues |
| Multi-GPU collectives | NCCL communicators, ring topology, P2P channels, IPC handles, /dev/shm NCCL segments |
| OS process state | FDs, sockets, pipes, mmap regions, child process tree, signal handlers |
| Network state | TCP connections, ZMQ sockets, listening ports, in-flight requests |

Preserved: model weights, compiled engine plans (TRT-LLM C++), JIT
kernel caches (~/.triton, ~/.cache/torch/compile), tokenizer/config
files, NIM workspace metadata, driver firmware, ldconfig cache.

## 5. Empirical validation (2026-05-03)

Spike (`scripts/spike-rootfs-only.sh`) validated end-to-end on flagship
engines:

| Engine | Model | TP | GOLDEN cold | FRESH (tar-based spike) | Speedup |
|---|---|---|---:|---:|---:|
| vLLM | Llama-3.1-8B | 2 | 110s | 60s | 1.82x |
| SGLang | Llama-3.1-8B | 2 | 100s | 51s | 1.95x |
| TRT-LLM | Llama-3.1-8B | 2 | 151s | 77s | 1.95x |
| NIM | Llama-3.3-70B fp8 | 2 | 482s | 433s | 1.11x |
| vLLM | Llama-3.1-70B | 4 | 300s | 255s | 1.18x |

The 70B numbers are hostPath-wiped (honest). NIM speedup is small because
NGC CDN caches the second pull; real cross-node fan-out should show
higher savings per pod.

`kubectl logs <fresh-pod>` works on all FRESH pods (no setns / hostPID).

Tar-based numbers are pessimistic for production. Replacing tar -x with
bind-mount (§3.4 Option A) on cache hit should drop restore by ~70-80s
on 70-class models.

## 6. Phase 1 implementation plan

### 6.1 Components

| Component | Path | Role |
|---|---|---|
| Capture orchestrator | `internal/rootfsonly/capture.go` | Discover source pod, scan mountinfo, classify volumes, drive copy |
| File-tree copier | `internal/rootfsonly/copier.go` | Parallel rsync-style copy → GCS, content-addressed |
| Storage abstraction | `internal/checkpointstore/store.go` | `Get(hash) → tree`, `Put(hash, tree) → manifest` |
| GCS backend | `internal/checkpointstore/gcs.go` | GCP-specific impl |
| Local cache | `internal/checkpointstore/local.go` | Node-local NVMe cache |
| Hash compute | `internal/rootfsonly/hash.go` | Content-addressed id from pod spec |
| Mutating webhook | `cmd/nvsnap-server/webhook.go` | Inject volumes + init containers based on annotation |
| Webhook config | `deploy/k8s/webhook.yaml` | MutatingWebhookConfiguration + cert |
| Capture trigger | `internal/agent/capture_watch.go` | Watch labeled pods, drive capture on warm |

### 6.2 Sequencing (rough)

| Week | Deliverable |
|---|---|
| 1 | `internal/checkpointstore/` skeleton + GCS backend + node-local cache + unit tests |
| 2 | `internal/rootfsonly/capture.go` — turn the spike into Go; preserve heuristics; emit content-addressed manifest |
| 3 | Mutating webhook + cert mgmt; bind-mount restore (Option A); single-engine e2e (vllm-8b TP=2) |
| 4 | All-engine e2e (vllm/sglang/trtllm + NIM); fix engine-specific edge cases |
| 5 | Capture trigger (warm-pod watch); retention/audit integration |
| 6 | Hardening: failure modes, cache eviction races, GCS auth, partial download recovery |

### 6.3 Performance targets

| Workload | TP | Target FRESH ready (cache hit) | Target FRESH ready (cache miss, GCS) | Stretch |
|---|---|---:|---:|---:|
| vllm-8b | 2 | < 35s | < 60s | < 25s |
| sglang-8b | 2 | < 30s | < 55s | < 20s |
| trtllm-8b | 2 | < 35s | < 70s | < 25s |
| vllm-70b | 4 | < 90s | < 180s | < 60s |
| nim-llama-70b | 2 | < 60s (fp8) | < 150s | < 40s |

Cache-hit numbers assume node-local NVMe + bind-mount (no copy). Cache-miss
numbers assume parallel GCS download at ~1.5 GB/s sustained.

## 7. Phase 2 — CRIU CPU-dump hybrid (sketch)

Layered on Phase 1. Saves Python import + module init time.

Flow: pre-checkpoint signal → engine releases CUDA contexts (vLLM
`sleep_mode`, SGLang pause) → CRIU dumps CPU memory + FDs (no GPU
device fds open) → restore: CRIU restores CPU state → resume signal →
engine re-acquires GPU + re-loads weights from rootfs cache.

Scope: vLLM first (cleanest hook). SGLang next. TRT-LLM and NIM later.

Estimated additional saving on top of Phase 1: 10-30% (smaller for
70B-class, larger for 8B-class).

## 8. Phase 3 — full CRIU + cuda-checkpoint + rootfs

Single restore path that handles both single-GPU and multi-GPU
transparently. Contingent on NVIDIA fixing the multi-GPU
`cuMemUnregisterGpu` hang in cuda-checkpoint.

Action: file detailed bug report with NVIDIA. Track. Do not gate
platform delivery on this.

## 9. Open problems

1. **GCS download bandwidth ceiling per VM** — typical sustained 1-2
   GB/s. For 100 pods cache-cold simultaneously on different VMs, total
   GCS egress can hit project-level quotas.
2. **Engine-specific cache layout drift** — engines change cache schemas
   across versions (vLLM 0.10 → 0.12 has different
   `~/.cache/torch/compile/` keys). Capture format version handles this
   but invalidation strategy is operator-managed today.
3. **Hostpath cache GC on the node** — node-local NVMe fills up with
   `/var/lib/nvsnap/cache/<hash>/` trees. Need a daemonset-side reaper.
4. **Atomic capture** — pod can be deleted mid-capture. Current spike
   doesn't detect this. Need a finalize step that promotes a tmp prefix
   to the final hash key.
5. **Concurrent captures of the same hash** — two pods of the same
   workload finish warming at the same time. The store should deduplicate
   (write-once). Use GCS `IfGenerationMatch: 0`.
6. **Webhook failure mode** — if the webhook is down, pods must continue
   to be admitted (cold start). Fail-open.
7. **Driver-version mismatch** — capture made on driver 580.x, restored
   on driver 555.x. Hash includes driver major; minor mismatches today
   force cold start. Could relax with testing.

## 10. Non-goals

- Replacing single-GPU CRIU. Single-GPU continues to use CRIU for
  sub-second restore.
- Hot-pool / pre-warmed engines. Out of scope for generic-workload
  clusters.
- Cross-cloud portability. Phase 1 is GCP-native (GCS). S3/Azure later.
- Live migration. Captures are point-in-time, not continuous.

## 11. Open issues (to be filed)

| Title | Phase | Priority |
|---|---|---|
| Multi-GPU rootfs-only restore — Phase 1 design (this doc) + tracking | 1 | P0 |
| Content-addressed checkpoint store (GCS backend + local cache) | 1 | P0 |
| Capture orchestrator (`internal/rootfsonly/`) | 1 | P0 |
| Mutating admission webhook for transparent restore | 1 | P0 |
| Bind-mount restore path (eliminate tar -x bottleneck) | 1 | P0 |
| Capture trigger: warm-pod watch + content-addressing | 1 | P1 |
| Retention/audit integration for rootfs captures | 1 | P1 |
| Phase 1.5 — erofs / nydus overlayfs lower layer | 1.5 | P2 |
| Phase 2 — CRIU CPU-dump hybrid for vLLM (sleep_mode) | 2 | P1 |
| Phase 2 — SGLang pause / disk-swap integration | 2 | P2 |
| Phase 3 — track upstream NVIDIA fix for cuda-checkpoint multi-GPU `cuMemUnregisterGpu` | 3 | P3 |
| GPUDirect Storage for weight load (orthogonal restore-perf optimization) | n/a | P3 |
