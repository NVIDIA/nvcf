# NVIDIA Cloud Functions

![NVIDIA Cloud Functions banner](images/nvcf-banner.svg)

This guide provides information for deploying and operating NVCF in self-managed environments.

NVCF ships as three independently versioned Helm stacks. Each stack has its
own documentation set and version menu.

- [Self-Managed Stack](/nvcf/self-managed/)
  : Control plane installation, configuration, function APIs, and operations.
- [Compute Plane Stack](/nvcf/compute-plane/)
  : GPU cluster setup, scheduling, and caches.
- [Observability Stack](/nvcf/observability/)
  : Metrics, dashboards, and alerting.
- [Compatibility Matrix](./compatibility-matrix.md)
  : Stack releases that are qualified to run together.

## Getting started

- [Quickstart](./quickstart.md)
  : Install the control plane, register a GPU cluster, and validate the deployment with the one-click CLI flow.
- [Deployment](/nvcf/self-managed/installation-overview)
  : Compare the one-click and Helmfile installation paths.
- [GPU Cluster Setup](/nvcf/compute-plane/gpu-cluster-setup)
  : Connect GPU clusters to the NVCF control plane.
- [Configuration](/nvcf/self-managed/optional-enhancements)
  : Configure gateway routing, registries, and optional enhancements.
- [Using Cloud Functions](/nvcf/self-managed/api)
  : Create and invoke functions using the NVCF API and CLI.
