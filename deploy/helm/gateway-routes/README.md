# NVCF Gateway Routes Helm Chart

This repository contains the Helm chart for deploying NVCF ingress routes via the Kubernetes Gateway API.

## Overview

The chart deploys `HTTPRoute`, `TCPRoute`, and `ReferenceGrant` resources that attach to an existing Gateway provisioned separately by the cluster operator (e.g. Envoy Gateway, Istio, Traefik, Kong). It also includes optional `PodMonitor` resources for scraping Envoy Gateway proxy metrics with Prometheus.

The chart deploys routing configuration only. It does not include any container images. Backend services referenced by the routes (`api`, `nvct-api`, `api-keys`, `invocation`, `llm-api-gateway`, `sis`, `grpc`) must already be deployed separately.

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`
- A Gateway API compatible controller installed in the cluster
- An existing `Gateway` resource with an HTTP listener (and a TCP listener if the gRPC route is enabled)
- The backend services that the routes target, deployed in their respective namespaces

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install nvcf-gateway-routes chart \
  --namespace gateway \
  --values chart/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade nvcf-gateway-routes chart \
  --namespace gateway \
  --values chart/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall nvcf-gateway-routes --namespace gateway
```

## Configuration

The default chart configuration lives in `chart/values.yaml`.

Important settings to review before deployment:

- `nvcfGatewayRoutes.domain` for the base hostname used when templating route hostnames
- `nvcfGatewayRoutes.gateways.shared.*` for the HTTP Gateway name, namespace, and listener
- `nvcfGatewayRoutes.gateways.grpc.*` for the TCP Gateway name, namespace, and listener
- `nvcfGatewayRoutes.routes.<route>.enabled` to toggle individual routes
- `nvcfGatewayRoutes.routes.<route>.hostnames` to override the templated hostnames
- `nvcfGatewayRoutes.routes.<route>.backend.{name,namespace,port}` to point a route at the correct backend service
- `nvcfGatewayRoutes.routes.<route>.routeAnnotations` to add annotations consumed by external controllers (e.g. external-dns, cert-manager)
- `nvcfGatewayRoutes.podMonitors.enabled` to opt in to Envoy Gateway proxy `PodMonitor` resources

The default values use `localhost` as the domain and assume backend services are named consistently with NVCF defaults. Override these for any shared or production environment.

## Routes

| Route | Kind | Default hostname | Backend |
| --- | --- | --- | --- |
| `nvcfApi` | HTTPRoute | `api.<domain>` | `api.nvcf:8080` |
| `nvctApi` | HTTPRoute | `tasks.<domain>` | `nvct-api.nvcf:8080` |
| `apiKeys` | HTTPRoute | `api-keys.<domain>` | `api-keys.api-keys:8080` |
| `invocation` | HTTPRoute | `*.invocation.<domain>` and `invocation.<domain>` | `invocation.nvcf:8080` |
| `llmApiGateway` | HTTPRoute | `llm.<domain>` | `llm-api-gateway.nvcf:8080` |
| `llmInvocation` | HTTPRoute (disabled by default) | `llm.invocation.<domain>` | `llm-api-gateway.nvcf:8080` |
| `sis` | HTTPRoute | `sis.<domain>` | `api.sis:8080` |
| `grpc` | TCPRoute | `grpc.<domain>` | `grpc.nvcf:10081` |

Cross-namespace routing is supported via `ReferenceGrant` resources rendered into each backend namespace.

## Notes

- The chart assumes the Gateway is reachable at the resolved hostnames. DNS records and TLS termination are out of scope and must be configured in the surrounding infrastructure.
- The `grpc` TCPRoute does not enforce hostname matching at the Gateway layer; hostnames in values are documentation only.
