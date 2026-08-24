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
v0.2.65 with `NVSNAP_MULTI_GPU_CRIU=1`.

```text
workload                  checkpoint      restore   result
TinyLlama TP=2            2m34s   56G     1m00s     PASS (x3)
TinyLlama TP=4            4m16s  110G     1m14s     PASS
Llama-3.1-70B TP=4        9m56s  290G     2m02s     PASS
```

The 70B run is the production-shaped case: four ranks, 76.5G of GPU state each,
and the restored pod answered a completion correctly.

Restore scales far better than size. 5x the data between TP=2 TinyLlama and 70B
TP=4 costs 2x the time, and the 70B restore moved 290G in 122s, about 2.4 GB/s.
Single-GPU restore measures nearer 0.9 GB/s. The per-rank GPU restores are
therefore overlapping rather than serialising, which contradicts the premise
behind the deferred per-pid parallelisation work and is worth re-examining
before anyone invests there.

The engine is criu-v2 plus cuda-checkpoint. No interception library, no patched
libzmq/libuv, no D2H save. The same path single-GPU uses.

The captures are complete, which matters because an incomplete one looks very
similar: a partial capture still exits zero and still produces a well-formed
checkpoint, just a much smaller one.

```text
TinyLlama TP=2   5 tasks, cuda_plugin paused 4 pids, 2 x 28.4G images
TinyLlama TP=4   7 tasks, cuda_plugin paused 6 pids, 4 x 28.4G images
70B TP=4         7 tasks, cuda_plugin paused 6 pids, 4 x 76.5G images
```

Every Worker_TP rank appears in the dumped tree, there is one GPU image per
rank, and each image matches the per-GPU memory budget. The agent's own capture
guard reported "all GPU processes present in the capture", and every restored
pod served live inference.

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
its own (custom all-reduce, symmetric memory). Each creates cross-GPU
state, and the checkpoint blocks unless all of them are closed. Closing one door
leaves the others open. For tensor parallel that means all-reduce over sockets.

Correction, measured 2026-08-24: "no peer mappings at all" is too strong and was
wrong. A passing vLLM rank still holds mappings to the peer GPU's device node
(3 to its own /dev/nvidia0, 2 to /dev/nvidia1) and captures cleanly regardless.
Whatever the flags remove, it is not visible as device mappings. The precise
mechanism is not established.

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

## SGLang: the recipe does not transfer

Tested 2026-08-24. SGLang TP=2 (Llama-3.1-8B, `--mem-fraction-static 0.6`) with
what should be the equivalent sever set: `--disable-cuda-graph` in place of
`--enforce-eager`, plus `--disable-custom-all-reduce` and the same three
`NCCL_*_DISABLE` env vars, which are engine-independent.

The capture hangs. Not slowly: a `criu` and a `cuda-checkpoint` were still
wedged three hours later, both blocked in `anon_pipe_read`, CRIU waiting on a
cuda-checkpoint that never returns. A retry fails earlier with
`text file busy` because the wedged process still holds the binary, which is a
useful tell that the first attempt is stuck rather than finished.

Two hypotheses were tested and both died:

- Device fds. SGLang processes hold fds on all eight GPUs despite
  CUDA_VISIBLE_DEVICES=0,1. So does vLLM, with an identical per-device count
  (23/7 on the assigned pair, 3 on each other GPU). Not the discriminator.
- Peer device mappings. A passing vLLM rank has 2 mappings to the peer GPU's
  node; so does SGLang. Not the discriminator either.

The only measured difference is that SGLang holds ~40 /dev/nvidiactl mappings
per rank against vLLM's ~28. That is a lead, not a cause.

Flag coverage was also verified rather than assumed: --disable-cuda-graph and
--disable-custom-all-reduce both exist and were accepted, and SGLang's peer
features (--enable-nccl-nvls, --enable-symm-mem, --enable-torch-symm-mem,
--enable-p2p-check) are all opt-in and were never enabled, so the NCCL env vars
were suppressing features that were already inactive. Note also
--disable-piecewise-cuda-graph and --disable-decode-cuda-graph exist and were
NOT set; they are unlikely to matter for a capture hang, since graphs fail late
at restore-inference rather than at capture, but they are untested.

The honest state: the vLLM recipe does not transfer, and why is not known. Do
not assume it generalises to another engine without measuring.

Worth noting for whoever picks this up: a wedged capture leaves host processes
behind that block subsequent attempts on that node. Check for `criu` and
`cuda-checkpoint` in `/host/proc` and kill them before re-running.

NIM was not attempted. `nim-qwen3-32b` defines no command or args, so it runs the
image entrypoint, and the criu-v2 generator requires the setsid stdio-redirect
convention. It needs a hand-written command wrapper around the image's
entrypoint before it can be tested at all.

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

CAPTURE_PATH=criu-v2 ./scripts/test-e2e.sh vllm-tp2-criu

# 70B needs a warm model cache and a raised checkpoint timeout:
CAPTURE_PATH=criu-v2 CHECKPOINT_TIMEOUT=2400 ./scripts/test-e2e.sh vllm-70b-criu
```

`vllm-tp2-criu` and `vllm-70b-criu` are separate manifests from `vllm-tp2` and
`vllm-70b`, which stay on the rootfs/cachedir path; the two engines no longer
share a file. The restore placeholder is generated, not hand-written: the source
carries `nvsnap.io/path: "criu"` so the generator derives a criu-v2 placeholder
with the checkpoint hostPath mounted. Regenerate with
`go run ./internal/manifests/gen -dir deploy/k8s/workloads`. Before that
annotation was set, the manifest was a rootfs/webhook target and restore failed
with "checkpoint images not visible at /checkpoints inside placeholder".

## Scope

Two topologies (TP=2, TP=4) and two model scales (1.1B, 70B) on H100, same-node
restore, vLLM only. SGLang was tested and hangs (above). NIM/TRT-LLM untested.
Also untested: cross-node restore and TP=8.

Operational note: a cold 70B run does not fit the harness. test-e2e.sh pins
POD_READY_TIMEOUT to 1800s for any workload matching *70b*, which a 140G model
pull exceeds, and the checkpoint step needs CHECKPOINT_TIMEOUT well above its
600s default (the capture alone took 596s). Both are harness constants, not
mechanism limits.

Do not read this as "multi-GPU works". Read it as "multi-GPU capture and restore
work on criu-v2 when no peer mappings exist, and that condition currently has to
be arranged by configuration".
