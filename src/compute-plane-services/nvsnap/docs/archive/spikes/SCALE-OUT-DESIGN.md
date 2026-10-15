# Scaling Out to N Pods From a Cold Cluster

**Status:** design doc, not yet implemented
**Branch:** `spike/fast-data-movement`
**Date:** 2026-05-16
**Driver:** "Empty cluster + one multi-GPU checkpoint, scale to 200 pods quickly" is a fundamentally different problem than "make a single restore faster." The bottleneck is data fan-out, not state-restore latency. This doc captures the architecture for that case.

---

## The scenario

- Cluster has ~100 GPU nodes (8× H100 each) and no running workloads
- One multi-GPU checkpoint: vllm-70b TP=4, rootfs-path, **133 GiB**
- Goal: bring up 200 pods serving Llama-3.1-70B in minutes, not hours
- Implicit: this is a real ops scenario — bursty demand, blue-green deployments, regional fan-out

200 pods × TP=4 = 800 GPUs ≈ 100 nodes ≈ 2 pods/node.

## Why this isn't the single-pod restore problem

The 233 s single-pod-restore breakdown we measured (vllm-70b T8) has these stages:

| stage | per-pod time | parallelizable across pods? |
|---|---|---|
| pod sandbox + image pull | 5 s | yes (per-node bound) |
| Python imports + vLLM init | 30 s | yes (per-pod CPU) |
| weights → CPU memory (NVMe read) | 25 s | partly (per-node NVMe) |
| CPU → GPU PCIe DMA (4 GPUs) | 30 s | yes (per-pod) |
| NCCL TP=4 rendezvous | 25 s | yes (intra-pod) |
| torch.compile + CUDA graph capture | 60 s | yes (per-pod CPU+GPU) |
| readiness stabilization | 5 s | yes |

**The application bringup parallelizes well across pods.** The bottleneck for 200-pod scale is **getting the 133 GiB checkpoint onto every node**.

## The data movement math

Naive single-blobstore pull at the Spike 3 measured ceiling:

- Blobstore aggregate throughput: **~10 Gbit/s** (NVMesh-specific; gp3/Hyperdisk will vary)
- 100 nodes × 133 GiB = **13.3 TiB total**
- Wall-clock: 13.3 TiB / 10 Gbit/s ≈ **~3 hours**

That's the floor if every node pulls independently from the central store. Unacceptable.

The realistic ceiling, with torrent-style fan-out and ~36 Gbit/s pod-to-pod TCP (Spike 3):

- Each node has 36 Gbit/s effective uplink to peers
- 100 nodes participating in swarm → ~125 GiB/s collective serving capacity once seeded
- 13.3 TiB / 125 GiB/s ≈ **~110 s**

**The gap is two orders of magnitude.** Closing it requires fan-out via peers, not central source.

## What the current architecture (Phase 5d) does for fan-out

`internal/agent/cascade_fetch.go` — `EnsureLocal` is tiered:

1. **Tier 1** — same-node cache: short-circuits if `inventory.img` or capture files exist
2. **Tier 2** — peer HTTP fetch from another agent that has the data
3. **Tier 3** — `nvsnap-blobstore` fallback

So once one node has the artifact, peers can pull from it. Conceptually right. **But the implementation has limitations at the 100-node scale:**

### Limitations measured / observed

- **Sequential file pulls per fetch.** Each cascade fetch does roughly one HTTP request per file. With 5,710 files × 100 nodes = ~570K HTTP requests just to fan out. Per-request overhead is small but non-zero — at scale, latency adds up.
- **Single-stream cap per peer.** Spike 3 showed single-stream HTTP caps at ~1.4 Gbit/s. Even with two peers serving, a node pulling sequentially gets ~2.8 Gbit/s — far below the 10 Gbit/s storage ceiling and the 36 Gbit/s wire ceiling.
- **First-peer-found bias.** When tier-2 finds a peer with the data, it pulls from that single peer. No multi-source pull. Doesn't form a true swarm.
- **Random pod placement.** Today's scheduler doesn't know that 2 pods on the same node could share one local copy of the 133 GiB. They might be scheduled on different nodes, doubling the transfer.
- **No anticipatory seeding.** When a fan-out wave hits, only one node has the data (the original capture node). The cascade is bottlenecked at that single seed until enough nodes have copies to peer-serve.

## Proposed priorities

In order of impact-per-engineering-week:

### Priority 1 — Multi-source parallel cascade fetch

**Change:** instead of fetch-from-first-peer-found, fetch chunks from multiple peers + blobstore in parallel.

- Spike 3 measured: 8-way parallel PUT gives ~7× speedup over single-stream. Symmetric on GET.
- For the fan-out swarm, this also means a receiver can saturate its inbound bandwidth across multiple sources rather than being bottlenecked by any single peer's outbound.
- Mechanism: chunk per-file pulls (sharded by sha or byte range); fan requests across the live peer set + blobstore.

**Expected impact:** receiver-side throughput from 1.4 Gbit/s to 8–10 Gbit/s per node. At 100 nodes the swarm aggregate sees the 36 Gbit/s pod-to-pod ceiling, not the blobstore 10 Gbit/s ceiling. **Single biggest lever.**

**Engineering:** ~1–2 weeks. Extend `cascade_fetch.go`. Tests on a 5–10 node mini-fanout. No new artifact format.

### Priority 2 — Data-locality-aware pod placement

**Change:** webhook injects nodeAffinity prefs based on which nodes already have the requested hash.

- Mechanism: `nvsnap-server` maintains a catalog of `{hash → [nodes with it]}` (already has the pieces — see `internal/server/sources.go`, `internal/checkpointstore/cmregistry.go`).
- At pod admission, the mutating webhook adds a soft nodeAffinity for nodes that have the hash locally, or 1 hop away (peers known to share fast).
- Falls through to any-GPU-node if no match.

**Expected impact:** for the 200-pod / 100-node case, 2 pods/node means ~50% of pulls can be avoided entirely (the second pod on a node hits Tier 1 — local cache). Roughly halves the 13.3 TiB → ~6.6 TiB to move.

**Engineering:** ~1 week. Adds annotation read in webhook, query to `nvsnap-server`, soft-affinity injection.

### Priority 3 — Pre-staging to seed nodes

**Change:** when a checkpoint is captured (or first served), opportunistically replicate to N seed nodes in the background.

- N = configurable, say 10% of the GPU pool (10 nodes here).
- Don't block on it; capture path returns when one copy exists; replication runs as background goroutine.
- When a fan-out wave hits, there are 10 seeds instead of 1 → torrent swarm bootstraps much faster.

**Expected impact:** for a cold cluster start, the swarm reaches saturation in `log2(100/10) = 3.3` hops instead of `log2(100/1) = 6.6`. Halves the fan-out time once Priority 1 is in place.

**Engineering:** ~2 weeks. Background replication goroutine in nvsnap-server, target-node selection (prefer empty GPU nodes), backoff on the source.

### Priority 4 — Per-node shared mount (EROFS revisit, properly framed)

**Change:** ship the rootfs capture as a single EROFS artifact per node, mount once, share read-only across all pods on that node. Each pod adds a tmpfs overlay for its writable scratch (`/root/.cache/vllm` compile artifacts that grow at runtime).

This is the EROFS revisit trigger we captured in `EROFS-VALIDATION.md`:

> A multi-receiver fanout pattern (one source → N target nodes). EROFS's instant mount is per-receiver; the pack cost amortizes across N restores. If N gets large, the calculus shifts. We didn't measure this scenario.

In the 200-pod scenario, the math:

| | per-file CAS | EROFS-as-shared-mount |
|---|---|---|
| files per node transfer | 5,710 | 1 |
| HTTP requests per fan-out | ~570K | ~100 |
| atomicity | partial (per-file) | full (per-artifact) |
| pack cost | 0 (no pack needed) | 14 min (one-time, capture-side) |
| compression | n/a | 1.01× on safetensors (no savings) |
| **multi-pod-per-node sharing** | each pod has own copy | **one mount serves N pods** |

The pack cost (14 min) is one-time at capture and is **already async per the user's earlier accept**. The fan-out benefit at scale is real:
- Atomic transfer means cascade can verify completion with one hash check, not 5,710
- Single artifact is easier to schedule, evict, and pin
- N pods on one node literally share the same `mmap`'d backing — no duplication

The pod-spec change: rootfs restore manifest gets an `initContainer` that loop-mounts the EROFS, and an `emptyDir` for the writable overlay. The main container's `args` mount the overlay union as `/root/.cache/...`.

**Expected impact:**
- Cascade simplification (single-file transfers) cuts coordination overhead
- 2 pods/node sharing one mount halves per-node disk footprint
- vs. Priority 1+2 alone, this is incremental — but it's also the cleanest operational story

**Engineering:** ~3 weeks. Includes EROFS userland (already built — `mkfs.erofs 1.8.4` with our `--workers` patch), capture-side optional pack stage, restore-side pod-spec changes, overlay handling.

**Caveat:** still 1.01× compression on rootfs; this is about operational simplicity at fan-out scale, not bytes-on-wire.

## What this means for the overall fast-restore proposal

The `FAST-RESTORE-PROPOSAL.md` doc focuses on single-pod restore acceleration. That doc is still valid for the single-pod case (long-tail single-GPU CRIU + the user's bursty individual restores). But for **scale-out from cold**, this is a meaningfully different program:

| problem | single-pod fast restore | 200-pod scale-out |
|---|---|---|
| bottleneck | application bringup (Python, NCCL, compile) | data fan-out (13.3 TiB) |
| dominant fix | warm pool / lazy-pages / GDS bulk | parallel cascade + locality + EROFS |
| metric | seconds to ready per pod | total wall-clock to 200 ready |
| state | research/spike | engineering (mostly known levers) |

The two programs share infrastructure (cascade fetch, nvsnap-blobstore, peer agents) but pursue different optimization axes.

## Concrete next steps

If you want to pursue this scale-out scenario:

1. **Build a 10-node fan-out test** — current cluster has 3 GPU nodes. To validate the priority-1 multi-source cascade, we'd need ~10+ nodes (or simulate with reduced-size artifacts on the 3 GPU + 3 CPU nodes). Cheap experiment: scaled-down 13 GiB artifact + 6 receiver pods.
2. **Measure today's baseline** — fan-out 13 GiB to 6 nodes via current cascade. Establish baseline wall-clock.
3. **Implement Priority 1** (multi-source parallel cascade), re-measure.
4. **Implement Priority 2** (locality scheduling), re-measure.
5. Decision point: are Priorities 3 and 4 needed, given the win from 1+2?

Total: ~3–4 weeks for a measurable scale-out improvement. The work is mostly cascade-fetch refactoring and webhook augmentation — known territory, no new artifact format required for the first two priorities.

## Open questions

1. **What's the real fan-out distribution?** 200 pods all in one wave, or staggered over minutes? Staggered means natural cascade with less coordination — even today's architecture might handle it.

2. **What's the typical model diversity?** If 5 different base models share the 200-pod fleet, each base has its own fan-out fight. Fewer seeds per model. Worth measuring.

3. **Are we IO-bound on the seeds?** Spike 3 showed parallel-PUT caps at ~10 Gbit/s on NVMesh storage. Per-node uplink is 36 Gbit/s. So the seed node may be storage-bound serving, not network-bound. Fan-out from M seeds avoids this.

4. **Image pull at scale.** 200 pods × ~10 GiB vllm-openai image. With containerd's content store caching per-node, second-pod-on-node is instant. But fresh image pull on 100 nodes from one registry could rival the checkpoint fan-out. **Maybe a P2P container image distributor (Spegel / Dragonfly) is the parallel investment.**

5. **GPU thermal / power budget at fan-out start.** 800 H100s simultaneously loading a 70B model + initializing NCCL is a coordinated power spike. Probably handled by hardware/PMU but worth understanding.

## Related docs

- `docs/spikes/SPIKE-RESULTS.md` — Spike 3 measurements that motivate Priority 1 (parallel-PUT 7× win, symmetric on GET)
- `docs/spikes/EROFS-VALIDATION.md` — EROFS results; this doc fires the "multi-receiver fanout" revisit trigger from that doc
- `docs/spikes/FAST-RESTORE-PROPOSAL.md` — single-pod restore architecture (complementary, not competing)
- `internal/agent/cascade_fetch.go` — current Phase 5d implementation
- `internal/agent/capture_peer.go`, `internal/checkpointstore/cmregistry.go` — peer + catalog plumbing this design extends
