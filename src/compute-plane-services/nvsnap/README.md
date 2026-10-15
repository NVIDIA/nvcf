# NvSnap

**GPU checkpoint/restore for Kubernetes. Start large models warm, not cold.**

NvSnap captures a warmed GPU inference pod and restores it on any node with
the expensive work already done: no model download, no weight reload, no
kernel or graph recompilation, and, for single-GPU workloads, no engine
re-initialization at all. It works on **unmodified** inference containers
(NVIDIA NIM, vLLM, SGLang, TensorRT-LLM): no application changes, no custom
images, no SDK.

> **DeepSeek-V4-Flash (SGLang, TP=8): serving in 289 s instead of 1162 s.**
> gemma-4-31B 4.5x, gpt-oss-120b 3.2x, NVFP4/FP8 MoE models 1.9-2.6x.
> [Benchmarks](#benchmarks).

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
![Kubernetes](https://img.shields.io/badge/kubernetes-1.25%2B-326ce5.svg)

**Why it works:** GPU cold starts are dominated by repeatable work: weight
download, model load, CUDA graph capture, JIT and torch.compile. NvSnap does
that work once, captures the result, and distributes it: same-node cache,
shared ReadOnlyMany volume, peer-to-peer cascade (every node that restores
becomes a source), and object store for cross-cluster.

---

## How it works

NvSnap picks one of two capture paths automatically, based on the
workload:

- **Warm cache restore (cachedir), any GPU count.** NvSnap captures the
  warm pod's model and JIT/compile caches into one canonical cache
  directory and mounts it read-only on restore. Engines skip the weight
  download and reuse prebuilt kernels, graphs, and compile caches. This is
  the path behind the headline numbers above.
- **Full process checkpoint/restore, single GPU.** CRIU plus NVIDIA
  `cuda-checkpoint` snapshot the whole process (CPU state, open FDs,
  io_uring/epoll, and GPU memory). The restored pod resumes a live,
  already-serving engine with no re-initialization at all. Multi-GPU
  process restore is currently blocked upstream by a `libcudart`
  per-process GPU-state replay limitation.

A node-level **agent** (DaemonSet) performs capture/restore using
`/proc` + cgroup inspection — runtime-agnostic, not tied to any container
runtime API. A **server** provides a REST API, web UI, and checkpoint
catalog. An optional **admission webhook** auto-injects restore plumbing
into workload pods.

Deeper design docs:
[architecture](docs/architecture/NVSNAP-ARCHITECTURE.md) ·
[quiescence / io_uring](docs/architecture/QUIESCENCE-ARCHITECTURE.md) ·
[runtime-agnostic discovery](docs/architecture/04-RUNTIME-AGNOSTIC.md) ·
[cuda-checkpoint call flow](docs/architecture/06-CUDA-CHECKPOINT-CALL-FLOW.md) ·
[vLLM multi-process](docs/architecture/VLLM-DEEP-DIVE.md).

---

## Benchmarks

### Warm cache restore (cachedir)

Measured June 2026 on H100 80GB (GKE, 1-8x H100 per workload). The capture
records the warmed model plus JIT/compile caches into a canonical cache
directory; restore mounts it ReadOnlyMany, so engines skip the weight
download and reuse prebuilt kernels, graphs, and compile caches instead of
recompiling. "Speedup" is `cold_start / restore` for time-to-ready.

| Model | Engine | Cold | Restore | Speedup |
|---|---|---:|---:|---:|
| DeepSeek-V4-Flash | SGLang TP=8 | 1162 s | **289 s** | 4.0x |
| gemma-4-31B-it | SGLang | 492 s | **109 s** | 4.5x |
| gpt-oss-120b | vLLM TP=4 | ~550 s | **171 s** | 3.2x |
| Qwen3.6-35B-A3B (NVFP4) | vLLM TP=4 | 410 s | **218 s** | 1.9x |
| Nemotron-3-Nano-30B-A3B (FP8) | vLLM TP=2 | 370 s | **144 s** | 2.6x |
| Qwen-Image-2512 | vLLM | 198 s | **114 s** | 1.7x |

Wins scale with the cold start's download + JIT/compile cost (the full
measured matrix, including compile-light workloads that see little change,
is in [docs/BENCHMARK.md](docs/BENCHMARK.md)). Phase breakdown for
Qwen3.6-35B-A3B: model download 269 s to ~5 s (loads from the cache volume),
model init 128 s to ~32 s (torch.compile 53.5 s to 5.8 s on cache hit).

What cachedir compresses is the avoidable disk work: weight download and
recompilation. Process bring-up (scheduling, framework import, CUDA init,
tensor-parallel worker spawn) is a fixed floor paid by cold and warm alike.

### Cross-node distribution (peer cascade)

Every node that fetches a checkpoint becomes a source for the next, so
aggregate bandwidth scales with peer count instead of bottlenecking on the
origin node.

| Payload | Size | Per receiver |
|---|---|---|
| CRIU dump (vLLM 1.1B) | 30 GiB | 25 s (1.20 GB/s) |
| Cache capture (Llama-70B) | 133 GiB | 415 s (0.32 GB/s, many-files HTTP overhead) |

### Process checkpoint/restore (CRIU + cuda-checkpoint, single GPU)

The full process-restore path resumes a live, already-serving engine
(no engine re-init at all). Currently single-GPU only.

| Engine | Model | Ckpt size | Cold start | Capture | Restore | Speedup |
|---|---|---|---|---|---|---|
| NIM (TRT-LLM) | Qwen3-32B | 111 GB | 4m 03s | 3m 27s | 1m 40s | 2.4x |
| vLLM | TinyLlama 1.1B | 28 GB | 2m 11s | 1m 32s | 46 s | 2.9x |
| vLLM | Llama-3.1-8B | 76 GB | 1m 50s | 2m 41s | 1m 41s | 1.1x |
| TRT-LLM | TinyLlama 1.1B | 29 GB | 1m 21s | 1m 54s | 49 s | 1.7x |
| SGLang | TinyLlama 1.1B | 57 GB | 1m 01s | 6m 36s | 2m 51s | -- |
| SGLang | Llama-3.1-8B | 88 GB | 1m 40s | 6m 52s | 3m 42s | -- |

"--" where restore is not faster than a cold start (SGLang's init is fast
enough that CRIU overhead doesn't pay back at these sizes).

We are actively working on improving the performance of the pure
CRIU + cuda-checkpoint workflow; expect these numbers to improve.

See [docs/BENCHMARK.md](docs/BENCHMARK.md) for methodology and the full
measured matrix.

---

## Quickstart

Prerequisites: a Kubernetes 1.25+ cluster with GPU nodes, NVIDIA driver
**555+** (required by `cuda-checkpoint`), containerd 1.7+/2.x or CRI-O, and
`kubectl` + `helm` 3.13+.

```bash
# 1. Namespace + image pull secret for the project registry
kubectl create namespace nvsnap-system
kubectl create secret docker-registry nvsnap-pull-secret \
  --namespace=nvsnap-system \
  --docker-server=nvcr.io \
  --docker-username='$oauthtoken' \
  --docker-password='<NGC_API_KEY>'

# 2. Install (agent + server + blobstore)
helm install nvsnap deploy/helm/nvsnap --namespace nvsnap-system

# 3. Verify
kubectl -n nvsnap-system rollout status ds/nvsnap-agent
kubectl -n nvsnap-system get svc nvsnap-server
```

`scripts/install-nvsnap.sh` wraps the above (prereq checks, pull secret,
optional webhook). Full guide, options, and troubleshooting:
[docs/INSTALL.md](docs/INSTALL.md). Pull-secret details:
[docs/PULL-SECRET-SETUP.md](docs/PULL-SECRET-SETUP.md).

### Try it end-to-end

```bash
./scripts/test-e2e.sh vllm-small   # TinyLlama 1.1B on vLLM (single-GPU)
./scripts/test-e2e.sh vllm-8b      # Llama-3.1-8B on vLLM
./scripts/test-e2e.sh vllm-70b     # Llama-3.1-70B TP=4 (cachedir path)
```

---

## Architecture

```
┌──────────────────┐     ┌───────────────────────────────────────┐
│  nvsnap-server   │     │            GPU Node                   │
│  REST API + UI   │◄───►│  nvsnap-agent (DaemonSet)             │
│  catalog (SQLite)│     │   ├─ process discovery (/proc+cgroup) │
│  metrics + audit │     │   ├─ CRIU orchestration (go-criu RPC) │
└──────────────────┘     │   ├─ cuda-checkpoint integration      │
                         │   ├─ cachedir capture watcher           │
                         │   └─ peer-cascade HTTP server         │
                         │                                       │
                         │  GPU Pod (unmodified image)           │
                         │   ├─ NIM / vLLM / SGLang / TRT-LLM    │
                         │   └─ libnvsnap_intercept.so (PRELOAD) │
                         └───────────────────────────────────────┘
                                        │
              L1 same-node ─► L2 shared PVC ─► L3 object store
```

Checkpoints are fetched through a tiered path (same-node → shared
filesystem → peer cascade → object store); cluster config decides which
tiers are present. See
[docs/TRANSPORT-ARCHITECTURE.md](docs/TRANSPORT-ARCHITECTURE.md).

**Why the moving parts exist** (details in
[docs/architecture/](docs/architecture/)):

- **go-criu RPC, not CLI CRIU** — engines like vLLM run 900+ threads; CLI
  CRIU's per-thread ptrace detach takes minutes and often hangs, while RPC
  is seconds regardless of thread count.
- **`libnvsnap_intercept.so` (LD_PRELOAD)** — `io_uring` (via uvloop/libuv)
  and `libzmq` epoll don't survive CRIU restore cleanly. Rather than fork
  every engine, the library reinitializes them on a restore marker.
- **Forked CRIU** — 26 patches for Kubernetes container support, io_uring,
  and the CUDA plugin. See [docs/THIRD-PARTY-FORKS.md](docs/THIRD-PARTY-FORKS.md).

---

## Supported platforms

| Component | Supported | Notes |
|---|---|---|
| Linux kernel | 5.15.x | Kernel 6.x in progress (io_uring restore + cuda-checkpoint interaction). |
| NVIDIA driver | R555+ | Required by `cuda-checkpoint`. Installed via the NVIDIA GPU Operator. |
| Container runtime | containerd 1.7+/2.x, CRI-O | Discovery is at the Linux process level, not via runtime APIs. |
| Kubernetes | 1.25+ | |
| Engines tested | NIM, vLLM, SGLang, TensorRT-LLM | Unmodified stock images. |

Checkpointing operates at the Linux process level, not at the engine or
framework level — so any GPU process is a candidate. The engines above are
the ones validated end-to-end; others should work without modification.

---

## Security requirements

CRIU checkpoint/restore needs elevated privileges, configured per-pod (not
cluster-wide):

| Requirement | Why |
|---|---|
| `privileged: true` | CRIU needs `/proc/<pid>`, ptrace, device files |
| `hostPID: true` (restore) | Host PID namespace for CRIU mount ops |
| Host path volumes | Checkpoint data on node-local storage |
| NVIDIA device access | `/dev/nvidia*` for GPU checkpoint |

The agent DaemonSet runs privileged with `hostPID`, `hostNetwork`, and
host-path mounts for `/proc`, the container runtime state dir, and
checkpoint storage.

---

## REST API

`nvsnap-server` exposes a REST API and web UI on port 8080. Key endpoints:

| Method | Endpoint | Description |
|---|---|---|
| POST | `/api/v1/checkpoints` | Checkpoint a running GPU pod |
| GET | `/api/v1/checkpoints` | List checkpoints (cursor pagination, filters) |
| GET | `/api/v1/checkpoints/{id}` | Checkpoint detail |
| DELETE | `/api/v1/checkpoints/{id}` | Delete checkpoint |
| POST | `/api/v1/restores` | Restore a checkpoint |
| GET/POST | `/api/v1/retention-policies` | Retention policies (CRUD) |
| GET | `/api/v1/audit` | Audit trail |
| GET | `/metrics` | Prometheus metrics |

The full endpoint list (including agent-side cascade endpoints) is in
[`internal/server/openapi.yaml`](internal/server/openapi.yaml).

---

## Project layout

```
cmd/                    binary entry points (agent, server, restore-entrypoint, gpu-restore, CLI)
internal/               agent, server, webhook, CRIU, checkpointstore (Go)
lib/nvsnap_intercept/   LD_PRELOAD interception library (C)
deploy/helm/nvsnap/     Helm chart
deploy/k8s/             manifests + sample workloads
docker/                 Dockerfiles
scripts/                build / test / deploy / benchmark automation
docs/                   architecture, design, reference
terraform/              example GKE cluster provisioning
```

---

## Documentation

- [docs/](docs/README.md) — documentation index (operator + developer)
- [docs/dev/getting-started.md](docs/dev/getting-started.md) — build → capture → validate → merge
- [docs/INSTALL.md](docs/INSTALL.md) — full install guide + troubleshooting
- [docs/architecture/NVSNAP-ARCHITECTURE.md](docs/architecture/NVSNAP-ARCHITECTURE.md) — system architecture
- [docs/BENCHMARK.md](docs/BENCHMARK.md) — benchmark methodology + results
- [docs/THIRD-PARTY-FORKS.md](docs/THIRD-PARTY-FORKS.md) — forked dependencies + patch policy
- [CONTRIBUTING.md](CONTRIBUTING.md) — developer setup + build-from-source

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for developer setup, building from
source, and the forked-dependency layout. Issues and merge requests are
welcome.

## License

Apache 2.0 — see [LICENSE](LICENSE). Forked third-party components retain
their original licenses; see [NOTICE](NOTICE).
