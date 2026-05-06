---
---

# Control Plane Installation

This page is superseded. For a fresh install, start with the [Quickstart](./quickstart). The quickstart uses `nvcf-cli self-hosted up` to install the control plane, register a GPU cluster, install NVCA, and run basic health checks.

Use [Helmfile Installation](./helmfile-installation) when you need manual release control, partial recovery, or upgrade operations. You can also install each Helm chart individually using `helm install` or `helm upgrade` (see [Standalone Deployment](./standalone-deployment)).

Using `helm` is useful when:

- You want fine-grained control over each component's deployment
- Your environment doesn't support Helmfile
- You need to integrate NVCF components into an existing GitOps pipeline
- You want to install only a subset of the stack

## Installation guides

- [Quickstart](./quickstart) - use `nvcf-cli self-hosted up` for one-click fresh installation.
- [Helmfile Installation](./helmfile-installation) - use `helmfile` for manual control-plane deployment.
- [Standalone Deployment](./standalone-deployment) - use `helm` for chart-by-chart control-plane deployment.

<Note>
Installation methods are now documented directly in [Deployment](./installation).

</Note>
