# Helm Chart Installation

By default, the NVCF self-hosted stack is deployed using the provided Helmfile
(see [helmfile-installation](./helmfile-installation.md)). However, you can also install each Helm chart
individually using `helm install` or `helm upgrade`. This is useful when:

- You want fine-grained control over each component's deployment
- Your environment doesn't support Helmfile
- You need to integrate NVCF components into an existing GitOps pipeline
- You want to install only a subset of the stack

<Warning>
Each chart **must** be installed into the exact namespace shown in the tables below.
These namespace assignments are fixed — service-to-service cluster DNS addressing and
Vault (OpenBao) authentication claims depend on this layout. Installing a chart into the
wrong namespace will cause authentication failures such as
`error validating claims: claim "/kubernetes.io/namespace" does not match any associated bound claim values`.

</Warning>

## Installation Phases

The standalone installation follows five phases. Each phase must complete successfully
before proceeding to the next.

1. [Prerequisites](./standalone-prerequisites.md) — Shared setup: tools, namespaces, pull secrets, configuration variables
2. [Infrastructure Dependencies](./standalone-infrastructure.md) — NATS, OpenBao, Cassandra
3. [Core Services](./standalone-core-services.md) — API Keys, SIS, ESS API, NVCF API, Invocation Service, gRPC Proxy, Notary Service, Admin Issuer Proxy
4. [Gateway & Ingress](./standalone-gateway.md) — Envoy Gateway, Gateway Routes, end-to-end verification
5. [NVCA Operator](./cluster-management/self-managed.md) — Cluster agent for GPU workload scheduling (in Cluster Management section)

## Chart Inventory

The NVCF self-hosted stack consists of **14 Helm charts** across three groups. Charts must
be installed in the order shown below, as later charts depend on earlier ones.

### Dependencies

These infrastructure services must be installed first.

| Chart | Description | Namespace |
| --- | --- | --- |
| `helm-nvcf-nats` | NATS messaging system for inter-service communication | `nats-system` |
| `helm-nvcf-openbao-server` | OpenBao (Vault-compatible) secrets management | `vault-system` |
| `helm-nvcf-cassandra` | Apache Cassandra database for persistence | `cassandra-system` |

### Core Services

These NVCF control plane services depend on the infrastructure above.

| Chart | Description | Namespace |
| --- | --- | --- |
| `helm-nvcf-api-keys` | API key management service | `api-keys` |
| `helm-nvcf-sis` | Spot Instance Service (cluster registration and management) | `sis` |
| `helm-nvcf-ess-api` | ESS API for secrets distribution | `ess` |
| `helm-nvcf-api` | NVCF API service (depends on ESS) | `nvcf` |
| `helm-nvcf-invocation-service` | Function invocation service (depends on API) | `nvcf` |
| `helm-nvcf-grpc-proxy` | gRPC proxy for streaming workloads (depends on API) | `nvcf` |
| `helm-nvcf-notary-service` | Request signing and validation | `nvcf` |
| `helm-reval` | Reval service (resource evaluation) | `nvcf` |
| `helm-admin-token-issuer-proxy` | Admin token issuer proxy (depends on API Keys) | `api-keys` |

### Gateway & Ingress

Gateway routing is installed after all core services are running.

| Chart | Description | Namespace |
| --- | --- | --- |
| `nvcf-gateway-routes` | Ingress / Gateway API routing (depends on Notary, API Keys) | *(configurable)* |

### Worker

The NVCA Operator is installed last, after the control plane is running.

| Chart | Description | Namespace |
| --- | --- | --- |
| `helm-nvca-operator` | NVIDIA Cluster Agent Operator (see [self-managed-clusters](./cluster-management/self-managed.md)) | `nvca-operator` |

## Chart Sources

All charts are distributed as OCI artifacts. Pull them from your mirrored registry:

```bash
# Example: pull a chart
helm pull oci://<your-registry>/<your-repo>/helm-nvcf-api --version <version>

# Example: install a chart
helm upgrade --install api -n nvcf \
  oci://<your-registry>/<your-repo>/helm-nvcf-api --version <version> \
  -f values.yaml
```

For the full list of NVCF artifacts to mirror, see [self-hosted-artifact-manifest](./manifest.md).
