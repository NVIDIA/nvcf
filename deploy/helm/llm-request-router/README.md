# NVCF LLM Request Router Helm Chart

This repository contains the Helm chart for deploying the NVCF LLM Request Router (Stargate) on Kubernetes.

## Overview

The chart packages the LLM Request Router StatefulSet with HTTP and gRPC services, a metrics endpoint, and a headless service for multi-instance DNS discovery. A Vault Agent sidecar is configured to fetch a service token from a Vault or OpenBao backend; the application reads `nvcfApiToken` from `/vault/secrets/secrets.json` and attaches it as a Bearer token to outgoing worker authentication gRPC calls.

The default chart values do not set the required image registry and repository. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
llmRequestRouter:
  image:
    registry: <your-registry>
    repository: <your-org>/llm-request-router
    tag: <appVersion>
```

Single-replica deployments may use self-only discovery with `llmRequestRouter.discovery.disableDnsDiscovery=true`. Multi-replica deployments require DNS discovery and stable per-pod identity, so the chart fails rendering if DNS discovery is disabled while `llmRequestRouter.replicaCount > 1`. For multi-replica deployments, the default advertised hostname template is `{pod_name}.<headless-service>.<namespace>.svc.cluster.local`; the StatefulSet and headless service provide the stable pod DNS names required for router replicas to discover each other and share backend registrations.

Upgrading from a chart version that rendered a Deployment can briefly run both the old Deployment and new StatefulSet during `helm upgrade` while Helm replaces the workload kind.

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
- `llmRequestRouter.replicaCount`, resource requests, and limits for your environment
- `llmRequestRouter.service.*` for HTTP, gRPC, metrics, and headless service ports
- `llmRequestRouter.metrics.enabled` to expose the metrics port on the Service (default: `false`)
- `llmRequestRouter.metrics.serviceMonitor.enabled` to create a Prometheus `ServiceMonitor` (requires `metrics.enabled`)
- `llmRequestRouter.certificate.*` to let cert-manager issue the Stargate QUIC server certificate
- `llmRequestRouter.tls.*` to mount the issued TLS Secret and pass cert/key paths to Stargate
- `llmRequestRouter.pki.*` to provision the OpenBao service-issuing PKI hierarchy that cert-manager mints the Certificate from. Opt-in via `pki.enabled=true`. Mirrors the SIS chart's `hook-lls-migrations.yaml` pattern: a Helm pre-install/pre-upgrade Job runs the `nvcf-openbao-migrations` image with `CORE_MIGRATIONS_ENABLED=false` + `ADDONS_LLM_ENABLED=true` so only the LLM addon executes. `pki.allowedDomains` (comma-separated DNS suffixes) is required when enabled and is the OpenBao PKI role's `allowed_domains` security constraint. Typically this is `<customer-domain>,cluster.local`. Job-level fail-hard is handled by `restartPolicy: OnFailure` + `pki.backoffLimit` combined with the migrations image's `FAILED_MIGRATIONS` accumulator (image `>= 0.12.1`).
- `llmRequestRouter.vault.audience` for the projected ServiceAccount token audience used to authenticate to OpenBao
- `llmRequestRouter.vault.noVaultAnnotations` to disable Vault Agent injection (useful for local testing without OpenBao)

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

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
