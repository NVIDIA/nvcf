# NVCF LLM Request Router Helm Chart

This repository contains the Helm chart for deploying the NVCF LLM Request Router (Stargate) on Kubernetes.

## Overview

The chart packages the LLM Request Router as either a Deployment or a
StatefulSet, with Deployment as the default. It includes HTTP and gRPC
services, a metrics endpoint, and a headless service. It can also deploy the
Stargate Kubernetes backend router for worker gRPC registration and reverse
QUIC tunnels through a shared Gateway or load balancer. The backend router
selects the correct Stargate pod from gRPC authority and QUIC SNI.

A Vault Agent sidecar is configured to fetch a service token from a Vault or
OpenBao backend. The application reads `nvcfApiToken` from
`/vault/secrets/secrets.json` and attaches it as a Bearer token to outgoing
worker authentication gRPC calls.

The default chart values do not set the required image registry and repository. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
llmRequestRouter:
  image:
    registry: <your-registry>
    repository: <your-org>/llm-request-router
    tag: <appVersion>
```

Single-replica Deployments automatically use self-only discovery so their
headless Service cannot introduce dashed-IP SRV aliases. A multi-replica
Deployment requires the backend router. The default `null` value for
`llmRequestRouter.backendRouter.enabled` enables it automatically in that
topology; explicitly setting `false` is rejected. The backend router builds
Watch responses and forwarding routes from the same
EndpointSlice snapshot. Each ready endpoint is keyed by its Pod
`targetRef.name`, so one pod produces one canonical identity even when DNS also
exposes a dashed-IP SRV alias. A multi-replica StatefulSet can instead run
without the backend router and retain direct headless Service SRV discovery.
`llmRequestRouter.discovery.watchHeartbeatMs` controls the maximum interval
between unchanged Watch snapshots from both Stargate and the backend router.

`llmRequestRouter.kubernetes.advertisedHostnameTemplate` supports the Stargate
placeholders `{pod_name}` and `{namespace}`. Stargate resolves both placeholders
at runtime. For certificate validation, the chart substitutes the deployment
namespace and a representative pod name. `{pod_name}` must stay within the
leftmost DNS label when certificate coverage relies on a wildcard. When
`llmRequestRouter.certificate.enabled=true`, `certificate.dnsNames` must cover
the advertised hostname with either a case-insensitive exact name or a valid
leftmost `*.` wildcard. A wildcard covers exactly one label and requires at
least two suffix labels. For example, `*.nvcf.example.internal` covers
`{pod_name}.nvcf.example.internal`, but `*.example.internal` does not cover
`{pod_name}.nvcf.example.internal`.

Existing installations that currently run the StatefulSet must set
`llmRequestRouter.workload.kind=StatefulSet` before upgrading to this chart.
Changing `workload.kind` is a controlled migration, not an in-place Kubernetes
mutation. Plan a maintenance window, remove or rename the old workload, and
verify that only the selected kind owns the request-router Pods before scaling
it. A plain Helm upgrade across workload kinds can briefly run both workloads.

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`
- A reachable Vault or OpenBao instance with a JWT authentication path configured for this service (or set `llmRequestRouter.vault.noVaultAnnotations: true` to disable Vault Agent injection)

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install llm-request-router llm-request-router \
  --namespace llm-request-router \
  --create-namespace \
  --values llm-request-router/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade llm-request-router llm-request-router \
  --namespace llm-request-router \
  --values llm-request-router/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall llm-request-router --namespace llm-request-router
```

## Configuration

The default chart configuration lives in `llm-request-router/values.yaml`.

Important settings to review before deployment:

- `llmRequestRouter.image.*` for the router container image
- `llmRequestRouter.imagePullSecrets` for private registry access
- `llmRequestRouter.workload.kind` to select `Deployment` (default) or `StatefulSet`
- `llmRequestRouter.workload.deployment.strategy` and `llmRequestRouter.workload.statefulSet.*` for workload-specific rollout settings
- `llmRequestRouter.replicaCount`, resource requests, and limits for your environment
- `llmRequestRouter.service.*` for HTTP, gRPC, metrics, and headless service ports
- `llmRequestRouter.backendRouter.*` for multi-replica worker gRPC and reverse-tunnel routing
- `llmRequestRouter.metrics.enabled` to expose the metrics port on the Service (default: `false`)
- `llmRequestRouter.metrics.serviceMonitor.enabled` to create a Prometheus `ServiceMonitor` (requires `metrics.enabled`)
- `llmRequestRouter.certificate.*` to let cert-manager issue the Stargate QUIC server certificate
- `llmRequestRouter.tls.*` to mount the TLS Secret and pass cert/key paths to Stargate
- `llmRequestRouter.tls.mode` to choose the source of the QUIC server identity. `certManager` (default) mounts the Secret cert-manager writes for `certificate.*`. `existingSecret` mounts a pre-created Secret instead: the chart renders no `Certificate` and adds no issuer dependency, `certificate.enabled` must stay `false`, `tls.secretName`, `tls.certPath`, and `tls.keyPath` are required, and the operator owns issuance, renewal, rotation, and recovery. The Secret must provide the `tls.crt` and `tls.key` entries. The chart cannot read a pre-created Secret, so it does not validate its SANs or expiry.
- `llmRequestRouter.pki.*` to provision the OpenBao service-issuing PKI hierarchy that cert-manager mints the Certificate from. Opt-in via `pki.enabled=true`. Mirrors the SIS chart's `hook-lls-migrations.yaml` pattern: a Helm pre-install/pre-upgrade Job runs the `nvcf-openbao-migrations` image with `CORE_MIGRATIONS_ENABLED=false` + `ADDONS_LLM_ENABLED=true` so only the LLM addon executes. `pki.allowedDomains` (comma-separated DNS suffixes) is required when enabled and is the OpenBao PKI role's `allowed_domains` security constraint. Typically this is `<customer-domain>,cluster.local`. Job-level fail-hard is handled by `restartPolicy: OnFailure` + `pki.backoffLimit` combined with the migrations image's `FAILED_MIGRATIONS` accumulator (image `>= 0.12.1`).
- `llmRequestRouter.vault.audience` for the projected ServiceAccount token audience used to authenticate to OpenBao
- `llmRequestRouter.vault.noVaultAnnotations` to disable Vault Agent injection (useful for local testing without OpenBao)

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

## Backend Worker Routing

The backend router is enabled automatically for a multi-replica Deployment.
Set `llmRequestRouter.backendRouter.enabled=true` explicitly when workers reach
another supported workload topology through a shared endpoint. Set both pylon
dial addresses to the external endpoints that workers can resolve:

```yaml
llmRequestRouter:
  backendRouter:
    enabled: true
    pylonGrpcDialAddress: llm-router.example.com:443
    pylonReverseTunnelDialAddress: llm-router.example.com:8080
```

The chart uses the main Stargate image for both workloads. The backend router
inherits the main image registry, repository, and tag, falling back to the
chart appVersion. That image must contain
`/usr/local/bin/stargate-k8s-router`; override `backendRouter.image.*` only to
validate a different Stargate build.

The backend router watches EndpointSlices and publishes those ready targets
directly through `WatchStargates`. It uses the same snapshot for gRPC and QUIC
forwarding, so a removed or replaced Pod cannot remain as a discovery-only
target. The chart creates a dedicated ServiceAccount by default and binds a
namespaced Role to it when
`llmRequestRouter.rbac.create=true`. When
`llmRequestRouter.backendRouter.serviceAccount.create=false`, set
`llmRequestRouter.backendRouter.serviceAccount.name` to an existing account.
When `rbac.create=false`, grant `get`, `list`, and `watch` on
`discovery.k8s.io/endpointslices` to that account outside this chart.

Route TCP port `50071` and UDP port `50072` to the
`llm-request-router-backend-router` Service. The NVCF gateway-routes chart can
create the matching `TCPRoute`, `UDPRoute`, and `ReferenceGrant` resources.
The Gateway implementation must support Gateway API `UDPRoute`.

When QUIC verification is enabled, the mounted certificate must cover the
advertised per-pod hostname produced by
`llmRequestRouter.kubernetes.advertisedHostnameTemplate`. The default template
is `{pod_name}.llm-request-router-headless.<namespace>.svc.cluster.local`. The
external `pylonReverseTunnelDialAddress` selects the UDP network path and needs
a SAN only when the advertised identity is also changed to that hostname.

Stargate and the backend router poll the mounted TLS certificate and key and
reload the server identity for new connections. Client trust-bundle changes
are not hot-reloaded; roll out workers and other Pylon clients after changing
the CA bundle they use to verify the router.

## Load Balancer Configuration

The chart can pass a Stargate load-balancer config in either of two ways:

- `llmRequestRouter.loadBalancer.config` embeds JSON directly in the release. The chart writes it to a ConfigMap and starts Stargate with `--lb-config-path=/etc/llm-request-router/lb-config.json`.
- `llmRequestRouter.loadBalancer.configPath` points Stargate at an existing file path and starts it with `--lb-config-path=<configPath>`.

`config` takes precedence over `configPath` when both are set. If neither value is set, Stargate uses its built-in default algorithm, `power-of-two`.

See the
[Stargate load balancer configuration](../../../src/libraries/rust/stargate/docs/load-balancer-configuration.md)
for the JSON schema, algorithm behavior, and tuning fields. See
[LLM Request Router Load Balancing](../../../docs/user/llm-request-router-load-balancing.md)
for stack ownership, trusted headers, rollout checks, and troubleshooting.

## Local Render

```bash
helm template llm-request-router llm-request-router
```

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
