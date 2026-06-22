# NVCF cert-manager Helm Chart

> [!IMPORTANT]
> Active development of the helm-nvcf-cert-manager chart has moved to
> the NVCF umbrella monorepo. This repository is retained as
> historical source context only; new commits, issues, and merge
> requests should target the umbrella.
>
> Internal: https://github.com/NVIDIA/nvcf/nvcf/-/tree/main/deploy/helm/cert-manager
> Public mirror: https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/cert-manager

## Scope

This chart installs cert-manager for self-managed NVCF. It is intentionally a minimal wrapper around the pinned upstream Jetstack cert-manager Helm chart, not a fork of cert-manager and not a custom NVCF cert-manager implementation.

By default, self-managed NVCF installs the pinned upstream Jetstack cert-manager chart through this wrapper, typically into the `cert-manager` namespace. If a customer already operates cert-manager in the cluster, the stack should support using that existing installation and skip installing another cert-manager/CRD owner.

In existing cert-manager mode, NVCF does not install, upgrade, or mutate cert-manager CRDs, webhooks, controllers, issuer credentials, or provider-specific issuer configuration. Customers own those components. NVCF service charts create NVCF-owned `Certificate` resources that reference the configured `Issuer` or `ClusterIssuer` and consume the resulting Kubernetes TLS Secret.

This chart does not own application `Certificate` resources. Stargate/LPU certificates are owned by the `llm-request-router` chart for P0. Other NVCF-owned service certificates are owned by their service charts as P1 fast-follow work.

This chart does not own the OpenBao PKI backend. OpenBao PKI mounts, signing roles, and cert-manager auth are provisioned by `nvcf-openbao-migrations`.

## Layout

- `Chart.yaml`: local wrapper chart with a pinned dependency on Jetstack cert-manager
- `values.yaml`: default values for the cert-manager dependency
- `Makefile`: repeatable dependency, install, template, and status commands
- `charts/cert-manager/`: vendored upstream Jetstack cert-manager chart

## Prerequisites

- `kubectl` pointed at the target cluster
- `helm` installed
- `yq` installed for Makefile metadata helpers
- cluster-admin privileges for CRDs and namespace-scoped controllers

## Install cert-manager

```bash
make install
```

## Render manifests without applying

```bash
make template
```

## Check status

```bash
make status
```
