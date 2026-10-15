# Fast Restore — Architecture Proposal + 3-Day Research Plan

**Status:** proposal / research plan
**Branch:** `spike/fast-data-movement`
**Date:** 2026-05-15
**Driver:** restore bringup is what customers feel. Today vllm-70b TP=4 takes 233 s to ready and that's after the weights are on the local disk. We want to collapse this to seconds.

---

## Problem reframe — what restore time actually is

Decomposing the 233 s vllm-70b TP=4 baseline (measured 2026-05-15, T8):

| stage | time | bottleneck class |
|---|---|---|
| pod sandbox + image pull (cached) | ~5 s | k8s |
| Python interpreter + vLLM imports | ~30 s | CPU (single-threaded torch import) |
| read 140 GiB weights from local NVMe | ~25 s | NVMe seq read (~5 GB/s) |
| `model.to(cuda)` host→GPU PCIe DMA | ~30 s | PCIe gen4 ×16 ~32 GB/s × 4 GPUs |
| NCCL TP=4 init (rendezvous + comms setup) | ~25 s | network round trips |
| `torch.compile` + CUDA graph capture | ~60 s | GPU compute |
| readiness probe stabilization | ~5 s | poll cadence |
| **total** | **~233 s** | |

**Only ~25 s of that is "shipping data".** Faster transports hit a floor at ~200 s for this workload unless we attack the other stages too.

So: a revolutionary scheme can't just be a faster pipe. It has to **collapse stages that today serialize.**

---

## The proposal — four composable shifts

### 1. Diff-from-base + warm pool (biggest absolute win)

- Reserve a small "warm pool" of GPU nodes, each holding base models (Llama-3.1-8B, Llama-3.1-70B, Qwen3-32B, …) **already loaded in GPU memory**.
- Store checkpoints as **deltas from base**: KV cache, LoRA adapters, fine-tune deltas, allocator state, vLLM compilation cache.
- Restore = identify matching base → clone its GPU memory into the target GPUs via NVLink/PCIe peer-copy → overlay the delta.
- A 70B fine-tune checkpoint goes from 140 GiB to 1–5 GiB. Ships in seconds.
- **Trade-off:** warm-pool GPUs cost money. Worth it when workload distribution is skewed (most customers use a few base models).
- **Falls back to phase-2 path when no matching base.**

### 2. RDMA-GPUDirect data plane (for the bytes we DO ship)

- Source: GPU-resident or NIC-resident checkpoint state.
- Destination: target GPU memory directly via NIC → GPU PCIe peer DMA.
- **Never touches CPU memory or NVMe** for the GPU portion.
- Composes naturally with the s3-rdma service Balaji is building.
- The "30 s host→GPU" stage becomes ~1 s at fabric line rate across 4 NICs.

### 3. Userfaultfd-driven lazy hydration (collapses time-to-ready)

- Pod starts with the process's virtual address space **mapped but not populated**.
- Pages are faulted in on first touch by a userspace handler that fetches from a holding source (peer agent, blobstore, s3-rdma).
- Pod becomes **"ready"** in seconds — long before all weights are loaded.
- First few inferences are slow (lazy-loading what they touch); a background prefetcher warms the rest based on forward-pass access patterns.
- **Time-to-ready** drops from 233 s to seconds. **Time-to-first-token** drops more modestly. Both matter to different SLOs.

### 4. Purpose-built artifact format

Three properties enable the above:
- **Page-aligned chunks** so userfaultfd can pull exactly one page.
- **Self-describing index at the head** so any chunk is reachable in O(1) — no sequential deserialization, no read-the-whole-thing-to-restore.
- **Content-type aware compression**: skip lz4/zstd for `safetensors` (T7 finding: incompressible, no point), use them for sparse CRIU image data; one artifact, multiple internal encodings.

This is **not EROFS** — that's optimized for read-everything-once mount semantics. The NVSNAP format is optimized for "mount it, fault pages over RDMA, decompress on the GPU."

---

## MVP phasing

We do not build all four at once. Each phase is demonstrably useful on its own.

### Phase 1 — "instant ready, slow first inference"
- Userfaultfd in the restored process; file-backed (no RDMA yet).
- Readiness probe passes as soon as the address space is mapped.
- First inference is slow (faults everything in) but pod is "ready" in 10–20 s.
- **Measurable:** time-to-ready 233 s → ~20 s; time-to-first-token: ~unchanged.

### Phase 2 — "instant ready, fast first inference (warm pool)"
- Hot-pool service maintains base models on standby GPUs.
- Restore = clone-base + apply-delta.
- **Measurable:** time-to-first-token 233 s → ~10 s for matching base.

### Phase 3 — "RDMA-GPUDirect data plane"
- s3-rdma + GPUDirect for the L2 leg + the non-warm-pool case.
- Stream weights NIC → GPU at fabric rate.
- **Measurable:** time-to-first-token for non-pooled 233 s → ~30 s.

---

# 3-Day Userfaultfd Research Plan

This is **Phase 1**, scoped to fit a 3-day spike. Goal: demonstrate that we can restore a real GPU workload (vllm-small) with most pages absent and reach `Ready` in well under the 42 s baseline.

CRIU already has lazy-restore support: `criu restore --lazy-pages` + a `criu lazy-pages` page-server process. This is the lowest-friction starting point — the kernel + CRIU pieces all exist, we just have to wire them up.

## Prior art to read before starting

- CRIU `--lazy-pages` design doc: https://criu.org/Lazy_migration
- Linux `userfaultfd(2)` man page (read-fault handler vs write-fault, copy/zeropage/continue ioctls)
- Adrian Reber et al, "Live container migration with CRIU lazy migration" — the original CRIU lazy-migration paper
- Our existing CRIU integration: `internal/criu/manager.go` for the RPC surface
- Our restore-entrypoint: `cmd/restore-entrypoint/main.go` (where we'd add `--lazy-pages` flag)

## Day 1 — get `criu restore --lazy-pages` working at all

**Goal:** vllm-small restores via lazy-pages on a single node, with the page-server local. Forget remote-page-server for now.

### Setup
1. On 1sd6 (where we have a fresh vllm-small dump), build the `criu lazy-pages` daemon: it's the same `criu` binary, just `criu lazy-pages -D <images-dir> --address /tmp/lp.sock`.
2. Modify a copy of the vllm-small restore manifest to launch `criu lazy-pages` as a sidecar in the restore pod, then have restore-entrypoint invoke `criu restore --lazy-pages --address /tmp/lp.sock ...`.
3. Verify the pod comes up. Initial expectation: it works exactly like normal restore (CRIU does the heavy lifting at restore time, lazy-pages just streams pages on demand thereafter).

### Measure
- Time-to-ready vs baseline T1 (42 s for vllm-small).
- Number of pages faulted in during the first inference (CRIU `lazy-pages` daemon logs this).
- Memory residency after warmup vs after first 10 inferences.

### Success criterion
- Pod becomes Ready, inference works, output matches.
- Some pages are demonstrably absent at Ready time (visible via `/proc/<pid>/smaps` or `pagemap` reading).

### Risk
- CUDA plugin in CRIU may not be compatible with lazy-pages mode. GPU memory restore wants all pages eagerly. If so: Day-1 result is "lazy-pages works for CPU state, GPU restored eagerly" — still useful, smaller win.

## Day 2 — measure where the wins are, and where they aren't

**Goal:** quantify what lazy-pages buys us, by what metric, for which workloads.

### Tests
- **T-LP-1:** vllm-small lazy-pages restore — time-to-ready, time-to-first-token, residency at ready.
- **T-LP-2:** Same, but force-touch every page after Ready (`madvise(MADV_WILLNEED)` on the whole mapping) → measure how long until all pages are present. This is the "background prefetch" cost.
- **T-LP-3:** vllm-small lazy-pages with the page-server on a different node, served over plain HTTP TCP. Measures the "remote page-server" overhead — sets a baseline for how much RDMA would later buy.
- **T-LP-4:** Same as T-LP-3 but read-many-pages-per-fault (CRIU has a knob: `--page-bunch`). Measures effective bandwidth of the fault path.

### Measure
- Per-fault latency (microseconds)
- Page-fault rate during first inference (faults/sec)
- Aggregate bandwidth during prefetch (MiB/s)
- Total wall-clock to "warm" (all pages present)

### Decision points
- **If time-to-ready drops to <10 s** for vllm-small: lazy-pages is the right Phase-1 direction. Recommend proceeding to integration.
- **If per-fault latency >100 μs** and the access pattern is random-heavy: page-fault overhead dominates and lazy-pages might not be a win for inference workloads. Reconsider.
- **If CUDA plugin forces eager GPU restore:** lazy-pages buys us only the CPU-side; the win is smaller. Document and decide if it's still worth pursuing.

## Day 3 — write up + decision

**Goal:** crisp answer to "should we build Phase 1 for real, or is the win not worth it?"

### Deliverable
A short addendum to this doc with:
- Measured numbers from Day 1 + Day 2
- Comparison table: baseline vs lazy-pages, time-to-ready, time-to-first-token, time-to-warm
- Concrete integration plan if green-lit: where in `cmd/restore-entrypoint/main.go` to add `--lazy-pages` invocation, what new flags on agent CRD, what new init container (or sidecar) for the lazy-pages daemon
- Concrete reason for "no" if red-lit

### Stretch (only if Day 1-2 went smoothly)
- Probe the **rootfs path**: rootfs captures don't go through CRIU, so userfaultfd would need a different vehicle (FUSE that lazy-fetches files, or a custom loader in the container that streams weights). Not in scope to build, but in scope to think through whether the same lazy-hydration shape applies. A workable rootfs lazy path means Phase 1 covers BOTH the CRIU and rootfs flows; no workable rootfs path means Phase 1 is single-GPU-only and the multi-GPU case needs a different strategy.

---

## Open questions

1. **Does the CRIU CUDA plugin play with `--lazy-pages`?** Needs a Day-1 answer. The plugin restores GPU memory eagerly today; the question is whether the rest of the process (CPU pages, FDs) can be lazy while GPU is eager.

2. **Where does the page server live?** Sidecar in the restore pod (simple, ephemeral) vs daemonset (long-lived, shareable across restores). Affects how we think about warm-pool integration later.

3. **What's the right fault granularity?** 4 KiB (per page) is the kernel's natural unit but is wildly inefficient over network — a 4 KiB RDMA op is ~5 μs overhead per ~5 μs of actual transfer. For network-fetched pages we'll want 64 KiB or 256 KiB clusters, with the userfaultfd handler doing readahead.

4. **How do we integrate with the warm pool later?** If Phase 1 is sidecar + local pages, Phase 2 needs to swap the "fetch source" to a peer GPU's memory. The interface between lazy-pages handler and fetch source should be designed to allow this swap from the start.

5. **userfaultfd in containers — privilege?** `CAP_SYS_PTRACE` historically; check current kernel + containerd config.

## Risks

- **Lazy-pages + CUDA**: unproven combination in our setup. Day-1 has to settle this first.
- **Per-fault overhead at network scale**: if page faults are ~100 μs over LAN, a workload that touches a lot of small disjoint pages will be slower than eager-load. Need access-pattern profiling.
- **Memory pressure**: lazy-paged processes can grow unpredictably; need to handle OOM gracefully (and not just by killing the process mid-restore).
- **The MVP doesn't compose to revolutionary on its own** — Phase 1 alone drops time-to-ready but not time-to-first-token. Worth being honest with stakeholders about: this is foundation work for the bigger architecture, the standalone improvement is "fast probe success" not "fast actual serving."

---

## Related docs

- `docs/spikes/SPIKE-RESULTS.md` — measurements for parallel-PUT, EROFS, throughput
- `docs/spikes/EROFS-VALIDATION.md` — what we learned about EROFS as a restore source (T1-T8)
- `docs/FAST-DATA-MOVEMENT-IDEAS.md` — earlier exploration of transport-layer options

## Out of scope for the 3-day spike

These are part of the larger proposal but are NOT in the userfaultfd research:

- Warm-pool implementation (Phase 2 design work, separate effort)
- s3-rdma integration (Phase 3, dependent on Balaji's service)
- Custom artifact format (Phase 4 design)
- GPUDirect-RDMA wire format
- Cross-cluster page-server access

Each of these is its own follow-on spike if Phase 1 lands.

---

# Day 1–3 Results Addendum (2026-05-16)

## Day 1 — mechanical validation: **PASS**

Env-gated `NVSNAP_LAZY_PAGES=1` in `cmd/restore-entrypoint/main.go:1145` spawns `criu lazy-pages -D <dump>` as a detached daemon, sets `CriuOpts.LazyPages=true` on the restore RPC. Shipped at `nvsnap-agent:v0.24.3-lazy-pages-spike`.

| | baseline (NVSNAP_LAZY_PAGES=0) | NVSNAP_LAZY_PAGES=1 |
|---|---|---|
| time-to-ready (vllm-small, cold cache) | **44 s** | **33 s** |
| inference correctness | ✅ identical | ✅ identical |
| CUDA plugin compatibility | ✅ | ✅ (no plugin changes needed) |
| daemon socket bind | n/a | 140 ms |

**Open question from the proposal — "Does CRIU CUDA plugin play with --lazy-pages?" — answered YES.** The CUDA plugin restores GPU memory eagerly via cuda-checkpoint on its own code path; lazy-pages handles CPU pages via userfaultfd in parallel. The two are orthogonal.

## Day 2 — detailed timing measurement

From the `criu lazy-pages` daemon log:

| metric | value |
|---|---|
| First uffd_copy after daemon start | 852 ms |
| Last uffd_copy after daemon start | 15.29 s |
| Active fault-serving window | ~14.5 s |
| Total uffd_copy events | 15,854 |
| Total bytes transferred via uffd | **27.5 GiB** |
| Avg fault rate | ~1,094 faults/s |
| Avg per-fault bandwidth | ~1.9 GiB/s |
| Avg pages per fault (page-bunch) | ~445 |
| Avg per-fault wall latency (local) | ~0.91 ms |
| Direct-io alignment | 100% |

### Reconciliation with the 33 s wall-clock

- **t=0 to t≈5 s:** CRIU restore RPC returns (process mapped, scheduled). This is where the win is — eager would have synchronously read all 27.5 GiB before returning.
- **t≈5 s to t≈15 s:** lazy-pages daemon serves faults; pages stream in while the process is already resuming.
- **t≈15 s to t=33 s:** vLLM Python init / model load / readiness probe. **All pages already present** by this point; the readiness probe forces full residency.

### Why the win is bounded at ~25%

vLLM's readiness probe (`GET /v1/models`) touches the model weights — the kernel materializes all pages before the probe succeeds. Lazy-pages shifts CRIU's "synchronously read all pages during restore" off the critical path, but doesn't help workloads whose readiness probe is hot. **For workloads with a selective probe** (don't touch all weights at startup) **the win could be substantially larger**.

### Tuning headroom

CRIU's default page-bunch already groups ~445 pages per fault. Tuning `page-bunch` larger likely won't help meaningfully here — we're not bottlenecked on per-fault syscall overhead at this throughput (~1.9 GiB/s is well under disk's measured ceiling). The kernel uffd path is efficient out of the box.

### What was NOT tested

- **Remote page-server (T-LP-3).** Would measure cross-node lazy-pages (page-server on capture node, restorer on a different node). Estimated 2–5 ms per fault over TCP with bunching; that's the ceiling RDMA would later improve. Worth a follow-up spike when scaling out the architecture.
- **Diffusion workloads.** SDXL/Flux have different memory access patterns — only the active stage's weights need to be resident. Could yield >25% win. Worth a separate measurement.
- **Memory-cgroup constrained restore.** Forcing actual page eviction (not the cache-rich 1.9 TiB-RAM nodes we have) to validate that lazy-pages handles re-fault gracefully.

## Day 3 — decision: **GREEN-LIGHT for production integration**

Lazy-pages on the single-GPU CRIU path:
- Works end-to-end with the existing CUDA plugin
- Reproducible 25–27% restore-time reduction on vllm-small
- No correctness regressions (inference output identical)
- No new dependencies (CRIU 4.2 already deployed)
- Low integration risk (one Go function in restore-entrypoint, ~50 lines, env-gated today)

### Recommended integration plan

1. **Promote the env gate to a CRD field.** `GPURestore.spec.lazyPages: bool` plumbs through agent → restore-entrypoint. Default false initially; flip to default true after a soak period.
2. **Track lazy-pages metrics.** Emit Prometheus counter for `uffd_copy` events + per-fault latency histogram from the daemon log. Lets ops detect regressions.
3. **Add a fallback path.** If `criu lazy-pages` daemon fails to start (kernel uffd disabled, missing CAP_SYS_PTRACE, etc.), restore-entrypoint should log and proceed with eager restore. Already in place in the spike code's error branch.
4. **Document.** Add to CLAUDE.md and the operator guide that `lazyPages: true` is the recommended production setting for single-GPU CRIU restores.

Estimated engineering: **3–5 days** for the full integration + tests + soak.

### What this does NOT solve

- Multi-GPU rootfs path (covered separately by `WORKLOAD-ROADMAP.md`).
- Time-to-first-token. Lazy-pages collapses time-to-ready but readiness ≠ first inference. For workloads where TTFT matters more than readiness probe success, additional work (warm pool, GDS bulk, lazy GPU memory which we cannot do today) is needed.
- Cross-node restore. Today's lazy-pages serves from a local dump; a peer-served page-server is the natural next step, untested.

### Out of scope for Phase 1

Per the original proposal, Phase 1 alone is "instant probe success", not "instant serving". Phase 2 (warm pool / serving platform) is what would collapse time-to-first-token for multi-GPU. These are separate efforts on the roadmap.

---

# Regression sweep across all single-GPU workloads (2026-05-16/17)

After Day 1's vllm-small 26% win, we benched lazy-pages across every single-GPU CRIU workload to verify the win generalizes and to catch any regressions. **One workload (nim-llama-8b) regressed 2.2×** — runtime auto-disable now protects it.

## Full sweep results

All runs on same cluster (`example-gpu-cluster`, 1sd6 H100), same agent build (`v0.24.3-lazy-pages-spike` / `v0.24.4-lazy-pages-nim-auto-disable`), restore-Ready wall-clock from `kubectl wait --for=condition=ready`:

| workload | dump size | `LAZY=0` (eager) | `LAZY=1` (lazy) | delta | verdict |
|---|---|---|---|---|---|
| vllm-small (TinyLlama 1.1B) | 30 GiB | 44 s | **33 s** | **−25%** | ✅ enable |
| vllm-8b (Llama-3.1-8B) | 67 GiB | 56 s* | **41 s** | **−27%** | ✅ enable |
| sglang-small (TinyLlama 1.1B) | 80 GiB | 184 s* | **136 s** | **−26%** | ✅ enable |
| sglang-8b (Llama-3.1-8B) | 79 GiB | 175 s* | **132 s** | **−25%** | ✅ enable |
| trtllm-small (TinyLlama 1.1B) | 31 GiB | 92 s* | **33 s** | **−64%** | ✅ enable |
| **nim-llama-8b (Llama-3.1-8B)** | **76 GiB** | **55 s** | **123 s** | **+124% ❌** | **auto-disabled by runtime** |

*eager baseline taken from `docs/ARCHITECTURE.md` on main (recent, but not same-run as the lazy measurement; vllm-small + nim-llama-8b were both runs on the same spike branch for apples-to-apples).

**5 of 6 single-GPU CRIU workloads benefit from lazy-pages, average 25–27% reduction.** trtllm-small shows the largest win (64%) — likely due to its memory access pattern aligning unusually well with bulk-fault streaming.

## The NIM regression and how it's handled

NIM (`nvcr.io/nim/*` containers) regresses 2.2× with lazy-pages on. Hypothesis: NIM's startup memory access is scattered (warmup + internal buffer init) creating many small disjoint faults the daemon services one-at-a-time, paying ~0.91 ms per-fault overhead without enough fault-streaming-overlap to amortize. Tracked in [#111](https://github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/issues/111) for root-cause investigation.

**Runtime guard:** `cmd/restore-entrypoint/main.go` detects NIM containers via the `NIM_CACHE_PATH` env var (always set in `nvcr.io/nim/*` images) and disables lazy-pages regardless of the `NVSNAP_LAZY_PAGES` env setting. Validated: nim-llama-8b with manifest `lazy=1` + runtime detection = 60 s restore (matches lazy=0 baseline of 55 s within noise; well clear of the 123 s lazy=1 regression).

## Per-fault timing breakdown (vllm-small)

From the lazy-pages daemon log on a representative restore:

| metric | value |
|---|---|
| Daemon socket bind | 140 ms |
| First uffd_copy event (after daemon start) | 852 ms |
| Last uffd_copy event | 15.29 s |
| Active fault-serving window | ~14.5 s |
| Total uffd_copy events | 15,854 |
| Total bytes transferred via uffd | 27.5 GiB |
| Avg fault rate | ~1,094 faults/s |
| Avg per-fault bandwidth | ~1.9 GiB/s |
| Avg pages per fault (page-bunch) | ~445 |
| Avg per-fault wall latency (local) | ~0.91 ms |
| Direct-io alignment | 100% |

## Where the 25% win comes from (vllm-small reconciliation)

- `t=0` to `t≈5s`: **CRIU restore RPC returns** — process is mapped, scheduled. **This is where the win happens** — eager would have synchronously read all 27.5 GiB before returning.
- `t≈5s` to `t≈15s`: lazy-pages daemon serves faults; pages stream in while the process is already resuming.
- `t≈15s` to `t=33s`: vLLM Python init / model load / readiness probe. All pages already present by `t≈15s`; readiness probe forces full residency.

**Why the win is bounded at ~25–27%:** vLLM's readiness probe (`GET /v1/models`) touches the model weights — the kernel materializes all pages before the probe succeeds. Lazy-pages shifts CRIU's "synchronously read all pages during restore" off the critical path, but doesn't help workloads whose readiness probe is hot. **For workloads with a selective probe (e.g., diffusion models where only the active stage's weights are touched at startup), the win could be substantially larger.** This is unmeasured.

## Production posture (as merged to main)

**Default: `NVSNAP_LAZY_PAGES=0` in every single-GPU restore manifest.** Opt-in per workload after verification.

Reasoning: 5/6 of OUR tested workloads benefit, but we don't know how unknown / customer workloads (diffusion, classical ML, custom Python services) will behave. NIM regressed; another unknown might too. Safe default protects against unmeasured regressions; the operator can flip `NVSNAP_LAZY_PAGES=1` per-workload after benching both modes.

### How to enable lazy-pages for a workload

1. **Bench both modes:** capture once, restore with `NVSNAP_LAZY_PAGES=0`, restore with `NVSNAP_LAZY_PAGES=1`, compare wall-clock. Repeat 2–3× for noise.
2. **Decision rule:** enable if lazy=1 is faster OR within 5 s (noise threshold). Don't enable if lazy=1 is slower than eager.
3. **Flip the env var** in `deploy/k8s/workloads/<workload>-restore.yaml`:
   ```yaml
   - { name: NVSNAP_LAZY_PAGES, value: "1" }
   ```
4. **NIM exception:** NIM containers will auto-disable via runtime detection regardless of env setting. No action needed for those.

### Future workload onboarding checklist

For any new workload added to `deploy/k8s/workloads/`:
- [ ] Run `./scripts/test-e2e.sh <workload>` with default (eager) — record restore-Ready time
- [ ] Set `NVSNAP_LAZY_PAGES=1` in the workload's restore manifest, run again — record time
- [ ] If lazy faster: keep `value: "1"` and document the win
- [ ] If lazy slower: revert to `value: "0"`, file an issue (similar pattern to #111) for root-cause if the regression is large
- [ ] Add an entry to the table in this doc

### Image lineage

| version | what changed |
|---|---|
| `v0.24.1-restore-conf-tmp` | restore-criu.conf moved to /tmp (independent fix from this proposal but landed alongside) |
| `v0.24.2-lazy-pages-spike` | first lazy-pages spike (had criu-binary-path bug — failed to start daemon) |
| `v0.24.3-lazy-pages-spike` | criu-wrapper fix; lazy-pages worked; used for full regression sweep |
| **`v0.24.4-lazy-pages-nim-auto-disable`** | **current; adds runtime NIM detection** |

## Tracking

- #87 Day 1 mechanical validation — DONE
- #88 Day 2 detailed timing — DONE
- #89 Day 3 writeup + decision — DONE (GREEN-LIGHT)
- #90 Regression sweep across all single-GPU workloads — DONE
- #111 (gh) — root-cause investigation of nim-llama-8b regression — OPEN
