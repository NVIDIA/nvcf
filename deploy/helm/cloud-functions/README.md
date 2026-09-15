# NVCF API Helm Chart

This directory contains the Helm chart for deploying the NVCF API service on Kubernetes.

## Overview

The chart packages the NVCF API deployment together with a post-install account bootstrap hook.

The default chart values do not set the required image registries and repositories for the API or the account bootstrap job. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
api:
  image:
    registry: <your-registry>
    repository: <your-org>/nvcf-api
    tag: <appVersion>
  accountBootstrap:
    image:
      registry: <your-registry>
      repository: <your-org>/nvcf-account-bootstrap
      tag: <version>
```

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install api nvcf-api \
  --namespace nvcf \
  --create-namespace \
  --values nvcf-api/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --wait-for-jobs \
  --timeout 20m
```

Upgrade an existing release:

```bash
helm upgrade api nvcf-api \
  --namespace nvcf \
  --values nvcf-api/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --wait-for-jobs \
  --timeout 20m
```

Uninstall the release:

```bash
helm uninstall api --namespace nvcf
```

The `Makefile` wraps these with the same defaults (`release=api`, `namespace=nvcf`):

```bash
make install additional_values=path/to/values.yaml
make status
make uninstall
```

## Configuration

The default chart configuration lives in `nvcf-api/values.yaml`.

Important settings to review before deployment:

- `api.image.*` for the API container image
- `api.accountBootstrap.image.*` for the post-install bootstrap job image
- `api.imagePullSecrets` for private registry access
- `api.replicaCount`, resource requests, and HPA settings for your environment
- `api.accountBootstrap.accountName`, `api.accountBootstrap.adminClientId`, and `api.accountBootstrap.registryCredentials` for initial account configuration. Use `registryCredentials: []` to create the account without system-provisioned registry credentials.
- `api.accountBootstrap.limits.*` for per-account quotas

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

## Remote Config RBAC

`spring-cloud-kubernetes` (v3.3.0, shipped in nvcf-service `1.3.x`) calls `listNamespacedConfigMap` without a `fieldSelector=metadata.name` filter and matches the target name in memory. Because the API request is unfiltered, the Role must grant `list`/`watch` on the configmaps collection itself, with no `resourceNames` scoping for those verbs, so the `nvcf-api` SA can read every ConfigMap in the namespace. Chart assumes none are sensitive; clusters that block broad namespace reads need a policy exception.

Set `api.remoteConfig.enabled: false` to opt out: the chart no longer renders the broad RBAC, and the service runs on JAR sidecar defaults in `application-ncp.yaml`. The SA keeps default-token automount on (the K8s default) so the vault-k8s injector's fallback service-account discovery continues to find a mount. Hot reload is unavailable.

## Sidecar release-artifact annotations

The `api.remoteConfig.configData.nvcf.sidecars` worker images reference the
registry via Spring placeholders (`${nvcf.sidecars.hostname}/${nvcf.sidecars.repository}`)
that resolve at runtime from `NVCF_SIDECARS_HOSTNAME` / `NVCF_SIDECARS_REPOSITORY`.
Stack release-artifact tooling scans rendered manifests for concrete image
references and skips any value containing `${...}`, so those worker images would
otherwise be excluded from stack release artifacts.

To surface them, the remote-config ConfigMap renders a
`release-artifact-<sidecar>-image` annotation for every sidecar entry that carries
the placeholder prefix, with the prefix resolved to
`<NVCF_SIDECARS_HOSTNAME>/<NVCF_SIDECARS_REPOSITORY>/<image>:<tag>`. This mirrors
the `release-artifact-*-image` annotations rendered by nvca-operator's
`self-managed-nvcfbackend-cm.yaml`. Annotations are emitted only when both sidecar
registry env vars are set, so partially resolved references are never produced.

## Account bootstrap

The chart installs a Helm `post-install` hook Job that calls the API to create an initial NVCF account. The Job waits for the API readiness probe to pass, authenticates to the configured secret store, and issues a single `POST /v2/nvcf/accounts/{ncaId}` request.

Key points to be noted:

- The Job is rendered from `nvcf-api/templates/account-bootstrap-hook-job.yaml` and configured by `api.accountBootstrap.*` in `values.yaml`.
- The bootstrap script lives at `nvcf-api/scripts/account-bootstrap.sh`.
- `registryCredentials: []` is valid and omits the `registryCredentials` field from the account payload.
- Non-empty `registryCredentials` may include `CONTAINER` and `HELM` entries. `MODEL` and `RESOURCE` entries are ignored if supplied.
- Set `DEBUG=true` in the Job environment to enable verbose logging.

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
