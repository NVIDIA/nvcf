# CSP End-to-End Example Installation (Helmfile)

This page is a worked example of the Helmfile installation path on
pre-provisioned managed Kubernetes clusters. Amazon EKS is the example
provider. It covers both topologies:

- Single-cluster: the control plane and the NVCA operator run on the same cluster.
- Multi-cluster: the control plane runs on one cluster, and the NVCA operator is
  registered and installed on a separate GPU (compute) cluster.

The procedures themselves live on two canonical pages. This page adds the
provider-specific values, the environment variables that tie the steps
together, and the multi-cluster deltas:

- [Helmfile Installation](./helmfile-installation.md) installs the control plane.
- [Register a GPU Cluster](/nvcf/compute-plane/register-gpu-cluster) registers a
  compute plane and installs the NVCA operator.

The only provider-specific pieces are the load balancer annotations on the
Gateway, the `storageClass` name, and the `kubectl` context names. Substitute
the equivalents for GKE, AKS, or on-prem.

<Info>
This guide assumes you have downloaded and extracted the control-plane
Helmfile bundle (`nvcf-self-managed-stack`) and have a source checkout for the
compute plane (`deploy/stacks/nvcf-compute-plane`). Control-plane commands run
from inside the bundle directory. Compute-plane commands run from the
repository root with `make -C`. See [Image Mirroring](/nvcf/overview/image-mirroring)
for pulling the bundles.

```bash
git clone https://github.com/nvidia/nvcf.git
```

</Info>

## Installation order

The order matters. Each step produces an input that the next step needs. The
load balancer address, in particular, must exist before you configure the
environment file, because it becomes `global.domain` and the NVCA Host headers.

```text
1. Install the Gateway          -> external load balancer address
2. Configure the environment    -> environments/<env>.yaml + secrets/<env>-secrets.yaml
3. Install the control plane    -> control-plane services + HTTPRoutes
4. Author the nvcf-cli config   -> points at the load balancer address
5. Register the GPU cluster     -> registration values file
6. Install the NVCA operator    -> agent connects back to the control plane
7. Verify the agent is healthy
```

Single-cluster and multi-cluster share steps 1 through 4. Steps 5 and 6 differ
only in the cluster target and the control-plane endpoint values. See
[Single-cluster vs multi-cluster](#single-cluster-vs-multi-cluster).

## Prerequisites

### Tools

Install on the machine you run these commands from: `kubectl`, `helm` (3.x),
`helmfile` (1.1.x), and `nvcf-cli`. Version constraints are in
[Helmfile Installation prerequisites](./helmfile-installation.md#prerequisites).

### Clusters

The clusters must be provisioned before you start. This guide does not create
them. Each cluster needs a default-capable `StorageClass` with dynamic
provisioning. On EKS this is `gp3`, backed by the EBS CSI driver.

The compute (GPU) cluster also needs a GPU operator (real, or the
[fake GPU operator](/nvcf/developer-guide/fake-gpu-operator) for non-GPU
validation) and the SMB CSI driver. See the
[GPU cluster prerequisites](/nvcf/compute-plane/register-gpu-cluster#prerequisites).

Both clusters must be reachable through `kubectl` contexts:

```bash
kubectl --context "${CONTROL_PLANE_CONTEXT}" get nodes -o name
# Multi-cluster only:
kubectl --context "${COMPUTE_CONTEXT}" get nodes -o name
```

### Environment variables

Set these once. In single-cluster, `COMPUTE_CONTEXT` equals
`CONTROL_PLANE_CONTEXT`. Later steps substitute them into the canonical
commands.

```bash
# Cluster targeting
export CONTROL_PLANE_CONTEXT="<kubectl-context-of-control-plane-cluster>"
export COMPUTE_CONTEXT="<kubectl-context-of-gpu-cluster>"   # = control plane in single-cluster
export CLUSTER_NAME="<name-to-register-the-gpu-cluster-as>"
export CLUSTER_REGION="<region-label>"                      # e.g. us-east-1

# Bundle environment file name (you create environments/<env>.yaml below)
export HELMFILE_ENV="eks"                                   # single-cluster example
# export HELMFILE_ENV="eks-multi"                           # multi-cluster example

# Repository path the bundles use for charts and images
export REPOSITORY="<your-ngc-org>/<your-ngc-team>"          # or your mirror path

# NGC credential used for chart/image pulls and the dockerconfig secret
export NGC_API_KEY="<your-ngc-api-key>"

# Path to the built nvcf-cli binary
export NVCF_CLI="<path-to>/nvcf-cli"

# Storage class for the control-plane bundle (provider specific)
export STORAGE_CLASS="gp3"
```

### Log in to the chart and image registry

The bundles pull OCI charts through Helm, so host-side registry auth must exist
before any `helmfile sync`:

```bash
printf '%s' "${NGC_API_KEY}" | helm registry login nvcr.io --username '$oauthtoken' --password-stdin
```

## Step 1: Install the Gateway and capture the load balancer address

Install the Gateway on the control-plane cluster by following the
[Gateway quickstart](./gateway-routing.md#gateway-quickstart) against
`${CONTROL_PLANE_CONTEXT}`. It installs the Gateway API CRDs, the Envoy
Gateway controller, the `GatewayClass`, and the `nvcf-gateway` Gateway, and
exports `GATEWAY_ADDR`.

On EKS, the Gateway `Service` needs the AWS load balancer annotations from the
quickstart so that an NLB is provisioned. Other providers use their own
annotations.

<Note>
The NVCA path also routes NATS. Make sure the `nvcf-gateway` Gateway includes a
`nats` listener on port 4222 (in addition to the `http` and `tcp` listeners
from the quickstart), and enable `routes.nats.enabled` in the environment file
in Step 2.
</Note>

After the quickstart, confirm the address is set:

```bash
test -n "${GATEWAY_ADDR}"
echo "GATEWAY_ADDR=${GATEWAY_ADDR}"
```

The gRPC listener is on this same Gateway at port 10081, so the gRPC address is
`${GATEWAY_ADDR}:10081` (used in the nvcf-cli config below).

<Info>
Why the NVCA Host headers matter: the NVCA agent dials the bare load balancer
URL (which resolves through DNS) and sends a per-service hostname as the HTTP
`Host` header (`sis.<addr>`, `reval.<addr>`, `nats.<addr>`) so the Gateway
HTTPRoutes match. These are set as `global.nvcaOperator.selfManaged.*Override`
in the compute-plane environment file in Step 5. This requires
`helm-nvca-operator` 1.12.0 or later.
</Info>

## Step 2: Configure the control-plane environment and secrets files

Follow [Helmfile Installation Steps 2 to 4](./helmfile-installation.md#step-2-configure-your-environment-file-environmentsenvironment-nameyaml)
to create `environments/${HELMFILE_ENV}.yaml`, `secrets/${HELMFILE_ENV}-secrets.yaml`,
and the image pull secrets. Every key is documented in the
[Environment File Reference](./environment-reference.md). The EKS-specific
values are:

| Key | Value for this example |
| --- | --- |
| `global.domain` | `${GATEWAY_ADDR}` |
| `global.helm.sources.repository`, `global.image.repository` | `${REPOSITORY}` |
| `global.imagePullSecrets` | `[{name: nvcr-pull-secret}]` |
| `global.storageClass` | `${STORAGE_CLASS}` (`gp3`) |
| `cassandra.resourcesPreset` | `xlarge` (do not use `small` on cloud installs) |
| `openbao.migrations.issuerDiscovery.enabled` | `true` (required on managed Kubernetes) |
| `ingress.gatewayApi.controllerNamespace` | `envoy-gateway-system` |
| `ingress.gatewayApi.gateways.*` | `name: nvcf-gateway`, `namespace: envoy-gateway` |
| `ingress.gatewayApi.routes.nats.enabled`, `routes.ess.enabled` | `true` (the NVCA agent and worker containers need them) |

Multi-cluster only: worker pods run on the compute cluster and cannot resolve
in-cluster service names on the control-plane cluster. Also set:

| Key | Value for multi-cluster |
| --- | --- |
| `global.workerEndpoints.nvcfServiceURL` | `http://api.${GATEWAY_ADDR}` |
| `global.workerEndpoints.nvcfGrpcServiceURL` | `http://worker-api.${GATEWAY_ADDR}` |
| `global.workerEndpoints.nvcfNatsServiceURL` | `nats://${GATEWAY_ADDR}:4222` |
| `global.workerEndpoints.nvctServiceURL` | `http://tasks.${GATEWAY_ADDR}` |
| `global.workerEndpoints.nvctGrpcServiceURL` | `http://worker-tasks.${GATEWAY_ADDR}` |
| `global.workerEndpoints.llmRequestRouterAddress` | a worker-reachable request-router host and port, only when `addons.llm` is enabled |
| `ingress.gatewayApi.routes.nvcfApi.grpc.enabled` | `true`, with hostname `worker-api.${GATEWAY_ADDR}` |
| `ingress.gatewayApi.routes.nvctApi.grpc.enabled` | `true`, with hostname `worker-tasks.${GATEWAY_ADDR}` |

<Info>
The `selfManaged.*Override` values in the compute-plane environment file
configure the NVCA agent's own connections to the control plane. The
`workerEndpoints` values configure the URLs that the control plane advertises
into launched worker pods. Both layers are required for multi-cluster function
execution.
</Info>

Create the pull secret in every control-plane namespace on the control-plane
cluster, and populate the secrets file with the base64 NGC credential:

```bash
for ns in cassandra-system nats-system nvcf api-keys ess sis vault-system cert-manager; do
  kubectl --context "${CONTROL_PLANE_CONTEXT}" create namespace "${ns}" \
    --dry-run=client -o yaml | kubectl --context "${CONTROL_PLANE_CONTEXT}" apply -f -
  kubectl --context "${CONTROL_PLANE_CONTEXT}" create secret docker-registry nvcr-pull-secret \
    --docker-server=nvcr.io --docker-username='$oauthtoken' --docker-password="${NGC_API_KEY}" \
    -n "${ns}" --dry-run=client -o yaml | kubectl --context "${CONTROL_PLANE_CONTEXT}" apply -f -
done

cp secrets/secrets.yaml.template "secrets/${HELMFILE_ENV}-secrets.yaml"
DOCKER_CRED_B64=$(printf '%s' '$oauthtoken:'"${NGC_API_KEY}" | base64 | tr -d '\n')
sed -i.bak "s|REPLACE_WITH_BASE64_DOCKER_CREDENTIAL|${DOCKER_CRED_B64}|g" \
  "secrets/${HELMFILE_ENV}-secrets.yaml"
rm "secrets/${HELMFILE_ENV}-secrets.yaml.bak"
```

<Warning>
Do not commit the populated secrets file or the environment file. They contain
cluster-specific and credential material.
</Warning>

## Step 3: Install the control plane

Run from the `nvcf-self-managed-stack` bundle directory. This is
[Helmfile Installation Step 5](./helmfile-installation.md#step-5-deploy-the-nvcf-control-plane-components)
wrapped by the bundle Makefile:

```bash
cd <path to nvcf-self-managed-stack>/
kubectl config use-context "${CONTROL_PLANE_CONTEXT}"
make install HELMFILE_ENV="${HELMFILE_ENV}"
```

Verify the releases are deployed and that `global.domain` propagated into the
API HTTPRoute hostname:

```bash
helm list --all-namespaces --kube-context "${CONTROL_PLANE_CONTEXT}"
kubectl --context "${CONTROL_PLANE_CONTEXT}" get httproute nvcf-api -n envoy-gateway \
  -o jsonpath='{.spec.hostnames[0]}'
# Expected: api.${GATEWAY_ADDR}
```

Expected releases include `nats`, `cert-manager`, `openbao-server`, `cassandra`,
`api-keys`, `sis`, `api`, `nvct-api`, `invocation-service`, `grpc-proxy`,
`ess-api`, `notary-service`, `admin-issuer-proxy`, `reval`,
`nats-auth-callout-service`, and `ingress`.

## Step 4: Author the nvcf-cli config

Create `nvcf-cli.yaml` pointing at the load balancer address. The static fields
are the same across self-hosted installs; only the URL and Host fields are
derived from `GATEWAY_ADDR`.

```bash
cat > nvcf-cli.yaml <<EOF
# Admin token issuer config (chart-level defaults; identical across installs)
api_keys_service_id: "nvidia-cloud-functions-ncp-service-id-aketm"
api_keys_issuer_service: "nvcf-api"
api_keys_owner_id: "svc@nvcf-api.local"
client_id: "nvcf-default"

# Endpoints, derived from the gateway load balancer address
base_http_url: "http://${GATEWAY_ADDR}"
invoke_url: "http://${GATEWAY_ADDR}"
base_grpc_url: "${GATEWAY_ADDR}:10081"
api_keys_service_url: "http://${GATEWAY_ADDR}"
icms_url: "http://${GATEWAY_ADDR}"
api_host: "api.${GATEWAY_ADDR}"
api_keys_host: "api-keys.${GATEWAY_ADDR}"
invoke_host: "invocation.${GATEWAY_ADDR}"
icms_host: "sis.${GATEWAY_ADDR}"
EOF

export NVCF_CLI_CONFIG="$(pwd)/nvcf-cli.yaml"
```

## Step 5: Register the GPU cluster and install the NVCA operator

Follow [Register a GPU Cluster](/nvcf/compute-plane/register-gpu-cluster) from
the source repository root. The compute-plane environment file
`deploy/stacks/nvcf-compute-plane/environments/${HELMFILE_ENV}.yaml` uses these
values for both topologies:

```yaml
global:
  helm:
    sources:
      repository: "${REPOSITORY}"
  image:
    repository: "${REPOSITORY}"
  imagePullSecrets:
    - name: nvcr-pull-secret
  nvcaOperator:
    selfManaged:
      icmsServiceURL: "http://${GATEWAY_ADDR}"
      icmsServiceHostHeaderOverride: "sis.${GATEWAY_ADDR}"
      revalServiceURL: "http://${GATEWAY_ADDR}"
      revalServiceHostHeaderOverride: "reval.${GATEWAY_ADDR}"
      natsURL: "nats://${GATEWAY_ADDR}:4222"
      natsHostOverride: "nats.${GATEWAY_ADDR}"
```

Then apply the topology-specific deltas below to the canonical
`control-plane profile export`, `register-cluster`, and `install` commands.

### Single-cluster

The GPU cluster is the same cluster as the control plane.

- Create the `nvca-operator` namespace and the `nvcr-pull-secret` on
  `${CONTROL_PLANE_CONTEXT}`.
- Run `control-plane profile export` without `--control-plane-context` or
  `--compute-plane-context`, and pass `--cluster-name "${CLUSTER_NAME}"`.
- Pass `COMPUTE_KUBE_CONTEXT="${CONTROL_PLANE_CONTEXT}"` to `register-cluster`.
  `KUBECONFIG_FILE` is not used.

### Multi-cluster

The NVCA operator installs on a separate compute cluster. Two extra concerns:

1. Registration must discover the OIDC issuer and JWKS from the compute cluster,
   not the control-plane cluster. Switch the context to the compute cluster and
   pass a compute-scoped kubeconfig to `register-cluster`.
2. The compute-plane environment file must carry the control-plane service URLs
   and Host headers (the `selfManaged` values above) so the agent on the
   compute cluster can reach the control plane through the Gateway.

<Warning>
Register with the compute cluster context active. The registration step probes
the current context for its JWKS. If the control-plane context is active, the
control-plane JWKS is recorded for the compute cluster, and the compute agent
then fails authentication at runtime with `Signed JWT rejected: no matching
key(s) found`.
</Warning>

- Create the `nvca-operator` namespace and the `nvcr-pull-secret` on
  `${COMPUTE_CONTEXT}`, and export a compute-scoped kubeconfig:

  ```bash
  kubectl --context "${COMPUTE_CONTEXT}" config view --raw --minify --flatten > compute-kubeconfig.yaml
  export COMPUTE_KUBECONFIG="$(pwd)/compute-kubeconfig.yaml"
  kubectl config use-context "${COMPUTE_CONTEXT}"
  ```

- Run `control-plane profile export` with
  `--control-plane-context "${CONTROL_PLANE_CONTEXT}"` and
  `--compute-plane-context "${COMPUTE_CONTEXT}"`.
- Pass `COMPUTE_KUBE_CONTEXT="${COMPUTE_CONTEXT}"` and
  `KUBECONFIG_FILE="${COMPUTE_KUBECONFIG}"` to both `register-cluster` and
  `install`.

<Info>
Profile export captures the installed control plane's endpoints and trust.
Run `nvcf-cli init` explicitly before registration. `make register-cluster`
writes `registration/${CLUSTER_NAME}-register-values.yaml`, and `make install`
consumes that file. If you skip registration, installation fails with a
"Registration values not found" error.
</Info>

## Step 6: Verify the agent is healthy

Confirm the NVCA operator is deployed and the backend reports the agent healthy.
Use the compute context (equal to the control-plane context in single-cluster):

```bash
helm list -n nvca-operator --kube-context "${COMPUTE_CONTEXT}"
# Expected: nvca-operator deployed

kubectl rollout status deployment/nvca-operator -n nvca-operator \
  --context "${COMPUTE_CONTEXT}" --timeout=10m

kubectl wait nvcfbackend "${CLUSTER_NAME}" -n nvca-operator \
  --context "${COMPUTE_CONTEXT}" \
  --for=jsonpath='{.status.agentStatus}'=healthy --timeout=10m

kubectl --context "${COMPUTE_CONTEXT}" get secret nvcr-pull-secret -n nvca-system
```

The agent reaching `healthy` confirms registration and the Host-header wiring
are correct.

## Single-cluster vs multi-cluster

| Concern | Single-cluster | Multi-cluster |
| --- | --- | --- |
| Clusters | One cluster for everything | Control plane on one cluster, NVCA on a separate GPU cluster |
| Gateway | On the only cluster | On the control-plane cluster only |
| Worker endpoints | Use in-cluster defaults | Set to externally resolvable addresses |
| Worker GRPCRoutes | Disabled | Enabled (`nvcfApi.grpc`, `nvctApi.grpc`) |
| Control-plane env file | Sets the three NVCA Host-header overrides | Same, plus external `workerEndpoints` and worker GRPCRoutes |
| Compute-plane env file | Required: uses endpoints reachable from the shared cluster | Required: uses endpoints reachable from the separate compute cluster |
| Context before `register-cluster` | Control-plane context | Compute context (so JWKS is discovered from the compute cluster) |
| `KUBECONFIG_FILE` | Not used | Compute-scoped kubeconfig passed to `register-cluster` and `install` |
| Verify context | Control-plane context | Compute context |

## Troubleshooting

- Gateway never becomes `Programmed`: check the load balancer annotations match
  your provider and that the controller pod in `envoy-gateway-system` is running.
- `make install` reports "Registration values not found": run
  `make register-cluster` first, in the same directory, with the same
  `CLUSTER_NAME`.
- Compute agent fails with `no matching key(s) found`: you registered with the
  wrong context active. Switch to the compute context and re-run
  `make register-cluster`.

See [Troubleshooting](./troubleshooting.md) for control-plane issues and
[Compute Plane Troubleshooting](/nvcf/compute-plane/dev/troubleshooting) for NVCA
issues.
