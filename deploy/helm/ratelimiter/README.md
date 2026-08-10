# NVCF Rate Limiter Helm Chart

This repository contains the Helm chart for deploying the NVCF Rate Limiter service on Kubernetes.

## Overview

The chart packages the Rate Limiter deployment, Service, and supporting resources.

The default chart values do not set the required image registry and repository. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
image:
  repository: <your-registry>/<your-org>/nvcf-ratelimiter
  tag: <appVersion>
imagePullSecrets:
  - name: <your-pull-secret>
```

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install nvcf-ratelimiter nvcf-ratelimiter \
  --namespace nvcf-ratelimiter \
  --create-namespace \
  --values nvcf-ratelimiter/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade nvcf-ratelimiter nvcf-ratelimiter \
  --namespace nvcf-ratelimiter \
  --values nvcf-ratelimiter/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall nvcf-ratelimiter --namespace nvcf-ratelimiter
```

## Configuration

The default chart configuration lives in `nvcf-ratelimiter/values.yaml`.

Important settings to review before deployment:

- `image.*` for the Rate Limiter container image
- `imagePullSecrets` for private registry access
- `replicaCount`, resource requests, and limits for your environment
- `service.*` for service type and ports
- `namespace` for the deployment target namespace

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
