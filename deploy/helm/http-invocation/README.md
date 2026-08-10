# NVCF Invocation Service Helm Chart

This repository contains the Helm chart for deploying the NVCF Invocation Service on Kubernetes.

## Overview

The chart packages the Invocation Service deployment along with its Vault Agent sidecar configuration for fetching service credentials from a Vault or OpenBao backend.

The default chart values do not set the required image registry and repository. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
invocation:
  image:
    registry: <your-registry>
    repository: <your-org>/nvcf-invocation-service
    tag: <appVersion>
```

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`
- A reachable NVCF API endpoint
- A reachable NATS cluster
- A reachable Rate Limiter service
- A reachable Vault or OpenBao instance with a JWT authentication path configured for this service

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install nvcf-invocation-service nvcf-invocation-service \
  --namespace nvcf-invocation-service \
  --create-namespace \
  --values nvcf-invocation-service/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade nvcf-invocation-service nvcf-invocation-service \
  --namespace nvcf-invocation-service \
  --values nvcf-invocation-service/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall nvcf-invocation-service --namespace nvcf-invocation-service
```

## Configuration

The default chart configuration lives in `nvcf-invocation-service/values.yaml`.

Important settings to review before deployment:

- `invocation.image.*` for the invocation service container image
- `invocation.imagePullSecrets` for private registry access
- `invocation.replicaCount`, resource requests, and limits for your environment
- `invocation.config.*` for the NVCF API address, NATS connection settings, and Rate Limiter address
- `invocation.vault.*` for JWT authentication path, role, and audience values used by the Vault Agent injector

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
