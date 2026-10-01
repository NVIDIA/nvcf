![NVIDIA Cloud Functions banner](images/nvcf-banner.svg)

<Warning>
0.6.2 documentation draft. Stable publication and live upgrade qualification
are pending. This tree is based on the 0.6.1 documentation and the 0.6.2-rc.0
control-plane inventory; it is not a qualified release.
</Warning>

Read the [0.6.2 release notes](./release-notes/0.6.2.md) and
[0.6.1 to 0.6.2 patch procedure](./release-notes/0.6.1-to-0.6.2-upgrade.md)
before proceeding to the [1.0.1 upgrade](/nvcf/overview/0-6-2-to-1-0-1-upgrade).

This guide provides information for deploying and operating NVCF in self-managed environments.

- [Quickstart](./quickstart.md)
  : Install the control plane, register a GPU cluster, and validate the deployment with the one-click CLI flow.
- [Deployment](./installation.md)
  : Compare the one-click and Helmfile installation paths.
- [GPU Cluster Setup](./cluster-management/index.md)
  : Connect GPU clusters to the NVCF control plane.
- [Configuration](./optional-enhancements.md)
  : Configure gateway routing, registries, and optional enhancements.
- [Using Cloud Functions](./api.md)
  : Create and invoke functions using the NVCF API and CLI.
- [Managed (Legacy)](../ngc-managed/cluster-management/ngc-managed.md)
  : Documentation for the legacy NGC-managed NVCF platform (BYOC).
