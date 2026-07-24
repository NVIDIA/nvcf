# Artifact Manifest

This section provides a comprehensive list of all components required for NVIDIA Cloud Functions (NVCF) Self-Hosted deployment for basic inference. Additional components are needed for Low Latency Streaming (Simulation).

## Artifacts Overview

The following inventories list the artifacts for an inference-only self-hosted
NVCF deployment. Artifacts are grouped by deployment plane and type.

<Warning>
Artifact version compatibility

Newer artifact versions might be available. NVCF self-managed stack and
compute-plane stack releases are QA-qualified as umbrella releases with the
specific versions shown on this page. Use these versions together. NVIDIA
cannot guarantee compatibility when you substitute other artifact versions.

</Warning>

## Prepare Helm charts for Helmfile

The self-managed Helmfile bundles currently expect NVCF charts in an OCI
registry. Chart distributions that start with `https://` are Helm repository
charts. Before deploying with Helmfile, copy each required repository chart
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

Repeat this process for every required chart with an `https://` distribution.
Copy required charts with an `nvcr.io` distribution into the same target
repository so Helmfile can resolve all NVCF charts from one source. Configure
the stack environment with that OCI location:

```yaml
global:
  helm:
    sources:
      registry: "<registry-host>"
      repository: "<repository>"
```

See [Image Mirroring](./image-mirroring.md) for additional registry examples.

### Use upstream container images

You can configure a chart to pull a supporting image directly from its
upstream registry. For example, replace the `nats.reloader.image` block in
`deploy/stacks/self-managed/global.yaml.gotmpl` to pull the NATS configuration
reloader from Docker Hub:

```yaml
nats:
  reloader:
    image:
      registry: docker.io
      repository: natsio/nats-server-config-reloader
      tag: "0.23.0"
```

Use the version listed in the artifact table. Verify that your cluster can
reach the upstream registry. If the registry requires authentication, add its
pull secret to `global.imagePullSecrets`.

To pull the API account-bootstrap Kubernetes utilities from their upstream
image, replace the `api.accountBootstrap.image` block in
`global.yaml.gotmpl`:

```yaml
api:
  accountBootstrap:
    image:
      registry: docker.io
      repository: alpine/k8s
      tag: "1.36.1"
```

The current Cassandra initialization hook uses the
`nvcf-cassandra-migrations` image, and the current NATS chart renders NKeys as
Secrets without an nkey job. Their legacy `cassandra.initialization.image` and
`nats.nkeyJob.image` values do not control rendered workloads. Using
`alpine-k8s` for those operations requires chart support rather than a
configuration-only override.

<Info>
Some supporting components such as the GPU Operator, OpenBao, NATS, Cassandra, etc. can alternatively be pulled directly from public NGC Catalog or other public opensource repositories if desired.

</Info>

The following tables list the complete artifact inventory.

{/* docs-version-sync:BEGIN manifest-artifact-registry-paths */}

### Control plane Helm charts

| Artifact | Version | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- |
| `helm-admin-token-issuer-proxy` | `1.4.3` | Optional | Deploys the admin token issuer proxy used by the reference architecture. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-admin-token-issuer-proxy:1.4.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/admin-token-issuer-proxy) |
| `helm-nvcf-api` | `1.23.9` | Required | Deploys the NVCF API service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-api:1.23.9` |  |
| `helm-nvcf-api-keys` | `1.6.0` | Required | Deploys the API key management service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-api-keys:1.6.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/api-keys-colocated) |
| `helm-nvcf-cassandra` | `0.18.0` | Required | Deploys Cassandra and its initialization jobs. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-cassandra:0.18.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/cassandra) |
| `helm-nvcf-cert-manager` | `0.1.0` | Required | Deploys the NVCF cert-manager configuration. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-cert-manager:0.1.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/cert-manager) / [Upstream](https://github.com/cert-manager/cert-manager) |
| `helm-nvcf-ess-api` | `1.6.1` | Required | Deploys the Encrypted Secrets Service API. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-ess-api:1.6.1` |  |
| `helm-nvcf-grpc-proxy` | `1.6.7` | Required | Deploys the gRPC proxy service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-grpc-proxy:1.6.7` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/grpc-proxy) |
| `helm-nvcf-invocation-service` | `1.5.6` | Required | Deploys the HTTP invocation service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-invocation-service:1.5.6` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/http-invocation) |
| `helm-nvcf-llm-api-gateway` | `1.3.5` | Optional | Deploys the OpenAI-compatible LLM API gateway. | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvcf-llm-api-gateway:1.3.5` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/llm-api-gateway) |
| `helm-nvcf-llm-request-router` | `1.6.6` | Optional | Deploys the LLM request router. | `nvcr.io/0833294136851237/nvcf-ncp-staging/helm-nvcf-llm-request-router:1.6.6` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/llm-request-router) |
| `helm-nvcf-nats` | `0.7.1` | Required | Deploys NATS messaging for the control plane. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-nats:0.7.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nats) / [Upstream](https://github.com/nats-io/k8s) |
| `helm-nvcf-nats-auth-callout-service` | `1.1.3` | Required | Deploys the NATS authorization callout service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-nats-auth-callout-service:1.1.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nats-auth-callout) |
| `helm-nvcf-notary-service` | `1.4.2` | Required | Deploys the notary service for signing and validation. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-notary-service:1.4.2` |  |
| `helm-nvcf-nvct-api` | `1.4.3` | Required | Deploys the NVCF tenant API service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-nvct-api:1.4.3` |  |
| `helm-nvcf-openbao-server` | `0.30.25` | Required | Deploys OpenBao secret management. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-openbao-server:0.30.25` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/openbao) / [Upstream](https://github.com/openbao/openbao-helm) |
| `helm-nvcf-rate-limiter` | `1.0.3` | Optional | Deploys request rate limiting for supported invocation paths. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-rate-limiter:1.0.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/ratelimiter) |
| `helm-nvcf-sis` | `1.18.8` | Required | Deploys the Spot Instance Service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-sis:1.18.8` |  |
| `helm-nvcf-state-metrics` | `1.0.1` | Optional | Deploys NVCF state metrics for observability. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-state-metrics:1.0.1` |  |
| `helm-nvcf-vanity-gateway` | `0.1.0-nvcf-10204.1` | Optional | Deploys the optional vanity hostname gateway. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-vanity-gateway:0.1.0-nvcf-10204.1` |  |
| `helm-reval` | `1.3.8` | Required | Deploys the function revalidation service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-reval:1.3.8` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/helm-reval) |
| `nvcf-example-dashboards` | `1.6.0` | Optional | Deploys example Grafana dashboards for NVCF telemetry. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-example-dashboards:1.6.0` |  |
| `nvcf-gateway-routes` | `1.14.0` | Optional | Deploys Gateway API routes for the reference architecture. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-gateway-routes:1.14.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/gateway-routes) |
| `nvcf-observability-reference-stack` | `1.10.0` | Optional | Deploys a reference observability backend for evaluation. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-observability-reference-stack:1.10.0` |  |

### Control plane services and images

| Artifact | Version | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- |
| `admin-token-issuer-proxy` | `1.0.2` | Optional | Proxies admin token requests for the reference architecture. | `nvcr.io/nvidia/nvcf/admin-token-issuer-proxy:1.0.2` |  |
| `alpine-k8s` | `1.36.1` | Required | Provides Kubernetes command-line utilities for deployment jobs. | `docker.io/alpine/k8s:1.36.1` | [GitHub](https://github.com/alpine-docker/k8s) |
| `cassandra` | `2.0.1` | Required | Stores NVCF account, function, cluster, and service state. | `nvcr.io/nvidia/nvcf/cassandra:2.0.1` | [Upstream](https://github.com/apache/cassandra) |
| `cert-manager-cainjector` | `v1.20.2` | Required | Injects certificate authority data into Kubernetes resources. | `nvcr.io/nvidia/nvcf/cert-manager-cainjector:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `cert-manager-controller` | `v1.20.2` | Required | Reconciles certificates and issuers for the control plane. | `nvcr.io/nvidia/nvcf/cert-manager-controller:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `cert-manager-startupapicheck` | `v1.20.2` | Required | Verifies that the cert-manager API is ready. | `nvcr.io/nvidia/nvcf/cert-manager-startupapicheck:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `cert-manager-webhook` | `v1.20.2` | Required | Validates and converts cert-manager API resources. | `nvcr.io/nvidia/nvcf/cert-manager-webhook:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `ess-api` | `v0.57.26` | Required | Provides encrypted application secrets to NVCF workloads. | `nvcr.io/nvidia/nvcf/ess-api:v0.57.26` |  |
| `llm-api-gateway` | `0.8.3` | Optional | Exposes OpenAI-compatible APIs for LLM functions. | `nvcr.io/0833294136851237/selfhosted-ga/llm-api-gateway:0.8.3-ea` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/llm-api-gateway) |
| `llm-request-router` | `0.3.0` | Optional | Routes LLM requests to eligible worker instances. | `nvcr.io/0833294136851237/selfhosted-ga/stargate:0.3.0-ea` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/libraries/rust/stargate) |
| `nats-box` | `0.19.7-nonroot` | Required | Provides NATS administration and diagnostic utilities. | `nvcr.io/nvidia/nvcf/nats-box:0.19.7-nonroot` | [Upstream](https://github.com/nats-io/nats-box) |
| `nats-server` | `2.11.17-alpine3.22` | Required | Provides messaging for function deployment and invocation. | `nvcr.io/nvidia/nvcf/nats-server:2.11.17-alpine3.22` | [Upstream](https://github.com/nats-io/nats-server) |
| `nats-server-config-reloader` | `0.23.0` | Required | Reloads NATS server configuration when mounted settings change. | `docker.io/natsio/nats-server-config-reloader:0.23.0` | [Upstream](https://github.com/nats-io/k8s) |
| `notary-service` | `1.8.1` | Required | Signs and validates functions and cluster nodes. | `nvcr.io/nvidia/nvcf/notary-service:1.8.1` |  |
| `nvcf-api-keys-service` | `1.5.0` | Required | Creates and manages NVCF API keys. | `nvcr.io/nvidia/nvcf/nvcf-api-keys-service:1.5.0` |  |
| `nvcf-cassandra-migrations` | `0.14.1` | Required | Applies the Cassandra schemas required by NVCF services. | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-cassandra-migrations:0.14.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/migrations/cassandra) |
| `nvcf-grpc-proxy` | `1.29.1` | Required | Proxies bidirectional gRPC traffic between the control and compute planes. | `nvcr.io/nvidia/nvcf/nvcf-grpc-proxy:1.29.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/grpc-proxy) |
| `nvcf-invocation-service` | `0.8.8` | Required | Routes stateless HTTP function invocation requests. | `nvcr.io/nvidia/nvcf/nvcf-invocation-service:0.8.8` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/http-invocation) |
| `nvcf-nats-auth-callout-service` | `0.5.10` | Required | Authorizes NATS clients for NVCF services and workloads. | `nvcr.io/nvidia/nvcf/nvcf-nats-auth-callout-service:0.5.10` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/control-plane-services/nats-auth-callout) |
| `nvcf-openbao` | `2.5.5-nv-1.3.1` | Required | Stores and manages control-plane secrets. | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-openbao:2.5.5-nv-1.3.1` | [Upstream](https://github.com/openbao/openbao) |
| `nvcf-openbao-migrations` | `0.16.2` | Required | Applies the OpenBao configuration required by NVCF. | `nvcr.io/nvidia/nvcf/nvcf-openbao-migrations:0.16.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/migrations/openbao) |
| `nvcf-service-oss` | `1.9.0-hotfix.1` | Required | Provides the primary NVCF control-plane API. | `nvcr.io/nvidia/nvcf/nvcf-service-oss:1.9.0-hotfix.1` |  |
| `nvct-service-oss` | `1.5.9-hotfix.1` | Required | Provides tenant-scoped NVCF control-plane operations. | `nvcr.io/nvidia/nvcf/nvct-service-oss:1.5.9-hotfix.1` |  |
| `oss-vault-k8s` | `1.7.4` | Required | Integrates Kubernetes workloads with OpenBao secrets. | `nvcr.io/nvidia/nvcf/oss-vault-k8s:1.7.4` |  |
| `reval-server` | `0.2.2` | Required | Revalidates function state in the background. | `nvcr.io/nvidia/nvcf/reval-server:0.2.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/control-plane-services/helm-reval) |
| `spot` | `1.563.1-hotfix.1` | Required | Manages deployments, clusters, and function instances. | `nvcr.io/nvidia/nvcf/spot:1.563.1-hotfix.1` |  |

### Compute plane Helm charts

| Artifact | Version | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- |
| `csi-driver-smb` | `supported` | Optional | Provides SMB persistent volumes for supported deployments. | `https://raw.githubusercontent.com/kubernetes-csi/csi-driver-smb/master/charts` | [Upstream](https://github.com/kubernetes-csi/csi-driver-smb) |
| `ebs-csi-driver` | `supported` | Optional | Provides Amazon EBS persistent volumes for EKS clusters. | `https://kubernetes-sigs.github.io/aws-ebs-csi-driver` | [Upstream](https://github.com/kubernetes-sigs/aws-ebs-csi-driver) |
| `gpu-operator` | `supported` | Required | Manages NVIDIA GPU software on Kubernetes nodes. | `https://helm.ngc.nvidia.com/nvidia` | [Upstream](https://github.com/NVIDIA/gpu-operator) |
| `helm-nvca-operator` | `1.12.7` | Required | Deploys the NVCA operator and compute-plane integration. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvca-operator:1.12.7` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nvca-operator) |
| `nvcf-container-cache` | `0.25.22` | Optional | Deploys container image caching on GPU cluster nodes. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-container-cache:0.25.22` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/container-cache) |

### Compute plane services and images

| Artifact | Version | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- |
| `ess-agent` | `1.3.1` | Required | Injects encrypted application secrets into function workloads. | `nvcr.io/nvidia/nvcf/ess-agent:1.3.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/ess-agent) |
| `gpu-operator-validator` | `supported` | Required | Validates GPU Operator components on GPU nodes. | `https://catalog.ngc.nvidia.com/orgs/nvidia/teams/cloud-native/containers/gpu-operator-validator` | [Upstream](https://github.com/NVIDIA/gpu-operator) |
| `k8s-device-plugin` | `supported` | Required | Advertises NVIDIA GPU resources to Kubernetes. | `https://catalog.ngc.nvidia.com/orgs/nvidia/teams/k8s/containers/device-plugin` | [Upstream](https://github.com/NVIDIA/k8s-device-plugin) |
| `nvca` | `3.0.3` | Required | Registers GPU clusters and orchestrates deployments in-cluster. | `nvcr.io/nvidia/nvcf/nvca:3.0.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/nvca) |
| `nvca-operator` | `3.0.3` | Required | Reconciles NVCA resources and compute-plane configuration. | `nvcr.io/nvidia/nvcf/nvca-operator:3.0.3` |  |
| `nvcf-container-cache` | `v1.1.36` | Optional | Caches container image layers on GPU cluster nodes. | `nvcr.io/nvidia/nvcf/nvcf-container-cache:v1.1.36` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/container-cache) |
| `nvcf-image-credential-helper` | `0.10.2` | Required | Resolves container image credentials for function workloads. | `nvcr.io/nvidia/nvcf/nvcf-image-credential-helper:0.10.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/image-credential-helper) |
| `nvcf-proxy-tls-certs` | `v1.2.10` | Optional | Configures TLS trust for the optional container cache proxy. | `nvcr.io/nvidia/nvcf/nvcf-proxy-tls-certs:v1.2.10` |  |
| `nvcf-worker-init-oss` | `1.0.9` | Required | Prepares function resources before the user container starts. | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-worker-init-oss:1.0.9` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/worker-init) |
| `nvcf-worker-llm-credentials-oss` | `1.0.4` | Optional | Maintains a current NVCF worker token for LLM function workloads. | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-worker-llm-credentials-oss:1.0.4` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/worker-llm-credentials) |
| `nvcf-worker-utils-oss` | `1.0.4` | Required | Proxies NATS traffic between function containers and the control plane. | `nvcr.io/0833294136851237/nvcf-ncp-staging/nvcf-worker-utils-oss:1.0.4` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/worker-utils) |
| `pylon` | `0.3.2` | Optional | Connects LLM worker pods to the LLM request router. | `nvcr.io/nvidia/nvcf/pylon:0.3.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/libraries/rust/stargate) |

### Tools and deployment resources

| Artifact | Version | Description | Distribution | Source code |
| --- | --- | --- | --- | --- |
| `nvcf-cli` | `1.10.3` | Manages functions, deployments, and clusters from the command line. | `nvcr.io/nvidia/nvcf/nvcf-cli:1.10.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/clis/nvcf-cli) |
| `nvcf-compute-plane-stack` | `1.0.6` | Provides the Helmfile bundle for compute-plane deployment. | `nvcr.io/nvidia/nvcf/nvcf-compute-plane-stack:1.0.6` |  |
| `nvcf-self-managed-stack` | `0.6.0-rc.118` | Provides the Helmfile bundle for control-plane deployment. | `nvcr.io/nvidia/nvcf/nvcf-self-managed-stack:0.6.0-rc.118` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/stacks/self-managed) |

{/* docs-version-sync:END manifest-artifact-registry-paths */}
