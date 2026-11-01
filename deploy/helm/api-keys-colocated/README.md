# NVCF API Keys Helm Chart

This repository contains the Helm chart for deploying the NVCF API Keys service on Kubernetes.

## Overview

The chart packages the API Keys service deployment along with its Vault Agent sidecar configuration for fetching encryption keys, JWE key mapping, and service registration data from a Vault or OpenBao backend.

The default chart values do not set the required image registry and repository. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
apikeys:
  image:
    registry: <your-registry>            # e.g. nvcr.io
    repository: <your-org>/<your-team>/nvcf-api-keys-service
    tag: <appVersion>
```

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`
- A reachable Cassandra cluster
- A reachable Vault or OpenBao instance with a JWT authentication path configured for this service

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install api-keys api-keys \
  --namespace api-keys \
  --create-namespace \
  --values api-keys/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade api-keys api-keys \
  --namespace api-keys \
  --values api-keys/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall api-keys --namespace api-keys
```

## Configuration

The chart's `api-keys/values.yaml` carries minimal defaults; most service config (Cassandra, service IDs, NCA ID, key prefix, tracing) lives in the image's `ncp` Spring profile.

Settings to review before deployment:

- `apikeys.image.*` for the API Keys container image
- `apikeys.imagePullSecrets` for private registry access
- `apikeys.replicaCount`, resource requests, and limits for your environment
- `apikeys.env.HOSTNAME` / `apikeys.env.AWS_REGION` — observability tags (`host_id`, `host_dc`); keep stable for consistent metrics.
- `apikeys.env.SPRING_PROFILES_ACTIVE` — defaults to `ncp`; do not change unless using a different bundled profile.
- The chart's Vault Agent annotations and `configmap-vault-agent-template` for the Vault Agent sidecar's JWT auth path, role, and audience values (defaults set in `templates/_helpers.tpl`)

To override image-profile defaults (Cassandra, `apikeys.nca-id`, `apikeys.service-id-map.*`, etc), set the corresponding env var under `apikeys.env.*` — Spring relaxed binding maps env vars to Spring properties.

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
- The Vault secret payload supplied to the service must include encryption keys (`private-key-jwks`), the JWE key mapping (`jwe-key-mapping`), the data domain key (`data-domain-key`), the Cassandra credentials (`cassandra`), and the consumer registration array (`registrations.services`). Once a key id is in use it must not be removed: records encrypted with it become impossible to decrypt.
