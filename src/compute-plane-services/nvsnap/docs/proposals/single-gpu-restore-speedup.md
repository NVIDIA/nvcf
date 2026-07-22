<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# Single-GPU restore speedup (target: -80%)

Status: Lever A is implemented, measured, and merged (see MEASURED below).
Lever B's combined "resume" invocation (restore+unlock in one cuda-checkpoint
spawn) is implemented and measured; eliminating get-restore-tid via a /proc scan
is remaining medium work, and per-pid context parallelization is deferred hard
work. Lever C is proposed, not implemented. The decision to build A+B and
defer C is locked (see Decision). Numbers are measured on aws-dev1 (driver
580.126, p5 H100, criu-v2 engine).

Timing convention: all top-line figures are agent-restore (the full
`POST /v1/restore` wall time). The CRIU-restore span is a subset of that
(40.3s of the 44.3s baseline); phase splits below are within the CRIU span.

## Measured baseline

Agent restore for a 3.2G single-GPU checkpoint (vllm-small) = 44.3s, of which
the `criu restore` span is 40.3s. From the `-v4` restore.log, that span splits
cleanly:

```text
0 ------------- 13.5s -------------- 22s ---------------- 38s --- 40s
|  CPU pages restore  | CUDA restore pid 53 | CUDA restore pid 453  |done
|      13.5s (33%)    |      ~8s            |      ~15s             |
+---------------------+---- cuda_plugin: ~27s (67%), SERIAL --------+
```

Two load-bearing facts:

1. The GPU work is 67%, split across TWO cuda contexts restored one-after-another
   (vllm-small spawns a second CUDA process even with multiprocessing off: the
   main server pid 53 + a torch/triton worker pid 453). They do not overlap.
2. The GPU cost is NOT the VRAM copy (3G host->device is ~0.1s at PCIe). It is
   CUDA context re-creation: cuda-checkpoint releases the context at checkpoint
   and rebuilds it at restore, re-loading every module/kernel (libtorch_cuda
   fatbins, triton kernels) into a fresh context. Inherent to how cuda-checkpoint
   works today.

Not the bottleneck: image IO (materialized in 4ms, local NVMe RAID), placeholder
prep (~4s), network (0).

## The three levers

### A. madvise huge pages (attacks the 13.5s CPU-pages phase) -- IMPLEMENTED

CRIU installs the process image as ~800k 4KB pages, single-threaded. dev1's host
THP policy is `madvise` = "grant huge pages to regions that ask via madvise."
We own the CRIU fork (io-uring-cr), so the restorer `madvise(MADV_HUGEPAGE)`s the
premapped private-VMA arena before populating it: 2MB pages, ~1500 faults
instead of ~800k. No host-wide THP change (which is off the table on the shared
cluster); works under the existing madvise policy.

```text
prepare_mappings(), after premap_priv_vmas():
  mmap the private-VMA arena             # as today
+ madvise(arena, used, MADV_HUGEPAGE)    # NEW: opt the arena into THP
  restore_priv_vma_content()             # faults now allocate 2MB pages
```

MEASURED (2026-07-19, criu fork 1ddd5c9c3, base v0.0.11 / app v0.2.21). Matched
agent-restore before(v0.2.20)/after(A) across single-GPU workloads:

```text
workload      checkpoint   before   after   delta
vllm-small    3.2G         44.3s    32.2s   -27%
vllm-8b       18G          58.9s    38.5s   -35%
e5-mistral    15G          84.0s    40.8s   -51%
vllm-qwen32b  64G          77.4s    48.5s   -37%
```

Every workload faster; the win grows with the CPU-side memory footprint.
Phase-level (vllm-small -v4 restore.log, within the CRIU span): CPU pages
13.5s -> 3.8s (-72%); cuda_plugin ~27s unchanged; CRIU restore total
40.3s -> 28.2s. Single-GPU regression 4/4 PASS (vllm-small, vllm-8b, e5-mistral,
vllm-qwen32b). No RSS bloat: AnonHugePages=0 in the FINAL restored process --
the huge pages exist only transiently in the staging arena during population
(where they cut the fault count), then split back to 4KB on the PIE remap to
final addresses. So we bank the restore-time win with zero lasting memory cost.

Prize is 33% of the CRIU span. Fully in our control. DONE.

Safety (must-have, implemented): madvise is a HINT, restore correctness never
depends on it. It is called best-effort with the return value ignored.
Degradation by node THP state:

- `madvise` (dev1): grants 2MB pages -> the speedup.
- `always`: redundant, harmless.
- `never`: silent no-op (returns 0), normal 4KB pages, no crash, no speedup.
- kernel without CONFIG_TRANSPARENT_HUGEPAGE (rare on distro kernels): madvise
  returns -EINVAL; we ignore it -> restore runs exactly as today.

Worst case is "no gain on that node", never "broken restore". Safe to ship to
arbitrary customer clusters.

### B. Coalesce cuda-checkpoint spawns to cut cuInit cost -- EASY HALF DONE

Each cuda-checkpoint invocation pays a flat ~2.7s cuInit driver-attach (measured:
6 successive --get-state calls on a running process, all ~2.7s, so it is driver
attach, not a wait). The plugin spawns cuda-checkpoint ~3x per pid on the restore
path (get-restore-tid, restore, unlock). Two collapses:

- DONE (easy, shipped): on the common restore path a process needs both a restore
  and an unlock; the plugin now issues them as one combined "resume" invocation
  (one cuInit instead of two). Same driver operations, same order, one fewer
  spawn per pid. Measured -4 to -6s per single-GPU workload; the win scales with
  the GPU pid count.
- NOT done (medium): eliminate the get-restore-tid spawn by scanning
  /proc/<pid>/task for the restore-thread name instead of spawning
  cuda-checkpoint --get-restore-tid. Riskier: a wrong tid signals the wrong
  thread.

The HARD half is truly parallelizing the per-pid context rebuilds. CRIU calls the
cuda_plugin resume hook one pid at a time as it restores the task tree serially,
and a UVM-ordering constraint ("the process must stay frozen or UVM pointer access
crashes") means parallelizing needs a CRIU-core change and risks a crash-on-restore.
Deferred: high risk, and much of the residual is the context rebuild's module/kernel
reload, an NVIDIA-side cost.

### C. Warm-standby: never release the context (attacks the ENTIRE 27s) -- OUT OF SCOPE

The 27s exists only because cuda-checkpoint tore the context down. For the
same-node fast-restart case (crash recovery, scale-from-warm), keep the dumped
GPU state resident so there is nothing to rebuild:

- Option C1 (frozen donor): after checkpoint, leave the source process frozen
  (cgroup freezer) with its CUDA context + VRAM intact instead of killing it.
  "Restore" = thaw + reattach. No CRIU image parse, no context rebuild.
- Option C2 (pinned host mirror): keep the dumped VRAM in pinned host RAM;
  restore = host->device memcpy into a pre-created context. Skips image parse;
  still pays one context create (cheaper than full rebuild if modules cached).

```text
cold (today):  disk image -> CRIU parse -> ctx REBUILD -> resume     ~44s
warm (C1):     frozen donor ------------- thaw + reattach            ~1-3s
```

Ceiling: agent restore 44s -> ~1-3s for same-node. The only lever that reaches
80%+, but same-node only (see Decision).
Cost: holds GPU memory + a frozen process per warm checkpoint (capacity tradeoff,
a standby pool, not free).

## Decision (locked 2026-07-19)

Target is CROSS-NODE cold restore. C (warm-standby) cannot cross nodes, so it is
OUT of scope for this effort (revisit later, same-node only). Build A + B.

Ceiling for A + B is agent restore ~44s -> ~20s (~55%), bounded by the CUDA
context rebuild. The residual gap to 80% is the module/kernel reload inside
cuda-checkpoint's context rebuild, an NVIDIA-side dependency; open a parallel
investigation (see open questions) rather than block A+B on it.

## Phasing

1. Instrument (planned): OTel spans for pages-restore vs per-pid cuda-restore so
   the split is a dashboard number, not a one-off log parse (extends #114-#116).
2. Lever A (madvise huge pages) in the CRIU fork -- DONE, merged, regression-gated.
3. Lever B (parallelize + de-latency the plugin) -- resume-coalesce (restore +
   unlock in one spawn) DONE and shipped; get-restore-tid /proc scan and per-pid
   context parallelization still remaining. Measure real GPU overlap first; keep
   the handshake-latency removal even if context creation serializes.
4. Lever C only if the target is same-node -- deferred; larger, touches agent
   lifecycle (donor freeze/thaw, standby accounting) and NVCA (warm-pool
   semantics).

## Open questions

- Does the pid 453 (torch/triton worker) context rebuild dominate because of
  triton kernel reload? If so, a triton cache warm in the placeholder could cut
  it independent of A/B/C.
- Real GPU-side overlap of two concurrent context creations on one H100
  (measure before B).
- Warm-standby capacity model: how many frozen donors per node is acceptable
  (GPU-mem bound), and does NVCA drive the pool.
