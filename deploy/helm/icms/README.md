# ICMS API Helm Chart

Helm chart for deploying the NVCF Instance Cluster Management Service (ICMS) API
on Kubernetes. The paired image source is
`src/control-plane-services/instance-cluster-management`.

The chart publishes as `helm-nvcf-sis` and its value keys are namespaced under
`sis.*`. That name predates the rename from Spot Instance Service to ICMS. It is
kept so the published chart continues its existing version lineage and so
existing override files keep working; the ServiceAccount name and the OpenBao
JWT role are `sis-api` for the same reason. Renaming those is a coordinated
change across the service and the OpenBao configuration, not a chart-only edit.

## Overview

The chart packages the ICMS API deployment along with its Vault Agent sidecar configuration for fetching service credentials from a Vault or OpenBao backend, and includes a credential-rotation Job that refreshes signing material on a schedule.

The default chart values do not set the required image registry and repository. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
sis:
  image:
    registry: <your-registry>
    repository: <your-org>/icms-api
    tag: <image-tag>
```

Set `sis.image.tag` explicitly rather than relying on `Chart.appVersion`. The
chart release pipeline rewrites `appVersion` to the chart version when it
publishes, so a published chart's `appVersion` is not the application version.

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
helm install icms-api icms-api \
  --namespace nvcf \
  --create-namespace \
  --values icms-api/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade icms-api icms-api \
  --namespace nvcf \
  --values icms-api/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall icms-api --namespace nvcf
```

## Configuration

The default chart configuration lives in `icms-api/values.yaml`.

Important settings to review before deployment:

- `sis.image.*` for the ICMS API container image
- `sis.imagePullSecrets` for private registry access
- `sis.replicaCount`, resource requests, and limits for your environment
- `sis.config.*` for the NVCF FQDN, Cassandra contact points, and authentication issuer URLs
- `sis.rotation.image.*` for the credential rotation Job image
- `sis.vault.audience` for the OpenBao JWT audience used by the Vault Agent injector (the auth path and role are fixed by the chart)

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

When `sis.lls.hmacRotation.image.digest` is set, the chart renders both LLS
workloads with `registry/repository@digest`; the digest takes precedence over
the tag. `icms-api/values.versions.yaml` pins the multi-architecture OCI index
for the configured migration-image tag.

## Remote Config RBAC

`spring-cloud-kubernetes` (5.0.2, shipped in icms-service `1.2.x`) calls `listNamespacedConfigMap` without a `fieldSelector=metadata.name` filter and matches the target name in memory. Because the API request is unfiltered, the Role must grant `list`/`watch` on the configmaps collection itself, with no `resourceNames` scoping for those verbs, so the `sis-api` SA can read every ConfigMap in the namespace. Chart assumes none are sensitive; clusters that block broad namespace reads need a policy exception.

Set `sis.remoteConfig.enabled: false` to opt out: the chart no longer renders the broad RBAC, and the service runs on JAR defaults in `application-ncp.yaml`. The SA keeps default-token automount on (the K8s default); with remote config disabled the token is simply unused. Hot reload is unavailable.

## Upgrade: `sis.volumes` / `sis.volumeMounts` renamed

The OpenBao token volume and mount are now chart-owned (rendered in `deployment.yaml`)
so a list override can't accidentally drop the vault-agent wiring. Two keys were renamed:

- `sis.volumes` becomes `sis.extraVolumes`
- `sis.volumeMounts` becomes `sis.extraVolumeMounts`

They still append your own entries after the chart-owned ones. Before upgrading, move any
custom entries to the new keys. The chart now fails rendering (rather than silently
dropping them) if the old keys are set. Don't redefine the chart-owned `openbao-token` or
`vault-config-templates` volumes in the `extra*` lists.

If you had overridden the token volume inline, drop it, since it is fully chart-owned now:

- volume `token` becomes `openbao-token`
- mount path `/var/run/secrets/kubernetes.io/serviceaccount` becomes `/var/run/secrets/openbao/serviceaccount`
- a custom `audience` now goes in `sis.vault.audience`

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
