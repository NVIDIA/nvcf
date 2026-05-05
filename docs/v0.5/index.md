# NVIDIA Cloud Functions

This guide provides information for deploying and operating NVCF in self-managed environments.

- [Deployment](./installation-overview)
  : Install the NVCF control plane and connect GPU clusters.
- [Configuration](./optional-enhancements)
  : Configure gateway routing, registries, and optional enhancements.
- [Using Cloud Functions](./api)
  : Create and invoke functions using the NVCF API and CLI.

<Warning>
Decoupled control plane deployments (GPU cluster separate from the control plane cluster) are not available in Early Access. All EA deployments use a co-located architecture where the control plane and GPU workloads run in the same cluster.

</Warning>

