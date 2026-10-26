# Artifact Manifest

This manifest records the exact NVCF 1.0.4 candidate selection. The generated
tables come from the original inventories of Self-Managed 1.0.7, Compute Plane
1.0.3 and Observability 1.0.0.

<Warning>
Pending QA. Artifact availability and successful assembly do not qualify this
combination. Customer stack/umbrella publication and docs-site activation
remain held for approval. Use the [upgrade procedure](release-notes/1.0.2-to-1.0.4-upgrade.md)
before changing an existing deployment.
</Warning>

## Candidate identity

- [Selected product manifest](https://github.com/NVIDIA/nvcf/blob/077248a76580307ad823e2a9601187dca10099d1/deploy/releases/1.0.4/manifest.json),
  at candidate commit `077248a76580307ad823e2a9601187dca10099d1`.
- [Candidate assembly and downloadable QA artifact](https://github.com/NVIDIA/nvcf/actions/runs/40253438227).
  The artifact contains `nvcf-1.0.4.tar.gz` and the original inventories whose
  SHA-256 hashes are recorded in the product manifest.
- Bundle SHA-256:
  `e97ba5f3d4d917d4ae6c1fb390453b87068af10e91049f945446c9e1cbcd4801`.

The product manifest is the selection authority. Use the assembled bundle;
the umbrella branch's incidental stack directories are not that selection.
The [docs edition manifest](https://github.com/NVIDIA/nvcf/blob/sbaum1994/umbrella-104-docs-preview/docs/edition-manifest.json)
records the same three stack versions and source commits for this preview.

"Publication pending" means this catalog does not yet record verified customer
access for that exact artifact, or the stack resource is held for QA. It does
not mean a new chart or image version must be built. The upgrade-receipt and
Observability helper charts are bundled in the selected stack sources. The
original inventories remain unchanged; historical supplemental accounting does
not replace them.

## Artifacts overview

The tables group the selected artifacts by deployment plane and type. Optional
components require the corresponding deployment profile.

## Prepare Helm charts for Helmfile

The selected Self-Managed and Compute Plane stacks support the authenticated
HTTPS NVCF chart repository directly. Configure
`global.helm.sources.url: https://helm.ngc.nvidia.com/nvidia/nvcf`, its customer
username/password, and `global.image.registry: nvcr.io` with
`global.image.repository: nvidia/nvcf`. The base files contain placeholders.
See [customer artifact access](umbrella-releases.md#configure-customer-artifact-access)
for the complete values and separate image/worker credentials.

Mirroring into OCI is an alternative when your deployment requires a private
registry. Omit `global.helm.sources.url` for that path and set the OCI registry
and repository after copying the selected charts.

Set `NGC_API_KEY` to a customer key with chart access. The following example
copies one public NVCF chart into an OCI registry:

```bash
export CHART_NAME="helm-nvcf-api"
export CHART_VERSION="1.27.4"
export TARGET_REGISTRY="<registry-host>"
export TARGET_REPOSITORY="<repository>"

helm repo add nvcf https://helm.ngc.nvidia.com/nvidia/nvcf \
  --username '$oauthtoken' --password "$NGC_API_KEY"
helm repo update
helm pull "nvcf/${CHART_NAME}" --version "${CHART_VERSION}"

helm registry login "${TARGET_REGISTRY}"
helm push "${CHART_NAME}-${CHART_VERSION}.tgz" \
  "oci://${TARGET_REGISTRY}/${TARGET_REPOSITORY}"
```

For an OCI mirror, repeat this process for every required external chart with
an `https://` distribution. Bundled charts remain in their stack directories.
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
For archive download, integrity checks and deployment order, use
[Consume an umbrella release](umbrella-releases.md).

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

### Use upstream NATS images

To use upstream images, merge these values into
`deploy/stacks/self-managed/environments/<environment>.yaml`. Helmfile passes
these settings to the selected NATS chart; no chart or release-definition
changes are needed. This example uses NATS server 2.14.6, reloader 0.24.0, and
NATS box 0.20.1. The box pin identifies the upstream multi-platform index for
Linux AMD64 and ARM64.

```yaml
nats:
  container:
    image:
      registry: docker.io
      repository: synadia/nats-server
      tag: "2.14.6-alpine3.22"
  reloader:
    image:
      registry: docker.io
      repository: natsio/nats-server-config-reloader
      tag: "0.24.0"
  podTemplate:
    merge:
      spec:
        securityContext:
          runAsNonRoot: true
          runAsUser: 1000
          runAsGroup: 1000
          fsGroup: 1000
  natsBox:
    container:
      image:
        registry: docker.io
        repository: natsio/nats-box
        tag: "0.20.1-nonroot@sha256:9eec37227e52bf4d207ffc246284034c18dd09d51bae0bfd2af9dbbd77ba4d44"
    podTemplate:
      merge:
        spec:
          securityContext:
            runAsNonRoot: true
            runAsUser: 1000
            runAsGroup: 1000
```

The box image declares user/group `1000:1000`. `runAsNonRoot` adds Kubernetes
enforcement; `runAsUser` and `runAsGroup` make the identity explicit. The server
pod settings also cover its reloader; neither image declares a nonroot user.
Server `fsGroup` assigns group 1000 to the JetStream and shared PID volumes.
The box image provides writable directories for CLI state and `nsc` keys.
Its default context links to read-only mounted Secrets;
use a separate `XDG_CONFIG_HOME` to save additional contexts. The box needs no
additional volume or `fsGroup` for this example.

For a private mirror, set each image's `registry` and `repository` together and
use the mirrored `tag` or `tag@sha256:...`. Preserve the multi-platform index
when copying the box image and verify the destination digest. Unset or empty
keys retain the stack's existing defaults; this example does not change them.
Configure `global.imagePullSecrets` if the destination requires authentication.

Render before installing, using your normal chart source and secrets file:

```bash
cd deploy/stacks/self-managed
export HELMFILE_ENV=<environment>
helmfile -f helmfile.d/01-dependencies.yaml.gotmpl --selector name=nats template
```

Check the StatefulSet's server and reloader images and the box Deployment's
full `tag@digest` reference and security context. Install with the normal
stack workflow, then verify in the intended Kubernetes context:

```bash
kubectl --context "$KUBE_CONTEXT" -n nats-system rollout status statefulset/nats
kubectl --context "$KUBE_CONTEXT" -n nats-system rollout status deployment/nats-box
kubectl --context "$KUBE_CONTEXT" -n nats-system exec nats-0 -c nats -- id
kubectl --context "$KUBE_CONTEXT" -n nats-system exec nats-0 -c reloader -- id
kubectl --context "$KUBE_CONTEXT" -n nats-system exec deployment/nats-box -- id
kubectl --context "$KUBE_CONTEXT" -n nats-system exec deployment/nats-box -- \
  nats --context default rtt
```

The identities should report UID/GID 1000. The CLI check uses the mounted NKey
and requires the stack's authentication service to be ready. Expect latency
output; `failed` or an authorization error is a failure even if `rtt` exits
successfully. Check the reloader logs after a configuration update to confirm
it reloads successfully.

### Use upstream cert-manager images

Merge these overrides into the Helmfile environment file
`deploy/stacks/self-managed/environments/<environment>.yaml` to use upstream
cert-manager images.
For a private mirror, replace each full `repository`, which takes precedence
over `global.image`.

```yaml
certManager:
  image:
    repository: "quay.io/jetstack/cert-manager-controller"
    tag: "v1.20.2"
    digest: "sha256:fe0623d7d04a382c888f03343a3a2da716e0d96ad3d5d790c0ebcbcb2a4329a5"
  webhook:
    image:
      repository: "quay.io/jetstack/cert-manager-webhook"
      tag: "v1.20.2"
      digest: "sha256:baf651128b9f05c426cbd5e60e2036bf382c99ca270f49d0757d6f7d2452f4e5"
  cainjector:
    image:
      repository: "quay.io/jetstack/cert-manager-cainjector"
      tag: "v1.20.2"
      digest: "sha256:6f5a644135887b2aa7d5cc145072fa56421560e3586ff1f184358022d490f4e1"
  startupapicheck:
    image:
      repository: "quay.io/jetstack/cert-manager-startupapicheck"
      tag: "v1.20.2"
      digest: "sha256:4e2a69b4a0cc9627905bbeecf720f95d5153ca39cacdab923d2748e73556792b"
  acmesolver:
    image:
      repository: "quay.io/jetstack/cert-manager-acmesolver"
      tag: "v1.20.2"
      digest: "sha256:cfa55e9c3dcfc6a3cba97575748c1549b04a30fd14d88a2fcb9e2dddef415e2f"
```

For authenticated mirrors, set `global.imagePullSecrets` for chart pods and
separate pull credentials in the ACME Issuer's HTTP-01 solver
[pod template](https://cert-manager.io/docs/configuration/acme/http01/#podtemplate).

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

### Use the upstream Vault injector image

To use the upstream injector at the same version as the NVCF mirror, merge
this opt-in example into
`deploy/stacks/self-managed/environments/<environment>.yaml`:

```yaml
openbao:
  injector:
    image:
      registry: docker.io
      repository: hashicorp/vault-k8s
      tag: "1.7.4@sha256:e2ef79b457f50d971851df836f78cf41be454644095d84a7411f93ccf3304c15"
```

This changes only the injector distribution source.
The webhook image is separate from `openbao.injector.agentImage` and
`openbao.server.image`; keep those matched NVCF OpenBao images unchanged.

<Info>
Some supporting components such as the GPU Operator, OpenBao, NATS, Cassandra, etc. can alternatively be pulled directly from public NGC Catalog or other public opensource repositories if desired.

</Info>

The following tables list the complete artifact inventory.

{/*docs-version-sync:BEGIN manifest-artifact-registry-paths*/}

### Stack releases

| Stack | Version | Source tag |
| --- | --- | --- |
| [Self-managed (control plane)](../self-managed/installation.md) | `1.0.7` | `deploy/stacks/self-managed/v1.0.7` |
| [Compute plane](../compute-plane/cluster-management/index.md) | `1.0.3` | `deploy/stacks/nvcf-compute-plane/v1.0.3` |
| [Observability](../observability/observability.md) | `1.0.0` | `deploy/stacks/observability/v1.0.0` |

### Control plane Helm charts

| Artifact | Version | Stack | Required | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- | --- |
| `helm-admin-token-issuer-proxy` | `1.5.3` | `self-managed` | Required | Deploys the admin token issuer proxy. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-admin-token-issuer-proxy:1.5.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/admin-token-issuer-proxy) |
| `helm-nvcf-api` | `1.27.4` | `self-managed` | Required | Deploys the NVCF API service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-api:1.27.4` |  |
| `helm-nvcf-api-keys` | `1.8.1` | `self-managed` | Required | Deploys the API key management service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-api-keys:1.8.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/api-keys-colocated) |
| `helm-nvcf-cassandra` | `0.21.3` | `self-managed` | Required | Deploys Cassandra and its initialization jobs. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-cassandra:0.21.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/cassandra) / [Upstream](https://github.com/bitnami/charts/tree/main/bitnami/cassandra) |
| `helm-nvcf-cert-manager` | `0.1.0` | `self-managed` | Required | Deploys the NVCF cert-manager configuration. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-cert-manager:0.1.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/cert-manager) / [Upstream](https://github.com/cert-manager/cert-manager) |
| `helm-nvcf-ess-api` | `1.8.2` | `self-managed` | Required | Deploys the Encrypted Secrets Service API. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-ess-api:1.8.2` |  |
| `helm-nvcf-function-autoscaler` | `0.5.2` | `self-managed` | Required | Deploys the function autoscaler for observability-driven scaling. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-function-autoscaler:0.5.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/function-autoscaler) |
| `helm-nvcf-grpc-proxy` | `1.7.4` | `self-managed` | Required | Deploys the gRPC proxy service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-grpc-proxy:1.7.4` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/grpc-proxy) |
| `helm-nvcf-invocation-service` | `1.6.2` | `self-managed` | Required | Deploys the HTTP invocation service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-invocation-service:1.6.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/http-invocation) |
| `helm-nvcf-llm-api-gateway` | `1.4.3` | `self-managed` | Optional | Deploys the OpenAI-compatible LLM API gateway. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-llm-api-gateway:1.4.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/llm-api-gateway) |
| `helm-nvcf-llm-request-router` | `1.14.2` | `self-managed` | Optional | Deploys the LLM request router. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-llm-request-router:1.14.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/llm-request-router) |
| `helm-nvcf-nats` | `0.8.4` | `self-managed` | Required | Deploys NATS messaging for the control plane. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-nats:0.8.4` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nats) / [Upstream](https://github.com/nats-io/k8s) |
| `helm-nvcf-nats-auth-callout-service` | `1.2.1` | `self-managed` | Required | Deploys the NATS authorization callout service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-nats-auth-callout-service:1.2.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nats-auth-callout) |
| `helm-nvcf-notary-service` | `1.6.0` | `self-managed` | Required | Deploys the notary service for signing and validation. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-notary-service:1.6.0` |  |
| `helm-nvcf-nvct-api` | `1.6.0` | `self-managed` | Required | Deploys the NVCF tenant API service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-nvct-api:1.6.0` |  |
| `helm-nvcf-openbao-server` | `0.32.7` | `self-managed` | Required | Deploys OpenBao secret management. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-openbao-server:0.32.7` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/openbao) / [Upstream](https://github.com/openbao/openbao-helm) |
| `helm-nvcf-pki` | `0.1.0` | `self-managed` | Optional | Provisions the OpenBao-backed ClusterIssuer for NVCF service TLS. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-pki:0.1.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/nvcf-pki) |
| `helm-nvcf-rate-limiter` | `1.2.1` | `self-managed` | Required | Deploys request rate limiting for supported invocation paths. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-rate-limiter:1.2.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/ratelimiter) |
| `helm-nvcf-sis` | `2.4.1` | `self-managed` | Required | Deploys the Spot Instance Service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-sis:2.4.1` |  |
| `helm-nvcf-state-metrics` | `1.0.2` | `self-managed` | Required | Deploys NVCF state metrics for observability. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-state-metrics:1.0.2` |  |
| `helm-nvcf-ui` | `1.1.3` | `self-managed` | Optional | Deploys the optional NVCF UI admin panel. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-ui:1.1.3` |  |
| `helm-nvcf-vanity-gateway` | `0.5.0` | `self-managed` | Optional | Deploys the optional vanity hostname gateway. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-nvcf-vanity-gateway:0.5.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/vanity-gateway) |
| `helm-reval` | `1.4.1` | `self-managed` | Required | Deploys the function revalidation service. | `https://helm.ngc.nvidia.com/nvidia/nvcf/helm-reval:1.4.1` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/helm-reval) |
| `nvcf-example-dashboards` | `1.6.0` | `self-managed` | Optional | Deploys example Grafana dashboards for NVCF telemetry. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-example-dashboards:1.6.0` |  |
| `nvcf-gateway-routes` | `1.18.3` | `self-managed` | Required | Deploys Gateway API routes for NVCF services. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-gateway-routes:1.18.3` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/gateway-routes) |
| `nvcf-observability-reference-stack` | `1.10.0` | `self-managed` | Optional | Deploys a reference observability backend for evaluation. | `https://helm.ngc.nvidia.com/nvidia/nvcf/nvcf-observability-reference-stack:1.10.0` |  |
| `nvcf-upgrade-receipt` | `0.1.0` | `self-managed` | Required | Records the deployed stack version after Helmfile completes. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/stacks/self-managed/charts/nvcf-upgrade-receipt) |

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
| `ess-agent` | `1.4.4` | `self-managed` | Required | Injects encrypted application secrets into function workloads. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/ess-agent) |
| `icms-service-oss` | `0.7.6` | `self-managed` | Required | Manages instance and cluster lifecycle operations. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/control-plane-services/instance-cluster-management) |
| `k8s` | `1.37.0` | `self-managed` | Required | Provides Kubernetes command-line utilities for deployment jobs. | `docker.io/alpine/k8s:1.37.0` | [GitHub](https://github.com/alpine-docker/k8s) |
| `k8s` | `1.33.1` | `self-managed` | Required | Runs the upgrade receipt hook; separate from the account-bootstrap helper. | `docker.io/alpine/k8s:1.33.1` | [GitHub](https://github.com/alpine-docker/k8s) |
| `llm-api-gateway` | `0.14.11` | `self-managed` | Optional | Exposes OpenAI-compatible APIs for LLM functions. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/llm-api-gateway) |
| `nats-box` | `0.19.7-nonroot` | `self-managed` | Required | Provides NATS administration and diagnostic utilities. | `nvcr.io/nvidia/nvcf/nats-box:0.19.7-nonroot` | [Upstream](https://github.com/nats-io/nats-box) |
| `nats-server` | `2.14.6-alpine3.22` | `self-managed` | Required | Provides messaging for function deployment and invocation. | `docker.io/synadia/nats-server:2.14.6-alpine3.22` | [Upstream](https://github.com/nats-io/nats-server) |
| `nvcf-ai-api-gateway-service` | `1.35.3` | `self-managed` | Optional | Serves the optional vanity hostname gateway. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/vanity-gateway) |
| `nvcf-api-keys-service` | `1.9.3` | `self-managed` | Required | Creates and manages NVCF API keys. | `Publication pending` |  |
| `nvcf-cassandra-migrations` | `0.17.7` | `self-managed` | Required | Applies the Cassandra schemas required by NVCF services. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/migrations/cassandra) |
| `nvcf-ess` | `0.5.4` | `self-managed` | Required | Provides encrypted application secrets to NVCF workloads. | `Publication pending` |  |
| `nvcf-function-autoscaler` | `1.21.10` | `self-managed` | Required | Scales functions from NVCF workload metrics. | `Publication pending` |  |
| `nvcf-grpc-proxy` | `1.33.7` | `self-managed` | Required | Proxies bidirectional gRPC traffic between the control and compute planes. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/grpc-proxy) |
| `nvcf-invocation-service` | `0.12.3` | `self-managed` | Required | Routes stateless HTTP function invocation requests. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/invocation-plane-services/http-invocation) |
| `nvcf-nats-auth-callout-service` | `0.8.6` | `self-managed` | Required | Authorizes NATS clients for NVCF services and workloads. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/control-plane-services/nats-auth-callout) |
| `nvcf-notary` | `1.14.5` | `self-managed` | Required | Signs and validates functions and cluster nodes. | `Publication pending` |  |
| `nvcf-openbao` | `2.6.2-nv-1.3.4` | `self-managed` | Required | Stores and manages control-plane secrets. | `nvcr.io/nvidia/nvcf/nvcf-openbao:2.6.2-nv-1.3.4` | [Upstream](https://github.com/openbao/openbao) |
| `nvcf-openbao-migrations` | `0.19.9` | `self-managed` | Required | Applies the OpenBao configuration required by NVCF. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/migrations/openbao) |
| `nvcf-ratelimiter` | `1.17.5` | `self-managed` | Required | Enforces request rate limits for supported invocation paths. | `Publication pending` |  |
| `nvcf-service-oss` | `1.18.1` | `self-managed` | Required | Provides the primary NVCF control-plane API. | `Publication pending` |  |
| `nvcf-state-metrics-service` | `1.23.7` | `self-managed` | Required | Exports NVCF resource state as Prometheus metrics. | `nvcr.io/nvidia/nvcf/nvcf-state-metrics-service:1.23.7` |  |
| `nvcf-ui` | `1.1.2` | `self-managed` | Optional | Serves the optional NVCF administrative interface. | `nvcr.io/nvidia/nvcf/nvcf-ui:1.1.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/uis/nvcf-ui) |
| `nvcf-worker-init-oss` | `1.2.4` | `self-managed` | Required | Prepares function resources before the user container starts. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/worker-init) |
| `nvcf-worker-llm-credentials-oss` | `1.1.5` | `self-managed` | Required | Maintains a current NVCF worker token for LLM function workloads. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/worker-llm-credentials) |
| `nvcf-worker-task-oss` | `1.1.6` | `self-managed` | Required | Runs task workloads selected by the task API. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services) |
| `nvcf-worker-utils-oss` | `1.2.6` | `self-managed` | Required | Proxies NATS traffic between function containers and the control plane. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/worker-utils) |
| `nvcf_worker_niclls` | `2.118.2` | `self-managed` | Required | Runs Low Latency Streaming workers. | `nvcr.io/nvidia/nvcf/nvcf_worker_niclls:2.118.2` |  |
| `nvct-service-oss` | `1.66.1` | `self-managed` | Required | Provides tenant-scoped NVCF control-plane operations. | `Publication pending` |  |
| `oss-vault-k8s` | `1.7.4` | `self-managed` | Required | Integrates Kubernetes workloads with OpenBao secrets. | `nvcr.io/nvidia/nvcf/oss-vault-k8s:1.7.4` |  |
| `pylon` | `0.18.3` | `self-managed` | Optional | Connects LLM worker pods to the LLM request router. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/libraries/rust/stargate) |
| `reval-server` | `0.20.3` | `self-managed` | Required | Revalidates function state in the background. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/control-plane-services/helm-reval) |
| `stargate` | `0.18.3` | `self-managed` | Optional | Routes LLM requests to eligible worker instances. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/libraries/rust/stargate) |

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
| `byoo-otel-collector` | `0.160.0-nv-0.2.9` | `compute-plane` | Required | Exports telemetry from Bring Your Own Observability (BYOO) workloads to the endpoints configured for each function or task. NVCA injects it as a sidecar when the BYOObservability feature gate is enabled. | `nvcr.io/nvidia/nvcf/byoo-otel-collector:0.160.0-nv-0.2.9` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/byoo-otel-collector) |
| `crd-upgrader` | `v0.17.1` | `compute-plane` | Optional | Upgrades KAI Scheduler custom resources. | `ghcr.io/kai-scheduler/kai-scheduler/crd-upgrader:v0.17.1` | [Upstream](https://github.com/NVIDIA/KAI-Scheduler) |
| `fluent-bit` | `3.2` | `compute-plane` | Optional | Collects container logs for Bring Your Own Observability (BYOO) workloads when the BYOOFluentBit feature gate is enabled. NVCA injects it as a sidecar. | `docker.io/fluent/fluent-bit:3.2` | [Upstream](https://github.com/fluent/fluent-bit) |
| `gpu-operator-validator` | `supported` | Independent | Required | Validates GPU Operator components on GPU nodes. | `https://catalog.ngc.nvidia.com/orgs/nvidia/teams/cloud-native/containers/gpu-operator-validator` | [Upstream](https://github.com/NVIDIA/gpu-operator) |
| `grove-install-crds` | `v0.1.0-alpha.12` | `compute-plane` | Optional | Installs Grove custom resource definitions. | `ghcr.io/ai-dynamo/grove/grove-install-crds:v0.1.0-alpha.12` | [Upstream](https://github.com/ai-dynamo/grove) |
| `grove-operator` | `v0.1.0-alpha.12` | `compute-plane` | Optional | Reconciles Grove topology-aware scheduling resources. | `ghcr.io/ai-dynamo/grove/grove-operator:v0.1.0-alpha.12` | [Upstream](https://github.com/ai-dynamo/grove) |
| `k8s-device-plugin` | `supported` | Independent | Required | Advertises NVIDIA GPU resources to Kubernetes. | `https://catalog.ngc.nvidia.com/orgs/nvidia/teams/k8s/containers/device-plugin` | [Upstream](https://github.com/NVIDIA/k8s-device-plugin) |
| `kubernetes-operator` | `1.4.2` | `compute-plane` | Optional | Reconciles NVIDIA Dynamo workloads on Kubernetes. | `nvcr.io/nvidia/ai-dynamo/kubernetes-operator:1.4.2` | [Upstream](https://github.com/ai-dynamo/dynamo) |
| `modelexpress-server` | `supported` | Independent | Optional | Serves model weights to Dynamo workers over NIXL RDMA transports. | `https://catalog.ngc.nvidia.com/orgs/nvidia/teams/ai-dynamo/containers/modelexpress-server` | [Upstream](https://github.com/ai-dynamo/modelexpress) |
| `nats` | `2.10.21-alpine` | `compute-plane` | Optional | Provides messaging for the optional NVIDIA Dynamo operator. | `docker.io/library/nats:2.10.21-alpine` | [Upstream](https://github.com/nats-io/nats-server) |
| `nvca` | `3.10.3` | `compute-plane` | Required | Registers GPU clusters and orchestrates deployments in-cluster. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/nvca) |
| `nvca-operator` | `3.10.3` | `compute-plane` | Required | Reconciles NVCA resources and compute-plane configuration. | `Publication pending` |  |
| `nvcf-container-cache` | `v1.1.36` | `compute-plane` | Optional | Caches container image layers on GPU cluster nodes. | `nvcr.io/nvidia/nvcf/nvcf-container-cache:v1.1.36` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/helm/container-cache) |
| `nvcf-image-credential-helper` | `0.11.3` | `compute-plane` | Required | Resolves container image credentials for function workloads. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/image-credential-helper) |
| `nvcf-otel-collector` | `0.160.0-nv-0.2.9` | `compute-plane` | Optional | Collects compute-plane workload telemetry. | `nvcr.io/nvidia/nvcf/nvcf-otel-collector:0.160.0-nv-0.2.9` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/compute-plane-services/byoo-otel-collector) |
| `nvcf-proxy-tls-certs` | `v1.2.10` | `compute-plane` | Optional | Configures TLS trust for the optional container cache proxy. | `nvcr.io/nvidia/nvcf/nvcf-proxy-tls-certs:v1.2.10` |  |
| `operator` | `v0.17.1` | `compute-plane` | Optional | Reconciles KAI Scheduler resources. | `ghcr.io/kai-scheduler/kai-scheduler/operator:v0.17.1` | [Upstream](https://github.com/NVIDIA/KAI-Scheduler) |
| `samba` | `1.0.8` | `compute-plane` | Required | Provides shared model storage for worker pods; retains its root runtime. | `nvcr.io/nvidia/nvcf/samba:1.0.8` | [Upstream](https://github.com/samba-team/samba) |

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

### Tools and deployment resources

| Artifact | Version | Stack | Description | Distribution | Source code |
| --- | --- | --- | --- | --- | --- |
| `nvcf-cli` | `1.16.2` | Independent | Manages functions, deployments, and clusters from the command line. | `nvcr.io/nvidia/nvcf/nvcf-cli:1.16.2` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/src/clis/nvcf-cli) |
| `nvcf-compute-plane-stack` | `1.0.3` | `compute-plane` | Provides the Helmfile bundle for compute-plane deployment. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/stacks/nvcf-compute-plane) |
| `nvcf-observability-stack` | `1.0.0` | `observability` | Provides the Helmfile bundle for standalone observability deployment. | `nvcr.io/nvidia/nvcf/nvcf-observability-stack:1.0.0` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/stacks/observability) |
| `nvcf-self-managed-stack` | `1.0.7` | `self-managed` | Provides the Helmfile bundle for control-plane deployment. | `Publication pending` | [GitHub](https://github.com/NVIDIA/nvcf/tree/main/deploy/stacks/self-managed) |

{/*docs-version-sync:END manifest-artifact-registry-paths*/}
