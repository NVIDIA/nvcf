# NVCF OpenBao Helm Chart

This repository contains an opinionated Helm chart for deploying OpenBao in the
NVIDIA Cloud Functions (NVCF) self-hosted environment.

## Overview

The chart is designed to provide:

- An OpenBao server deployment built on top of the upstream
  `openbao/openbao-helm` chart
- NVCF-specific post-install and post-upgrade jobs for cluster initialization
  and migrations
- Opinionated defaults for HA, injector enablement, and operational hooks

## Prerequisites

- Kubernetes cluster compatible with the bundled upstream OpenBao chart
- Helm 3
- `kubectl` configured for the target cluster
- Access to the required container images

## Images

This repository is published as chart source only.

This chart depends on NVIDIA-managed container images for the OpenBao server
extensions, migration jobs, and related operational tooling.

The default chart values do not set the required image registries and
repositories for those components. They must be supplied through an additional
values file at install time, and access to those images must be arranged
separately.

Example:

```yaml
openbao:
  migrations:
    image:
      registry: nvcr.io
      repository: 0651155215864979/ncp-dev/nvcf-openbao-migrations
  server:
    image:
      registry: nvcr.io
      repository: 0651155215864979/ncp-dev/nvcf-openbao
  injector:
    image:
      registry: nvcr.io
      repository: 0651155215864979/ncp-dev/oss-vault-k8s
    agentImage:
      registry: nvcr.io
      repository: 0651155215864979/ncp-dev/nvcf-openbao
```

## Installation

This chart is intended to be installed by invoking Helm directly from source.

1. Build dependencies:

```bash
helm dependency build helm
```

2. Install the chart:

```bash
helm install openbao-server helm \
  --namespace vault-system \
  --create-namespace \
  --values helm/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --wait-for-jobs \
  --timeout 20m
```

## Configuration

Chart configuration lives in [helm/values.yaml](./helm/values.yaml). Additional
values files can be layered on top during installation.

Key configurable areas include:

- OpenBao server image settings and HA configuration
- Migration job image settings and migration environment variables
- Injector image settings
- Image pull secrets
- OIDC issuer discovery for migration-time JWT auth configuration

Important note on Helm list merging:

When overriding list values such as `extraContainers`, Helm replaces the
original list instead of merging it. Override the full list item to preserve
required fields from the base values file.

## Uninstallation

```bash
helm uninstall openbao-server --namespace vault-system
```

## License

This project is licensed under the
[Apache License, Version 2.0](https://www.apache.org/licenses/LICENSE-2.0).
