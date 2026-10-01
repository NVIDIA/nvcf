# NVCF LLM API Gateway Helm Chart

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
- `llmApiGateway.auth.*` for how callers authenticate. See [Authentication, Vault and TLS](#authentication-vault-and-tls)
- `llmApiGateway.config.nvcfGrpc*` for the NVCF gRPC auth service used in `nvcf` mode
- `llmApiGateway.metrics.enabled` to expose a metrics port on the Service and Deployment (default: `false`)
- `llmApiGateway.metrics.serviceMonitor.enabled` to create a Prometheus `ServiceMonitor` (requires `metrics.enabled`)
- `llmApiGateway.olric.*` for embedded rate-limit state and peer discovery
- `llmApiGateway.vault.*` to turn the Vault Agent off (`vault.enabled`) and for the JWT authentication path, role, and audience values used by the Vault Agent injector
- `llmApiGateway.tls.*` to serve the listener over TLS

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

## Authentication, Vault and TLS

The gateway fails closed: it starts only with an authenticator or with
anonymous access explicitly allowed. `llmApiGateway.auth.mode` selects one:

| Mode | Gateway env | Needs |
| --- | --- | --- |
| `nvcf` (default) | `NVCF_GRPC_ADDR`, `NVCF_GRPC_INSECURE`, `NVCF_GRPC_TIMEOUT`, `SECRETS_PATH` | The NVCF LLM gRPC auth service and the Vault Agent token |
| `staticKeys` | `API_KEYS_PATH`, `STATIC_ALLOWED_PATHS` | A Secret with key `api-keys.json` |
| `anonymous` | `ALLOW_ANONYMOUS=true` | Nothing. Development only |

`nvcf` mode renders exactly what earlier chart versions rendered. The other
modes do not set `NVCF_GRPC_ADDR`, because the gateway refuses to start with
both `NVCF_GRPC_ADDR` and `API_KEYS_PATH`. An unknown mode, `staticKeys` without
a Secret, and `tls.enabled` without a Secret fail the render.

| Value | Default | Description |
| --- | --- | --- |
| `llmApiGateway.auth.mode` | `nvcf` | `nvcf`, `staticKeys` or `anonymous` |
| `llmApiGateway.auth.staticKeys.existingSecret` | `""` | Secret with key `api-keys.json`, mounted read-only at `/etc/llm-api-gateway/auth`. Required in `staticKeys` mode |
| `llmApiGateway.auth.staticKeys.allowedPaths` | `["/v1/chat/completions"]` | Request paths served in `staticKeys` mode, comma-joined into `STATIC_ALLOWED_PATHS`. Other paths return 403 |
| `llmApiGateway.auth.serviceToken.existingSecret` | `""` | Optional Secret holding the bearer the gateway sends to the request router, passed as `STARGATE_SERVICE_TOKEN` |
| `llmApiGateway.auth.serviceToken.key` | `token` | Key of that Secret |
| `llmApiGateway.vault.enabled` | `true` | Vault Agent annotations, the `vault-token` and `vault-config-templates` volumes, and the agent template ConfigMap |
| `llmApiGateway.vault.noVaultAnnotations` | unset | Legacy: drops only the Vault Agent annotations and keeps the volumes |
| `llmApiGateway.tls.enabled` | `false` | Serve the listener over TLS. Probes switch to HTTPS |
| `llmApiGateway.tls.existingSecret` | `""` | `kubernetes.io/tls` Secret mounted read-only at `/etc/llm-api-gateway/tls`, passed as `TLS_CERT_FILE` and `TLS_KEY_FILE`. Required when TLS is enabled |

The key file holds SHA-256 digests, never the keys:

```bash
digest="$(printf '%s' "${API_KEY}" | shasum -a 256 | cut -d' ' -f1)"
kubectl -n nvcf create secret generic llm-api-gateway-api-keys \
  --from-literal=api-keys.json="{\"keys\": [{\"id\": \"team-a\", \"sha256\": \"${digest}\"}]}"
```

A static-key install without Vault or the NVCF API:

```yaml
llmApiGateway:
  auth:
    mode: staticKeys
    staticKeys:
      existingSecret: llm-api-gateway-api-keys
  vault:
    enabled: false
  tls:
    enabled: true
    existingSecret: llm-api-gateway-tls
```

The gateway re-reads the key file at most every 60 seconds, so key changes
need no restart. It reads the TLS files at startup, so a renewed certificate
takes effect on the next pod restart. With Vault enabled, every mode also
reads an optional tracing token from `SECRETS_PATH`. The
`llm-gateway-stack` chart wires this mode together with the request router,
generated Secrets and a self-signed CA.

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
