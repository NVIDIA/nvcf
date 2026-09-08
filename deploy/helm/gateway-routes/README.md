# NVCF Gateway Routes Helm Chart

This repository contains the Helm chart for deploying NVCF ingress routes via the Kubernetes Gateway API.

## Overview

The chart deploys `HTTPRoute`, `GRPCRoute`, `TCPRoute`, `UDPRoute`, and
`ReferenceGrant` resources that attach to an existing Gateway provisioned
separately by the cluster operator, such as Envoy Gateway, Istio, Traefik, or
Kong. It also reserves enabled HTTPRoute hostnames with a Kubernetes validating
admission policy. Secure LLM worker routing renders an optional cert-manager
`Certificate` and an Envoy Gateway `BackendTrafficPolicy` for long-lived gRPC
streams. The chart includes optional `PodMonitor` resources for scraping Envoy
Gateway proxy metrics with Prometheus.

The chart does not include any container images or create Gateways. Backend
services referenced by the routes (`api`,
`nvct-api`, `api-keys`, `invocation`, `llm-api-gateway`,
`llm-request-router-backend-router`, `vanity-gateway`, `reval`, `sis`, `grpc`,
`nats`) must already be deployed separately.

## Prerequisites

- Kubernetes 1.30 or later
- Helm 3.x
- `kubectl`
- A Gateway API compatible controller installed in the cluster
- Existing `Gateway` resources with the listeners required by each enabled route
- A Gateway controller with `GRPCRoute` and `UDPRoute` support when secure LLM
  worker routing is enabled
- cert-manager when `llmRequestRouter.grpcTls.mode=certManager`
- Envoy Gateway's `BackendTrafficPolicy` CRD when secure LLM worker routing is
  enabled
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
- `nvcfGatewayRoutes.gateways.nats.*` for the NATS TCP Gateway name, namespace, and listener
- `nvcfGatewayRoutes.gateways.llmGrpc.*` for the LLM worker gRPC HTTPS listener
- `nvcfGatewayRoutes.gateways.llmQuic.*` for the LLM reverse-tunnel UDP listener
- `llmRequestRouter.grpcTls.*` for the dedicated gRPC listener identity,
  explicit plaintext opt-in, and certificate ownership mode
- `nvcfGatewayRoutes.routes.<route>.enabled` to toggle individual routes
- `nvcfGatewayRoutes.hostnameConflictPolicy.enabled` to reject other
  HTTPRoutes that reuse an enabled chart route's hostname on the shared listener
- `nvcfGatewayRoutes.routes.nvcfApi.grpc.enabled` and
  `nvcfGatewayRoutes.routes.nvctApi.grpc.enabled` to expose API gRPC routes
- `nvcfGatewayRoutes.routes.<http-route>.hostnames` to override the templated HTTP route hostnames
- `nvcfGatewayRoutes.routes.<route>.backend.{name,namespace,port}` to point a route at the correct backend service
- `nvcfGatewayRoutes.routes.<route>.routeAnnotations` to add annotations consumed by external controllers (e.g. external-dns, cert-manager)
- `nvcfGatewayRoutes.podMonitors.enabled` to opt in to Envoy Gateway proxy `PodMonitor` resources

The default values use `localhost` as the domain and assume backend services are named consistently with NVCF defaults. Override these for any shared or production environment.

Enabled `HTTPRoute` entries must not share a resolved hostname because each `HTTPRoute` in this chart uses a root `PathPrefix /` match on the shared Gateway. Helm rendering fails if two enabled HTTPRoutes would claim the same hostname.

The hostname conflict policy is enabled by default. It rejects create and
update requests for another HTTPRoute that reuses a hostname reserved by this
chart on the configured shared Gateway listener. Disable it only when another
admission controller owns the same hostname policy.

## Routes

| Route | Kind | Default hostname | Backend |
| --- | --- | --- | --- |
| `nvcfApi` | HTTPRoute | `api.<domain>` | `api.nvcf:8080` |
| `nvcfApi.grpc` | GRPCRoute (disabled by default) | `api.<domain>` | `api.nvcf:9090` |
| `nvctApi` | HTTPRoute | `tasks.<domain>` | `nvct-api.nvcf:8080` |
| `nvctApi.grpc` | GRPCRoute (disabled by default) | `tasks.<domain>` | `nvct-api.nvcf:9090` |
| `apiKeys` | HTTPRoute | `api-keys.<domain>` | `api-keys.api-keys:8080` |
| `invocation` | HTTPRoute | `*.invocation.<domain>` and `invocation.<domain>` | `invocation.nvcf:8080` |
| `llmApiGateway` | HTTPRoute | `llm.<domain>` | `llm-api-gateway.nvcf:8080` |
| `llmInvocation` | HTTPRoute (disabled by default) | `llm.invocation.<domain>` | `llm-api-gateway.nvcf:8080` |
| `vanityGateway` | HTTPRoute (disabled by default) | `vanity.<domain>` | `vanity-gateway.nvcf:8080` |
| `reval` | HTTPRoute | `reval.<domain>` | `reval.nvcf:8080` |
| `sis` | HTTPRoute | `sis.<domain>` | `api.sis:8080` |
| `grpc` | TCPRoute | Not rendered | `grpc.nvcf:10081` |
| `grpcWorker` | TCPRoute (disabled by default) | Not rendered | `grpc.nvcf:10086` |
| `nats` | TCPRoute (disabled by default) | Not rendered | `nats.nats-system:4222` |
| `llmWorker` | GRPCRoute (secure) or TCPRoute (explicit development), plus UDPRoute; disabled by default | Not rendered | `llm-request-router-backend-router.<backend namespace>:50071/h2c,50072/UDP` |

Cross-namespace routing is supported via `ReferenceGrant` resources rendered into each backend namespace.

## Notes

- The chart assumes the Gateway is reachable at the resolved hostnames. DNS
  records and Gateway creation remain infrastructure responsibilities.
- The `nats` TCPRoute is plain TCP and does not render hostnames. Configure DNS or TCP load balancer routing outside this chart.
- The `grpc` TCPRoute does not enforce HTTP hostname matching at the Gateway layer. Configure DNS or TCP load balancer routing outside this chart.
- The `grpcWorker` TCPRoute is beta support for split or multi-cluster gRPC worker callbacks. It carries HTTP/1 CONNECT callback traffic only. Enable it only when the control-plane grpc-proxy runs one replica with HPA disabled. Multi-replica grpc-proxy requires pod-specific callback routing and is not supported by this shared TCPRoute.
- Enabling the `nats` route requires a reachable TCP listener for NATS on the referenced Gateway. The HTTP Gateway address does not imply NATS reachability unless that same Gateway also has the NATS TCP listener configured.
- The `llmWorker` routes target Stargate's authority/SNI-aware backend router.
  Set `nvcfGatewayRoutes.routes.llmWorker.backend.namespace` to the effective
  namespace of the `llm-request-router` release. The gateway chart cannot
  derive the namespace of a separate Helm release.
  Use `nvcfGatewayRoutes.routes.llmWorker.backend.grpcPort` for registration
  traffic and `nvcfGatewayRoutes.routes.llmWorker.backend.quicPort` for reverse
  tunnels; this route does not use the generic `backend.port` setting.
  Secure mode renders a `GRPCRoute` with no `hostnames` and requires a
  dedicated HTTPS listener with no `hostname`. The listener serves the
  configured certificate for normal external SNI verification, while the
  backend router receives the advertised Stargate identity as HTTP/2
  `:authority`. Setting the listener hostname to the public dial name would
  incorrectly require that same value in `:authority`.
  `grpcTls.mode=certManager` creates the named Secret through a dedicated
  `Certificate` in the gRPC Gateway namespace. `mode=existingSecret` expects
  the operator to create that Secret. The HTTPS listener must reference the
  same Secret. The Envoy `BackendTrafficPolicy` sets the request timeout to
  `0s` for Watch and Register streams.
  Plaintext is intended only for development and requires
  `grpcTls.allowInsecureHttp=true`; it renders the legacy `TCPRoute`.
  Keep the HTTPS and UDP Gateways separate when the infrastructure requires
  separate load balancers for each protocol.
