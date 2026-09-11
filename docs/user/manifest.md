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

### Supporting images that ship from Docker Hub

The NATS configuration reloader and the account-bootstrap Kubernetes
utilities are not republished under the public `nvidia/nvcf` catalog, so they
default to their upstream Docker Hub source:

| Value | Default image |
| --- | --- |
| `nats.reloader.image` | `docker.io/natsio/nats-server-config-reloader:0.23.0` |
| `api.accountBootstrap.image` | `docker.io/alpine/k8s:1.36.1` |

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
      tag: "0.23.0"

api:
  accountBootstrap:
    image:
      registry: <your-registry>
      repository: <your-repository>/alpine-k8s
      tag: "1.36.1"
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

### Control plane Helm charts

| Artifact | Version | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- |
| `helm-admin-token-issuer-proxy` | `1.5.2` | Required | Deploys the admin token issuer proxy. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/admin-token-issuer-proxy) |
| `helm-nvcf-api` | `1.27.0` | Required | Deploys the NVCF API service. | `Publication pending` |  |
| `helm-nvcf-api-keys` | `1.8.0` | Required | Deploys the API key management service. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/api-keys-colocated) |
| `helm-nvcf-cassandra` | `0.21.0` | Required | Deploys Cassandra and its initialization jobs. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/cassandra) / [Upstream](https://github.com/bitnami/charts/tree/main/bitnami/cassandra) |
| `helm-nvcf-cert-manager` | `0.1.0` | Required | Deploys the NVCF cert-manager configuration. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-cert-manager:0.1.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/cert-manager) / [Upstream](https://github.com/cert-manager/cert-manager) |
| `helm-nvcf-ess-api` | `1.8.2` | Required | Deploys the Encrypted Secrets Service API. | `Publication pending` |  |
| `helm-nvcf-function-autoscaler` | `0.4.0` | Required | Deploys the function autoscaler for observability-driven scaling. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/function-autoscaler) |
| `helm-nvcf-grpc-proxy` | `1.7.3` | Required | Deploys the gRPC proxy service. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/grpc-proxy) |
| `helm-nvcf-invocation-service` | `1.6.1` | Required | Deploys the HTTP invocation service. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/http-invocation) |
| `helm-nvcf-llm-api-gateway` | `1.4.3` | Optional | Deploys the OpenAI-compatible LLM API gateway. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/llm-api-gateway) |
| `helm-nvcf-llm-request-router` | `1.13.3` | Optional | Deploys the LLM request router. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/llm-request-router) |
| `helm-nvcf-nats` | `0.8.1` | Required | Deploys NATS messaging for the control plane. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nats) / [Upstream](https://github.com/nats-io/k8s) |
| `helm-nvcf-nats-auth-callout-service` | `1.2.0` | Required | Deploys the NATS authorization callout service. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nats-auth-callout) |
| `helm-nvcf-notary-service` | `1.6.0` | Required | Deploys the notary service for signing and validation. | `Publication pending` |  |
| `helm-nvcf-nvct-api` | `1.6.0` | Required | Deploys the NVCF tenant API service. | `Publication pending` |  |
| `helm-nvcf-openbao-server` | `0.32.4` | Required | Deploys OpenBao secret management. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/openbao) / [Upstream](https://github.com/openbao/openbao-helm) |
| `helm-nvcf-pki` | `0.1.0` | Optional | Provisions the OpenBao-backed ClusterIssuer for NVCF service TLS. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-pki:0.1.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nvcf-pki) |
| `helm-nvcf-rate-limiter` | `1.2.1` | Required | Deploys request rate limiting for supported invocation paths. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/ratelimiter) |
| `helm-nvcf-sis` | `2.3.0` | Required | Deploys the Spot Instance Service. | `Publication pending` |  |
| `helm-nvcf-state-metrics` | `1.0.2` | Required | Deploys NVCF state metrics for observability. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-state-metrics:1.0.2` |  |
| `helm-nvcf-ui` | `1.1.2` | Optional | Deploys the optional NVCF UI admin panel. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-ui:1.1.2` |  |
| `helm-nvcf-vanity-gateway` | `0.4.4` | Optional | Deploys the optional vanity hostname gateway. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/vanity-gateway) |
| `helm-reval` | `1.4.0` | Required | Deploys the function revalidation service. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/helm-reval) |
| `nvcf-default-monitors` | `0.2.0` | Required | Deploys the default service and pod monitors for NVCF. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/stacks/observability/charts/nvcf-default-monitors) |
| `nvcf-example-dashboards` | `1.6.0` | Optional | Deploys example Grafana dashboards for NVCF telemetry. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-example-dashboards:1.6.0` |  |
| `nvcf-gateway-routes` | `1.18.1` | Required | Deploys Gateway API routes for NVCF services. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/gateway-routes) |
| `nvcf-observability-reference-stack` | `1.10.0` | Optional | Deploys a reference observability backend for evaluation. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-observability-reference-stack:1.10.0` |  |
| `nvcf-otel-collector` | `0.2.0` | Required | Configures the OpenTelemetry Collector used by NVCF. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/stacks/observability/charts/nvcf-otel-collector) |
| `opentelemetry-operator` | `0.122.0` | Required | Deploys the OpenTelemetry Operator. | `Publication pending` | [Upstream](https://github.com/open-telemetry/opentelemetry-operator) |
| `prometheus-operator-crds` | `31.0.1` | Required | Installs the Prometheus Operator custom resource definitions. | `Publication pending` | [Upstream](https://github.com/prometheus-community/helm-charts) |
| `victoria-metrics-single` | `0.45.0` | Required | Deploys the default metrics storage backend. | `Publication pending` | [Upstream](https://github.com/VictoriaMetrics/helm-charts) |

### Control plane services and images

| Artifact | Version | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- |
| `admin-token-issuer-proxy` | `1.1.1` | Required | Proxies admin token requests for stack services. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/control-plane-services/admin-token-issuer-proxy) |
| `cassandra` | `5.0.8-nv-2.0.1` | Required | Stores NVCF account, function, cluster, and service state. | `nvcr.io/nvidia/nvcf/cassandra:5.0.8-nv-2.0.1` | [Upstream](https://github.com/apache/cassandra) |
| `cert-manager-cainjector` | `v1.20.2` | Required | Injects certificate authority data into Kubernetes resources. | `nvcr.io/nvidia/nvcf/cert-manager-cainjector:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `cert-manager-controller` | `v1.20.2` | Required | Reconciles certificates and issuers for the control plane. | `nvcr.io/nvidia/nvcf/cert-manager-controller:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `cert-manager-startupapicheck` | `v1.20.2` | Required | Verifies that the cert-manager API is ready. | `nvcr.io/nvidia/nvcf/cert-manager-startupapicheck:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `cert-manager-webhook` | `v1.20.2` | Required | Validates and converts cert-manager API resources. | `nvcr.io/nvidia/nvcf/cert-manager-webhook:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `icms-service-oss` | `0.7.1` | Required | Manages instance and cluster lifecycle operations. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/control-plane-services/instance-cluster-management) |
| `k8s` | `1.37.0` | Required | Provides Kubernetes command-line utilities for deployment jobs. | `Publication pending` | [GitHub](https://github.com/alpine-docker/k8s) |
| `llm-api-gateway` | `0.14.2` | Optional | Exposes OpenAI-compatible APIs for LLM functions. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/llm-api-gateway) |
| `nats-box` | `0.19.7-nonroot` | Required | Provides NATS administration and diagnostic utilities. | `nvcr.io/nvidia/nvcf/nats-box:0.19.7-nonroot` | [Upstream](https://github.com/nats-io/nats-box) |
| `nats-server` | `2.14.6-alpine3.22` | Required | Provides messaging for function deployment and invocation. | `Publication pending` | [Upstream](https://github.com/nats-io/nats-server) |
| `nats-server-config-reloader` | `0.24.0` | Required | Reloads NATS server configuration when mounted settings change. | `Publication pending` | [Upstream](https://github.com/nats-io/k8s) |
| `nvcf-ai-api-gateway-service` | `1.35.1` | Optional | Serves the optional vanity hostname gateway. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/vanity-gateway) |
| `nvcf-api-keys-service` | `1.9.1` | Required | Creates and manages NVCF API keys. | `Publication pending` |  |
| `nvcf-ess` | `0.5.0` | Required | Provides encrypted application secrets to NVCF workloads. | `Publication pending` |  |
| `nvcf-function-autoscaler` | `1.21.5` | Required | Scales functions from NVCF workload metrics. | `Publication pending` |  |
| `nvcf-grpc-proxy` | `1.33.4` | Required | Proxies bidirectional gRPC traffic between the control and compute planes. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/grpc-proxy) |
| `nvcf-invocation-service` | `0.12.1` | Required | Routes stateless HTTP function invocation requests. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/http-invocation) |
| `nvcf-nats-auth-callout-service` | `0.8.3` | Required | Authorizes NATS clients for NVCF services and workloads. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/control-plane-services/nats-auth-callout) |
| `nvcf-notary` | `1.14.1` | Required | Signs and validates functions and cluster nodes. | `Publication pending` |  |
| `nvcf-openbao` | `2.5.5-nv-1.3.1` | Required | Stores and manages control-plane secrets. | `Publication pending` | [Upstream](https://github.com/openbao/openbao) |
| `nvcf-openbao-migrations` | `0.19.1` | Required | Applies the OpenBao configuration required by NVCF. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/migrations/openbao) |
| `nvcf-ratelimiter` | `1.17.3` | Required | Enforces request rate limits for supported invocation paths. | `Publication pending` |  |
| `nvcf-service-oss` | `1.18.0` | Required | Provides the primary NVCF control-plane API. | `Publication pending` |  |
| `nvcf-state-metrics-service` | `1.23.7` | Required | Exports NVCF resource state as Prometheus metrics. | `nvcr.io/nvidia/nvcf/nvcf-state-metrics-service:1.23.7` |  |
| `nvcf-ui` | `1.1.2` | Optional | Serves the optional NVCF administrative interface. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/uis/nvcf-ui) |
| `nvct-service-oss` | `1.66.0` | Required | Provides tenant-scoped NVCF control-plane operations. | `Publication pending` |  |
| `opentelemetry-collector-contrib` | `0.160.0` | Required | Collects and exports NVCF telemetry. | `Publication pending` | [Upstream](https://github.com/open-telemetry/opentelemetry-collector-contrib) |
| `opentelemetry-operator` | `0.158.0` | Required | Reconciles OpenTelemetry Collector resources. | `Publication pending` | [Upstream](https://github.com/open-telemetry/opentelemetry-operator) |
| `oss-vault-k8s` | `1.7.4` | Required | Integrates Kubernetes workloads with OpenBao secrets. | `nvcr.io/nvidia/nvcf/oss-vault-k8s:1.7.4` |  |
| `reval-server` | `0.20.2` | Required | Revalidates function state in the background. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/control-plane-services/helm-reval) |
| `stargate` | `0.16.2` | Optional | Routes LLM requests to eligible worker instances. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/libraries/rust/stargate) |
| `victoria-metrics` | `v1.150.0` | Required | Stores metrics for the default observability profile. | `Publication pending` | [Upstream](https://github.com/VictoriaMetrics/VictoriaMetrics) |

### Compute plane Helm charts

| Artifact | Version | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- |
| `csi-driver-smb` | `supported` | Optional | Provides SMB persistent volumes for supported deployments. | `https://raw.githubusercontent.com/kubernetes-csi/csi-driver-smb/master/charts` | [Upstream](https://github.com/kubernetes-csi/csi-driver-smb) |
| `dynamo-platform` | `1.4.2` | Optional | Deploys the optional NVIDIA Dynamo operator. | `Publication pending` | [Upstream](https://github.com/ai-dynamo/dynamo) |
| `ebs-csi-driver` | `supported` | Optional | Provides Amazon EBS persistent volumes for EKS clusters. | `https://kubernetes-sigs.github.io/aws-ebs-csi-driver` | [Upstream](https://github.com/kubernetes-sigs/aws-ebs-csi-driver) |
| `gpu-operator` | `supported` | Required | Manages NVIDIA GPU software on Kubernetes nodes. | `https://helm.ngc.nvidia.com/nvidia` | [Upstream](https://github.com/NVIDIA/gpu-operator) |
| `grove-charts` | `v0.1.0-alpha.12` | Optional | Deploys the optional Grove operator for topology-aware scheduling. | `Publication pending` | [Upstream](https://github.com/ai-dynamo/grove) |
| `helm-nvca-operator` | `1.24.0` | Required | Deploys the NVCA operator and compute-plane integration. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nvca-operator) |
| `kai-scheduler` | `v0.17.1` | Optional | Deploys the optional KAI Scheduler. | `Publication pending` | [Upstream](https://github.com/NVIDIA/KAI-Scheduler) |
| `modelexpress` | `supported` | Optional | Distributes model weights peer-to-peer between Dynamo workers to reduce scale-out cold starts. Installed separately from the compute-plane stack. | `https://helm.ngc.nvidia.com/nvidia/ai-dynamo` | [Upstream](https://github.com/ai-dynamo/modelexpress) |
| `nvcf-cluster-topology` | `0.1.0` | Required | Configures cluster topology resources for compute scheduling. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/stacks/nvcf-compute-plane/charts/nvcf-cluster-topology) |
| `nvcf-container-cache` | `0.25.22` | Optional | Deploys container image caching on GPU cluster nodes. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-container-cache:0.25.22` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/container-cache) |

### Compute plane services and images

| Artifact | Version | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- |
| `crd-upgrader` | `v0.17.1` | Optional | Upgrades KAI Scheduler custom resources. | `Publication pending` | [Upstream](https://github.com/NVIDIA/KAI-Scheduler) |
| `ess-agent` | `1.4.1` | Required | Injects encrypted application secrets into function workloads. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/ess-agent) |
| `gpu-operator-validator` | `supported` | Required | Validates GPU Operator components on GPU nodes. | `https://catalog.ngc.nvidia.com/orgs/nvidia/teams/cloud-native/containers/gpu-operator-validator` | [Upstream](https://github.com/NVIDIA/gpu-operator) |
| `grove-install-crds` | `v0.1.0-alpha.12` | Optional | Installs Grove custom resource definitions. | `Publication pending` | [Upstream](https://github.com/ai-dynamo/grove) |
| `grove-operator` | `v0.1.0-alpha.12` | Optional | Reconciles Grove topology-aware scheduling resources. | `Publication pending` | [Upstream](https://github.com/ai-dynamo/grove) |
| `k8s-device-plugin` | `supported` | Required | Advertises NVIDIA GPU resources to Kubernetes. | `https://catalog.ngc.nvidia.com/orgs/nvidia/teams/k8s/containers/device-plugin` | [Upstream](https://github.com/NVIDIA/k8s-device-plugin) |
| `kubernetes-operator` | `1.4.2` | Optional | Reconciles NVIDIA Dynamo workloads on Kubernetes. | `Publication pending` | [Upstream](https://github.com/ai-dynamo/dynamo) |
| `modelexpress-server` | `supported` | Optional | Serves model weights to Dynamo workers over NIXL RDMA transports. | `https://catalog.ngc.nvidia.com/orgs/nvidia/teams/ai-dynamo/containers/modelexpress-server` | [Upstream](https://github.com/ai-dynamo/modelexpress) |
| `nats` | `2.10.21-alpine` | Optional | Provides messaging for the optional NVIDIA Dynamo operator. | `Publication pending` | [Upstream](https://github.com/nats-io/nats-server) |
| `nats-server-config-reloader` | `0.16.0` | Optional | Reloads NATS configuration for the optional NVIDIA Dynamo operator. | `Publication pending` | [Upstream](https://github.com/nats-io/k8s) |
| `nvca` | `3.5.2` | Required | Registers GPU clusters and orchestrates deployments in-cluster. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/nvca) |
| `nvca-operator` | `3.6.0` | Required | Reconciles NVCA resources and compute-plane configuration. | `Publication pending` |  |
| `nvcf-container-cache` | `v1.1.36` | Optional | Caches container image layers on GPU cluster nodes. | `nvcr.io/nvidia/nvcf/nvcf-container-cache:v1.1.36` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/container-cache) |
| `nvcf-image-credential-helper` | `0.10.2` | Required | Resolves container image credentials for function workloads. | `nvcr.io/nvidia/nvcf/nvcf-image-credential-helper:0.10.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/image-credential-helper) |
| `nvcf-proxy-tls-certs` | `v1.2.10` | Optional | Configures TLS trust for the optional container cache proxy. | `nvcr.io/nvidia/nvcf/nvcf-proxy-tls-certs:v1.2.10` |  |
| `nvcf-worker-init-oss` | `1.2.1` | Required | Prepares function resources before the user container starts. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/worker-init) |
| `nvcf-worker-llm-credentials-oss` | `1.1.2` | Optional | Maintains a current NVCF worker token for LLM function workloads. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/worker-llm-credentials) |
| `nvcf-worker-utils-oss` | `1.2.3` | Required | Proxies NATS traffic between function containers and the control plane. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/worker-utils) |
| `operator` | `v0.17.1` | Optional | Reconciles KAI Scheduler resources. | `Publication pending` | [Upstream](https://github.com/NVIDIA/KAI-Scheduler) |
| `pylon` | `0.16.2` | Optional | Connects LLM worker pods to the LLM request router. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/libraries/rust/stargate) |

### EA-only CVE-impacted artifacts

These Early Access artifacts have known CVE impact. Use only the QA-qualified versions listed for this EA stack.

| Artifact | Version | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- |
| `nvcf-cassandra-migrations` | `0.17.5` | Required | Applies the Cassandra schemas required by Early Access NVCF services. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/migrations/cassandra) |

### Tools and deployment resources

| Artifact | Version | Description | Distribution | Source code |
| --- | --- | --- | --- | --- |
| `nvcf-cli` | `1.16.2` | Manages functions, deployments, and clusters from the command line. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/clis/nvcf-cli) |
| `nvcf-compute-plane-stack` | `0.16.1` | Provides the Helmfile bundle for compute-plane deployment. | `Publication pending` |  |
| `nvcf-self-managed-stack` | `0.16.1` | Provides the Helmfile bundle for control-plane deployment. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/stacks/self-managed) |

{/*docs-version-sync:END manifest-artifact-registry-paths*/}
