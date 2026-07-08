# Artifact Manifest

This section provides a comprehensive list of all components required for NVIDIA Cloud Functions (NVCF) Self-Hosted deployment for basic inference. Additional components are needed for Low Latency Streaming (Simulation).

<Tip>
**Deploying to AWS with ECR?** If you're using the [Terraform Installation](./terraform-installation.md) to deploy to Amazon EKS, you can skip manual artifact mirroring entirely. Set `create_sm_ecr_repos = true` in your `terraform.tfvars` and Terraform will automatically mirror all required images and Helm charts to ECR. See [ecr-automated-mirroring](./image-mirroring.md) for details.

</Tip>

## Artifacts Overview

The following tables list all artifacts required for an inference-only self-hosted NVCF deployment, organized by category, with their container images, Helm charts, and other resources.

<Warning>
**Early Access (EA) Version Policy**

During Early Access, artifact versions are updated frequently. The versions shown for Infrastructure Components are stable references, but **all other components should use the latest published version** from NGC.

To find the latest versions:

First, ensure you have the latest version of the [NGC CLI installed and configured](https://org.ngc.nvidia.com/setup/installers/cli).

```bash
# List available versions for any container image
ngc registry image list "0833294136851237/nvcf-ncp-staging/<artifact-name>:*"

# For Helm charts (OCI-compliant charts are stored in the container registry)
ngc registry image list "0833294136851237/nvcf-ncp-staging/<chart-name>:*"
```

</Warning>

<Note>
Helm chart types

Rows marked `Chart (OCI)` are OCI-compliant charts stored in the NGC container registry. This means:

- Charts are pulled using `oci://` URLs: `helm pull oci://nvcr.io/0833294136851237/nvcf-ncp-staging/<chart-name> --version <version>`
- Charts are listed using the image registry command: `ngc registry image list`
- When mirroring to private registries (e.g., ECR), use container image tools like `skopeo` or `helm push/pull` with OCI support

Rows marked `Chart (HTTP)` are traditional Helm repository charts, not OCI
URLs. In this manifest,
`https://helm.ngc.nvidia.com/nvidia/omniverse/ddcs:5.0.0` means the chart
`ddcs` in the `omniverse` Helm repository
(`https://helm.ngc.nvidia.com/nvidia/omniverse`), at version `5.0.0`. Add the
Helm repository and pull the chart by name and version, for example:

```bash
helm repo add omniverse https://helm.ngc.nvidia.com/nvidia/omniverse
helm repo update
helm pull omniverse/ddcs --version 5.0.0
```

</Note>

<Info>
Some supporting components such as the GPU Operator, OpenBao, NATS, Cassandra, etc. can alternatively be pulled directly from public NGC Catalog or other public opensource repositories if desired.

</Info>

### Artifact Registry Paths

#### Infrastructure Components

Core infrastructure services including NATS for messaging, Cassandra for data storage, and OpenBao for secret management.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Image | nats-box | `nvcr.io/0833294136851237/nvcf-ncp-staging/nats-box:0.19.2-nonroot` |
| Image | nats-server | `nvcr.io/0833294136851237/nvcf-ncp-staging/nats-server:2.11.10-alpine3.22` |
| Image | nats-server-config-reloader | `nvcr.io/0833294136851237/nvcf-ncp-staging/nats-server-config-reloader:0.20.0` |
| Chart (OCI) | helm-nvcf-nats | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvcf-nats:0.5.0` |
| Image | bitnami-cassandra | `nvcr.io/0833294136851237/nvcf-ncp-staging/bitnami-cassandra:5.0.6-nv-1` |
| Image | nvcf-cassandra-migrations | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-cassandra-migrations:0.3.1` |
| Chart (OCI) | helm-nvcf-cassandra | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvcf-cassandra:0.11.1` |
| Image | nvcf-openbao | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-openbao:2.2.2-nv-1` |
| Image | nvcf-openbao | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-openbao:2.5.1-nv-1.1.0` |
| Image | nvcf-openbao-migrations | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-openbao-migrations:0.5.1` |
| Chart (OCI) | helm-nvcf-openbao-server | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvcf-openbao-server:0.27.1` |
| Image | oss-vault-k8s | `nvcr.io/0833294136851237/nvcf-ncp-staging/oss-vault-k8s:1.6.0` |

#### Control Plane Components

Services that manage the NVCF platform including API gateway, deployment orchestration, invocation handling, and security services.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Image | spot | `nvcr.io/0833294136851237/nvcf-ncp-staging/spot:1.536.0` |
| Image | strap | `nvcr.io/0833294136851237/nvcf-ncp-staging/strap:2.234.0` |
| Chart (OCI) | helm-nvcf-api | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvcf-api:1.13.0` |
| Chart (OCI) | helm-nvcf-sis | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvcf-sis:1.8.0` |
| Image | nvcf-grpc-proxy | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-grpc-proxy:1.24.18` |
| Chart (OCI) | helm-nvcf-grpc-proxy | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvcf-grpc-proxy:1.4.0` |
| Image | nvcf-invocation-service | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-invocation-service:0.2.30` |
| Chart (OCI) | helm-nvcf-invocation-service | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvcf-invocation-service:1.3.1` |
| Image | ess-api | `nvcr.io/0833294136851237/nvcf-ncp-staging/ess-api:v0.48.31` |
| Chart (OCI) | helm-nvcf-ess-api | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvcf-ess-api:1.3.0` |
| Image | notary-service | `nvcr.io/0833294136851237/nvcf-ncp-staging/notary-service:1.1.39` |
| Chart (OCI) | helm-nvcf-notary-service | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvcf-notary-service:1.2.0` |
| Image | reval-server | `nvcr.io/0833294136851237/nvcf-ncp-staging/reval-server:0.12.1` |
| Chart (OCI) | helm-reval | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-reval:1.2.2` |
| Image | nv-api-keys | `nvcr.io/0833294136851237/nvcf-ncp-staging/nv-api-keys:0.0.4` |
| Chart (OCI) | helm-nvcf-api-keys | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvcf-api-keys:1.0.4` |

#### GPU Workload Components

Components that run on GPU nodes to manage function execution, including the NVCA operator and supporting containers.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Image | nvca | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvca:2.52.1` |
| Image | nvca-operator | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvca-operator:2.52.1` |
| Chart (OCI) | helm-nvca-operator | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvca-operator:1.6.6` |
| Image | nvcf_worker_utils | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf_worker_utils:2.101.0` |
| Image | nvcf_worker_init | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf_worker_init:2.102.0` |
| Image | nvcf_worker_niclls | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf_worker_niclls:2.105.7` |
| Image | ess-agent | `nvcr.io/0833294136851237/nvcf-ncp-staging/ess-agent:1.0.5` |
| Image | nvcf-image-credential-helper | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-image-credential-helper:0.5.1` |

#### Supporting Components

Additional utilities and helper services required for the platform, including the NVIDIA GPU Operator for GPU node management.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Image | alpine-k8s | `nvcr.io/0833294136851237/nvcf-ncp-staging/alpine-k8s:1.30.12` |
| Chart (HTTP) | gpu-operator | `https://helm.ngc.nvidia.com/nvidia` (public Helm repo) |
| Image | gpu-operator-validator | `nvcr.io/0833294136851237/nvcf-ncp-staging/gpu-operator-validator` or public NGC |
| Image | k8s-device-plugin | `nvcr.io/0833294136851237/nvcf-ncp-staging/k8s-device-plugin` or public NGC |
| Chart (HTTP) | ebs-csi-driver | `https://kubernetes-sigs.github.io/aws-ebs-csi-driver` (public Helm repo) |
| Chart (HTTP) | csi-driver-smb | `https://raw.githubusercontent.com/kubernetes-csi/csi-driver-smb/master/charts` (public Helm repo) |
| Image | load_tester_supreme | `nvcr.io/0833294136851237/nvcf-ncp-staging/load_tester_supreme:0.0.8` |

#### Reference Architecture Components

Optional components for the reference deployment architecture.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Chart (OCI) | nvcf-gateway-routes | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-gateway-routes:1.5.0` |
| Image | admin-token-issuer-proxy | `nvcr.io/0833294136851237/nvcf-ncp-staging/admin-token-issuer-proxy:1.0.1` |
| Chart (OCI) | helm-admin-token-issuer-proxy | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-admin-token-issuer-proxy:1.2.2` |

#### Observability Components

Optional example components for monitoring and observability. These are provided as reference implementations only and are not intended for production use. See [self-hosted-example-dashboards](./example-dashboards.md) for deployment instructions.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Chart (OCI) | nvcf-observability-reference-stack | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-observability-reference-stack:1.7.0` |
| Chart (OCI) | nvcf-example-dashboards | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-example-dashboards:1.6.0` |
| Chart (OCI) | helm-nvcf-state-metrics | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvcf-state-metrics:1.0.0` |

#### Container Caching Components

Optional components for accelerating container image pulls across all workload types.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Image | nvcf-container-cache | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-container-cache:v1.1.31` |
| Chart (OCI) | helm-nvcf-container-cache | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-container-cache:0.25.6` |
| Image | nvcf-proxy-tls-certs | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-proxy-tls-certs:1.2.0` |

#### Simulation Caching Components

Optional caching components for Low Latency Streaming (LLS) and simulation workloads, including shader caching, derived data caching, and USD content caching.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Image | gxcache-webhook | `nvcr.io/0833294136851237/nvcf-ncp-staging/gxcache-webhook:59bd8ec5` |
| Image | gxcache-init | `nvcr.io/0833294136851237/nvcf-ncp-staging/gxcache-init:1e47f722` |
| Image | gxcache-service | `nvcr.io/0833294136851237/nvcf-ncp-staging/gxcache-service:b206ce39` |
| Chart (OCI) | helm-gxcache | `nvcr.io/0833294136851237/nvcf-ncp-staging/gxcache:0.8.2` |
| Image | ddcs-dist-kv | `nvcr.io/nvidia/omniverse/ddcs-dist-kv:5.0.0` |
| Chart (HTTP) | ddcs | `https://helm.ngc.nvidia.com/nvidia/omniverse/ddcs:5.0.0` |
| Image | usd-content-cache | `nvcr.io/nvidia/omniverse/usd-content-cache:3.0.1` |
| Chart (HTTP) | usd-content-cache | `https://helm.ngc.nvidia.com/nvidia/omniverse/usd-content-cache:3.0.3` |

#### Storage API Components

Optional components for USD Storage API functionality used in simulation workloads.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Image | storage-service | `nvcr.io/nvidia/omniverse/storage-service:1.0.2` |
| Chart (HTTP) | storage-service | `https://helm.ngc.nvidia.com/nvidia/omniverse/storage-service:1.0.2` |
| Image | simple-nginx | `nvcr.io/nvidia/omniverse/simple-nginx:1.0.2` |
| Chart (HTTP) | discovery-service | `https://helm.ngc.nvidia.com/nvidia/omniverse/discovery-service:2.3.8` |

#### Low Latency Streaming (LLS) Components

Components for Low Latency Streaming functionality.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Image | streaming-proxy | `nvcr.io/0833294136851237/nvcf-ncp-staging/streaming-proxy:2.0.1` |
| Chart (OCI) | gdn-streaming | `nvcr.io/0833294136851237/nvcf-ncp-staging/gdn-streaming:2.0.1` |

#### Deployment Resources

Terraform and Helmfile resources for automated deployment.

| Type | Component Name | Full Path |
| --- | --- | --- |
| Resource | nvcf-base | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-base:0.1.4` |
| Resource | nvcf-self-managed-stack | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-self-managed-stack:0.5.1` |
| Resource | nvcf-cli | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-cli:0.0.24` |

### Component Descriptions

#### Infrastructure Components

| Component Name | Description |
| --- | --- |
| nats-box | NATS utility container for debugging and administration |
| nats-server | Pub Sub Messages, used for Function Invocation and Deployment |
| nats-server-config-reloader | Configuration reloader for NATS server |
| helm-nvcf-nats | Helm chart for NATS deployment |
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
| strap | NVCF API service, refer to [self-hosted-api](./api.md) for full API specification |
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
| nv-api-keys | API Key generation and management |
| helm-nvcf-api-keys | Helm chart for API Keys service |

#### GPU Workload Components

| Component Name | Description |
| --- | --- |
| nvca | Performs the registration of the cluster and deployment orchestration in-cluster |
| helm-nvca-operator (chart) | Helm chart for NVCA operator deployment (current chart name, versions 1.4.0+) |
| nvcf_worker_utils | Acts as a proxy to NATS from the user's application |
| nvcf_worker_init | Setup & Resource loading on deployment for the users application |
| nvcf_worker_niclls | NIC LLS worker component for low latency streaming workloads |
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
| helm-nvcf-container-cache | Helm chart for container cache deployment |
| nvcf-proxy-tls-certs | TLS certificate management for container cache proxy |

#### Simulation Caching Components

| Component Name | Description |
| --- | --- |
| gxcache-webhook | Shader cache webhook for intercepting and caching shader compilation requests |
| gxcache-init | Init container for shader cache setup |
| gxcache-service | Backend service for shader cache storage and retrieval |
| helm-gxcache | Helm chart for deploying the complete shader cache stack |
| ddcs-dist-kv | Derived Data Cache Service - caches computed/derived data for simulation workloads |
| ddcs | Helm chart for DDCS deployment |
| usd-content-cache | USD Content Cache - caches Universal Scene Description assets for streaming |
| usd-content-cache (chart) | Helm chart for USD Content Cache deployment |

#### Storage API Components

| Component Name | Description |
| --- | --- |
| storage-service | USD Storage Service for managing assets in simulation workloads |
| storage-service (chart) | Helm chart for Storage Service deployment |
| simple-nginx | Simple NGINX container for Storage API routing |
| discovery-service | Helm chart for Storage API Discovery Service |

#### Low Latency Streaming (LLS) Components

| Component Name | Description |
| --- | --- |
| streaming-proxy | LLS Streaming Proxy Container |
| gdn-streaming | LLS Self-Hosted Helm Chart |

#### Deployment Resources

| Component Name | Description |
| --- | --- |
| nvcf-base | Terraform configuration for NVCF quickstart deployment |
| nvcf-self-managed-stack | Helmfile bundle for self-managed stack deployment |
| nvcf-cli | Command-line interface for managing functions and deployments |
