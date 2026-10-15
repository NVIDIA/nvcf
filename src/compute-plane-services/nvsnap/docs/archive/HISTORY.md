# Project history & superseded designs

A narrative digest of the design history captured by the documents in this
`docs/archive/` directory. The point is to keep the *reasoning* — why a path
was taken or abandoned, what a spike measured, where a fork came from —
readable in one place, so the individual time-capsule files can be skimmed
rather than excavated.

**Canonical current-state docs live in `docs/`**, not here:
- `docs/architecture/NVSNAP-ARCHITECTURE.md` — current system architecture
- `docs/architecture/04-RUNTIME-AGNOSTIC.md` — `/proc`+cgroup discovery mechanics (still as-built)
- `docs/architecture/06-CUDA-CHECKPOINT-CALL-FLOW.md` — the two cuda-checkpoint invocation paths (as-built; debugging reference)
- `docs/architecture/VLLM-DEEP-DIVE.md`, `docs/architecture/QUIESCENCE-ARCHITECTURE.md`
- `docs/design/STORAGE-AGNOSTIC-L2-PROMOTION.md` — current L2 promotion design
- `docs/TRANSPORT-ARCHITECTURE.md` — 3-leg data-movement design (under review)
- `docs/BENCHMARK.md`, `docs/PDF-BENCH-RESULTS.md`, `docs/BENCH-NVCA-2026-06.md` — bench methodology + current results

**Naming note.** These docs span the `nvsnap → cryo → nvSnap` naming churn.
Treat `nvsnap-lib` ≈ `cryo-lib`, `NVSNAP_RESTORED` ≈ `CRYO_RESTORED`,
`libnvsnap_intercept.so` ≈ `libcryo_intercept.so`. They describe one system.

---

## 1. Architecture lineage

**The original design was never shipped.** `architecture/01-OVERVIEW.md`,
`architecture/03-INTERCEPTION-LAYER.md`, and `architecture/05-GPU-STATE-COMPLETE.md`
describe a self-built `libnvsnap.so` that would intercept ~500 CUDA/NCCL/cuDNN
calls, shadow-track all GPU state, and dump GPU memory itself (via `cuMemcpy`),
remapping handles or reserving identical virtual addresses on restore. **We did
not build this.** The shipped system delegates GPU state entirely to NVIDIA's
`cuda-checkpoint` + CRIU's CUDA plugin; the intercept library is scoped to
io_uring / libuv / libzmq / NCCL-teardown / signals. Salvageable ideas recorded
there: same-VA reservation (`cuMemAddressReserve` with addr hint) to dodge handle
remapping, tracking the cuRAND generator *offset* for reproducibility, the
cuDNN-dropout RNG-state observation, and the agent Unix-socket protocol
(REGISTER/CHECKPOINT_PREPARE/READY/RESTORE_COMPLETE). The overhead numbers there
are unverified design figures.

**`ARCHITECTURE.md`** (top-level archive) is the polished pre-rename writeup of
the system as actually built. Durable content not in the current arch doc:
- the full **Python injection** mechanism (sitecustomize.py on `PYTHONPATH`,
  per-ABI `site-packages-cpXY/` shadowing) — superseded as primary reference by
  `GENERIC-PYTHON-INJECTION-DESIGN.md` (see §2);
- the **SONAME-dedup rationale** for why pyzmq need not be rebuilt;
- the **versioned-signal-handler interpose** (`sigaction@@GLIBC_2.2.5`) to beat
  libtorch reinstalling its handler;
- per-library patch-reason table (CRIU 3.17, uvloop 0.22.1, libuv 1.48, libzmq 4.3.6);
- the 2026-05-12 8-workload benchmark table.

**`architecture/05-MULTI-PROCESS.md`** — the barrier→quiesce→freeze→checkpoint
coordination protocol and NCCL-restore-by-reconstruction (save
{nranks,rank,uniqueId,device}; rank-0 regenerates uniqueId on restore;
intercept lib remaps old→new comm pointer). Concepts persist in spirit, but some
specifics were **later corrected**: vLLM workers fork *then* `ncclCommInitRank`,
the parent/engine holds no comms — the "parent-skip" assumption here was wrong.

## 2. Interception layer & library forks

- **Python injection** (`GENERIC-PYTHON-INJECTION-DESIGN.md`): a ~15-line
  `sitecustomize.py`, loaded once by CPython's `site.py`, detects the ABI tag and
  prepends the matching `/nvsnap-lib/site-packages-cpXY` to `sys.path`. uvloop is
  the only Python-version-keyed lib (cp310–313 wheel matrix on manylinux_2_28);
  libzmq/libuv are C-ABI singletons via `LD_LIBRARY_PATH`+dlopen, so **pyzmq is
  not rebuilt** (validated commit ff453b7). **Lasting gotcha:** CRIU verifies
  mmap'd file sizes, so a workload's `<id>.yaml` and `<id>-restore.yaml` must move
  to a new injection layout *in the same commit* or restore aborts with "restore
  requires file contents but not found in checkpoint."
- **uvloop post-restore segfault** (`uvloop-analysis/ANALYSIS.md`,
  `uvloop-analysis/PATCH-SPEC.md`): `UVHandle._handle` (a `uv_handle_t*`) is a
  stale pointer after restore. Proposed fix = generation-counter in `handle.pyx`
  keyed on `NVSNAP_RESTORED` env; on mismatch `_invalidate_after_restore()` nulls
  the handle without dereferencing so callers recreate it. This evolved into the
  **libuv fork** (`fix/issue-41-no-sqarray`, patches isolated in `src/unix/uv__cryo.c`).
- **libuv io_uring** (`libuv_io_uring_analysis.md`): libuv uses io_uring only for
  fs ops + `IORING_OP_EPOLL_CTL` (sockets stay on `epoll_pwait`); two rings
  (`iou` SQPOLL/64, `ctl` normal/256). After restore the mmap'd ring regions are
  invalid; `uv_loop_fork()`→`uv__io_fork` reinits but **loses pending
  submissions/completions** (matches the in-code TODO at linux.c:664). The
  leading suspect for the crash: libuv's cached `in_flight` counter goes stale
  (kernel ring empty, libuv still holds non-zero) — origin of the `uv__cryo.c` work.
- **libzmq fork origin** (from the deleted `TOMORROW.md`, 2026-02-03): the ZMQ
  C/R system began as a forked libzmq adding `zmq_ctx_checkpoint`/`zmq_ctx_restore`
  + a CRIU ZMQ plugin; post-restore `zmq_msg_send` returned EINVAL because sockets
  weren't re-bound. pyzmq must be forced off its bundled libzmq
  (`pip install pyzmq --no-binary pyzmq`). Now tracked as `libzmq v4.3.6-criu-epoll-*`.
- **Stale-binary incident** (from deleted `STATUS-TOMORROW.md`, 2026-02-06):
  Docker cached the `COPY restore-entrypoint` layer even after a rebuild, silently
  shipping stale code → the origin of the "always bump image tags / `NO_CACHE` on
  source change" rules. Same era: NVIDIA-device external mappings grew 24→30 as the
  fix for CRIU "Unable to restore 0xXX".
- **Early restore findings** (from deleted `summary.md` 2026-01-26 /
  `RESTORE_DEBUG_STATUS.md` 2026-01-20): uvloop's fork pending-call ran on a
  `Dummy` thread, not main; CRIU soccr bind failed because the pod IP wasn't
  captured → `getPodIP` fallback reading `/proc/net/fib_trie`. io_uring SQPOLL-flag
  stripping (0x2→0x0), ring-index sync, and ring-fd restore were all already
  working — the segfault was downstream, in the stale libuv counters above.

## 3. The rootfs capture pivot (multi-GPU)

cuda-checkpoint hangs on `cuMemUnregisterGpu` for TP>1 (the libcudart wall, see
MEMORY `multi_gpu_libcudart_wall`), so multi-GPU restore abandons GPU-state
preservation entirely and instead relaunches the container from pre-staged
on-disk caches.

- **`MULTI-GPU-ROOTFS-FANOUT-DESIGN.md`**: three-phase plan (Phase 1 rootfs-only
  "warm cold-start"; Phase 2 +CRIU CPU dump; Phase 3 +GPU state, blocked upstream).
  Content addressing over `image_digest + model_id + engine_compat_flags +
  cuda_driver_major + capture_format_version`. **Kill the tar:** bind-mount the
  pre-fetched tree (~1s on cache hit vs ~80s `tar -x`). Phase 1 went
  production-grade on vllm-8b TP2 (79s vs ~5min), 2026-05-04.
- **`design/ROOTFS-EVERYWHERE.md`** (#228, v0.0.49): made rootfs the single
  production path (CRIU kept as `NVSNAP_DEFAULT_CAPTURE_PATH=criu` rollback).
  ChainBackend `Local→ConfigMap→PerCapturePVC`; manifest ConfigMap moved to the
  source pod's namespace; Watcher poll-loop → SharedIndexInformer (≤2s vs ~60s);
  NVCA warmup buffer default 10s→0.
- **Restore-overlay evolution (one feature, four docs):**
  1. `proposals/rootfs-restore-overlayfs.md` (#194/MR65) — per-pod **host-side**
     OverlayFS (lower = captured tree, upper keyed by pod-uid) bind-mounted in, so
     writes (e.g. inductor cache during cudagraph capture) don't hit EROFS errno-30.
  2. `proposals/whole-rootfs-restore.md` (#88) — enumerate the *whole* captured
     rootfs (≥100 KiB subdirs) instead of a curated catalog; capture-side excludes
     `/tmp,/run,/proc,...`. Driven by DeepSeek-V4-Flash JIT caches captured but not
     exposed → warm restore as slow as cold (~15min re-JIT).
  3. `proposals/init-container-mount-prep.md` (#202) — move mount work out of the
     webhook (355 DeepSeek mounts blew the 30s webhook timeout) into a
     `nvsnap-mount-prep` init container; webhook becomes a <200ms patch-only step.
  4. `proposals/rootfs-overlay-mount-scale.md` — the architectural fix: keep the
     Pod object **O(1)** regardless of path count (gpt-oss-120b's 1055 paths blew
     the etcd 1.5 MiB cap). `RootfsProvider` B′ (in-container whole-rootfs overlay +
     `pivot_root`, `CAP_SYS_ADMIN` not privileged) or C (containerd proxy
     snapshotter). Supersedes the per-path injection of the three docs above.
- **`design/ROOTFS-RESTORE-INJECTION.md`** (v0.0.55): bugfix — dispatch on capture
  *type* (`manifestIsRootfs`), not PVC existence; a rootfs capture promoted to an
  L2 PVC was wrongly hitting the CRIU `restore-entrypoint` and hanging on
  `inventory.img` it never produced.
- **`proposals/vllm-config-hash-prestage.md`** — **RESOLVED, but not as proposed.**
  All pre-stage designs were rejected (byte-neutrality / workload-agnostic
  constraints). Real root cause: the team had added `HF_HUB_OFFLINE=1` to restore
  env (to silence `.no_exist` warnings); vLLM rewrites repo-id→path only when that
  is set, flipping the `config_hash`. Fix = *remove* `HF_HUB_OFFLINE` (v0.0.99) →
  compile-cache hit (torch.compile 19.8s→4.58s). Pre-stage shelved unless a future
  air-gapped deployment needs offline mode.

## 4. L2 per-capture PVC fan-out

`L2-PVC-CRIU-DESIGN.md` — land each dump in a per-capture PVC so N restore pods
mount it ReadOnly in parallel instead of bottlenecking on the source node:
- writer Job mounts `rwx-<hash>` RWO (lease-serialized, one writer per content
  hash via `coordination.k8s.io/v1.Lease nvsnap-promote-<hash>`) → VolumeSnapshot
  → clone to `rox-<hash>` ReadOnlyMany → delete the rwx PVC. Two-name convention;
  `rox-` is never RW-mounted. RWX-for-both was relaxed (nvsnap#81) once it was
  clear the writer is single-pod — enabling block CSIs like Hyperdisk-ML.
- **No PV-flip**: the old approach failed because restore-entrypoint wrote
  `restore-criu.conf` into the now-RO dir; fixed by routing all CRIU runtime files
  to `/tmp`. PVC sized = GPU vRAM × gpu_count × 1.2.
- 5-tier restore resolver: L1 hostpath → L2 rox (Bound) → L2 in-flight poll on
  `pvc_promote_state` → L3 peer cascade → L4 blobstore.

This SnapshotClone mechanism was **generalized into a pluggable `Promoter`**
(#257–259) — see the current `docs/design/STORAGE-AGNOSTIC-L2-PROMOTION.md`.

## 5. Data movement: transport, spikes, cross-cluster

- **Spike series** (`spikes/SPIKE-RESULTS.md`, `spikes/FAST-RESTORE-PROPOSAL.md`,
  `spikes/EROFS-VALIDATION.md`, `spikes/SCALE-OUT-DESIGN.md`,
  `spikes/WORKLOAD-ROADMAP.md`; `FAST-DATA-MOVEMENT-IDEAS.md`):
  - **Spike 3 — the durable finding:** the L1 bottleneck is the *single-stream
    client* (curl/`http.Client`), not the wire (36 Gbit/s pod↔pod) or storage
    (~10 Gbit/s). Blobstore PUT scales 1.4→10.1 Gbit/s 1→8 streams (**7.2×**).
    Lever = shard into N parallel chunked PUTs (landed #91/#92/#95/#96).
  - **EROFS — RED light as primary path** (2026-05-15): 5.24× on CRIU dumps but
    **1.01× on multi-GPU rootfs** (safetensors incompressible); GKE 5.15 mounts
    lz4-only (zstd needs 6.1+). Receiver-side win is real (mount ~2ms) but per-file
    CAS already gives parallel transfer + dedup. Revisit triggers: kernel 6.1+,
    multi-receiver per-node fan-out (fires in SCALE-OUT P4).
  - **lazy-pages — GREEN light** (2026-05-16): vllm-small 44→33s (−25%); CRIU CUDA
    plugin *is* compatible (GPU eager, CPU pages via uffd). Win capped ~25% because
    the readiness probe touches all weights. **nim-llama-8b regressed +124%** →
    runtime auto-disable via `NIM_CACHE_PATH`. Ships default-off (`NVSNAP_LAZY_PAGES=0`).
  - **Scale-out** is a distinct problem (200 pods × 133 GiB = 13.3 TiB; naive
    central pull ~3h vs torrent-style ~110s). Priorities P1 parallel cascade →
    P2 locality placement → P3 seed pre-staging → P4 EROFS shared-mount.
- **Cross-cluster replication** — two designs, the second supersedes the first:
  - `CROSS-CLUSTER-REPLICATION-DESIGN.md` (2026-05-12): single canonical S3 bucket
    + SNS/SQS notification fan-out + per-cluster replicator with sidecar-manifest
    filter; keeps the custom blobstore. **Superseded.**
  - `design/cross-cluster-replication.md` (newer, canonical): **per-cluster home
    buckets + lazy pull-through cache + content-addressed bucket-probe discovery**
    (no SNS/SQS, no federated catalog); **eliminates the custom blobstore (L4)**;
    cloud-neutral. The GCS push/pull bridge was built on this basis (#234–238).
- **`CHECKPOINT-LOOKUP.md`** — the checkpoint identity/lookup contract:
  `hash = sha256(canonical-json(HashInput))`, `shortHash` = first 32 hex for K8s
  names; ConfigMap `nvsnap-capture-<shortHash>` is the lookup index, annotation
  `nvsnap.io/restore-from=<hash>` is the trigger; 3-tier cascade in
  `capture_cascade.go::EnsureCaptureLocal`.

## 6. Bench & infra history

- **`milestones/PHASE-0-FOUNDATION.md`** — the original 4-week scaffolding plan;
  uses `nvsnap.io` group, `github.com/nvsnap/nvsnap` module, gRPC-first API, GitHub
  Actions. The codebase **diverged** (Cryo rebrand, REST not gRPC, GitLab CI).
- **`BAZEL-FORKS-PLAN.md`** — plan to bring the five sibling forks under
  `bazel build //...`: Strategy A (`rules_foreign_cc`) for libuv/libzmq/pyzmq/uvloop,
  Strategy B (keep buildah) for CRIU (CUDA-plugin headers can't be vendored without
  legal sign-off). Document-only MR; the work followed later (#245 lint/bazel sweep).
- **`GCP-BENCH-CLUSTER.md`** — kept active in `docs/`; the two A3-Mega H100 bench
  clusters are live.
- **`CACHING-MEETING-BRIEF.md`** (2026-05-19) — measured cascade numbers: CRIU
  cascade 1.20 GB/s/receiver, rootfs cascade 0.32 GB/s/receiver (per-file HTTP
  overhead, not network); cluster ceilings md0 RAID0 2.9 GB/s, 8-stream
  curl-to-disk 2.5 GB/s. Flagged GH#114: rootfs path did **not** fan out cross-node
  (webhook pinned restore pods to source node) — since addressed.
- **KubeCon CFP** (the gitignored, local-only `KUBECON.md`, *not* in the repo):
  title "Sub-Minute Restore — Application-Transparent GPU C/R for Inference on
  Kubernetes", AI+ML track, deadline 2026-05-29, talk Nov 2026. Re-measure
  2026-05-19: vllm-small cold 81s / restore 40s; vllm-70b TP=4 133 GiB rootfs,
  332s cold.

---

## Still active in `docs/` (not archived)

Several designs discussed above remain **live in `docs/`** because shipping
code cites them as the design-of-record — archiving them would orphan in-code
pointers. They are current references, not history:
`L2-PVC-CRIU-DESIGN.md`, `MULTI-GPU-ROOTFS-FANOUT-DESIGN.md`,
`GENERIC-PYTHON-INJECTION-DESIGN.md`, `design/ROOTFS-EVERYWHERE.md`,
`design/ROOTFS-RESTORE-INJECTION.md`, `design/cross-cluster-replication.md`,
`proposals/rootfs-restore-overlayfs.md`, `proposals/init-container-mount-prep.md`,
`proposals/rootfs-overlay-mount-scale.md`.

## Index of archived files (here in `docs/archive/`)

| File | What it is |
|------|------------|
| `ARCHITECTURE.md` | Polished pre-rename system writeup (as-built) |
| `architecture/01-OVERVIEW.md` | Original problem framing + abandoned shadow-tracker design |
| `architecture/03-INTERCEPTION-LAYER.md` | Abandoned ~500-call CUDA interposition design |
| `architecture/05-GPU-STATE-COMPLETE.md` | Exhaustive GPU-state catalog (abandoned approach) |
| `architecture/05-MULTI-PROCESS.md` | vLLM coordination protocol + NCCL reconstruction |
| `uvloop-analysis/ANALYSIS.md` | uvloop stale-handle root cause |
| `uvloop-analysis/PATCH-SPEC.md` | generation-counter patch spec |
| `libuv_io_uring_analysis.md` | libuv io_uring rings + stale-counter analysis |
| `proposals/whole-rootfs-restore.md` | whole-tree enumeration (#88; superseded by mount-scale) |
| `proposals/vllm-config-hash-prestage.md` | resolved via removing HF_HUB_OFFLINE |
| `CHECKPOINT-LOOKUP.md` | checkpoint identity/lookup contract |
| `CROSS-CLUSTER-REPLICATION-DESIGN.md` | old SNS/SQS replication (superseded) |
| `FAST-DATA-MOVEMENT-IDEAS.md` | compression/format/RDMA exploration |
| `spikes/SPIKE-RESULTS.md` | Spikes 1–4 + EROFS T1–T8 results |
| `spikes/EROFS-VALIDATION.md` | EROFS validation + no-ship verdict |
| `spikes/FAST-RESTORE-PROPOSAL.md` | single-pod fast-restore + lazy-pages green-light |
| `spikes/SCALE-OUT-DESIGN.md` | 200-pod fan-out design |
| `spikes/WORKLOAD-ROADMAP.md` | two-track (CRIU vs rootfs) strategy |
| `BAZEL-FORKS-PLAN.md` | plan to bazel-ify the sibling forks |
| `CACHING-MEETING-BRIEF.md` | 2026-05-19 cascade/caching measurements |
| `milestones/PHASE-0-FOUNDATION.md` | original scaffolding milestone (diverged) |
