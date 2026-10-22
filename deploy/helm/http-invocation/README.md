# NVCF Invocation Service Helm Chart

This repository contains the Helm chart for deploying the NVCF Invocation Service on Kubernetes.

## Overview

The chart packages the Invocation Service deployment along with its Vault Agent sidecar configuration for fetching service credentials from a Vault or OpenBao backend.

The default chart values do not set the required image registry and repository. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
invocation:
  image:
    registry: <your-registry>
    repository: <your-org>/nvcf-invocation-service
    tag: <appVersion>
```

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`
- A reachable NVCF API endpoint
- A reachable NATS cluster
- A reachable Rate Limiter service
- A reachable Vault or OpenBao instance with a JWT authentication path configured for this service

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install nvcf-invocation-service nvcf-invocation-service \
  --namespace nvcf-invocation-service \
  --create-namespace \
  --values nvcf-invocation-service/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade nvcf-invocation-service nvcf-invocation-service \
  --namespace nvcf-invocation-service \
  --values nvcf-invocation-service/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall nvcf-invocation-service --namespace nvcf-invocation-service
```

## Configuration

The default chart configuration lives in `nvcf-invocation-service/values.yaml`.

Important settings to review before deployment:

- `invocation.image.*` for the invocation service container image
- `invocation.imagePullSecrets` for private registry access
- `invocation.replicaCount`, resource requests, and limits for your environment
- `invocation.env.*` for the NVCF API address, NATS connection settings, and Rate Limiter address
- `invocation.cors.allowedOrigins` for browser access to legacy exec/pexec routes
- `invocation.polling.enforceOwnership` for the staged polling ownership rollout
- `invocation.workerTls.*` for the optional HTTPS listener for worker callbacks
- `invocation.vault.*` for JWT authentication path, role, and audience values used by the Vault Agent injector

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

### Polling ownership and browser origins

Use an invocation-service image containing support for these settings.
Before deploying it, configure the exact browser origins that need access:

```yaml
invocation:
  cors:
    allowedOrigins:
      - https://console.example.com
      - https://other.example.com
  polling:
    enforceOwnership: false
```

The chart renders origins as comma-separated `CORS_ALLOWED_ORIGINS` and renders
the boolean as `ENFORCE_POLLING_OWNER`. An empty origin list disables browser
access to legacy routes. Explicit entries in `invocation.env` take precedence
over the corresponding settings. Both settings participate in the Deployment
checksum, so changes restart the invocation pods.

Keep enforcement false for the preparation release in every region. All new
invocations record caller ownership. Headerless records retain the existing
function authorization checks, and polling neither changes their owner nor
extends their expiry. Known owner mismatches and malformed metadata are rejected.

After every region and every producer writes ownership headers, wait longer
than the tracking stream's retention (60 minutes by default). Then set
`invocation.polling.enforceOwnership: true` in a subsequent release in every
region. Do not enforce while legacy NVCF producers still write headerless
records. Monitor increments of
`nvcf_http_invocation_polling_owner_checks_total{outcome="legacy_accepted"}`
for residual legacy polls. The preparation release alone does not provide full
caller ownership enforcement.

Validate the chart wiring locally with:

```bash
bash tests/access_policy_test.sh
```

### Worker callback TLS

Workers connect back to an individual invocation pod to stream responses. By
default that callback is plain HTTP on port 8080. Set
`invocation.workerTls.enabled: true` to also serve HTTPS on a separate port
(8443 by default) for worker callbacks:

```yaml
invocation:
  workerTls:
    enabled: true
    tls:
      certificate:
        enabled: true
        issuerRef:
          name: nvcf-worker-callback
```

With `tls.certificate.enabled`, the chart creates a cert-manager Certificate
for `*.invocation.<namespace>.svc.cluster.local`, which covers the per-pod
callback names. To use an existing `kubernetes.io/tls` Secret instead, leave the
certificate disabled and set `invocation.workerTls.tls.secretName`. The
service reloads the certificate from the mounted Secret without a restart.

While `invocation.workerTls.advertise` is true (the default), the chart sets
`WORKER_STREAM_PROPERTIES__SERVICE_ADDRESS` to
`https://invocation.<namespace>.svc.cluster.local:<port>`, so workers call back
over TLS. Workers must trust the issuing CA through `NVCF_PROXY_CA_FILE` or
`NVCF_PROXY_CA_PEM`. Set `advertise: false` to serve the listener before you
switch workers to it. An explicit `invocation.env` value for the callback
address takes precedence. The `WORKER_TLS__*` keys are reserved for this
setting.

Validate the chart wiring locally with:

```bash
bash tests/worker_tls_test.sh
```

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
