# NVCF gRPC Proxy Helm Chart

This repository contains the Helm chart for deploying the NVCF gRPC Proxy on Kubernetes.

## Overview

The chart packages the gRPC proxy deployment with two mutually exclusive modes selected by the `grpcproxy.deploymentType` value:

- `deployment` - a horizontally scalable Deployment with optional HPA, suitable for stateless workloads
- `daemonset` - a DaemonSet that runs one pod per node, suitable for node-level traffic handling

A Vault Agent sidecar is configured automatically to fetch service credentials from a Vault or OpenBao backend.

The default chart values do not set the required image registry and repository. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
grpcproxy:
  image:
    registry: <your-registry>
    repository: <your-org>/grpc-proxy
    tag: <appVersion>
  deploymentType: deployment
```

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`
- A reachable NATS cluster
- A reachable Vault or OpenBao instance with a JWT authentication path configured for this service

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install grpc-proxy grpc-proxy \
  --namespace grpc-proxy \
  --create-namespace \
  --values grpc-proxy/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade grpc-proxy grpc-proxy \
  --namespace grpc-proxy \
  --values grpc-proxy/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall grpc-proxy --namespace grpc-proxy
```

## Configuration

The default chart configuration lives in `grpc-proxy/values.yaml`.

Important settings to review before deployment:

- `grpcproxy.image.*` for the proxy container image
- `grpcproxy.imagePullSecrets` for private registry access
- `grpcproxy.deploymentType` to select `deployment` or `daemonset` mode
- `grpcproxy.deployment.*` for replica count, autoscaling, and pod placement when running in Deployment mode
- `grpcproxy.daemonset.*` for tolerations, node selectors, and update strategy when running in DaemonSet mode
- `grpcproxy.config.*` for service endpoints, NATS connection settings, and tracing configuration
- `grpcproxy.vault.*` for JWT authentication path, role, and audience values used by the Vault Agent injector

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
