![NVCF banner](docs/user/images/nvcf-banner.svg)

[Preview docs](https://nvidia-nvcf.docs.buildwithfern.com/nvcf/overview) | [Public docs](https://docs.nvidia.com/nvcf/overview) | [Installation](docs/user/installation.md) | [API Reference](docs/user/api.md) | [Contributing](CONTRIBUTING.md) | [build.nvidia.com Powered By NVCF](https://build.nvidia.com/)

# NVIDIA Cloud Functions

NVIDIA Cloud Functions (NVCF) is a platform for deploying, managing, and running GPU-accelerated workloads at scale. It routes inference, streaming, and other GPU work to worker clusters, so you can scale demanding workloads with less infrastructure to run yourself.

This monorepo contains NVCF service code, deployment assets, documentation,
examples, CLI code, agent skills, and validation tooling.

## Architecture

![NVCF architecture](docs/user/images/nvcf-high-level-stack.svg)

NVCF runs as Kubernetes services that manage function lifecycle, invocation
routing, GPU cluster integration, artifact access, secrets, observability, and
operations.

At a high level:

- The control plane exposes the NVCF API, manages function and deployment
  state, handles secret management, and coordinates platform operations.
- The invocation plane receives HTTP, streaming, and gRPC requests, applies
  routing and rate limiting, and sends work to running function workloads.
- GPU clusters connect through the NVIDIA Cluster Agent (NVCA). NVCA registers
  GPU resources and manages workload execution on GPU nodes.
- Function artifacts live in registries that the NVCF deployment can access.
- Observability, dashboards, and runbooks help operators monitor health and
  debug workload behavior.

The following diagram shows how self-managed NVCF can span regions and GPU
clusters.

<img src="docs/user/images/nvcf-multi-region-multi-cluster.svg" alt="NVCF multi-region and multi-cluster architecture" width="80%">

### Workload types

NVCF functions are long-running, invokable workloads. Use a function when a
client needs an endpoint for inference, streaming, or another service-style GPU
workflow. Functions can be packaged as a container when the workload is a single
service with health and inference endpoints, or as a Helm chart when the
workload needs multiple coordinated containers, services, sidecars, or other
Kubernetes resources.

NVCF tasks are asynchronous, run-to-completion workloads. Use a task for batch
inference, evaluation, fine-tuning, data preparation, or other GPU jobs that
should finish and report status instead of staying online behind an invocation
endpoint. Tasks can be packaged as a container when the workload is a single
service with health and inference endpoints, or as a Helm chart when the
workload needs multiple coordinated containers, services, sidecars, or other
Kubernetes resources.

### Core capabilities

| Capability | What it does |
|------------|--------------|
| Unified control plane | Manages and routes requests across multi-region GPU clusters. |
| Load-balanced workload routing | Balances inference, streaming, and custom workloads based on worker availability. |
| Multiple protocols | Supports multiple protocols for different workload and client needs. |
| Multi-cluster autoscaling | Scales workloads from zero to max across clusters. |
| Mixed GPU support | Supports mixed GPU types across clusters for workloads with different GPU requirements. |
| Health checks and telemetry | Tracks worker status and request latency through health checks and telemetry. |

## Repository map

| Area | Paths | Purpose |
|------|-------|---------|
| Control plane | [`src/control-plane-services/`](src/control-plane-services/) | APIs and services that manage NVCF function and deployment state. |
| Invocation plane | [`src/invocation-plane-services/`](src/invocation-plane-services/) | HTTP invocation, gRPC proxying, rate limiting, LLM gateway paths, and request authorization. |
| Compute plane | [`src/compute-plane-services/`](src/compute-plane-services/) | GPU cluster integration, cache services, image credentials, ESS Agent, and telemetry collection. |
| CLI and libraries | [`src/clis/`](src/clis/), [`src/libraries/`](src/libraries/) | User and developer clients plus shared Go and Python code. |
| Deployment | [`deploy/`](deploy/), [`migrations/`](migrations/) | Helm charts, stack installation, infrastructure services, and datastore migrations. |
| Documentation | [`docs/user/`](docs/user/index.md), [`docs/dev/`](docs/dev/), [`fern/`](fern/) | Self-managed user docs, developer docs, and published docs navigation. |
| Examples | [`examples/`](examples/) | Local development guides, function samples, and load-test assets. |
| Tools | [`tools/`](tools/) | Build, docs, dependency, license, and validation utilities. |
| AI tooling | [`ai-tooling/`](ai-tooling/) | Public agent skills and workflow helpers for NVCF users and developers. |

## Building with Bazel

The `nvcf-cli` binary, multi-platform release matrix, and OCI image are built
with Bazel. The shared Go library under `src/libraries/go/lib` also has Bazel
targets. Other subtrees still use their existing build systems and will be
onboarded one at a time.

Quick start (Linux):

```bash
curl -fSL -o ~/.local/bin/bazel \
  "https://github.com/bazelbuild/bazelisk/releases/download/v1.25.0/bazelisk-linux-$(dpkg --print-architecture)"
chmod +x ~/.local/bin/bazel

bazel build //src/clis/nvcf-cli:nvcf-cli            # host binary
bazel test  //src/clis/nvcf-cli/...                 # unit tests
bazel build //src/clis/nvcf-cli:dist                # all 5 platforms
```

Quick start (macOS):

```bash
brew install bazelisk

bazel build //src/clis/nvcf-cli:nvcf-cli
bazel test  //src/clis/nvcf-cli/...
bazel build //src/clis/nvcf-cli:dist
```

Full setup, day-to-day commands, OCI image build/push, stamping, caches, and
CI map live in [`BAZEL.md`](BAZEL.md). For CLI-specific developer flow see
[`src/clis/nvcf-cli/README.md`](src/clis/nvcf-cli/README.md).

## Support

- If you have found a bug or want to request a feature, please open a [GitHub issue](https://github.com/nvidia/nvcf/issues/new/choose) in this repository. Use the appropriate template and include the component name in the title (e.g., [nvcf-nvca] Pod fails to start on arm64).
- To report a security vulnerability see [`SECURITY.md`](SECURITY.md). Do not open a public issue.

## Contributing

We welcome contributions of all sizes, from typo fixes to new features. See
[`CONTRIBUTING.md`](CONTRIBUTING.md) for the full guide.

NVCF is a new open source project, and we are actively smoothing the
contribution workflow. We accept external contributions through GitHub pull
requests today, with a few temporary wrinkles while the repository becomes more
GitHub-native.

Before changing a service, read [`AGENTS.md`](AGENTS.md) and the nearest nested
`AGENTS.md`. The nested file is the best source for service-specific build,
test, style, and review expectations.

Use Conventional Commits.
For documentation-only changes, run `git diff --check` and any targeted
validation that applies to the changed files.

## Code of conduct

This project follows the Contributor Covenant Code of Conduct. Contributors
agree to uphold this standard. See
[`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) for the full text and enforcement
guidelines.

## Dependency rollups

Dependency collection guide and tool:
[`tools/collect-dependencies/README.md`](tools/collect-dependencies/README.md)
and [`tools/collect-dependencies`](tools/collect-dependencies).
