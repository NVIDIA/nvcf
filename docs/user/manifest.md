# Artifact Manifest

This section provides a comprehensive list of all components required for NVIDIA Cloud Functions (NVCF) Self-Hosted deployment for basic inference. Additional components are needed for Low Latency Streaming (Simulation).

## Artifacts Overview

The following inventories list the artifacts for an inference-only self-hosted
NVCF deployment. Artifacts are grouped by deployment plane and type.

<Warning>
Artifact version compatibility

Newer artifact versions might be available. NVCF self-managed, compute-plane,
and observability stack releases are QA-qualified as umbrella releases with
the specific versions shown on this page. Use these versions together. NVIDIA
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

### Supporting images that ship from Docker Hub

The NATS configuration reloader and the account-bootstrap Kubernetes
utilities are not republished under the public `nvidia/nvcf` catalog, so they
default to their upstream Docker Hub source:

| Value | Default image |
| --- | --- |
| `nats.reloader.image` | `docker.io/natsio/nats-server-config-reloader:0.24.0` |
| `api.accountBootstrap.image` | `docker.io/alpine/k8s:1.37.0` |

A public-catalog install needs no configuration for these two images. Verify
that your cluster can reach Docker Hub. If your egress to Docker Hub is
authenticated or rate-limited, add the pull secret to
`global.imagePullSecrets`.

If you have mirrored both images into your own registry, redirect them from
your environment file
(`deploy/stacks/self-managed/environments/<environment>.yaml`). Set `registry`
and `repository` together, because each key falls back to its own Docker Hub
default rather than to `global.image`. You do not need to edit
`deploy/stacks/self-managed/global.yaml.gotmpl`.

```yaml
nats:
  reloader:
    image:
      registry: <your-registry>
      repository: <your-repository>/nats-server-config-reloader
      tag: "0.24.0"

api:
  accountBootstrap:
    image:
      registry: <your-registry>
      repository: <your-repository>/alpine-k8s
      tag: "1.37.0"
```

### Override the Cassandra images

The Cassandra server and its dynamic seed discovery container take the same
override keys, but they default to `global.image`. Set them when your registry
uses a different path or tag than the `cassandra` default under
`global.image.repository`:

```yaml
cassandra:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf/cassandra
  dynamicSeedDiscovery:
    image:
      registry: nvcr.io
      repository: nvidia/nvcf/cassandra
```

Every `registry`, `repository`, and `tag` key is optional. Any key you leave
unset keeps its default: for Cassandra, `registry` and `repository` fall back
to `global.image.registry` and `global.image.repository`; for the reloader and
account-bootstrap images they fall back to the Docker Hub coordinates above.
`tag` falls back to the version the stack pins, or to the chart's own default
when the stack pins none.

Use the version listed in the artifact table. Verify that your cluster can
reach the registry you point each image at. If it requires authentication, add
its pull secret to `global.imagePullSecrets`.

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

{/*docs-version-sync:BEGIN manifest-artifact-registry-paths*/}

### Stack release set

Documentation: `dev` (development)

| Stack | Version | Source tag |
| --- | --- | --- |
| Control plane | `0.20.6` | `deploy/stacks/self-managed/v0.20.6` |
| Compute plane | `0.4.4` | `deploy/stacks/nvcf-compute-plane/v0.4.4` |
| Observability | `0.2.2` | `deploy/stacks/observability/v0.2.2` |

### Control plane Helm charts

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |
| `helm-admin-token-issuer-proxy` | `1.5.3` | `self-managed` | Required | Deploys the admin token issuer proxy. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-admin-token-issuer-proxy:1.5.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/admin-token-issuer-proxy) |
| `helm-nvcf-api` | `1.27.1` | `self-managed` | Required | Deploys the NVCF API service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-api:1.27.1` |  |
| `helm-nvcf-api-keys` | `1.8.0` | `self-managed` | Required | Deploys the API key management service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-api-keys:1.8.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/api-keys-colocated) |
| `helm-nvcf-cassandra` | `0.21.3` | `self-managed` | Required | Deploys Cassandra and its initialization jobs. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-cassandra:0.21.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/cassandra) / [Upstream](https://github.com/bitnami/charts/tree/main/bitnami/cassandra) |
| `helm-nvcf-cert-manager` | `0.1.0` | `self-managed` | Required | Deploys the NVCF cert-manager configuration. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-cert-manager:0.1.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/cert-manager) / [Upstream](https://github.com/cert-manager/cert-manager) |
| `helm-nvcf-ess-api` | `1.8.2` | `self-managed` | Required | Deploys the Encrypted Secrets Service API. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-ess-api:1.8.2` |  |
| `helm-nvcf-function-autoscaler` | `0.5.2` | `self-managed` | Required | Deploys the function autoscaler for observability-driven scaling. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/function-autoscaler) |
| `helm-nvcf-grpc-proxy` | `1.7.4` | `self-managed` | Required | Deploys the gRPC proxy service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-grpc-proxy:1.7.4` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/grpc-proxy) |
| `helm-nvcf-invocation-service` | `1.6.1` | `self-managed` | Required | Deploys the HTTP invocation service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-invocation-service:1.6.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/http-invocation) |
| `helm-nvcf-llm-api-gateway` | `1.4.3` | `self-managed` | Optional | Deploys the OpenAI-compatible LLM API gateway. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-llm-api-gateway:1.4.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/llm-api-gateway) |
| `helm-nvcf-llm-request-router` | `1.14.1` | `self-managed` | Optional | Deploys the LLM request router. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-llm-request-router:1.14.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/llm-request-router) |
| `helm-nvcf-nats` | `0.8.4` | `self-managed` | Required | Deploys NATS messaging for the control plane. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-nats:0.8.4` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nats) / [Upstream](https://github.com/nats-io/k8s) |
| `helm-nvcf-nats-auth-callout-service` | `1.2.1` | `self-managed` | Required | Deploys the NATS authorization callout service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-nats-auth-callout-service:1.2.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nats-auth-callout) |
| `helm-nvcf-notary-service` | `1.6.0` | `self-managed` | Required | Deploys the notary service for signing and validation. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-notary-service:1.6.0` |  |
| `helm-nvcf-nvct-api` | `1.6.0` | `self-managed` | Required | Deploys the NVCF tenant API service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-nvct-api:1.6.0` |  |
| `helm-nvcf-openbao-server` | `0.32.6` | `self-managed` | Required | Deploys OpenBao secret management. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-openbao-server:0.32.6` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/openbao) / [Upstream](https://github.com/openbao/openbao-helm) |
| `helm-nvcf-pki` | `0.1.0` | `self-managed` | Optional | Provisions the OpenBao-backed ClusterIssuer for NVCF service TLS. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-pki:0.1.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nvcf-pki) |
| `helm-nvcf-rate-limiter` | `1.2.1` | `self-managed` | Required | Deploys request rate limiting for supported invocation paths. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-rate-limiter:1.2.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/ratelimiter) |
| `helm-nvcf-sis` | `2.4.0` | `self-managed` | Required | Deploys the Spot Instance Service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-sis:2.4.0` |  |
| `helm-nvcf-state-metrics` | `1.0.2` | `self-managed` | Required | Deploys NVCF state metrics for observability. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-state-metrics:1.0.2` |  |
| `helm-nvcf-ui` | `1.1.2` | `self-managed` | Optional | Deploys the optional NVCF UI admin panel. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-ui:1.1.2` |  |
| `helm-nvcf-vanity-gateway` | `0.5.0` | `self-managed` | Optional | Deploys the optional vanity hostname gateway. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-vanity-gateway:0.5.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/vanity-gateway) |
| `helm-reval` | `1.4.1` | `self-managed` | Required | Deploys the function revalidation service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-reval:1.4.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/helm-reval) |
| `nvcf-example-dashboards` | `1.6.0` | `self-managed` | Optional | Deploys example Grafana dashboards for NVCF telemetry. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-example-dashboards:1.6.0` |  |
| `nvcf-gateway-routes` | `1.18.2` | `self-managed` | Required | Deploys Gateway API routes for NVCF services. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-gateway-routes:1.18.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/gateway-routes) |
| `nvcf-observability-reference-stack` | `1.10.0` | `self-managed` | Optional | Deploys a reference observability backend for evaluation. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-observability-reference-stack:1.10.0` |  |

### Control plane services and images

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |
| `admin-token-issuer-proxy` | `1.1.2` | `self-managed` | Required | Proxies admin token requests for stack services. | `nvcr.io/nvidia/nvcf/admin-token-issuer-proxy:1.1.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/control-plane-services/admin-token-issuer-proxy) |
| `cassandra` | `5.0.9-nv-2.0.5` | `self-managed` | Required | Stores NVCF account, function, cluster, and service state. | `nvcr.io/nvidia/nvcf/cassandra:5.0.9-nv-2.0.5` | [Upstream](https://github.com/apache/cassandra) |
| `cert-manager-acmesolver` | `v1.20.2` | `self-managed` | Optional | Serves temporary ACME HTTP-01 domain-validation challenges. | `quay.io/jetstack/cert-manager-acmesolver:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `cert-manager-cainjector` | `v1.20.2` | `self-managed` | Required | Injects certificate authority data into Kubernetes resources. | `nvcr.io/nvidia/nvcf/cert-manager-cainjector:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `cert-manager-controller` | `v1.20.2` | `self-managed` | Required | Reconciles certificates and issuers for the control plane. | `nvcr.io/nvidia/nvcf/cert-manager-controller:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `cert-manager-startupapicheck` | `v1.20.2` | `self-managed` | Required | Verifies that the cert-manager API is ready. | `nvcr.io/nvidia/nvcf/cert-manager-startupapicheck:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `cert-manager-webhook` | `v1.20.2` | `self-managed` | Required | Validates and converts cert-manager API resources. | `nvcr.io/nvidia/nvcf/cert-manager-webhook:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `ess-agent` | `1.4.1` | `self-managed` | Required | Injects encrypted application secrets into function workloads. | `nvcr.io/nvidia/nvcf/ess-agent:1.4.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/ess-agent) |
| `icms-service-oss` | `0.7.2` | `self-managed` | Required | Manages instance and cluster lifecycle operations. | `nvcr.io/nvidia/nvcf/icms-service-oss:0.7.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/control-plane-services/instance-cluster-management) |
| `k8s` | `1.37.0` | `self-managed` | Required | Provides Kubernetes command-line utilities for deployment jobs. | `docker.io/alpine/k8s:1.37.0` | [GitHub](https://github.com/alpine-docker/k8s) |
| `llm-api-gateway` | `0.14.2` | `self-managed` | Optional | Exposes OpenAI-compatible APIs for LLM functions. | `nvcr.io/nvidia/nvcf/llm-api-gateway:0.14.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/llm-api-gateway) |
| `nats-box` | `0.19.7-nonroot` | `self-managed` | Required | Provides NATS administration and diagnostic utilities. | `nvcr.io/nvidia/nvcf/nats-box:0.19.7-nonroot` | [Upstream](https://github.com/nats-io/nats-box) |
| `nats-server` | `2.14.6-alpine3.22` | `self-managed` | Required | Provides messaging for function deployment and invocation. | `nvcr.io/nvidia/nvcf/nats-server:2.14.6-alpine3.22` | [Upstream](https://github.com/nats-io/nats-server) |
| `nvcf-ai-api-gateway-service` | `1.35.1` | `self-managed` | Optional | Serves the optional vanity hostname gateway. | `nvcr.io/nvidia/nvcf/nvcf-ai-api-gateway-service:1.35.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/vanity-gateway) |
| `nvcf-api-keys-service` | `1.9.1` | `self-managed` | Required | Creates and manages NVCF API keys. | `nvcr.io/nvidia/nvcf/nvcf-api-keys-service:1.9.1` |  |
| `nvcf-ess` | `0.5.0` | `self-managed` | Required | Provides encrypted application secrets to NVCF workloads. | `nvcr.io/nvidia/nvcf/nvcf-ess:0.5.0` |  |
| `nvcf-function-autoscaler` | `1.21.8` | `self-managed` | Required | Scales functions from NVCF workload metrics. | `Publication pending` |  |
| `nvcf-grpc-proxy` | `1.33.5` | `self-managed` | Required | Proxies bidirectional gRPC traffic between the control and compute planes. | `nvcr.io/nvidia/nvcf/nvcf-grpc-proxy:1.33.5` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/grpc-proxy) |
| `nvcf-invocation-service` | `0.12.1` | `self-managed` | Required | Routes stateless HTTP function invocation requests. | `nvcr.io/nvidia/nvcf/nvcf-invocation-service:0.12.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/http-invocation) |
| `nvcf-nats-auth-callout-service` | `0.8.3` | `self-managed` | Required | Authorizes NATS clients for NVCF services and workloads. | `nvcr.io/nvidia/nvcf/nvcf-nats-auth-callout-service:0.8.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/control-plane-services/nats-auth-callout) |
| `nvcf-notary` | `1.14.1` | `self-managed` | Required | Signs and validates functions and cluster nodes. | `nvcr.io/nvidia/nvcf/nvcf-notary:1.14.1` |  |
| `nvcf-openbao` | `2.6.2-nv-1.3.4` | `self-managed` | Required | Stores and manages control-plane secrets. | `nvcr.io/nvidia/nvcf/nvcf-openbao:2.6.2-nv-1.3.4` | [Upstream](https://github.com/openbao/openbao) |
| `nvcf-openbao-migrations` | `0.19.5` | `self-managed` | Required | Applies the OpenBao configuration required by NVCF. | `nvcr.io/nvidia/nvcf/nvcf-openbao-migrations:0.19.5` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/migrations/openbao) |
| `nvcf-ratelimiter` | `1.17.3` | `self-managed` | Required | Enforces request rate limits for supported invocation paths. | `nvcr.io/nvidia/nvcf/nvcf-ratelimiter:1.17.3` |  |
| `nvcf-service-oss` | `1.18.0` | `self-managed` | Required | Provides the primary NVCF control-plane API. | `nvcr.io/nvidia/nvcf/nvcf-service-oss:1.18.0` |  |
| `nvcf-state-metrics-service` | `1.23.7` | `self-managed` | Required | Exports NVCF resource state as Prometheus metrics. | `nvcr.io/nvidia/nvcf/nvcf-state-metrics-service:1.23.7` |  |
| `nvcf-ui` | `1.1.2` | `self-managed` | Optional | Serves the optional NVCF administrative interface. | `nvcr.io/nvidia/nvcf/nvcf-ui:1.1.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/uis/nvcf-ui) |
| `nvcf-worker-init-oss` | `1.2.1` | `self-managed` | Required | Prepares function resources before the user container starts. | `nvcr.io/nvidia/nvcf/nvcf-worker-init-oss:1.2.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/worker-init) |
| `nvcf-worker-llm-credentials-oss` | `1.1.2` | `self-managed` | Required | Maintains a current NVCF worker token for LLM function workloads. | `nvcr.io/nvidia/nvcf/nvcf-worker-llm-credentials-oss:1.1.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/worker-llm-credentials) |
| `nvcf-worker-utils-oss` | `1.2.3` | `self-managed` | Required | Proxies NATS traffic between function containers and the control plane. | `nvcr.io/nvidia/nvcf/nvcf-worker-utils-oss:1.2.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/worker-utils) |
| `nvct-service-oss` | `1.66.0` | `self-managed` | Required | Provides tenant-scoped NVCF control-plane operations. | `nvcr.io/nvidia/nvcf/nvct-service-oss:1.66.0` |  |
| `oss-vault-k8s` | `1.7.4` | `self-managed` | Required | Integrates Kubernetes workloads with OpenBao secrets. | `nvcr.io/nvidia/nvcf/oss-vault-k8s:1.7.4` |  |
| `reval-server` | `0.20.2` | `self-managed` | Required | Revalidates function state in the background. | `nvcr.io/nvidia/nvcf/reval-server:0.20.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/control-plane-services/helm-reval) |
| `stargate` | `0.18.0` | `self-managed` | Optional | Routes LLM requests to eligible worker instances. | `nvcr.io/nvidia/nvcf/stargate:0.18.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/libraries/rust/stargate) |

### Compute plane Helm charts

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |
| `csi-driver-smb` | `supported` | Independent | Optional | Provides SMB persistent volumes for supported deployments. | `https://raw.githubusercontent.com/kubernetes-csi/csi-driver-smb/master/charts` | [Upstream](https://github.com/kubernetes-csi/csi-driver-smb) |
| `dynamo-platform` | `1.4.2` | `compute-plane` | Optional | Deploys the optional NVIDIA Dynamo operator. | `https://helm.ngc.nvidia.com/nvidia/ai-dynamo/dynamo-platform:1.4.2` | [Upstream](https://github.com/ai-dynamo/dynamo) |
| `ebs-csi-driver` | `supported` | Independent | Optional | Provides Amazon EBS persistent volumes for EKS clusters. | `https://kubernetes-sigs.github.io/aws-ebs-csi-driver` | [Upstream](https://github.com/kubernetes-sigs/aws-ebs-csi-driver) |
| `gpu-operator` | `supported` | Independent | Required | Manages NVIDIA GPU software on Kubernetes nodes. | `https://helm.ngc.nvidia.com/nvidia` | [Upstream](https://github.com/NVIDIA/gpu-operator) |
| `grove-charts` | `v0.1.0-alpha.12` | `compute-plane` | Optional | Deploys the optional Grove operator for topology-aware scheduling. | `oci://ghcr.io/ai-dynamo/grove/grove-charts:v0.1.0-alpha.12` | [Upstream](https://github.com/ai-dynamo/grove) |
| `helm-nvca-operator` | `1.28.0` | `compute-plane` | Required | Deploys the NVCA operator and compute-plane integration. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvca-operator:1.28.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nvca-operator) |
| `kai-scheduler` | `v0.17.1` | `compute-plane` | Optional | Deploys the optional KAI Scheduler. | `oci://ghcr.io/kai-scheduler/kai-scheduler/kai-scheduler:v0.17.1` | [Upstream](https://github.com/NVIDIA/KAI-Scheduler) |
| `modelexpress` | `supported` | Independent | Optional | Distributes model weights peer-to-peer between Dynamo workers to reduce scale-out cold starts. Installed separately from the compute-plane stack. | `https://helm.ngc.nvidia.com/nvidia/ai-dynamo` | [Upstream](https://github.com/ai-dynamo/modelexpress) |
| `nvcf-cluster-topology` | `0.1.0` | `compute-plane` | Required | Configures cluster topology resources for compute scheduling. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-cluster-topology:0.1.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/stacks/nvcf-compute-plane/charts/nvcf-cluster-topology) |
| `nvcf-container-cache` | `0.25.22` | `compute-plane` | Optional | Deploys container image caching on GPU cluster nodes. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-container-cache:0.25.22` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/container-cache) |

### Compute plane services and images

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |
| `crd-upgrader` | `v0.17.1` | `compute-plane` | Optional | Upgrades KAI Scheduler custom resources. | `ghcr.io/kai-scheduler/kai-scheduler/crd-upgrader:v0.17.1` | [Upstream](https://github.com/NVIDIA/KAI-Scheduler) |
| `gpu-operator-validator` | `supported` | Independent | Required | Validates GPU Operator components on GPU nodes. | `https://catalog.ngc.nvidia.com/orgs/nvidia/teams/cloud-native/containers/gpu-operator-validator` | [Upstream](https://github.com/NVIDIA/gpu-operator) |
| `grove-install-crds` | `v0.1.0-alpha.12` | `compute-plane` | Optional | Installs Grove custom resource definitions. | `ghcr.io/ai-dynamo/grove/grove-install-crds:v0.1.0-alpha.12` | [Upstream](https://github.com/ai-dynamo/grove) |
| `grove-operator` | `v0.1.0-alpha.12` | `compute-plane` | Optional | Reconciles Grove topology-aware scheduling resources. | `ghcr.io/ai-dynamo/grove/grove-operator:v0.1.0-alpha.12` | [Upstream](https://github.com/ai-dynamo/grove) |
| `k8s-device-plugin` | `supported` | Independent | Required | Advertises NVIDIA GPU resources to Kubernetes. | `https://catalog.ngc.nvidia.com/orgs/nvidia/teams/k8s/containers/device-plugin` | [Upstream](https://github.com/NVIDIA/k8s-device-plugin) |
| `kubernetes-operator` | `1.4.2` | `compute-plane` | Optional | Reconciles NVIDIA Dynamo workloads on Kubernetes. | `nvcr.io/nvidia/ai-dynamo/kubernetes-operator:1.4.2` | [Upstream](https://github.com/ai-dynamo/dynamo) |
| `modelexpress-server` | `supported` | Independent | Optional | Serves model weights to Dynamo workers over NIXL RDMA transports. | `https://catalog.ngc.nvidia.com/orgs/nvidia/teams/ai-dynamo/containers/modelexpress-server` | [Upstream](https://github.com/ai-dynamo/modelexpress) |
| `nats` | `2.10.21-alpine` | `compute-plane` | Optional | Provides messaging for the optional NVIDIA Dynamo operator. | `docker.io/library/nats:2.10.21-alpine` | [Upstream](https://github.com/nats-io/nats-server) |
| `nvca` | `3.10.0` | `compute-plane` | Required | Registers GPU clusters and orchestrates deployments in-cluster. | `nvcr.io/nvidia/nvcf/nvca:3.10.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/nvca) |
| `nvca-operator` | `3.10.0` | `compute-plane` | Required | Reconciles NVCA resources and compute-plane configuration. | `nvcr.io/nvidia/nvcf/nvca-operator:3.10.0` |  |
| `nvcf-container-cache` | `v1.1.36` | `compute-plane` | Optional | Caches container image layers on GPU cluster nodes. | `nvcr.io/nvidia/nvcf/nvcf-container-cache:v1.1.36` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/container-cache) |
| `nvcf-image-credential-helper` | `0.11.1` | `compute-plane` | Required | Resolves container image credentials for function workloads. | `nvcr.io/nvidia/nvcf/nvcf-image-credential-helper:0.11.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/image-credential-helper) |
| `nvcf-proxy-tls-certs` | `v1.2.10` | `compute-plane` | Optional | Configures TLS trust for the optional container cache proxy. | `nvcr.io/nvidia/nvcf/nvcf-proxy-tls-certs:v1.2.10` |  |
| `operator` | `v0.17.1` | `compute-plane` | Optional | Reconciles KAI Scheduler resources. | `ghcr.io/kai-scheduler/kai-scheduler/operator:v0.17.1` | [Upstream](https://github.com/NVIDIA/KAI-Scheduler) |
| `pylon` | `0.18.0` | `compute-plane` | Optional | Connects LLM worker pods to the LLM request router. | `nvcr.io/nvidia/nvcf/pylon:0.18.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/libraries/rust/stargate) |

### Observability Helm charts

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |
| `nvcf-default-monitors` | `0.2.0` | `observability` | Required | Deploys the default service and pod monitors for NVCF. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-default-monitors:0.2.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/stacks/observability/charts/nvcf-default-monitors) |
| `nvcf-otel-collector` | `0.2.0` | `observability` | Required | Configures the OpenTelemetry Collector used by NVCF. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-otel-collector:0.2.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/stacks/observability/charts/nvcf-otel-collector) |
| `opentelemetry-operator` | `0.122.0` | `observability` | Required | Deploys the OpenTelemetry Operator. | `https://open-telemetry.github.io/opentelemetry-helm-charts/opentelemetry-operator:0.122.0` | [Upstream](https://github.com/open-telemetry/opentelemetry-operator) |
| `prometheus-operator-crds` | `31.0.1` | `observability` | Required | Installs the Prometheus Operator custom resource definitions. | `https://prometheus-community.github.io/helm-charts/prometheus-operator-crds:31.0.1` | [Upstream](https://github.com/prometheus-community/helm-charts) |
| `victoria-metrics-single` | `0.45.0` | `observability` | Required | Deploys the default metrics storage backend. | `https://victoriametrics.github.io/helm-charts/victoria-metrics-single:0.45.0` | [Upstream](https://github.com/VictoriaMetrics/helm-charts) |

### Observability services and images

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |
| `opentelemetry-collector-contrib` | `0.160.0` | `observability` | Required | Collects and exports NVCF telemetry. | `ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.160.0` | [Upstream](https://github.com/open-telemetry/opentelemetry-collector-contrib) |
| `opentelemetry-operator` | `0.158.0` | `observability` | Required | Reconciles OpenTelemetry Collector resources. | `ghcr.io/open-telemetry/opentelemetry-operator/opentelemetry-operator:0.158.0` | [Upstream](https://github.com/open-telemetry/opentelemetry-operator) |
| `victoria-metrics` | `v1.150.0` | `observability` | Required | Stores metrics for the default observability profile. | `docker.io/victoriametrics/victoria-metrics:v1.150.0` | [Upstream](https://github.com/VictoriaMetrics/VictoriaMetrics) |

### Cross-stack Helm charts

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |

### Cross-stack services and images

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |
| `nats-server-config-reloader` | `0.24.0` | `compute-plane` / `self-managed` | Required | Reloads NATS configuration for the control plane and optional NVIDIA Dynamo deployment. | `docker.io/natsio/nats-server-config-reloader:0.24.0` | [Upstream](https://github.com/nats-io/k8s) |

### EA-only CVE-impacted artifacts

These Early Access artifacts have known CVE impact. Use only the QA-qualified versions listed for this EA stack.

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |
| `nvcf-cassandra-migrations` | `0.17.6` | `self-managed` | Required | Applies the Cassandra schemas required by Early Access NVCF services. | `nvcr.io/nvidia/nvcf/nvcf-cassandra-migrations:0.17.6` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/migrations/cassandra) |

### Tools and deployment resources

| Artifact | Version | Stack | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- |
| `nvcf-cli` | `1.16.2` | Independent | Manages functions, deployments, and clusters from the command line. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/clis/nvcf-cli) |
| `nvcf-compute-plane-stack` | `0.4.4` | `compute-plane` | Provides the Helmfile bundle for compute-plane deployment. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/stacks/nvcf-compute-plane) |
| `nvcf-observability-stack` | `0.2.2` | `observability` | Provides the Helmfile bundle for standalone observability deployment. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/stacks/observability) |
| `nvcf-self-managed-stack` | `0.20.6` | `self-managed` | Provides the Helmfile bundle for control-plane deployment. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/stacks/self-managed) |

{/*docs-version-sync:END manifest-artifact-registry-paths*/}
