# NVCF LLM Request Router Helm Chart

> [!IMPORTANT]
> Active development of the helm-nvcf-llm-request-router chart has moved to
> the NVCF umbrella monorepo. This repository is retained as
> historical source context only; new commits, issues, and merge
> requests should target the umbrella.
>
> Internal: https://github.com/NVIDIA/nvcf/nvcf/-/tree/main/deploy/helm/llm-request-router
> Public mirror: https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/llm-request-router

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
- `llmRequestRouter.pki.*` to provision the OpenBao service-issuing PKI hierarchy that cert-manager mints the Certificate from. Opt-in via `pki.enabled=true`. Mirrors the SIS chart's `hook-lls-migrations.yaml` pattern: a Helm pre-install/pre-upgrade Job runs the `nvcf-openbao-migrations` image with `CORE_MIGRATIONS_ENABLED=false` + `ADDONS_LLM_ENABLED=true` so only the LLM addon executes. `pki.allowedDomains` (comma-separated DNS suffixes) is required when enabled and is the OpenBao PKI role's `allowed_domains` security constraint — typically `<customer-domain>,cluster.local`. Job-level fail-hard is handled by `restartPolicy: OnFailure` + `pki.backoffLimit` combined with the migrations image's `FAILED_MIGRATIONS` accumulator (image `>= 0.12.1`).
- `llmRequestRouter.vault.audience` for the projected ServiceAccount token audience used to authenticate to OpenBao
- `llmRequestRouter.vault.noVaultAnnotations` to disable Vault Agent injection (useful for local testing without OpenBao)

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

## Load Balancer Configuration

The chart can pass a Stargate load-balancer config in either of two ways:

- `llmRequestRouter.loadBalancer.config` embeds JSON directly in the release. The chart writes it to a ConfigMap and starts Stargate with `--lb-config-path=/etc/llm-request-router/lb-config.json`.
- `llmRequestRouter.loadBalancer.configPath` points Stargate at an existing file path and starts it with `--lb-config-path=<configPath>`.

`config` takes precedence over `configPath` when both are set. If neither value is set, Stargate uses its built-in default algorithm, `power-of-two`.

Example:

```yaml
llmRequestRouter:
  loadBalancer:
    config: |
      {
        "default": "power-of-two",
        "models": {
          "dummy-model": {
            "algorithm": "groq-multiregion",
            "seed": "local-sticky-v1",
            "require_cache_affinity_key": true,
            "cache_affinity_virtual_nodes": 64,
            "cache_affinity_backend_selection_count": 1
          }
        }
      }
```

Supported routing algorithms:

- `power-of-two`, the default; samples two candidates and picks the one with more input-TPS headroom.
- `groq-multiregion`; estimates time-to-first-token from RTT and queue/input-token work, and can use `x-cache-affinity-key` for a stable per-key backend subset.
- `round-robin`; cycles through candidates sequentially.
- `random`; picks a candidate uniformly.
- `pulsar`; uses weighted rendezvous hashing and KV-cache feasibility gates. PULSAR requires suitable backend KV metrics for full behavior, so use it intentionally rather than as the default local E2E route.

`x-cache-affinity-key` is the router-facing sticky-cache header. It is an opaque stable key used by `groq-multiregion` and `pulsar`; Stargate does not derive it from request bodies.

## Local Render

```bash
helm template llm-request-router llm-request-router
```

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
