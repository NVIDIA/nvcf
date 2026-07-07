# Artifact Manifest

This section provides a comprehensive list of all components required for NVIDIA Cloud Functions (NVCF) Self-Hosted deployment for basic inference. Additional components are needed for Low Latency Streaming (Simulation).

## Artifacts Overview

The following tables list all artifacts required for an inference-only self-hosted NVCF deployment, organized by category, with their container images, Helm charts, and other resources.

<Warning>
Artifact version compatibility

Newer artifact versions might be available. NVCF self-managed stack and
compute-plane stack releases are QA-qualified as umbrella releases with the
specific versions shown on this page. Use these versions together. NVIDIA
cannot guarantee compatibility when you substitute other artifact versions.

</Warning>

## Prepare Helm charts for Helmfile

The self-managed Helmfile bundles currently expect NVCF charts in an OCI
registry. Rows marked `Chart (HTTP)` are NGC Helm repository
charts. Before deploying with Helmfile, copy each required `Chart (HTTP)`
version into the OCI registry configured by `global.helm.sources` in the
Helmfile bundles.

The following example copies one public NVCF chart into an OCI registry:

```bash
export CHART_NAME="helm-nvcf-api"
export CHART_VERSION="1.22.5"
export TARGET_REGISTRY="<registry-host>"
export TARGET_REPOSITORY="<repository>"

helm repo add nvcf https://helm.ngc.nvidia.com/nvidia/nvcf
helm repo update
helm pull "nvcf/${CHART_NAME}" --version "${CHART_VERSION}"

helm registry login "${TARGET_REGISTRY}"
helm push "${CHART_NAME}-${CHART_VERSION}.tgz" \
  "oci://${TARGET_REGISTRY}/${TARGET_REPOSITORY}"
```

Repeat this process for every required `Chart (HTTP)` row. Copy any required
`Chart (OCI)` rows into the same target repository so Helmfile can resolve all
NVCF charts from one source. Configure the stack environment with that OCI
location:

```yaml
global:
  helm:
    sources:
      registry: "<registry-host>"
      repository: "<repository>"
```

See [Image Mirroring](./image-mirroring.md) for additional registry examples.

<Info>
Some supporting components such as the GPU Operator, OpenBao, NATS, Cassandra, etc. can alternatively be pulled directly from public NGC Catalog or other public opensource repositories if desired.

</Info>

### Artifact Registry Paths

{/* docs-version-sync:BEGIN manifest-artifact-registry-paths */}

#### Infrastructure Components

Core infrastructure services including NATS for messaging, NATS auth callout support, Cassandra for data storage, and OpenBao for secret management.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Image | nats-box | `nvcr.io/0833294136851237/nvcf-ncp-staging/nats-box:0.19.7-nonroot` |
| Image | nats-server | `nvcr.io/nvidia/nvcf/nats-server:2.11.17-alpine3.22` |
| Image | nats-server-config-reloader | `nvcr.io/0833294136851237/nvcf-ncp-staging/nats-server-config-reloader:0.23.0` |
| Chart (HTTP) | helm-nvcf-nats | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-nats:0.7.1` |
| Image | nvcf-nats-auth-callout-service | `nvcr.io/nvidia/nvcf/nvcf-nats-auth-callout-service:0.5.10` |
| Chart (HTTP) | helm-nvcf-nats-auth-callout-service | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-nats-auth-callout-service:1.1.3` |
| Image | bitnami-cassandra | `nvcr.io/0833294136851237/nvcf-ncp-staging/bitnami-cassandra:5.0.6-nv-1` |
| Image | nvcf-cassandra-migrations | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-cassandra-migrations:0.8.1` |
| Chart (HTTP) | helm-nvcf-cassandra | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-cassandra:0.15.5` |
| Image | nvcf-openbao | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-openbao:2.5.4-nv-1.3.0` |
| Image | nvcf-openbao-migrations | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-openbao-migrations:0.16.1` |
| Chart (HTTP) | helm-nvcf-openbao-server | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-openbao-server:0.30.23` |
| Image | oss-vault-k8s | `nvcr.io/nvidia/nvcf/oss-vault-k8s:1.7.4` |

#### Control Plane Components

Services that manage the NVCF platform including API gateway, deployment orchestration, invocation handling, LLM routing, and security services.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Image | spot | `nvcr.io/0833294136851237/nvcf-ncp-staging/spot:1.563.1` |
| Image | nvcf-service-oss | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-service-oss:1.9.0` |
| Chart (OCI) | helm-nvcf-api | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvcf-api:1.23.1` |
| Chart (HTTP) | helm-nvcf-sis | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-sis:1.17.0` |
| Image | nvcf-grpc-proxy | `nvcr.io/nvidia/nvcf/nvcf-grpc-proxy:1.29.1` |
| Chart (HTTP) | helm-nvcf-grpc-proxy | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-grpc-proxy:1.6.7` |
| Image | nvcf-invocation-service | `nvcr.io/nvidia/nvcf/nvcf-invocation-service:0.8.5` |
| Chart (HTTP) | helm-nvcf-invocation-service | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-invocation-service:1.5.5` |
| Image | ess-api | `nvcr.io/nvidia/nvcf/ess-api:v0.57.26` |
| Chart (HTTP) | helm-nvcf-ess-api | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-ess-api:1.6.1` |
| Image | notary-service | `nvcr.io/0833294136851237/nvcf-ncp-staging/notary-service:1.9.4` |
| Chart (HTTP) | helm-nvcf-notary-service | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-notary-service:1.4.1` |
| Image | reval-server | `nvcr.io/nvidia/nvcf/reval-server:0.2.2` |
| Chart (HTTP) | helm-reval | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-reval:1.3.8` |
| Image | nvcf-api-keys-service | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-api-keys-service:1.2.14` |
| Chart (HTTP) | helm-nvcf-api-keys | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-api-keys:1.5.1` |
| Image | nvct-service-oss | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvct-service-oss:1.5.5` |
| Chart (HTTP) | helm-nvcf-nvct-api | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-nvct-api:1.4.2` |
| Image | llm-api-gateway | `nvcr.io/0833294136851237/nvcf-ncp-staging/llm-api-gateway:0.8.3` |
| Image | llm-request-router | `nvcr.io/0833294136851237/nvcf-ncp-staging/stargate:0.3.0` |
| Chart (HTTP) | helm-nvcf-llm-api-gateway | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-llm-api-gateway:1.2.0` |
| Chart (HTTP) | helm-nvcf-llm-request-router | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-llm-request-router:1.6.3` |

#### GPU Workload Components

Components that run on GPU nodes to manage function execution, including the NVCA operator and supporting containers.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Image | nvca | `nvcr.io/nvidia/nvcf/nvca:3.0.3` |
| Image | nvca-operator | `nvcr.io/nvidia/nvcf/nvca-operator:3.0.3` |
| Chart (HTTP) | helm-nvca-operator | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvca-operator:1.12.7` |
| Image | nvcf_worker_utils | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf_worker_utils:2.109.4` |
| Image | nvcf_worker_init | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf_worker_init:2.109.4` |
| Image | ess-agent | `nvcr.io/0833294136851237/nvcf-ncp-staging/ess-agent:1.1.0` |
| Image | nvcf-image-credential-helper | `nvcr.io/nvidia/nvcf/nvcf-image-credential-helper:0.10.2` |

#### Supporting Components

Additional utilities and helper services required for the platform, including the NVIDIA GPU Operator for GPU node management.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Image | alpine-k8s | `nvcr.io/0833294136851237/nvcf-ncp-staging/alpine-k8s:1.36.1` |
| Chart (HTTP) | gpu-operator | [Public NGC Helm repo](https://helm.ngc.nvidia.com/nvidia) |
| Image | gpu-operator-validator | [Public NGC](https://catalog.ngc.nvidia.com/orgs/nvidia/teams/cloud-native/containers/gpu-operator-validator) |
| Image | k8s-device-plugin | [Public NGC](https://catalog.ngc.nvidia.com/orgs/nvidia/teams/k8s/containers/device-plugin) |
| Chart (HTTP) | ebs-csi-driver | `https://kubernetes-sigs.github.io/aws-ebs-csi-driver` |
| Chart (HTTP) | csi-driver-smb | `https://raw.githubusercontent.com/kubernetes-csi/csi-driver-smb/master/charts` |

#### Reference Architecture Components

Optional components for the reference deployment architecture.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Chart (HTTP) | nvcf-gateway-routes | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-gateway-routes:1.13.4` |
| Image | admin-token-issuer-proxy | `nvcr.io/0833294136851237/nvcf-ncp-staging/admin-token-issuer-proxy:1.0.2` |
| Chart (HTTP) | helm-admin-token-issuer-proxy | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-admin-token-issuer-proxy:1.4.3` |

#### Observability Components

Optional example components for monitoring and observability. These are provided as reference implementations only and are not intended for production use. See [self-hosted-example-dashboards](./example-dashboards.md) for deployment instructions.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Chart (HTTP) | nvcf-observability-reference-stack | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-observability-reference-stack:1.10.0` |
| Chart (HTTP) | nvcf-example-dashboards | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-example-dashboards:1.6.0` |
| Chart (HTTP) | helm-nvcf-state-metrics | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-state-metrics:1.0.1` |

#### Container Caching Components

Optional components for accelerating container image pulls across all workload types.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Image | nvcf-container-cache | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-container-cache:v1.1.31` |
| Chart (HTTP) | nvcf-container-cache | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-container-cache:0.25.22` |
| Image | nvcf-proxy-tls-certs | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-proxy-tls-certs:1.2.0` |

#### Other Published Components

Additional components present in the current stack artifact manifest.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Image | cert-manager-cainjector | `nvcr.io/nvidia/nvcf/cert-manager-cainjector:v1.20.2` |
| Image | cert-manager-controller | `nvcr.io/nvidia/nvcf/cert-manager-controller:v1.20.2` |
| Image | cert-manager-startupapicheck | `nvcr.io/nvidia/nvcf/cert-manager-startupapicheck:v1.20.2` |
| Image | cert-manager-webhook | `nvcr.io/nvidia/nvcf/cert-manager-webhook:v1.20.2` |
| Chart (HTTP) | helm-nvcf-cert-manager | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-cert-manager:0.1.0` |
| Chart (HTTP) | helm-nvcf-rate-limiter | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-rate-limiter:1.0.3` |
| Chart (HTTP) | helm-nvcf-vanity-gateway | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-vanity-gateway:0.1.0-nvcf-10204.1` |
| Image | pylon | `nvcr.io/0833294136851237/nvcf-ncp-staging/pylon:0.3.0` |

#### Deployment Resources

Helmfile and CLI resources for deployment.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Resource | nvcf-self-managed-stack | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-self-managed-stack:0.6.0-rc.98` |
| Resource | nvcf-cli | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-cli:0.0.30` |
| Resource | nvcf-compute-plane-stack | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-compute-plane-stack:1.0.6` |

{/* docs-version-sync:END manifest-artifact-registry-paths */}

### Component Descriptions

#### Infrastructure Components

| Component Name | Description |
| --- | --- |
| nats-box | NATS utility container for debugging and administration |
| nats-server | Pub Sub Messages, used for Function Invocation and Deployment |
| nats-server-config-reloader | Configuration reloader for NATS server |
| helm-nvcf-nats | Helm chart for NATS deployment |
| nvcf-nats-auth-callout-service | Auth callout service for NATS authorization |
| helm-nvcf-nats-auth-callout-service | Helm chart for the NATS auth callout service |
| bitnami-cassandra | Database for Account, Function and Cluster Management |
| nvcf-cassandra-migrations | Database migration scripts for Cassandra |
| helm-nvcf-cassandra | Helm chart for Cassandra deployment |
| nvcf-openbao | Secret management (OpenBao/Vault) |
| nvcf-openbao-migrations | Migration scripts for OpenBao |
| helm-nvcf-openbao-server | OpenBao Helm chart |
| oss-vault-k8s | Kubernetes integration for secret management |

#### Control Plane Components

| Component Name | Description |
| --- | --- |
| spot | Spot Instance Service (SIS) - Manages deployments, cluster and instance state |
| nvcf-service-oss | NVCF API service, refer to [self-hosted-api](./api.md) for full API specification |
| helm-nvcf-api | Helm chart for NVCF API service |
| helm-nvcf-sis | Helm chart for Spot Instance Service |
| nvcf-grpc-proxy | Used for bi-directional communication and state management |
| helm-nvcf-grpc-proxy | Helm chart for GRPC Proxy deployment |
| nvcf-invocation-service | Handles stateless HTTP Function invocation requests |
| helm-nvcf-invocation-service | Helm chart for Invocation Service |
| ess-api | Encrypted Secrets Service - Used for application secret injection |
| helm-nvcf-ess-api | Helm chart for ESS API |
| notary-service | Used to sign and validate Functions and nodes |
| helm-nvcf-notary-service | Helm chart for Notary Service |
| reval-server | Reval (re-validation) service - Handles background re-validation of function state |
| helm-reval | Helm chart for Reval service |
| nvcf-api-keys-service | API key generation and management |
| helm-nvcf-api-keys | Helm chart for API Keys service |
| llm-api-gateway | Gateway service for OpenAI-compatible LLM requests |
| llm-request-router | Request routing service backed by the Stargate image |
| helm-nvcf-llm-api-gateway | Helm chart for LLM API gateway services |
| helm-nvcf-llm-request-router | Helm chart for LLM request routing services |

#### GPU Workload Components

| Component Name | Description |
| --- | --- |
| nvca | Performs the registration of the cluster and deployment orchestration in-cluster |
| helm-nvca-operator (chart) | Helm chart for NVCA operator deployment (current chart name, versions 1.4.0+) |
| nvcf_worker_utils | Acts as a proxy to NATS from the user's application |
| nvcf_worker_init | Setup & Resource loading on deployment for the users application |
| ess-agent | Injects User Secrets |
| nvcf-image-credential-helper | Helper for managing container image credentials |

#### Supporting Components

| Component Name | Description |
| --- | --- |
| alpine-k8s | Kubernetes utility container |
| gpu-operator | NVIDIA GPU Operator for dynamic GPU discovery - Can also pull directly from public NGC Catalog |
| gpu-operator-validator | GPU Operator validation component |
| k8s-device-plugin | Kubernetes device plugin for GPU support |
| ebs-csi-driver | AWS EBS CSI Driver for persistent volume provisioning on EKS |
| csi-driver-smb | CSI Driver for SMB/CIFS file shares |

#### Reference Architecture Components

| Component Name | Description |
| --- | --- |
| nvcf-gateway-routes | Gateway routing configuration for reference architecture |
| admin-token-issuer-proxy | Admin token management proxy |
| helm-admin-token-issuer-proxy | Helm chart for admin token issuer proxy |

#### Observability Components

| Component Name | Description |
| --- | --- |
| nvcf-observability-reference-stack | Reference observability backend (Prometheus, Grafana, Loki, Tempo, OpenTelemetry Collector) |
| nvcf-example-dashboards | Pre-configured Grafana dashboards for NVCF control-plane metrics |
| helm-nvcf-state-metrics | Helm chart for NVCF state metrics service |

#### Container Caching Components

| Component Name | Description |
| --- | --- |
| nvcf-container-cache | Accelerates container image pulls by caching layers locally on nodes |
| nvcf-container-cache (chart) | Helm chart for container cache deployment |
| nvcf-proxy-tls-certs | TLS certificate management for container cache proxy |

#### Deployment Resources

| Component Name | Description |
| --- | --- |
| nvcf-self-managed-stack | Helmfile bundle for self-managed stack deployment |
| nvcf-compute-plane-stack | Helmfile bundle for compute-plane stack deployment |
| nvcf-cli | Command-line interface for managing functions and deployments |
