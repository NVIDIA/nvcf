<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->
# Multi-GPU checkpoint/restore on criu-v2

Status: working under a config constraint, measured 2026-08-23. Not enabled by
default. The constraint is the finding, not a detail.

Multi-GPU CRIU was previously refused outright in the agent, with the reasoning
recorded in the code: cuda-checkpoint blocks on peer state, the D2H path could
never reconstruct CUDA context state on restore, so multi-GPU had to use the
rootfs/cachedir path. The first half of that is true and remains true. The
conclusion drawn from it was too strong: with peer state absent, criu-v2 does
capture and restore a TP=2 workload, weights, KV cache and all.

## What works

Measured on 8x H100 80GB (p5.48xlarge, NVSwitch), driver 580.126.16, agent
v0.2.65 with `NVSNAP_MULTI_GPU_CRIU=1`. Workload is TinyLlama-1.1B at
tensor-parallel-size 2, `--gpu-memory-utilization 0.3`.

```text
                    run 1     run 2
pod ready           3m06s     3m00s
checkpoint          2m34s     2m32s     OK
restore pod ready   1m00s     1m01s     OK
post-restore infer  OK        OK
checkpoint size     56G       56G
```

The engine is criu-v2 plus cuda-checkpoint. No interception library, no patched
libzmq/libuv, no D2H save. The same path single-GPU uses.

The capture is complete, which matters because an incomplete one looks similar:

```text
tasks dumped         5    vllm, python3, VLLM::EngineCore, VLLM::Worker_TP x2
cuda_plugin paused   4 pids
largest images       28366823424  pages-39.img
                     28357816320  pages-21.img
```

Two 28.4G images, one per rank, against a 0.3 x 80G budget per GPU. The size
arithmetic closes, both TP workers are present, and the agent's own capture
guard reported "all GPU processes present in the capture". The restored pod
served live inference.

## The configuration it requires

Every cross-GPU transport must be off before capture:

```yaml
--enforce-eager
--disable-custom-all-reduce
NCCL_P2P_DISABLE=1
NCCL_NVLS_DISABLE=1
NCCL_SHM_DISABLE=1
VLLM_ALLREDUCE_USE_SYMM_MEM=0
```

## The set is not padding: bisect results

This bundle was assembled during an earlier investigation and had never been
reduced. It was worth checking whether one flag was doing the work. It is not.

```text
removed                        result
--enforce-eager                capture OK (59G), restore OK, models OK,
                               INFERENCE FAILS after 2m13s
NCCL_P2P_DISABLE               capture hangs, 10m11s
NCCL_NVLS + SHM + SYMM_MEM     capture hangs, 10m07s
NCCL_SHM + SYMM_MEM            capture hangs, 10m06s
```

Three of four removals broke it. Not yet isolated individually:
`--disable-custom-all-reduce`, and NVLS versus SHM versus SYMM_MEM separately.

The mechanism explains the shape. NCCL reaches a peer through several
independent transports (NVLink P2P, shared memory, NVLS multicast) and vLLM adds
its own (custom all-reduce, symmetric memory). Each creates cross-GPU mappings,
and the checkpoint blocks if any mapping exists. Closing one door leaves the
others open, so the requirement is not "tune these flags" but "no peer mappings
at all". For tensor parallel that means all-reduce over sockets.

## The CUDA graph failure is a different kind of problem

Dropping `--enforce-eager` fails in a way worth separating from the rest,
because it fails late and quietly. Capture succeeds, the checkpoint is larger
(graphs reserve memory), restore succeeds, and the models endpoint answers. The
first actual inference then hangs.

A captured graph holds references to GPU-side resources that the checkpoint
destroys and rebuilds, so replaying it after restore drives dead handles.
Nothing in the capture path can detect this; only the framework that built the
graph can rebuild it.

That distinguishes this flag from the others. The peer-transport flags need a
mechanism nobody has yet. This one has a known owner and a known fix: have the
engine re-capture its graphs after restore. Until then `--enforce-eager` is a
placeholder for that hook, not a permanent tax.

Note also that aborting NCCL communicators does not help here and cannot. NCCL
has no record of which graphs captured its kernels, so an abort frees the
resources and leaves the graphs dangling rather than cleaning them.

## What is still open

Removing the config constraint needs peer mappings gone at capture time without
the workload being launched to avoid them. Two candidate routes:

1. Transparent sever: tear down peer mappings before the dump and re-establish
   them after. The pieces exist in the intercept library
   (`nvsnap_gpu_pre_checkpoint` disables peer access, `nvsnap_gpu_post_restore`
   re-enables it) but driving them requires the LD_PRELOAD stack that criu-v2
   deliberately does not use, and an attempt to run the older quiesce path
   against a live vLLM engine took the executor down with it.
2. Driver support for checkpointing the mappings themselves. NVIDIA publishes an
   example covering checkpoint and restore of IPC memory handles that requires
   display driver 610 or higher, which is the class vLLM's custom all-reduce
   uses. Worth measuring on 610 before building anything.

## Reproducing

```sh
# agent: NVSNAP_MULTI_GPU_CRIU=1 lifts the refusal and keeps the ordinary
# criu-v2 engine. NVSNAP_LEGACY_MULTI_GPU_D2H=1 selects the old quiesce+D2H
# path instead; that one's restore half has never worked.
helm upgrade nvsnap deploy/helm/nvsnap -n nvsnap-system -f <values with
  agent.extraEnv NVSNAP_MULTI_GPU_CRIU=1>

CAPTURE_PATH=criu-v2 ./scripts/test-e2e.sh vllm-tp2
```

The restore placeholder is generated, not hand-written. `vllm-tp2` carries
`nvsnap.io/path: "criu"` so the generator derives a criu-v2 placeholder with the
checkpoint hostPath mounted; regenerate with
`go run ./internal/manifests/gen -dir deploy/k8s/workloads`. Before that
annotation was set, the manifest was a rootfs/webhook target and restore failed
with "checkpoint images not visible at /checkpoints inside placeholder".

## Scope

One workload, one topology, one node. TP=2 TinyLlama on H100, same-node restore.
Untested: larger tensor-parallel degrees, cross-node restore, other engines
(SGLang, TRT-LLM, NIM), and whether the required flags are the same for any of
them. Do not read this as "multi-GPU works". Read it as "multi-GPU capture and
restore work on criu-v2 when no peer mappings exist, and that condition
currently has to be arranged by configuration".
