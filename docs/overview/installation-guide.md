# Installation Guide

A self-managed NVCF deployment is installed in stages. Each stage below links
to the pages that carry the detailed procedure. For a local evaluation on a
single k3d cluster, use the [Quickstart](./quickstart.md) instead; it performs
stages 2 and 3 in one command.

## 1. Plan the deployment

Size the control plane and GPU clusters, review the artifacts you must mirror,
and decide how tenants are isolated.

- [Infrastructure Sizing](./infrastructure-sizing.md)
- [Manifest](./manifest.md)
- [Image Mirroring](./image-mirroring.md)
- [Multi-Tenancy](./multi-tenancy.md)

## 2. Install the control plane

Install the Self-Managed Stack on the control-plane cluster with Helmfile.
The CSP example walks the same steps on Amazon EKS.

- [Installation Overview](/nvcf/self-managed/installation-overview)
- [Helmfile Installation](/nvcf/self-managed/helmfile-installation)
- [CSP End-to-End Example](/nvcf/self-managed/csp-end-to-end-example)

## 3. Register a GPU cluster

Register each GPU cluster with the control plane and install the NVCA
Operator from the Compute Plane Stack, then tune scheduling, node selection,
and caches for your workloads.

- [Register a GPU Cluster](/nvcf/compute-plane/register-gpu-cluster)
- [Cluster Configuration](/nvcf/compute-plane/cluster-configuration)

## 4. Deploy and invoke functions

Create functions and tasks, deploy them to registered clusters, and invoke
them through the gateway.

- [API](./api.md)
- [Function Creation](./function-creation.md)
- [CLI](./cli.md)

## 5. Operate

Monitor and maintain the running deployment.

- [Control Plane Operations](/nvcf/self-managed/control-plane-operations)
- [Cluster Monitoring](/nvcf/compute-plane/cluster-monitoring)
- [Observability](/nvcf/observability/observability)
