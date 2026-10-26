# NVCF gRPC Proxy Helm Chart

This repository contains the Helm chart for deploying the NVCF gRPC Proxy on Kubernetes.

## Overview

The chart packages the gRPC proxy deployment with two mutually exclusive modes selected by the `grpcproxy.deploymentType` value:

- `deployment` - a horizontally scalable Deployment with optional HPA, suitable for stateless workloads
- `daemonset` - a DaemonSet that runs one pod per node, suitable for node-level traffic handling

A Vault Agent sidecar is configured automatically to fetch service credentials from a Vault or OpenBao backend.

The default chart values do not set the required image registry and repository. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
grpcproxy:
  image:
    registry: <your-registry>
    repository: <your-org>/grpc-proxy
    tag: <appVersion>
  deploymentType: deployment
```

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`
- A reachable NATS cluster
- A reachable Vault or OpenBao instance with a JWT authentication path configured for this service

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install grpc-proxy grpc-proxy \
  --namespace grpc-proxy \
  --create-namespace \
  --values grpc-proxy/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade grpc-proxy grpc-proxy \
  --namespace grpc-proxy \
  --values grpc-proxy/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall grpc-proxy --namespace grpc-proxy
```

## Configuration

The default chart configuration lives in `grpc-proxy/values.yaml`.

Important settings to review before deployment:

- `grpcproxy.image.*` for the proxy container image
- `grpcproxy.imagePullSecrets` for private registry access
- `grpcproxy.deploymentType` to select `deployment` or `daemonset` mode
- `grpcproxy.deployment.*` for replica count, autoscaling, and pod placement when running in Deployment mode
- `grpcproxy.daemonset.*` for tolerations, node selectors, and update strategy when running in DaemonSet mode
- `grpcproxy.config.*` for service endpoints, NATS connection settings, and tracing configuration
- `grpcproxy.workerConnectBaseURL` for split deployments where workers need a routable HTTP/1 CONNECT callback endpoint instead of the grpc-proxy pod IP. The chart maps this value to `SELF_WORKER_FQDN` for the grpc-proxy container. It is the only chart input for that address: `grpcproxy.env.SELF_WORKER_FQDN` fails render, because every pod advertises the address and the self-managed stack sizes grpc-proxy from `workerConnectBaseURL`.
- `grpcproxy.http3.*` to serve worker callbacks over HTTP/3 with TLS on UDP 10084 instead of HTTP/1. Each pod advertises `https://<dashed-pod-ip>.<base host>`, where the base is `workerConnectBaseURL` or, by default, `https://grpc-headless.<namespace>.svc.cluster.local:10084`. The base must be an `https` URL with a DNS host and no path; anything else, including an `http://` HTTP/1 URL, fails render. Set `http3.tls.secretName` to an existing TLS Secret, or enable `http3.tls.certificate` and set `http3.tls.certificate.issuerRef.name` to issue one with cert-manager; the certificate must cover `*.<base host>`, which is the default `dnsNames`; explicit `dnsNames` without it fail render. grpc-proxy reloads the certificate when the Secret changes, including the Kubernetes `..data` symlink swap, without a restart. Workers must trust its CA, for example through `NVCF_PROXY_CA_PEM`. Workers must be able to resolve every advertised `<dashed-pod-ip>.<base host>` name, not only the base host. The default base relies on cluster DNS naming headless Service endpoints by dashed pod IP; with CoreDNS `endpoint_pod_names` enabled, set `workerConnectBaseURL` to a domain that resolves those names. A custom base host needs DNS records for the per-pod names, for example a wildcard `*.<base host>`.
- `grpcproxy.vault.*` for JWT authentication path, role, and audience values used by the Vault Agent injector
- `grpcproxy.env.EVENT_LEDGER_ADDR` to publish events to the event ledger. The Vault Agent template renders an `eventLedgerToken` from `services/event-ledger/jwt/sign/grpc-proxy-proxy`, so the OpenBao migrations must include the grpc-proxy event ledger role before this chart version is deployed.

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
