# Contributing to NvSnap

NvSnap is a GPU checkpoint/restore system for Kubernetes.

## Project location

The canonical repo is `github.com/NVIDIA/nvcf`, under
`src/compute-plane-services/nvsnap`. The CRIU fork NvSnap builds from is public
at `github.com/balajinvda/criu` (branch `io-uring-cr`).

## What you need to build

The criu-v2 engine builds from public inputs only:

- The CRIU fork (`github.com/balajinvda/criu`, `io-uring-cr`): one `make`
  produces both the `criu` binary and `cuda_plugin.so`.
- `cuda-checkpoint`: built from source (`docker/agent/nvsnap-cuda-checkpoint.c`)
  in the base image's `cuda-cli-builder` stage, on the public CUDA driver
  checkpoint API (`cuCheckpointProcess*`, driver 570+ at runtime). Works for
  x86-64 and arm64 — no committed binaries.
- The Go agent and `nvsnap_cr.so`: this repository.

The legacy LD_PRELOAD injection stack (patched uvloop/libuv/libzmq) is not used
by criu-v2 and is not required to build.

## Build

```bash
# 1. Clone the CRIU fork as a sibling (scripts auto-discover ../criu).
git clone -b io-uring-cr https://github.com/balajinvda/criu ../criu

# 2. Build the base image: CRIU + cuda_plugin on ubuntu:22.04. ~5 min.
BASE_VERSION=dev ./scripts/build-agent.sh base

# 3. Build the agent image on top of the base (no private inputs).
docker build -f docker/agent/Dockerfile.app.criuv2 \
  --build-arg BASE_IMAGE=nvsnap-agent-base:dev -t nvsnap-agent:dev .
```

Glibc: the base is `ubuntu:22.04` (glibc 2.35) and criu carries its own
`ld-linux` + `libc` in `/criu-bundle/lib`; the Go agent is built with
`CGO_ENABLED=0` (fully static). Do not bump the base off 22.04 and do not enable
CGO. A binary linked against a newer glibc aborts inside older workload
containers, which is the classic failure this layout avoids.

See [`scripts/versions.sh`](scripts/versions.sh) for version and registry config.

## Try it

```bash
# Single-GPU checkpoint/restore against a real GPU node.
./scripts/test-e2e.sh vllm-small
```

## Container registry

Prebuilt images live in a private NGC org and are entitlement-gated. Build from
source (above) for a fully public path.

## Code style

- Go: `gofmt`, idiomatic Go. Wrap errors with `fmt.Errorf("...: %w", err)`. Use
  the structured logger.
- Bash: `set -euo pipefail` at the top of new scripts.
- YAML manifests: kubectl-applyable from `deploy/k8s/`. Run
  `./scripts/sync-versions.sh` after a version bump.

## Pull requests

1. Branch from `main`.
2. Bump per-component image tags; never reuse a tag on rebuild
   (`:v0.0.1` to `:v0.0.2`).
3. Run `./scripts/test-e2e.sh vllm-small` on a real GPU cluster before merging
   anything that touches the agent, the CRIU bundle, or K8s manifests.
4. Reference the issue number if one exists.

## Reporting bugs

File an issue with:
- Agent image tag: `kubectl -n nvsnap-system get ds nvsnap-agent -o jsonpath='{.spec.template.spec.containers[0].image}'`
- Workload (vLLM, SGLang, TRT-LLM, etc.), model, and GPU count
- Agent log: `kubectl -n nvsnap-system logs ds/nvsnap-agent`
- CRIU log if relevant: `/var/log/criu/<checkpoint-id>/dump.log` on the GPU node

## License

Apache 2.0 (see `LICENSE`).
