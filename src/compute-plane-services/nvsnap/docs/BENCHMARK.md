# NVSNAP Checkpoint/Restore Benchmarks

## Huge-page CRIU restore (Lever A), July 2026

Cluster: aws-dev1, H100 80GB (single GPU), driver 580.126, criu-v2 engine.
The CRIU fork now `madvise(MADV_HUGEPAGE)`s the premapped private-VMA arena
before faulting in the process image, so the CPU-side memory installs as 2MB
pages instead of ~800k 4KB pages. Matched agent-restore time, before (app
v0.2.20) vs after (app v0.2.21, base v0.0.11, criu fork `1ddd5c9c3`):

| Workload | Checkpoint | Before | After | Delta |
|---|---:|---:|---:|---:|
| vllm-small (TinyLlama 1.1B) | 3.2G | 44.3 s | 32.2 s | -27% |
| vllm-8b (Llama-3.1-8B) | 18G | 58.9 s | 38.5 s | -35% |
| e5-mistral-7b | 15G | 84.0 s | 40.8 s | -51% |
| vllm-qwen32b (Qwen2.5-32B) | 64G | 77.4 s | 48.5 s | -37% |

All four tested single-GPU workloads restore faster; larger CPU-side footprints
generally benefit, although the improvement is not monotonic in this sample.
Phase split from the vllm-small `-v4` restore.log:
the CPU pages-restore phase dropped 13.5 s to 3.8 s (-72%); the cuda_plugin GPU
restore phase (~27 s) is unchanged. No lasting memory cost: `AnonHugePages` is 0
in the final restored process because the huge pages exist only transiently in
the CRIU staging arena during population and split on the remap to final
addresses. Best-effort and safe: on a non-THP node the madvise is a no-op and
restore proceeds unchanged. Design: `docs/proposals/single-gpu-restore-speedup.md`.

## Warm cache restore (cachedir), June 2026

Cluster: GKE, H100 80GB (1-8x per workload), kernel 6.x. Restore reads the
captured model and JIT/compile cache from a ReadOnlyMany volume. The cache
directory is set to one canonical path (`/opt/nvsnap`) identically at capture
and restore, so engines reuse prebuilt kernels and graphs instead of
recompiling.

| Model | Engine | Cold | Restore | Speedup |
|---|---|---:|---:|---:|
| DeepSeek-V4-Flash | SGLang TP=8 | 1162 s | 289 s | 4.0x |
| gemma-4-31B-it | SGLang | 492 s | 109 s | 4.5x |
| gpt-oss-120b | vLLM TP=4 | ~550 s | 171 s | 3.2x |
| Qwen3.6-35B-A3B (NVFP4) | vLLM TP=4 | 410 s | 218 s | 1.9x |
| Nemotron-3-Nano-30B-A3B (FP8) | vLLM TP=2 | 370 s | 144 s | 2.6x |
| Qwen-Image-2512 | vLLM | 198 s | 114 s | 1.7x |
| e5-mistral-7b-instruct | vLLM | 73 s | 90 s | -- |
| whisper-large-v3 | NIM (Riva) | 72 s | 74 s | -- |

Wins scale with the cold JIT/compile cost; neutral on compile-light and
framework-bound workloads. Two implementation details enable the cache reuse:

- Preserve file mtimes on the capture copy (`internal/treecopy`): ninja-based
  SGLang kernel caches key reuse on mtime, so a normalized copy forces a full
  kernel recompile (DeepSeek CUDA-graph capture 383 s vs 38 s with mtimes
  preserved).
- Do not set `HF_HUB_OFFLINE` on restore: it makes vLLM rewrite `--model` to
  the resolved path, which changes the `torch.compile` config hash relative to
  capture and misses the compile cache (gpt-oss torch.compile 19.8 s vs 4.6 s).

Phase breakdown, Qwen3.6-35B-A3B (cold vs warm): model download 269 s to ~5 s,
model init 128 s to ~32 s (torch.compile 53.5 s to 5.8 s, "directly load
compiled graph from cache"). The remaining warm time is the fixed process
bring-up floor (scheduling, framework import, CUDA init, tensor-parallel
worker spawn) that both cold and warm starts pay; cachedir compresses the
disk work, not live process initialization.


## Environment

- **Cluster**: example-gpu-cluster (GKE)
- **Kernel**: 5.15.0-1081-gke
- **GPUs**: NVIDIA H100 80GB HBM3 (8 per node, 3 nodes)
- **Node storage**: 5.9TB `/dev/md0` at `/var/lib/containerd`
- **Checkpoint path**: `/var/lib/containerd/nvsnap-checkpoints`
- **Container runtime**: containerd 1.7.26
- **Agent**: v0.9.46 (build at time of measurement; see `scripts/versions.sh` for the current pinned version)
- **CRIU**: forked, with io_uring C/R, CUDA plugin, binfmt_misc fixes
- **Patched libs**: uvloop v0.22.1-gpucr16, libzmq v4.3.6-criu-epoll-v12, pyzmq v27.2.0-gpucr3, libuv v1.48.0-criu-v3
- **vLLM**: v0.11.2
- **SGLang**: latest (lmsysorg/sglang:latest)

## Summary (2026-02-25)

| Workload | Model | GPUs | Cold Start | Checkpoint | Ckpt Size | Restore | Speedup | Result |
|----------|-------|------|-----------|------------|-----------|---------|---------|--------|
| vLLM | TinyLlama 1.1B | 1x H100 | 2m 30s | 1m 29s | 28 GB | **1m 02s** | 2.4x | PASS |
| SGLang | TinyLlama 1.1B | 1x H100 | 1m 00s | 5m 22s | 39 GB | **2m 55s** | — | PASS |
| vLLM | Llama-3.1-8B | 1x H100 | 1m 50s | 2m 41s | 76 GB | **1m 41s** | 1.1x | PASS |
| SGLang | Llama-3.1-8B | 1x H100 | 1m 40s | 6m 52s | 88 GB | **3m 42s** | — | PASS |

- **Speedup** = Cold Start / Restore (restore includes CRIU process restore + GPU memory restore)
- SGLang cold starts are faster (smaller base image, no torch.compile), so restore speedup is less pronounced
- SGLang checkpoints are larger due to higher default GPU memory allocation
- All tests run sequentially on a clean cluster with fresh checkpoints

## Detailed Results

### vLLM + TinyLlama 1.1B (single GPU)

- **Model**: TinyLlama/TinyLlama-1.1B-Chat-v1.0
- **GPUs**: 1x H100
- **max_model_len**: 2048, **gpu_memory_utilization**: 0.3

| Step | Duration |
|------|----------|
| Cold start (pod ready) | 2m 30s |
| /v1/models ready | 0m 01s |
| Pre-checkpoint inference | 0m 01s |
| **Checkpoint** | **1m 29s** |
| Checkpoint size | 28 GB |
| **Restore (pod ready)** | **1m 02s** |
| Post-restore /v1/models | 0m 01s |
| Post-restore inference | 0m 01s |
| **Total E2E** | **5m 18s** |

### SGLang + TinyLlama 1.1B (single GPU)

- **Model**: TinyLlama/TinyLlama-1.1B-Chat-v1.0
- **GPUs**: 1x H100
- **Image**: lmsysorg/sglang:latest
- **mem_fraction_static**: 0.3

| Step | Duration |
|------|----------|
| Cold start (pod ready) | 1m 00s |
| /v1/models ready | 0m 01s |
| Pre-checkpoint inference | 0m 01s |
| **Checkpoint** | **5m 22s** |
| Checkpoint size | 39 GB |
| **Restore (pod ready)** | **2m 55s** |
| Post-restore /v1/models | 0m 01s |
| Post-restore inference | 0m 01s |
| **Total E2E** | **9m 35s** |

### vLLM + Llama-3.1-8B-Instruct (single GPU)

- **Model**: meta-llama/Llama-3.1-8B-Instruct
- **GPUs**: 1x H100
- **max_model_len**: 4096, **gpu_memory_utilization**: 0.9

| Step | Duration |
|------|----------|
| Cold start (pod ready) | 1m 50s |
| /v1/models ready | 0m 01s |
| Pre-checkpoint inference | 0m 01s |
| **Checkpoint** | **2m 41s** |
| Checkpoint size | 76 GB |
| **Restore (pod ready)** | **1m 41s** |
| Post-restore /v1/models | 0m 01s |
| Post-restore inference | 0m 01s |
| **Total E2E** | **6m 31s** |

### SGLang + Llama-3.1-8B-Instruct (single GPU)

- **Model**: meta-llama/Llama-3.1-8B-Instruct
- **GPUs**: 1x H100
- **Image**: lmsysorg/sglang:latest
- **mem_fraction_static**: 0.85

| Step | Duration |
|------|----------|
| Cold start (pod ready) | 1m 40s |
| /v1/models ready | 0m 08s |
| Pre-checkpoint inference | 0m 01s |
| **Checkpoint** | **6m 52s** |
| Checkpoint size | 88 GB |
| **Restore (pod ready)** | **3m 42s** |
| Post-restore /v1/models | 0m 02s |
| Post-restore inference | 0m 01s |
| **Total E2E** | **12m 42s** |

## Multi-GPU Results

### Llama-3.1-70B-Instruct (TP=4, 4x H100)

Multi-GPU uses the cachedir path (process-level multi-GPU restore is
blocked upstream). Measured: cold start 332 s; cache capture 133 GiB;
cache-warm restore 180 s (~1.8x vs cold). Superseded by the June 2026
warm-cache matrix at the top of this document; see also
[PDF-BENCH-RESULTS.md](PDF-BENCH-RESULTS.md).
