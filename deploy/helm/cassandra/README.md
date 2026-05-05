# Cassandra Helm Chart

This repository contains the Helm chart for deploying NVCF Cassandra clusters on Kubernetes.

## Overview

The chart packages Cassandra cluster resources together with initialization and migration hooks. The repository is chart source only. It does not include public container images or a vendored dependency package in the OSS snapshot.

The default chart values do not set the required image registries and repositories for the Cassandra, migration, or seed-discovery components. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
cassandra:
  migrations:
    image:
      registry: nvcr.io
      repository: 0651155215864979/ncp-dev/nvcf-cassandra-migrations
  image:
    registry: nvcr.io
    repository: 0651155215864979/ncp-dev/bitnami-cassandra
  dynamicSeedDiscovery:
    image:
      registry: nvcr.io
      repository: 0651155215864979/ncp-dev/bitnami-cassandra
```

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`

## Getting Started

Build the chart dependency from source before installing:

```bash
helm dependency build helm
```

Install the chart with the default values plus your own overrides:

```bash
helm install cassandra helm \
  --namespace cassandra-system \
  --create-namespace \
  --values helm/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --wait-for-jobs \
  --timeout 20m
```

Upgrade an existing release:

```bash
helm upgrade cassandra helm \
  --namespace cassandra-system \
  --values helm/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --wait-for-jobs \
  --timeout 20m
```

Uninstall the release:

```bash
helm uninstall cassandra --namespace cassandra-system
```

## Configuration

The default chart configuration lives in `helm/values.yaml`.

Important settings to review before deployment:

- `cassandra.image.*` for the main Cassandra image
- `cassandra.migrations.image.*` for the migrations job image
- `cassandra.dynamicSeedDiscovery.image.*` for the seed discovery helper image
- `cassandra.global.imagePullSecrets` for private registry access
- `cassandra.replicaCount`, `cassandra.cluster.*`, and storage settings for your environment
- `cassandra.dbUser.*` and `cassandra.serviceRolePassword` for database credentials

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

## Notes

- The OSS snapshot intentionally excludes internal development files, deployment helpers, private registry setup, and vendored chart artifacts.
- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
