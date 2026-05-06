# Admin Token Issuer Proxy Helm Chart

This repository contains the Helm chart for deploying the Admin Token Issuer Proxy on Kubernetes.

## Overview

The Admin Token Issuer Proxy is a Kubernetes service that mints admin-scoped JSON Web Tokens by communicating with a Vault or OpenBao backend. The chart packages the proxy deployment, ServiceAccount, Service, and an optional Gateway API HTTPRoute for ingress integration.

The default chart values do not set the required image registry and repository. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
adminIssuerProxy:
  image:
    registry: <your-registry>
    repository: <your-org>/admin-token-issuer-proxy
    tag: <appVersion>
```

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`
- A reachable Vault or OpenBao instance with a JWT authentication path configured for this service
- A Gateway API compatible controller, if `gateway` integration is enabled

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install admin-token-issuer-proxy chart \
  --namespace admin-token-issuer-proxy \
  --create-namespace \
  --values chart/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade admin-token-issuer-proxy chart \
  --namespace admin-token-issuer-proxy \
  --values chart/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall admin-token-issuer-proxy --namespace admin-token-issuer-proxy
```

## Configuration

The default chart configuration lives in `chart/values.yaml`.

Important settings to review before deployment:

- `adminIssuerProxy.image.*` for the proxy container image
- `adminIssuerProxy.imagePullSecrets` for private registry access
- `adminIssuerProxy.replicaCount`, resource requests, and limits for your environment
- `adminIssuerProxy.gateway.*` for Gateway API HTTPRoute settings, if ingress integration is required
- `adminIssuerProxy.vault.*` for JWT authentication path, role, and audience values used by the Vault Agent injector

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
