# NVCF Spot Instance Service Helm Chart

This repository contains the Helm chart for deploying the NVCF Spot Instance Service (SIS) on Kubernetes.

## Overview

The chart packages the SIS deployment along with its Vault Agent sidecar configuration for fetching service credentials from a Vault or OpenBao backend, and includes a credential-rotation Job that refreshes signing material on a schedule.

The default chart values do not set the required image registry and repository. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
sis:
  image:
    registry: <your-registry>
    repository: <your-org>/sis
    tag: <appVersion>
```

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`
- A reachable Cassandra cluster
- A reachable NVCF API endpoint
- A reachable Vault or OpenBao instance with a JWT authentication path configured for this service

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install sis sis \
  --namespace sis \
  --create-namespace \
  --values sis/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade sis sis \
  --namespace sis \
  --values sis/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall sis --namespace sis
```

## Configuration

The default chart configuration lives in `sis/values.yaml`.

Important settings to review before deployment:

- `sis.image.*` for the SIS container image
- `sis.imagePullSecrets` for private registry access
- `sis.replicaCount`, resource requests, and limits for your environment
- `sis.config.*` for the NVCF FQDN, Cassandra contact points, and authentication issuer URLs
- `sis.rotation.image.*` for the credential rotation Job image
- `sis.vault.audience` for the OpenBao JWT audience used by the Vault Agent injector (the auth path and role are fixed by the chart)

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

## Remote Config RBAC

`spring-cloud-kubernetes` (5.0.2, shipped in icms-service `1.2.x`) calls `listNamespacedConfigMap` without a `fieldSelector=metadata.name` filter and matches the target name in memory. Because the API request is unfiltered, the Role must grant `list`/`watch` on the configmaps collection itself — no `resourceNames` scoping for those verbs — so the `sis-api` SA can read every ConfigMap in the namespace. Chart assumes none are sensitive; clusters that block broad namespace reads need a policy exception.

Set `sis.remoteConfig.enabled: false` to opt out: the chart no longer renders the broad RBAC, and the service runs on JAR defaults in `application-ncp.yaml`. The SA keeps default-token automount on (the K8s default); with remote config disabled the token is simply unused. Hot reload is unavailable.

## Upgrade: `sis.volumes` / `sis.volumeMounts` renamed

The OpenBao token volume and mount are now **chart-owned** (rendered in `deployment.yaml`)
so a list override can't accidentally drop the vault-agent wiring. Two keys were renamed:

- `sis.volumes` → `sis.extraVolumes`
- `sis.volumeMounts` → `sis.extraVolumeMounts`

They still append your own entries after the chart-owned ones. Before upgrading, move any
custom entries to the new keys — the chart now **fails rendering** (rather than silently
dropping them) if the old keys are set. Don't redefine the chart-owned `openbao-token` or
`vault-config-templates` volumes in the `extra*` lists.

If you had overridden the token volume inline, drop it — it's fully chart-owned now:

- volume `token` → `openbao-token`
- mount path `/var/run/secrets/kubernetes.io/serviceaccount` → `/var/run/secrets/openbao/serviceaccount`
- a custom `audience` now goes in `sis.vault.audience`

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
