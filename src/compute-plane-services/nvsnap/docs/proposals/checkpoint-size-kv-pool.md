<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# Stop checkpointing bytes that mean nothing

Status: measured, plan proposed. Nothing here is implemented yet.

Most of a criu-v2 checkpoint is an empty KV cache pool. We write it to disk,
read it back, and push it to the GPU, and none of it means anything to a
restored worker. This document records the measurement, what it implies for the
restore-time roadmap in `single-gpu-restore-speedup.md`, and the order to
attack it in.

## Measured

dev2, RTX PRO 6000 Blackwell (96G), driver 580.126, agent v0.2.62.
Workload `vllm-small` = TinyLlama-1.1B, about 2.2G of weights. Only
`--gpu-memory-utilization` changed between the two runs.

```text
                        0.3            0.05         delta
KV budget               ~28.8G         ~4.8G        -24.0G
checkpoint size         33G            8.4G         -75%
checkpoint time         1m10s          0m40s        -43%
restore pod ready       50s            23s          -54%
post-restore inference  OK             OK
```

The budget delta and the checkpoint delta agree to within 3 percent: 24.0G of
KV budget removed, 24.6G of checkpoint removed. The untouched pool is being
dumped byte for byte. `du` reports real blocks, so the zeros are genuinely on
disk.

## Why it happens

1. `gpu_memory_utilization` is a fraction of the whole card, not of what the
   model needs. A 1.1B model on a 96G card reserves about 29G regardless. This
   gets worse as cards get bigger, and it is the default behaviour of the knob
   rather than a misconfiguration on our side.
2. The checkpoint copies each device allocation in full, whether or not it was
   ever written. Our measurement shows this directly: 24.0G of KV budget
   removed produced 24.6G less checkpoint, a byte-for-byte correspondence that
   only holds if untouched memory is copied verbatim. There is no
   touched-page filtering to opt into, so the device-to-host copy cannot be
   avoided from outside the process.
3. The CUDA memory image is written outside CRIU's pagemap. CRIU has zero-page
   and sparse handling; the CUDA image never reaches it. That is the specific
   reason 29G of zeros lands on disk verbatim.

Point 3 is the actionable one: it localises the fix to a code path we already
fork, and it means the fix is not a general CRIU change.

## Step 0: explain the historical 3.2G before building anything

`docs/BENCHMARK.md` records `vllm-small` at a 3.2G checkpoint. Today the same
manifest produces 33G. `--gpu-memory-utilization 0.3` has never changed in git
history, so this is not a config drift.

The reported difference is the checkpoint binary: the 3.2G figure was taken with
`nvsnap-cuda-checkpoint`, not the stock tool. Reading
`docker/agent/nvsnap-cuda-checkpoint.c` does not yet explain a 10x: it is a
drop-in on the same driver API (`cuCheckpointProcess*`) whose only functional
addition is the coalesced `resume` action. Nothing in it filters memory.

Two candidates have since been ruled out:

- The binary. `Dockerfile.base` still compiles `nvsnap-cuda-checkpoint.c` in
  its `cuda-cli-builder` stage and the wrapper execs it as
  `cuda-checkpoint.real`. The 33G was produced by the same tool as the 3.2G.
  Confirm separately whether a switch to the official binary for 610-driver
  support landed anywhere outside this repo's history.
- Touched-page filtering. Since untouched memory is copied verbatim (see above),
  a 24G pool would have been dumped on the older setup too if it had existed
  there.

That leaves the size of the pool itself as the likely variable. The older runs
were on H100 nodes whose MIG strategy is `mixed`. If those pods received a MIG
slice rather than a whole card, `0.3` of a roughly 10G slice is about 3G, which
lands on the recorded 2.1G and 3.2G once the model's own weights are counted.
Unconfirmed: that cluster no longer has the agent installed.

This gates the plan's urgency. If the 33G is an artifact of testing a small
model on a whole 96G card, the production picture may be much better than these
numbers suggest, and the work below should be sized accordingly. Resolve by
capturing the same workload on a whole card and on a MIG slice and comparing.

## Two conclusions that reorder the roadmap

1. The bulk path is not bandwidth bound. Removing 24.6G saved 27s, which is
   about 0.91 GB/s for the bytes we stopped moving. PCIe 5 on this card is well
   over 50 GB/s. Whatever the constraint is, it is upstream of the transfer, so
   optimizations that make the transfer faster (GPUDirect Storage, faster host
   to device) will do very little until it is found. Note this appears to
   contradict `single-gpu-restore-speedup.md`, which measured image IO as not
   the bottleneck on a 3.2G checkpoint; both can be true if the cost is
   superlinear in image size. Reconcile as part of step 0.
2. Weight decoupling in the style of NVIDIA's GMS is aimed at a different
   problem than ours. It targets deployments where weights dominate. On
   `vllm-small` it would move 2.2G of a 33G problem. It stays interesting for
   large models and is not the first thing to build.

## Constraint

NVCF runs arbitrary tenant containers, so the primary mechanism has to work
without the workload's cooperation. Per-engine knobs are accelerators where
available, never the strategy.

## Tier 1: works on any container, no cooperation

### A. Elide zero pages in the CUDA memory image

Do not try to identify the KV cache. Identify zero pages. Every inference
server preallocates an arena it has not populated, so targeting zeros is
framework blind, degrades to a no-op when there is nothing to find, and is
semantically exact rather than a heuristic.

Elide contents only, never the mapping. The allocation must be recreated with
the same virtual address, size and access flags, then filled device-side with a
memset instead of a host-to-device copy from file. Getting that distinction
wrong presents as memory corruption.

Two implementations, cheapest first:

- agent-side hole punching on the finished image. No CRIU change. Reclaims disk
  and read time, not the transfer.
- plugin-side skip-write in the CUDA plugin. Better, and upstreamable to CRIU.

### B. Overlap the CUDA image restore with CRIU host-page restore

These consume different resources (PCIe versus CPU and memory bandwidth) and
today they run one after the other. The CUDA memory image is already a discrete
artifact written outside CRIU's pagemap, so the separation that NVIDIA needed
GMS to create already exists for us. This is sequencing in our own restore
path, not a new mechanism.

## Tier 2: where we control the launch

The set of containers with a large preallocated KV arena is enumerable. Where
we own the launch or can inject environment, use the engine's own release path,
which is what Dynamo does (`components/src/dynamo/{vllm,sglang,trtllm}/snapshot.py`).

```text
engine    runtime release   how
vLLM      yes               enable_sleep_mode at construction; /sleep, /wake_up
SGLang    yes               --enable-memory-saver; release/resume_memory_occupation
                            with tags, so KV and weights are independent
TRT-LLM   no                launch-time kv_cache_config.free_gpu_memory_fraction
NIM       no                NIM_* env vars, injectable via pod spec
```

Two cautions. vLLM sleep level 1 offloads weights to host RAM, which moves the
weight bytes from the CUDA image into CRIU's host pages rather than removing
them, and requires host RAM at least the size of the model. SGLang's tagged
release avoids this by dropping KV only. Any release path also needs a resume
call after restore but before readiness, which is orchestration we would own
and a new way for a restore to fail.

## Tier 3

Interposition, for whatever the first two tiers do not reach. Out of scope
here; see the separate discussion of a thin CUDA interposer.

## Execution order

1. Step 0 above. Explains the 3.2G, may make the rest much smaller.
2. Measure the zero fraction of the CUDA memory image. Sizes tier 1A.
   Kill criterion: a low zero fraction means the pool is dirtier than expected
   and 1A is not worth building.
3. Tier 1B (overlap). Independent of the above, and useful regardless.
4. Tier 1A (elision), agent-side first, then plugin-side if it pays.
5. Tier 2, scoped to workloads whose launch we control.

Asking the checkpoint API to skip a virtual address range was considered and
dropped: no such control is exposed. The device-to-host copy happens either
way, so elision can only apply when the image is written and read back. That
still targets the measured bottleneck, but it caps the ceiling: the win is in
disk and IO, not in the transfer.

## Rejected

- Shipping a lower `gpu_memory_utilization`. It is a serving-capacity knob;
  starving the pool is not the same as not checkpointing it. Useful as an
  experiment, not as a fix.
- Compression of the CUDA image. Zero elision is the cheap special case that
  covers the observed data.
- Making the primary mechanism engine-specific. Fails the container-agnostic
  constraint.
