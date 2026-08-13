# NVCF Notary Service Helm Chart

This repository contains the Helm chart for deploying the NVCF Notary Service on Kubernetes.

## Overview

The chart packages the Notary Service deployment along with its Vault Agent sidecar configuration for fetching signing keys and service credentials from a Vault or OpenBao backend.

The default chart values do not set the required image registry and repository. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
notary:
  image:
    registry: <your-registry>
    repository: <your-org>/nvcf-notary-service
    tag: <appVersion>
```

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`
- A reachable NVCF API endpoint
- A reachable Vault or OpenBao instance with a JWT authentication path configured for this service

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install nvcf-notary-service nvcf-notary-service \
  --namespace nvcf-notary-service \
  --create-namespace \
  --values nvcf-notary-service/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade nvcf-notary-service nvcf-notary-service \
  --namespace nvcf-notary-service \
  --values nvcf-notary-service/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall nvcf-notary-service --namespace nvcf-notary-service
```

## Configuration

The default chart configuration lives in `nvcf-notary-service/values.yaml`.

Important settings to review before deployment:

- `notary.image.*` for the notary container image
- `notary.imagePullSecrets` for private registry access
- `notary.replicaCount`, resource requests, and limits for your environment
- `notary.volumes` / `notary.volumeMounts` and the `configmap-vault-agent-template` for the Vault Agent sidecar's JWT auth path, role, and audience

The token issuer, JWKS URL, assertion issuer, and vault secrets path are baked into the image's `ncp` Spring profile rather than set via `notary.env`.

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
