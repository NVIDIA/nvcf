# NVSNAP Optimization Roadmap by Workload Class

**Status:** strategy doc
**Branch:** `spike/fast-data-movement`
**Date:** 2026-05-16
**Driver:** Single-GPU (diffusion, small LLMs) and multi-GPU (vLLM 70B+, NIM) are both real workload classes with different bottlenecks. This doc captures the two-track view, the cross-cutting work that benefits both, and the suggested ordering.

---

## TL;DR

| metric | single-GPU CRIU path | multi-GPU rootfs path |
|---|---|---|
| who uses it | diffusion (SDXL, Flux), small LLMs, classical ML | vLLM, NIM, anything TP≥2 |
| restore mechanism | CRIU restore + cuda-checkpoint | container starts fresh, reloads cached files |
| baseline single-pod restore | 42 s (vllm-small) / similar order for diffusion | 233 s (vllm-70b TP=4) |
| where the bytes are | mostly CPU pages (~30 GiB CRIU dump; GPU small) | mostly weights on disk (~133 GiB rootfs cache) |
| where the time goes | CRIU + cuda-checkpoint restore | Python imports + weight load + NCCL + torch.compile |
| single-pod-restore lever | **CRIU lazy-pages** (proven 26% win Day 1) | **warm pool / serving-platform pattern** (target ~95%) |
| fan-out lever (cold cluster → N pods) | parallel cascade + locality + pre-stage | parallel cascade + locality + pre-stage + EROFS shared mount |

Both paths share **cross-cutting infrastructure** for fetch/scheduling/staging that should be built first because the work benefits both.

---

## Single-GPU CRIU path (diffusion + small LLMs)

CRIU + cuda-checkpoint is the established restore path. cuda-checkpoint is non-negotiable (the user cannot restore CUDA state without it; replacement attempts like WrapCore did not ship).

### What we have

- **CRIU lazy-pages — Day 1 spike PASSED.** Env-gated `NVSNAP_LAZY_PAGES=1` in restore-entrypoint spawns `criu lazy-pages -D <dump>` daemon, sets `CriuOpts.LazyPages=true`. Measured on vllm-small: 32 s ready vs 43 s baseline — **26% reduction.** All 6.89 M pages (28.2 GiB) transferred via userfaultfd. CUDA plugin compatible. See `FAST-RESTORE-PROPOSAL.md`. Image: `nvsnap-agent:v0.24.3-lazy-pages-spike`.

### What's open

- **Days 2–3 of the userfaultfd plan:** per-fault latency measurement, remote page-server cost, `--page-bunch` knob tuning, integration cleanup (CRD knob instead of env-gated spike code). ~1 week.
- **Diffusion-specific measurement.** The 26% win on vllm-small came from a workload where the readiness probe touches all model weights, forcing all pages to materialize. Diffusion workloads have different memory access patterns (per-stage residency); the lazy-pages win could be larger. Worth measuring.

### What's not feasible

- **Replacing cuda-checkpoint.** WrapCore-class CUDA state shadow is multi-month greenfield work that did not ship before; we're not pursuing this near-term.
- **GPU-side lazy paging.** cuda-checkpoint owns GPU memory eagerly; userfaultfd doesn't extend to GPU memory.
- **Warm pool / diff-from-base on the CRIU path.** Without cuda-checkpoint replacement, we cannot inject base GPU bytes from a pool.

The ceiling for single-pod single-GPU CRIU restore optimization is roughly what lazy-pages gives: **~25–35% wall-clock reduction.** Anything beyond that requires architectural changes to cuda-checkpoint which is NVIDIA-owned and was abandoned previously.

---

## Multi-GPU rootfs path (vLLM 70B, NIM, TP≥2)

cuda-checkpoint doesn't support multi-GPU (`distinctGPUs >= 2` returns "multi-GPU CRIU is unsupported"). We use the **rootfs-only path**: capture the container's overlay diff (HF cache + vLLM compile cache), restore = re-launch container which reloads from cached files. **No CUDA state is restored** — it's a cold start with pre-staged files.

### What we have

- Working rootfs capture + restore via the agent's `rootfsonly.Watcher` and `nvsnap.io/restore-from` annotation. T8 validated vllm-70b TP=4 end-to-end: 233 s baseline.
- Phase 5d cascade fetch (Tier 1 same-node / Tier 2 peer / Tier 3 blobstore) for cross-node data movement.

### What's open — two distinct scenarios

**Scenario A: single-pod restore (sustained serving)** — customer running 70B, traffic spike or rolling restart, want quick spin-up.
- **Warm pool serving platform** is the right architectural pattern. Pre-loaded vLLM instances on reserved nodes; restore = route to a matching instance + apply customer config / LoRA / KV cache delta. Target ~95% wall-clock reduction (233 s → seconds). **3–6 months** of work; mostly Kubernetes + Go controller + small per-model-family diff extractor.
- **GDS bulk transfer** (NVMe → GPU direct) addresses the 30 s host→GPU stage. Hardware-gated to A3-Ultra-class. 2–4 weeks if hardware available.

**Scenario B: cold-cluster scale-out (bursty demand)** — empty cluster + one checkpoint, scale to N pods. Bottleneck is data fan-out, not compute bringup. See `SCALE-OUT-DESIGN.md` for the math (13.3 TiB / 100 nodes → ~3 hours naive vs ~110 s torrent-style).
- Cross-cutting wins below dominate this scenario.
- **EROFS-as-shared-mount** finally wins here: single artifact per node, mounted once per node, shared RO across pods, tmpfs overlay for writable scratch. The "multi-receiver fanout" revisit trigger from `EROFS-VALIDATION.md` fires.

---

## Cross-cutting infrastructure (helps both paths)

These are the levers worth building **first** because the work benefits both CRIU single-GPU restores and rootfs multi-GPU restores AND scale-out fan-out.

### Multi-source parallel cascade fetch

Spike 3 measured: single-stream upload caps at ~1.4 Gbit/s on a wire that does 36 Gbit/s. 8-way parallel: 10 Gbit/s. Same shape applies to GET / cascade fetch on the receiver side.

Today's `cascade_fetch.go` fetches sequentially from the first peer found. **Change** to chunk-parallel multi-source pull. Benefits:
- Faster cross-node restores in the single-pod CRIU case (lazy-pages over network gets faster too)
- Massive at-scale benefit for fan-out (receiver bound by collective bandwidth, not single peer)
- Symmetric improvement to PUT path on the capture side

**Top priority. ~1–2 weeks engineering. Helps every workload class.**

### Data-locality-aware pod placement

When a pod has `nvsnap.io/restore-from: <hash>`, mutating webhook adds a soft nodeAffinity for nodes that already have the hash (or are 1 hop from one).

nvsnap-server already tracks `{hash → [nodes with it]}` via the catalog (`internal/server/sources.go`). Just need the webhook side to consume it.

Benefits both paths: a CRIU dump that happens to live on node X gets restored on X (Tier-1 short-circuit), no cross-node transfer. A rootfs capture on node X with 2 pods wanting it gets both placed on X (one local copy, two pods). **~1 week.**

### Pre-staging to seed nodes

When a checkpoint is captured (or first served), opportunistically replicate to N seed nodes in the background (e.g., 10% of the GPU pool). Doesn't block capture; just provides more sources when a fan-out wave hits.

**~2 weeks.** Benefits both paths but the impact is larger for scale-out scenarios.

---

## Suggested ordering

The user's call: **finish single-GPU work first, then move to multi-GPU.** Combined with cross-cutting wins that help both, the order:

| weeks | work | path benefited | status |
|---|---|---|---|
| **W0 (done)** | CRIU lazy-pages Day 1 — env-gated spike | single-GPU CRIU | **shipped to spike branch** (commit `4c7e718`) |
| **W1** | CRIU lazy-pages Days 2–3 — measurement + integration | single-GPU CRIU | next up |
| **W2–3** | Multi-source parallel cascade fetch | **both** | cross-cutting #1 |
| **W3** | Data-locality scheduling in webhook | **both** | cross-cutting #2 |
| **W4–5** | Pre-staging service | **both** | cross-cutting #3 |
| **W5–7** | EROFS-as-shared-mount for fan-out | multi-GPU rootfs | scale-out specific |
| **W7+** | Warm pool serving platform (MVP) | multi-GPU rootfs | sustained-serving specific |

The first 5 weeks deliver real, measurable wins across both workload classes without committing to any heavy multi-GPU-specific architecture. After that, EROFS-fan-out and warm-pool-serving are independent investments that can be parallelized or sequenced based on customer access patterns:
- **Bursty cold-start traffic** → EROFS-fan-out wins
- **Sustained serving traffic** → warm-pool wins
- Both are buildable; not mutually exclusive

---

## What's not on this roadmap (explicitly)

- **WrapCore / CUDA interposition / cuda-checkpoint replacement.** Abandoned previously; not pursuing.
- **GPUDirect lazy paging on UVM.** Depends on NVIDIA proprietary driver internals; not pursuing.
- **EROFS as a primary single-pod CRIU artifact.** EROFS-VALIDATION.md's "don't ship as primary" conclusion still stands for single-pod. EROFS only revives for the multi-receiver fan-out case.

---

## Related docs

- `docs/spikes/SPIKE-RESULTS.md` — Spikes 1–4 measurements (tar+zstd, EROFS, throughput, parallel pack)
- `docs/spikes/EROFS-VALIDATION.md` — T1–T8 EROFS-as-restore-source validation + revisit triggers
- `docs/spikes/FAST-RESTORE-PROPOSAL.md` — Single-pod fast-restore architecture + 3-day userfaultfd plan
- `docs/spikes/SCALE-OUT-DESIGN.md` — 200-pod cold-cluster fan-out architecture
- `docs/FAST-DATA-MOVEMENT-IDEAS.md` — original design exploration that preceded the spikes
