# Artifact Manifest

<Warning>
Draft for 0.6.2. Inventory source: `deploy/stacks/self-managed/v0.6.2-rc.0`.
Stable release publication and live upgrade qualification are pending.
Do not use this draft as a qualified upgrade procedure.
</Warning>

This manifest records the exact control-plane artifacts resolved for the 0.6.2 candidate. Optional releases and hook images are included. It does not import separately released compute-plane or observability inventories. Existing compute clusters retain their qualified versions.

## Artifacts Overview

The inventory has 21 control-plane releases and 57 resolved artifacts. The
public manifest applies the catalog denylist and records unverified versions
as `Publication pending`. Public availability was checked on 2026-10-01.
A source registry reference alone is not publication evidence.

See the [release notes](./release-notes/0.6.2.md) and
[patch procedure](./release-notes/0.6.1-to-0.6.2-upgrade.md).

<Warning>
Artifact version compatibility

Newer artifact versions might be available. The stable 0.6.2 release is pending qualification. These candidate pins must
be reconciled against its final inventory before release. Do not substitute
other artifact versions or treat a pending location as a public download.

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
export CHART_VERSION="1.23.9"
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

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |
| `helm-admin-token-issuer-proxy` | `1.4.3` | `self-managed` | Required | Deploys the admin token issuer proxy. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-admin-token-issuer-proxy:1.4.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/admin-token-issuer-proxy) |
| `helm-nvcf-api` | `1.23.9` | `self-managed` | Required | Deploys the NVCF API service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-api:1.23.9` |  |
| `helm-nvcf-api-keys` | `1.6.0` | `self-managed` | Required | Deploys the API key management service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-api-keys:1.6.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/api-keys-colocated) |
| `helm-nvcf-cassandra` | `0.18.0` | `self-managed` | Required | Deploys Cassandra and its initialization jobs. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-cassandra:0.18.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/cassandra) / [Upstream](https://github.com/bitnami/charts/tree/main/bitnami/cassandra) |
| `helm-nvcf-cert-manager` | `0.1.0` | `self-managed` | Required | Deploys the NVCF cert-manager configuration. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-cert-manager:0.1.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/cert-manager) / [Upstream](https://github.com/cert-manager/cert-manager) |
| `helm-nvcf-ess-api` | `1.6.1` | `self-managed` | Required | Deploys the Encrypted Secrets Service API. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-ess-api:1.6.1` |  |
| `helm-nvcf-grpc-proxy` | `1.6.7` | `self-managed` | Required | Deploys the gRPC proxy service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-grpc-proxy:1.6.7` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/grpc-proxy) |
| `helm-nvcf-invocation-service` | `1.5.6` | `self-managed` | Required | Deploys the HTTP invocation service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-invocation-service:1.5.6` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/http-invocation) |
| `helm-nvcf-llm-api-gateway` | `1.3.5` | `self-managed` | Optional | Deploys the OpenAI-compatible LLM API gateway. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/llm-api-gateway) |
| `helm-nvcf-llm-request-router` | `1.6.6` | `self-managed` | Optional | Deploys the LLM request router. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-llm-request-router:1.6.6` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/llm-request-router) |
| `helm-nvcf-nats` | `0.7.1` | `self-managed` | Required | Deploys NATS messaging for the control plane. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-nats:0.7.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nats) / [Upstream](https://github.com/nats-io/k8s) |
| `helm-nvcf-nats-auth-callout-service` | `1.1.3` | `self-managed` | Required | Deploys the NATS authorization callout service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-nats-auth-callout-service:1.1.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nats-auth-callout) |
| `helm-nvcf-notary-service` | `1.4.2` | `self-managed` | Required | Deploys the notary service for signing and validation. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-notary-service:1.4.2` |  |
| `helm-nvcf-nvct-api` | `1.4.3` | `self-managed` | Required | Deploys the NVCF tenant API service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-nvct-api:1.4.3` |  |
| `helm-nvcf-openbao-server` | `0.32.6` | `self-managed` | Required | Deploys OpenBao secret management. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-openbao-server:0.32.6` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/openbao) / [Upstream](https://github.com/openbao/openbao-helm) |
| `helm-nvcf-rate-limiter` | `1.0.3` | `self-managed` | Optional | Deploys request rate limiting for supported invocation paths. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-rate-limiter:1.0.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/ratelimiter) |
| `helm-nvcf-sis` | `1.18.8` | `self-managed` | Required | Deploys the Spot Instance Service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-sis:1.18.8` |  |
| `helm-nvcf-state-metrics` | `1.0.1` | `self-managed` | Optional | Deploys NVCF state metrics for observability. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-state-metrics:1.0.1` |  |
| `helm-nvcf-vanity-gateway` | `0.1.0-nvcf-10204.1` | `self-managed` | Optional | Deploys the optional vanity hostname gateway. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-vanity-gateway:0.1.0-nvcf-10204.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/vanity-gateway) |
| `helm-reval` | `1.3.8` | `self-managed` | Required | Deploys the function revalidation service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-reval:1.3.8` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/helm-reval) |
| `nvcf-gateway-routes` | `1.14.0` | `self-managed` | Required | Deploys Gateway API routes for NVCF services. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-gateway-routes:1.14.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/gateway-routes) |

### Control plane services and images

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |
| `admin-token-issuer-proxy` | `1.0.2` | `self-managed` | Required | Proxies admin token requests for stack services. | `nvcr.io/nvidia/nvcf/admin-token-issuer-proxy:1.0.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/control-plane-services/admin-token-issuer-proxy) |
| `alpine-k8s` | `1.36.1` | `self-managed` | Required | Runs Kubernetes utilities for bootstrap jobs. | `Publication pending` | [Upstream](https://github.com/alpine-docker/k8s) |
| `cassandra` | `5.0.8-nv-2.0.1` | `self-managed` | Required | Stores NVCF account, function, cluster, and service state. | `nvcr.io/nvidia/nvcf/cassandra:5.0.8-nv-2.0.1` | [Upstream](https://github.com/apache/cassandra) |
| `cert-manager-acmesolver` | `v1.20.2` | `self-managed` | Optional | Serves temporary ACME HTTP-01 domain-validation challenges. | `Publication pending` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `cert-manager-cainjector` | `v1.20.2` | `self-managed` | Required | Injects certificate authority data into Kubernetes resources. | `nvcr.io/nvidia/nvcf/cert-manager-cainjector:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `cert-manager-controller` | `v1.20.2` | `self-managed` | Required | Reconciles certificates and issuers for the control plane. | `nvcr.io/nvidia/nvcf/cert-manager-controller:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `cert-manager-startupapicheck` | `v1.20.2` | `self-managed` | Required | Verifies that the cert-manager API is ready. | `nvcr.io/nvidia/nvcf/cert-manager-startupapicheck:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `cert-manager-webhook` | `v1.20.2` | `self-managed` | Required | Validates and converts cert-manager API resources. | `nvcr.io/nvidia/nvcf/cert-manager-webhook:v1.20.2` | [Upstream](https://github.com/cert-manager/cert-manager) |
| `ess-agent` | `1.3.1` | `self-managed` | Required | Injects encrypted application secrets into function workloads. | `nvcr.io/nvidia/nvcf/ess-agent:1.3.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/ess-agent) |
| `ess-api` | `v0.57.26` | `self-managed` | Required | Provides the legacy Encrypted Secrets Service API. | `nvcr.io/nvidia/nvcf/ess-api:v0.57.26` |  |
| `llm-api-gateway` | `0.8.3` | `self-managed` | Optional | Exposes OpenAI-compatible APIs for LLM functions. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/llm-api-gateway) |
| `nats-box` | `0.19.7-nonroot` | `self-managed` | Required | Provides NATS administration and diagnostic utilities. | `nvcr.io/nvidia/nvcf/nats-box:0.19.7-nonroot` | [Upstream](https://github.com/nats-io/nats-box) |
| `nats-server` | `2.11.17-alpine3.22` | `self-managed` | Required | Provides messaging for function deployment and invocation. | `nvcr.io/nvidia/nvcf/nats-server:2.11.17-alpine3.22` | [Upstream](https://github.com/nats-io/nats-server) |
| `nats-server-config-reloader` | `0.23.0` | `self-managed` | Required | Reloads NATS configuration for the control plane and optional NVIDIA Dynamo deployment. | `Publication pending` | [Upstream](https://github.com/nats-io/k8s) |
| `notary-service` | `1.8.1` | `self-managed` | Required | Signs and validates function artifacts. | `nvcr.io/nvidia/nvcf/notary-service:1.8.1` |  |
| `nvcf-ai-api-gateway-service` | `1.25.0-nvcf-10204.0` | `self-managed` | Optional | Serves the optional vanity hostname gateway. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/vanity-gateway) |
| `nvcf-api-keys-service` | `1.5.0` | `self-managed` | Required | Creates and manages NVCF API keys. | `nvcr.io/nvidia/nvcf/nvcf-api-keys-service:1.5.0` |  |
| `nvcf-cassandra-migrations` | `0.10.3` | `self-managed` | Required | Applies the Cassandra schemas required by NVCF services. | `nvcr.io/nvidia/nvcf/nvcf-cassandra-migrations:0.10.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/migrations/cassandra) |
| `nvcf-grpc-proxy` | `1.29.1` | `self-managed` | Required | Proxies bidirectional gRPC traffic between the control and compute planes. | `nvcr.io/nvidia/nvcf/nvcf-grpc-proxy:1.29.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/grpc-proxy) |
| `nvcf-invocation-service` | `0.8.8` | `self-managed` | Required | Routes stateless HTTP function invocation requests. | `nvcr.io/nvidia/nvcf/nvcf-invocation-service:0.8.8` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/http-invocation) |
| `nvcf-nats-auth-callout-service` | `0.5.10` | `self-managed` | Required | Authorizes NATS clients for NVCF services and workloads. | `nvcr.io/nvidia/nvcf/nvcf-nats-auth-callout-service:0.5.10` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/control-plane-services/nats-auth-callout) |
| `nvcf-openbao` | `2.5.5-nv-1.3.1` | `self-managed` | Required | Stores and manages control-plane secrets. | `nvcr.io/nvidia/nvcf/nvcf-openbao:2.5.5-nv-1.3.1` | [Upstream](https://github.com/openbao/openbao) |
| `nvcf-openbao-migrations` | `0.16.2` | `self-managed` | Required | Applies the OpenBao configuration required by NVCF. | `nvcr.io/nvidia/nvcf/nvcf-openbao-migrations:0.16.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/migrations/openbao) |
| `nvcf-ratelimiter` | `1.14.3` | `self-managed` | Optional | Enforces request rate limits for supported invocation paths. | `Publication pending` |  |
| `nvcf-service-oss` | `1.9.0-hotfix.1` | `self-managed` | Required | Provides the primary NVCF control-plane API. | `nvcr.io/nvidia/nvcf/nvcf-service-oss:1.9.0-hotfix.1` |  |
| `nvcf-state-metrics-service` | `1.21.4` | `self-managed` | Optional | Exports NVCF resource state as Prometheus metrics. | `Publication pending` |  |
| `nvcf-worker-init-oss` | `1.0.9` | `self-managed` | Required | Prepares function resources before the user container starts. | `nvcr.io/nvidia/nvcf/nvcf-worker-init-oss:1.0.9` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/worker-init) |
| `nvcf-worker-llm-credentials-oss` | `1.0.4` | `self-managed` | Required | Maintains a current NVCF worker token for LLM function workloads. | `nvcr.io/nvidia/nvcf/nvcf-worker-llm-credentials-oss:1.0.4` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/worker-llm-credentials) |
| `nvcf-worker-utils-oss` | `1.0.4` | `self-managed` | Required | Proxies NATS traffic between function containers and the control plane. | `nvcr.io/nvidia/nvcf/nvcf-worker-utils-oss:1.0.4` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/worker-utils) |
| `nvct-service-oss` | `1.5.9-hotfix.1` | `self-managed` | Required | Provides tenant-scoped NVCF control-plane operations. | `nvcr.io/nvidia/nvcf/nvct-service-oss:1.5.9-hotfix.1` |  |
| `oss-vault-k8s` | `1.7.4` | `self-managed` | Required | Integrates Kubernetes workloads with OpenBao secrets. | `nvcr.io/nvidia/nvcf/oss-vault-k8s:1.7.4` |  |
| `pylon` | `0.3.2` | `self-managed` | Required | Connects LLM worker pods to the LLM request router. | `nvcr.io/nvidia/nvcf/pylon:0.3.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/libraries/rust/stargate) |
| `reval-server` | `0.2.2` | `self-managed` | Required | Revalidates function state in the background. | `nvcr.io/nvidia/nvcf/reval-server:0.2.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/control-plane-services/helm-reval) |
| `spot` | `1.563.1-hotfix.1` | `self-managed` | Required | Provides instance management for the legacy stack. | `nvcr.io/nvidia/nvcf/spot:1.563.1-hotfix.1` |  |
| `stargate` | `0.3.2` | `self-managed` | Optional | Routes LLM requests to eligible worker instances. | `nvcr.io/nvidia/nvcf/stargate:0.3.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/libraries/rust/stargate) |

### Compute plane Helm charts

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |

### Compute plane services and images

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |

### Observability Helm charts

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |

### Observability services and images

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |

### Cross-stack Helm charts

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |

### Cross-stack services and images

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |

### Tools and deployment resources

| Artifact | Version | Stack | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- |
| `nvcf-self-managed-stack` | `0.6.2-rc.0` | `self-managed` | Provides the Helmfile bundle for control-plane deployment. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/stacks/self-managed) |

{/* docs-version-sync:END manifest-artifact-registry-paths */}
