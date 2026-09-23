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
  : Install the control plane, register a GPU cluster, and validate the deployment locally with the one-click CLI flow.
- [Installation Guide](./installation-guide.md)
  : Plan the deployment, install the control plane, register GPU clusters, and operate the result.
- [Using Cloud Functions](./api.md)
  : Create and invoke functions using the NVCF API and CLI.
