---
---

# Control Plane Installation

This section covers the installation of the NVCF control plane components, which are required for all self-hosted NVCF deployments.

By default, the NVCF self-hosted stack is deployed using the provided Helmfile (see [helmfile-installation](./helmfile-installation)). However, you can also install each Helm chart
individually using `helm install` or `helm upgrade` (see [self-hosted-standalone-deployment](./standalone-deployment)).

Using `helm` is useful when:

- You want fine-grained control over each component's deployment
- Your environment doesn't support Helmfile
- You need to integrate NVCF components into an existing GitOps pipeline
- You want to install only a subset of the stack

## Installation guides

- [helmfile-installation](./helmfile-installation) - use `helmfile` for automated control-plane deployment.
- [self-hosted-standalone-deployment](./standalone-deployment) - `helm` based control-plane deployment.

<Note>
This page is superseded. Installation methods are now documented directly
in [self-hosted-installation](./installation). See [helmfile-installation](./helmfile-installation) or
[self-hosted-standalone-deployment](./standalone-deployment).

</Note>
