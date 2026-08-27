# NVCF Vanity Gateway Helm Chart

This directory contains the Helm chart for deploying the NVCF Vanity Gateway on
Kubernetes. The Vanity Gateway maps OpenAI-compatible and vanity URL routes onto
NVCF function invocations.

## Overview

The chart renders a Deployment, a Service, a ServiceAccount, two ConfigMaps (one
for environment configuration, one for the route mapping file), and an optional
ServiceMonitor.

The route mapping is supplied through `vanityGateway.mappingConfig`, which is
serialized into `config.yaml` and mounted at
`/etc/vanity-gateway/config/config.yaml`. The defaults ship an empty mapping, so
routes must be provided through an override values file.

The default values leave `vanityGateway.image.registry` empty. Set the registry,
and the tag if you do not want the chart `appVersion`, in your override file.

```yaml
vanityGateway:
  image:
    registry: <your-registry>
    repository: nvcf-ai-api-gateway-service
    tag: <appVersion>
```

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`
- A reachable NVCF invocation endpoint, set through
  `vanityGateway.config.nvcfApiEndpoint`
- The Prometheus Operator CRDs, only if `vanityGateway.serviceMonitor.enabled`
  is `true`

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install vanity-gateway helm-nvcf-vanity-gateway \
  --namespace nvcf \
  --create-namespace \
  --values helm-nvcf-vanity-gateway/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade vanity-gateway helm-nvcf-vanity-gateway \
  --namespace nvcf \
  --values helm-nvcf-vanity-gateway/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall vanity-gateway --namespace nvcf
```

## Configuration

The default chart configuration lives in
`helm-nvcf-vanity-gateway/values.yaml`, and `values.schema.json` constrains it.
The schema sets `additionalProperties: false` on `vanityGateway` and most of its
sub-objects, so an unrecognized key fails the render rather than being ignored.

Important settings to review before deployment:

- `vanityGateway.image.*` for the container image
- `vanityGateway.imagePullSecrets` for private registry access
- `vanityGateway.replicaCount`, resource requests, and limits for your
  environment
- `vanityGateway.config.nvcfApiEndpoint` for the invocation endpoint
- `vanityGateway.config.otelExporterOtlpEndpoint` for trace export, empty by
  default
- `vanityGateway.mappingConfig.v2config` for the OpenAI and vanity route tables
- `vanityGateway.serviceMonitor.enabled` for Prometheus Operator scraping

### Ports

The container listens on 10081 for traffic and 10083 for admin and metrics. The
Service exposes those as `httpPort` (8080 by default) and `adminPort` (10083 by
default). Metrics are scraped from `/metrics` on the admin port, and the health
probes use `/health` on the traffic port.

### Shutdown

`vanityGateway.shutdown` controls draining. `preStopSleepSeconds` (70 by
default) is how long the preStop hook sleeps before the container is signaled,
which lets in-flight and newly routed requests settle.
`terminationGracePeriodSeconds` (330 by default) must stay comfortably above it,
or the pod is killed mid-drain.

## Route mapping

`vanityGateway.mappingConfig.v2config` has two sections:

- `openai`: per-endpoint model routes, keyed by endpoint (`chatCompletions`,
  `completions`, `embeddings`, `responses`, and the image endpoints). Each route
  requires `modelName` and `functionID`.
- `vanity`: host-based routes, each requiring a `host` and a `paths` map. Each
  path requires `path` and `functionID`.

Use `shadows` to set policy for each shadow target:

```yaml
primary:
  modelName: example/primary
  functionID: primary-function-id
  shadows:
    - modelName: private/example/shadow-a
      percentage: 10
      samplingMethod: perBearerKey
      cancelOnClientDisconnect: true
    - modelName: private/example/shadow-b
      percentage: 50
      samplingMethod: random
shadow-a:
  modelName: private/example/shadow-a
  functionID: shadow-a-function-id
shadow-b:
  modelName: private/example/shadow-b
  functionID: shadow-b-function-id
```

Each shadow model must be another model route in the same OpenAI endpoint.
`percentage` defaults to `100`, `samplingMethod` defaults to `random`, and
`cancelOnClientDisconnect` defaults to `false`.
Shadowing is not supported for `imageEdits` or `imageVariations` routes.

Legacy `shadowModelName`, `shadowModelNames`, `shadowPercentage`,
`shadowSamplingMethod`, and `shadowCancelOnClientDisconnect` fields remain
supported. Their policy applies to every legacy shadow target. Do not combine
the `shadows` field with legacy shadow fields on the same route.

Both sections are empty by default. `vanityGateway.config.shadowMaxConcurrent`
bounds concurrent shadow requests across all routes.

## Notes

- The chart version is `0.0.0` in `Chart.yaml`. The release pipeline packages
  the chart at the version in its `deploy/helm/vanity-gateway/v<X.Y.Z>` release
  tag, so the committed value only affects local renders. See `AGENTS.md`.
- If you publish or mirror the required images into another registry, set the
  image registry, repository, tag, and pull secret values explicitly in your
  override file.
