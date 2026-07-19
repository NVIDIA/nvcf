<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# Single-GPU restore speedup (target: -80%)

Status: design sketch, pre-implementation. Numbers are measured on aws-dev1
(agent v0.2.20, driver 580.126, p5 H100, criu-v2 engine), vllm-small 3.2G.

## Measured baseline

`criu restore` (in-namespace) for a 3.2G single-GPU checkpoint = ~44s. From the
`-v4` restore.log, the span splits cleanly:

```
0 ───────────── 13.5s ───────────── 22s ──────────────── 38s ─── 40s
│  CPU pages restore  │ CUDA restore pid 53 │ CUDA restore pid 453  │done
│      13.5s (33%)    │      ~8s            │      ~15s             │
└─────────────────────┴──── cuda_plugin: ~27s (67%), SERIAL ───────┘
```

Two load-bearing facts:

1. The GPU work is 67%, split across TWO cuda contexts restored one-after-another
   (vllm-small spawns a second CUDA process even with multiprocessing off: the
   main server pid 53 + a torch/triton worker pid 453). They do not overlap.
2. The GPU cost is NOT the VRAM copy (3G host->device is ~0.1s at PCIe). It is
   CUDA context *re-creation*: cuda-checkpoint releases the context at
   checkpoint and rebuilds it at restore, re-loading every module/kernel
   (libtorch_cuda fatbins, triton kernels) into a fresh context. Inherent to how
   cuda-checkpoint works today.

Not the bottleneck: image IO (materialized in 4ms, local NVMe RAID), placeholder
prep (~4s), network (0).

## The three levers

### A. madvise huge pages (attacks the 13.5s CPU-pages phase)

CRIU installs the process image as ~800k 4KB pages, single-threaded. dev1's host
THP policy is `[madvise]` = "grant huge pages to regions that ask via madvise."
We own the CRIU fork (io-uring-cr), so the restorer can `madvise(MADV_HUGEPAGE)`
its anonymous restore mappings before populating them -> 2MB pages, ~1500 faults
instead of ~800k. No host-wide THP change (which is off the table on the shared
cluster), works under the existing madvise policy.

```
restorer, per anon VMA:
  mmap(addr, len, ...MAP_ANONYMOUS)      # as today
+ madvise(addr, len, MADV_HUGEPAGE)      # NEW: opt this region into THP
  <populate pages from image>            # now backed by 2MB pages
```

MEASURED (2026-07-19, criu fork 1ddd5c9c3, base v0.0.11 / app v0.2.21):
vllm-small pages phase 13.5s -> 3.8s (-72%); CRIU restore 40.3s -> 28.2s
(-30%); agent restore 44.3s -> 32.2s (-27%). Single-GPU regression 5/5 PASS
(vllm-small, vllm-8b 38.5s, e5-mistral 40.8s, vllm-qwen32b 64G 48.5s). No RSS
bloat: AnonHugePages=0 in the FINAL restored process -- the huge pages exist
only transiently in the staging arena during population (where they cut the
fault count), then split back to 4KB on the PIE remap to final addresses. So
we bank the restore-time win with zero lasting memory cost.

Ceiling: 13.5s -> ~4s. Prize is 33% of total. Fully in our control. DONE.

Safety (must-have): madvise is a HINT, restore correctness never depends on it.
Call it best-effort and IGNORE the return value. Degradation by node THP state:
  - `madvise` (dev1): grants 2MB pages -> the speedup.
  - `always`: redundant, harmless.
  - `never`: silent no-op (returns 0), normal 4KB pages, no crash, no speedup.
  - kernel without CONFIG_TRANSPARENT_HUGEPAGE (rare on distro kernels):
    madvise returns -EINVAL; we ignore it -> restore runs exactly as today.
Worst case is "no gain on that node", never "broken restore". Safe to ship to
arbitrary customer clusters.
Risk (perf/mem only, not correctness): THP collapse latency / memory bloat on
sparsely-populated regions; gate per-VMA on size (only VMAs >= a few MB).

### B. Parallelize the per-process CUDA restore (attacks the 27s serial phase)

cuda_plugin restores GPU processes sequentially: pid 53 (13.5->22s) then pid 453
(22->38s). On one physical GPU the context rebuilds may not fully overlap, but
the ~3s per-proc "find restore thread" handshake gaps (spawn cuda-checkpoint +
cuInit + query) are pure latency that can overlap. We already parallelized
cuda-checkpoint loops in the legacy restore.go (#216); this is the criu-v2
equivalent inside the plugin.

```
today:   [ctx rebuild 53][gap][ctx rebuild 453][gap]      ~27s
target:  [ctx rebuild 53          ]
         [gap][ctx rebuild 453     ]  (overlapped)         ~15s
```

Ceiling: 27s -> ~15s (bounded by the single slowest context + GPU serialization
of concurrent context creation). Prize is part of 67%.
Risk: two contexts building on one GPU may serialize in the driver anyway;
measure the real overlap before committing. Handshake-latency removal is the
safe portion.

### C. Warm-standby: never release the context (attacks the ENTIRE 27s)

The 27s exists only because cuda-checkpoint tore the context down. For the
same-node fast-restart case (crash recovery, scale-from-warm), keep the dumped
GPU state resident so there is nothing to rebuild:

- Option C1 (frozen donor): after checkpoint, leave the source process frozen
  (cgroup freezer) with its CUDA context + VRAM intact instead of killing it.
  "Restore" = thaw + reattach. No CRIU image parse, no context rebuild.
- Option C2 (pinned host mirror): keep the dumped VRAM in pinned host RAM;
  restore = host->device memcpy into a pre-created context. Skips image parse;
  still pays one context create (cheaper than full rebuild if modules cached).

```
cold (today):  disk image -> CRIU parse -> ctx REBUILD -> resume     ~44s
warm (C1):     frozen donor --------------- thaw + reattach          ~1-3s
```

Ceiling: 44s -> ~1-3s for same-node. This is the only lever that reaches 80%+.
Cost: holds GPU memory + a frozen process per warm checkpoint (capacity tradeoff
-- a standby pool, not free). Scope: same-node only; cross-node still cold.

## Decision (locked 2026-07-19)

Target is CROSS-NODE cold restore. C (warm-standby) cannot cross nodes -> OUT of
scope for this effort (revisit later, same-node only). Build A + B.

Ceiling for A + B is ~44s -> ~19s (~57%), bounded by the CUDA context rebuild.
The residual gap to 80% is the module/kernel reload inside cuda-checkpoint's
context rebuild -- an NVIDIA-side dependency; open a parallel investigation (see
open questions) rather than block A+B on it.

## Proposed phasing (once case is chosen)

1. Instrument: add OTel spans for pages-restore vs per-pid cuda-restore so the
   split is a dashboard number, not a one-off log parse. (extends #114-#116)
2. Lever A (madvise huge pages) in the CRIU fork -- smallest, self-contained,
   fork-only, measurable in isolation. Regression-gate on the single-GPU suite.
3. Lever B (parallelize + de-latency the plugin) -- measure real GPU overlap
   first; keep the handshake-latency removal even if context creation serializes.
4. Lever C only if the target is same-node -- larger, touches agent lifecycle
   (donor freeze/thaw, standby accounting) and NVCA (warm-pool semantics).

## Open questions

- Does the pid 453 (torch/triton worker) context rebuild dominate because of
  triton kernel reload? If so, a triton cache warm in the placeholder could cut
  it independent of A/B/C.
- Real GPU-side overlap of two concurrent context creations on one H100
  (measure before B).
- Warm-standby capacity model: how many frozen donors per node is acceptable
  (GPU-mem bound), and does NVCA drive the pool.
