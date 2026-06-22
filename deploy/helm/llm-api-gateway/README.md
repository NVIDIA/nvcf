# NVCF LLM API Gateway Helm Chart

> [!IMPORTANT]
> Active development of the helm-nvcf-llm-api-gateway chart has moved to
> the NVCF umbrella monorepo. This repository is retained as
> historical source context only; new commits, issues, and merge
> requests should target the umbrella.
>
> Internal: https://github.com/NVIDIA/nvcf/nvcf/-/tree/main/deploy/helm/llm-api-gateway
> Public mirror: https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/llm-api-gateway

This repository contains the Helm chart for deploying the NVCF LLM API Gateway on Kubernetes.

## Overview

The chart packages the LLM API Gateway deployment, which fronts the LLM Request Router and provides hot-path rate limiting backed by embedded Olric.

The default chart values do not set the required image registry and repository. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
llmApiGateway:
  image:
    registry: <your-registry>
    repository: <your-org>/llm-api-gateway
    tag: <appVersion>
```

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`
- A reachable LLM Request Router HTTP endpoint

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install llm-api-gateway llm-api-gateway \
  --namespace llm-api-gateway \
  --create-namespace \
  --values llm-api-gateway/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade llm-api-gateway llm-api-gateway \
  --namespace llm-api-gateway \
  --values llm-api-gateway/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall llm-api-gateway --namespace llm-api-gateway
```

## Configuration

The default chart configuration lives in `llm-api-gateway/values.yaml`.

Important settings to review before deployment:

- `llmApiGateway.image.*` for the gateway container image
- `llmApiGateway.imagePullSecrets` for private registry access
- `llmApiGateway.replicaCount`, resource requests, and limits for your environment
- `llmApiGateway.config.requestRouterUrl` and timeout values for the LLM Request Router HTTP endpoint
- `llmApiGateway.config.nvcfGrpc*` for optional NVCF gRPC auth integration
- `llmApiGateway.metrics.enabled` to expose a metrics port on the Service and Deployment (default: `false`)
- `llmApiGateway.metrics.serviceMonitor.enabled` to create a Prometheus `ServiceMonitor` (requires `metrics.enabled`)
- `llmApiGateway.olric.*` for embedded rate-limit state and peer discovery
- `llmApiGateway.vault.*` for JWT authentication path, role, and audience values used by the Vault Agent injector

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
