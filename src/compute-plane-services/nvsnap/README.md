# NvSnap

**GPU checkpoint/restore for Kubernetes. Start large models warm, not cold.**

NvSnap captures a warmed GPU inference pod and restores it on any node with
the expensive work already done: with cache capture, no model download and no
kernel or graph recompilation; with process capture, no engine
re-initialization at all. It works on **unmodified** inference containers
(results recorded for NVIDIA NIM, vLLM and SGLang): no application changes, no
custom images, no SDK.

> **DeepSeek-V4-Flash (SGLang, TP=8): serving in 289 s instead of 1162 s**
> (8x H100, June 2026). gemma-4-31B 4.5x, gpt-oss-120b 3.2x, NVFP4/FP8 MoE
> models 1.9-2.6x. [Benchmarks](#benchmarks).

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

New here: [start here](docs/START-HERE.md) · install:
[docs/operate/INSTALL.md](docs/operate/INSTALL.md) · how it works:
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) · every number:
[docs/RESULTS.md](docs/RESULTS.md) · every document: [docs/](docs/README.md)

**Why it works:** GPU cold starts are dominated by repeatable work: weight
download, model load, CUDA graph capture, JIT and torch.compile. NvSnap does
that work once, captures the result, and distributes it: same-node cache,
shared ReadOnlyMany volume, peer-to-peer cascade (every node that restores
becomes a source), and, for cache captures, an object store for
cross-cluster.

---

## How it works

NvSnap has two capture methods. The cluster operator picks one with the
chart value `agent.captureMethod` (default `cachedir`); it applies to every
capture on the cluster:

- **Warm cache restore (`cachedir`), any GPU count.** NvSnap captures the
  warm pod's model and JIT/compile caches into one canonical cache
  directory and mounts it read-only on restore. Engines skip the weight
  download and reuse prebuilt kernels, graphs, and compile caches. This is
  the path behind the headline numbers above.
- **Full process checkpoint/restore (`criu`).** CRIU plus NVIDIA
  `cuda-checkpoint` snapshot the whole process (CPU state, open FDs,
  io_uring/epoll, and GPU memory). The restored pod resumes a live,
  already-serving engine with no re-initialization at all. Multi-GPU
  (tensor-parallel) workloads are captured this way when they run under
  [gpushare](docs/design/GPUSHARE.md), an `LD_PRELOAD` shim that releases the GPU
  memory processes share before the checkpoint and re-creates it after
  restore; without it a multi-GPU CRIU capture is refused. Multi-pod
  instances (a StatefulSet, LeaderWorkerSet or Grove group, possibly
  spanning nodes) are captured and restored as one group, but only on
  request: pods annotated `nvsnap.io/criu-capture: "true"`, or matched by
  the chart value `agent.criuCaptureOptIn`.

A node-level **agent** (DaemonSet) performs capture/restore: it finds a
container through the runtime's API (containerd or CRI-O, detected at start,
`internal/runtime`) and its processes through `/proc`. A **server** provides a
REST API, web UI, and checkpoint catalog. An **admission webhook**, served by
the agent and on by default (`webhook.enabled`), prepares workload pods for
capture and restore.

Deeper design docs:
[architecture](docs/ARCHITECTURE.md) ·
[gpushare](docs/design/GPUSHARE.md).

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
is in [docs/results/BENCHMARK.md](docs/results/BENCHMARK.md)). Phase breakdown for
Qwen3.6-35B-A3B: model download 269 s to ~5 s (loads from the cache volume),
model init 128 s to ~32 s (torch.compile 53.5 s to 5.8 s on cache hit).

What cachedir compresses is the avoidable disk work: weight download and
recompilation. Process bring-up (scheduling, framework import, CUDA init,
tensor-parallel worker spawn) is a fixed floor paid by cold and warm alike.

### Cross-node distribution (peer cascade)

Every node that fetches a checkpoint registers as a source for the next
(`internal/agent/cascade_fetch.go` `EnsureLocal`), so later fetches are not
limited to the origin node. No fan-out throughput has been recorded.

### Process checkpoint/restore (CRIU)

The full process-restore path resumes a live, already-serving engine
(no engine re-init at all). Multi-GPU workloads are checkpointed this way
under [gpushare](docs/design/GPUSHARE.md).

| Workload | Hardware, date | Result |
|---|---|---|
| Qwen2.5-72B-Instruct, vLLM TP=4, gpushare, restored on another node | 4x GB300, October 2026 | 66.7 s to first token from the node's copy, 87.8 s from the store; cold 242 s without the download |
| Llama-3.1-8B, vLLM, single GPU | H100, July 2026 | 32.4 s agent restore time |
| Qwen2.5-32B, vLLM, single GPU | H100, July 2026 | 44.3 s agent restore time |

Every number with its date, hardware, build and source is in
[docs/RESULTS.md](docs/RESULTS.md).

---

## Quickstart

Prerequisites: a Kubernetes cluster with GPU nodes, an NVIDIA driver with the
CUDA checkpoint driver API (`cuCheckpointProcess*`; multi-GPU capture under
gpushare needs 610 or later, see [gpushare](docs/design/GPUSHARE.md#requirements-and-limits)),
containerd or CRI-O, and `kubectl` + `helm` 3.

```bash
# 1. Namespace + image pull secret for the project registry
kubectl create namespace nvsnap-system
kubectl create secret docker-registry nvsnap-pull-secret \
  --namespace=nvsnap-system \
  --docker-server=nvcr.io \
  --docker-username='$oauthtoken' \
  --docker-password='<NGC_API_KEY>'

# 2. Install (agent, server, webhook; also cert-manager and the GPU Operator
#    as subcharts unless you set cert-manager.enabled / gpu-operator.enabled false)
helm dependency build deploy/helm/nvsnap
helm install nvsnap deploy/helm/nvsnap --namespace nvsnap-system

# 3. Verify
kubectl -n nvsnap-system rollout status ds/nvsnap-agent
kubectl -n nvsnap-system get svc nvsnap-server
```

`scripts/install-nvsnap.sh` wraps the above (prereq checks, pull secret,
optional webhook). Full guide, options, and troubleshooting:
[docs/operate/INSTALL.md](docs/operate/INSTALL.md). Pull-secret details:
[docs/operate/PULL-SECRETS.md](docs/operate/PULL-SECRETS.md).

### Try it end-to-end

```bash
./scripts/test-e2e.sh vllm-small   # TinyLlama 1.1B on vLLM (single-GPU)
./scripts/test-e2e.sh vllm-8b      # Llama-3.1-8B on vLLM
./scripts/test-e2e.sh vllm-70b     # Llama-3.1-70B TP=4 (cachedir path)
```

---

## Architecture

```text
┌──────────────────┐     ┌───────────────────────────────────────┐
│  nvsnap-server   │     │            GPU Node                   │
│  REST API + UI   │◄───►│  nvsnap-agent (DaemonSet)             │
│  catalog (SQLite)│     │   ├─ runtime API + /proc discovery    │
│  metrics + audit │     │   ├─ CRIU orchestration (criu-v2)     │
└──────────────────┘     │   ├─ cuda-checkpoint integration      │
                         │   ├─ cachedir capture watcher           │
                         │   └─ peer-cascade HTTP server         │
                         │                                       │
                         │  GPU Pod (unmodified image)           │
                         │   └─ NIM / vLLM / SGLang / TRT-LLM    │
                         └───────────────────────────────────────┘
                                        │
              L1 node ─► L2 cluster storage ─► L3 peers ─► L4 object store
```

A restore fetches a checkpoint from the first tier that has it: the node's
own copy, then cluster storage, then peer agents. The cross-cluster object
store (L4) is filled and pulled by replication in the background, not tried
by a restore. Cluster config decides which tiers are present. See
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md#6-where-bytes-live-and-how-they-move).

**Why the moving parts exist** (details in
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)):

- **In-namespace CRIU (criu-v2)** — the agent stages the CRIU bundle into
  the workload's mount namespace and runs dump and restore there, so the
  dump sees the container's own paths. Under process capture, the one
  library injected into the engine is gpushare, which multi-GPU capture needs.
- **Forked CRIU** — io_uring checkpoint and restore, portable ghost files
  and CUDA plugin fixes on top of upstream criu-dev. See [docs/design/THIRD-PARTY-FORKS.md](docs/design/THIRD-PARTY-FORKS.md).

---

## Supported platforms

| Component | Supported | Notes |
|---|---|---|
| Linux kernel | 6.x recorded | The June 2026 cachedir results ran on 6.x ([results](docs/RESULTS.md#cachedir)). |
| NVIDIA driver | CUDA checkpoint driver API | Multi-GPU capture under gpushare: 610 or later ([gpushare](docs/design/GPUSHARE.md#requirements-and-limits)). The chart can install it through the GPU Operator subchart. |
| Container runtime | containerd, CRI-O | The agent talks to the runtime's socket (`internal/runtime`). |
| Engines with recorded results | NIM, vLLM, SGLang | Unmodified stock images ([results](docs/RESULTS.md)). |

Checkpointing operates at the Linux process level, not at the engine or
framework level. Only the engines above have recorded results.

---

## Security requirements

The agent DaemonSet runs privileged, with `hostPID` and host-path mounts for
`/proc`, the container runtime's state and checkpoint storage: it enters the
workload's namespaces to dump and restore it. nvsnap never makes a workload
pod or restore placeholder privileged. One case changes the engine
container's security context: a restore from an older whole-rootfs capture
runs it as uid 0 with `DAC_OVERRIDE`, `CHOWN` and `FOWNER` added (and
`SYS_ADMIN` when the overlay is assembled in the pod), so it can write the
restored tree (`internal/webhook/bundle_mount.go` `securityContextPatches`,
`internal/webhook/mutate.go` `buildPatches`).

A checkpoint holds the captured processes' memory, which may include tokens,
keys and request data. A pod restores only a capture from its own tenant
scope, and the agent API takes a bearer token when `agent.auth.enabled` is
set. The security model is in
[docs/ARCHITECTURE.md §9](docs/ARCHITECTURE.md#9-security-model).

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

```text
cmd/                    binary entry points (agent, server, mount-prep, rootfs-restore, l2-wait, helper, idle, CLI)
internal/               agent, server, webhook, CRIU, checkpointstore (Go)
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
- [docs/operate/DEVELOPING.md](docs/operate/DEVELOPING.md) — build → capture → validate → merge
- [docs/operate/INSTALL.md](docs/operate/INSTALL.md) — full install guide + troubleshooting
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — system architecture
- [docs/START-HERE.md](docs/START-HERE.md) — what nvsnap is, in five minutes
- [docs/RESULTS.md](docs/RESULTS.md) — every measured number, with its source
- [docs/GLOSSARY.md](docs/GLOSSARY.md) · [docs/REFERENCE.md](docs/REFERENCE.md) — words; annotations, env vars, agent routes
- [docs/design/THIRD-PARTY-FORKS.md](docs/design/THIRD-PARTY-FORKS.md) — forked dependencies + patch policy
- [CONTRIBUTING.md](CONTRIBUTING.md) — developer setup + build-from-source

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for developer setup, building from
source, and the forked-dependency layout. Issues and merge requests are
welcome.

## License

Apache 2.0 — see [LICENSE](LICENSE). Forked third-party components retain
their original licenses; see [NOTICE](NOTICE).
