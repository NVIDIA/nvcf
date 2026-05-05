# NVCF ClusterAgent Operator Helm Chart

This repository contains the Helm chart for deploying the NVCF ClusterAgent (NVCA) Operator on Kubernetes.

## Overview

The chart packages the NVCA Operator deployment, which manages NVCA agent pods and reconciles cluster registration with the NVCF control plane. An optional OpenTelemetry Collector sidecar can be enabled to forward Kubernetes events.

The default chart values do not set the required image registry and repository. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
image:
  repository: <your-registry>/<your-org>/nvca-operator
  tag: <appVersion>
nvcaImage:
  repositoryOverride: <your-registry>/<your-org>/nvca
```

A `values.schema.json` is included in the chart and validates the supplied values during `helm install` / `helm upgrade`.

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`
- A reachable NVCF control plane

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install nvca-operator nvca-operator \
  --namespace nvca-operator \
  --create-namespace \
  --values nvca-operator/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade nvca-operator nvca-operator \
  --namespace nvca-operator \
  --values nvca-operator/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall nvca-operator --namespace nvca-operator
```

## Configuration

The default chart configuration lives in `nvca-operator/values.yaml`.

Important settings to review before deployment:

- `image.*` for the operator container image
- `nvcaImage.repositoryOverride` for the NVCA agent image, if the default needs to be overridden
- `imagePullSecretName` and `generateImagePullSecret` for private registry access
- `replicaCount`, resource requests, and limits for your environment
- `otelCollector.enabled` and `otelCollector.config.*` for the optional event-collection sidecar
- `clusterValidator.*` for the cluster validation hook image and behavior

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
- When vendoring a new NVCA ref, merge with a release-generating Conventional Commit type. Use `feat` for chart behavior changes and `fix` for patch fixes. A `chore` merge does not create a chart release tag.
