# NVCF NATS Helm Chart

This Helm chart provides an opinionated NATS deployment under the NVIDIA Cloud
Functions (NVCF) umbrella.

## Overview

The chart is designed to offer:

- Sensible Defaults: Pre-configured values that are well-suited for
    NVCF-managed deployments.
- NKey Generation: Helm templates generate NATS NKeys and store them in
    Kubernetes Secrets. The box mounts the client NKey and context.
- Dependency Management: Leverages the official NATS Helm chart as a
    dependency for the core NATS deployment, allowing users to benefit from
    upstream updates while layering NVCF defaults on top.

## Prerequisites

- Kubernetes cluster (version depends on the NATS chart version, typically
    1.20+)
- Helm (version 3+) installed
- `kubectl` configured to communicate with your cluster

## Installation

This chart is intended to be installed by invoking Helm directly from source.
For the full NVCF self-hosted flow, install it through the self-managed stack so
the NATS auth-callout service and OpenBao migrations are deployed in the
expected order. A direct Helm install renders NATS and the bootstrap Secrets,
but authenticated service traffic requires those downstream components.

### Install via Helm Directly

1. Build Dependencies:
    <!-- markdownlint-disable-next-line MD013 -->
    ```bash
    helm repo add nats https://nats-io.github.io/k8s/helm/charts/
    helm dependency build .
    ```

2. Install the Chart:
    <!-- markdownlint-disable-next-line MD013 -->
    ```bash
    helm install <release-name> . \
      --namespace <namespace> \
      --create-namespace \
      --values values.yaml
    ```

    For example:
    <!-- markdownlint-disable-next-line MD013 -->
    ```bash
    helm install nvcf-nats . \
      --namespace nats-system \
      --create-namespace \
      --values values.yaml
    ```

## Configuration

The chart can be configured by overriding values in the `values.yaml` file or
by providing a custom values file during installation
(`--values <your-values.yaml>`).

By default, the chart uses images from the public `docker.io` registry. If your
environment requires a different registry, mirror, or pull-secret setup,
set each component image registry and repository in your custom values file.
Explicit component registries take precedence over `nats.global.image.registry`.
Use `nats.global.image.pullSecretNames` for private-registry credentials.

Key configurable areas include:

- NATS server settings (via the `nats` subchart values)
- Secret-based NKey wiring for the bundled `natsBox` client context
- Auth callout NKey bootstrap via `nats.authCallout.*`
- Global settings like image registries and pull secrets (`nats.global.*`)
- Optional RBAC integration for external consumers of the generated NKey secret
  (`nats.rbac.openbao.*`)

Refer to the `values.yaml` file for a detailed list of configurable parameters
and their default values.

### Upstream images

NATS server uses `docker.io/synadia/nats-server`; the reloader uses
`docker.io/natsio/nats-server-config-reloader`. NATS box uses the nonroot
`docker.io/natsio/nats-box` image. Versions are pinned in `values.yaml`.
The box tag includes `@sha256:` and the multi-platform index digest, which
Helm preserves in the rendered image reference and release inventory.

[NATS box v0.20.1](https://github.com/nats-io/nats-box/releases/tag/v0.20.1)
is the selected box release. Its Docker Hub index includes `linux/amd64`
and `linux/arm64`; both image configs declare UID/GID `1000:1000`.
The chart also enforces that identity and keeps the NKeys/context mounts.
Registry metadata verifies image identity and platform availability; runtime
qualification must exercise `nsc` key generation and NATS CLI access on both
architectures.

For mirrors, set `registry` and `repository` under `nats.container.image`,
`nats.reloader.image`, and `nats.natsBox.container.image`. Copy all platforms
with `skopeo copy --all --preserve-digests`. Leave `tag` unset to keep the
selected pin. An explicit box `tag` replaces the complete version and digest;
use `version-nonroot@sha256:<mirrored-index-digest>` for different bytes.

### Auth Callout NKeys

The chart configures NATS auth_callout and generates a Kubernetes Secret named
`nats-auth-callout-nkeys` by default. The Secret contains `user_pub`,
`account_pub`, `nkey_seed`, and `nkey_signature`; OpenBao migrations mirror the
private seed fields into Vault for the auth-callout service.

Set `nats.authCallout.secretName` to use a different Secret name. If
`nats.authCallout.createNkeySecret=false`, this chart will not render the
Secret, but NATS and the OpenBao RBAC will still reference
`nats.authCallout.secretName`. In that mode, pre-provision the Secret before
installing this chart.

Do not commit rendered manifests from this chart. `helm template`, CI logs,
Helm release storage, and Kubernetes Secret access can expose the generated
private NKey seed material.

## Uninstallation

This chart can be uninstalled with standard `helm` commands. No data will be
preserved or backed up during this process.

<!-- markdownlint-disable-next-line MD013 -->
```bash
helm uninstall <release-name> --namespace <namespace>
```

Example:
<!-- markdownlint-disable-next-line MD013 -->
```bash
helm uninstall nvcf-nats --namespace nats-system
```
