# NVCF NATS Helm Chart

This Helm chart provides an opinionated NATS deployment under the NVIDIA Cloud
Functions (NVCF) umbrella.

## Overview

The chart is designed to offer:

- **Sensible Defaults**: Pre-configured values that are well-suited for
    NVCF-managed deployments.
- **NKey Generation**: Convenience hooks and a Kubernetes Job to
    automatically generate NATS NKeys and store them as a Kubernetes secret.
    This simplifies the setup of NATS authentication.
- **Dependency Management**: Leverages the official NATS Helm chart as a
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

1. **Build Dependencies:**
    <!-- markdownlint-disable-next-line MD013 -->
    ```bash
    helm dependency build .
    ```

2. **Install the Chart:**
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
override the relevant `nats.global.*` and component image values in your custom
values file.

Key configurable areas include:

- NATS server settings (via the `nats` subchart values)
- Secret-based NKey wiring for the bundled `natsBox` client context
- Auth callout NKey bootstrap via `nats.authCallout.*`
- Global settings like image registries and pull secrets (`nats.global.*`)
- Optional RBAC integration for external consumers of the generated NKey secret
  (`nats.rbac.openbao.*`)

Refer to the `values.yaml` file for a detailed list of configurable parameters
and their default values.

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
