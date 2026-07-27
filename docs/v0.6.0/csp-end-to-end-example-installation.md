# CSP End-to-End Example Installation (Helmfile)

This page provides a complete end-to-end example for installing NVCF on
pre-provisioned managed Kubernetes clusters using the split Helmfile bundles.
It covers both topologies:

- Single-cluster: the control plane and the NVCA operator run on the same cluster.
- Multi-cluster: the control plane runs on one cluster, and the NVCA operator is
  registered and installed on a separate GPU (compute) cluster.

The commands are written to work on any cloud provider (CSP). Amazon EKS is used
as the worked example. The only provider-specific pieces are the load balancer
annotations on the Gateway, the `storageClass` name, and the `kubectl` context
names. Substitute the equivalents for GKE, AKS, or on-prem.

For a deeper reference on each release and on values, see
[Helmfile Installation](./helmfile-installation.md). For pulling and mirroring
the bundles and images, see [Image Mirroring](./image-mirroring.md).

<Info>
This guide assumes you have already downloaded and extracted the control-plane
Helmfile bundle and have a source checkout for the compute plane:

- `nvcf-self-managed-stack` for the control plane.
- `deploy/stacks/nvcf-compute-plane` in the source repository for the compute
  plane (NVCA operator).

Control-plane commands run from inside the `nvcf-self-managed-stack` directory.
Compute-plane commands run from the repository root with `make -C`.

```bash
git clone https://github.com/nvidia/nvcf.git
```
</Info>

## Installation order

The order matters. Each step produces an input that the next step needs. The
load balancer address, in particular, must exist before you configure the
environment file, because it becomes `global.domain` and the NVCA Host headers.

```
1. Install the Gateway          -> external load balancer address
2. Configure the environment    -> environments/<env>.yaml + secrets/<env>-secrets.yaml
3. Install the control plane    -> control-plane services + HTTPRoutes
4. Author the nvcf-cli config   -> points at the load balancer address
5. Register the GPU cluster     -> registration values file
6. Install the NVCA operator    -> agent connects back to the control plane
7. Verify the agent is healthy
```

Single-cluster and multi-cluster share steps 1 through 4. Step 5 requires a
compute-plane environment file for both topologies. Only the cluster target and
control-plane endpoint values differ. See
[Single-cluster vs multi-cluster](#single-cluster-vs-multi-cluster).

## Prerequisites

### Tools

Install on the machine you run these commands from:

- `kubectl`
- `helm` (3.x)
- `helmfile`
- `nvcf-cli`

### Clusters

The clusters must be provisioned before you start. This guide does not create
them. Each cluster needs:

- A default-capable `StorageClass` with dynamic provisioning. On EKS this is
  `gp3`, backed by the EBS CSI driver. Substitute your provider's class name.
- The compute (GPU) cluster needs a GPU operator (real or the fake GPU operator
  for non-GPU validation). See [Fake GPU Operator](./fake-gpu-operator.md).
- The compute cluster needs the
  [SMB CSI driver](https://github.com/kubernetes-csi/csi-driver-smb)
  (`smb.csi.k8s.io`). NVCA uses it for shared model cache storage that function
  worker pods mount. Install and verify the driver before registering the GPU
  cluster. See the
  [Self-Managed Clusters prerequisites](./cluster-management/self-managed.md#prerequisites)
  for the installation command.

Both clusters must be reachable through `kubectl` contexts:

```bash
kubectl --context "${CONTROL_PLANE_CONTEXT}" get nodes -o name
# Multi-cluster only:
kubectl --context "${COMPUTE_CONTEXT}" get nodes -o name
```

### Environment variables

Set these once. In single-cluster, `COMPUTE_CONTEXT` equals
`CONTROL_PLANE_CONTEXT`.

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
[Gateway quickstart](./gateway-routing.md#gateway-quickstart). It installs the
Gateway API CRDs, the Envoy Gateway controller, the `GatewayClass`, and the
`nvcf-gateway` Gateway, and exports `GATEWAY_ADDR`. Run it against
`${CONTROL_PLANE_CONTEXT}`.

<Note>
This guide's NVCA path also routes NATS. Make sure the `nvcf-gateway` Gateway
includes a `nats` listener on port 4222 (in addition to the `http` and `tcp`
listeners from the quickstart), and enable `routes.nats.enabled` in the
environment file in Step 2.
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

This step produces two files in the `nvcf-self-managed-stack` bundle: the
environment values file `environments/<env>.yaml` (copied from `base.yaml`) and
the secrets file `secrets/<env>-secrets.yaml` (copied from
`secrets.yaml.template`). Both are required before the install.

From the `nvcf-self-managed-stack` directory, copy the base template. The file
name (`<env>`) must match `HELMFILE_ENV`.

```bash
cd <path to nvcf-self-managed-stack>/
cp environments/base.yaml "environments/${HELMFILE_ENV}.yaml"
```

Edit `environments/${HELMFILE_ENV}.yaml`. Every field is explained inline below.
Lines marked `CHANGE` must be updated for your cluster; replace the `${...}`
values with the literals you exported earlier.

```yaml
global:
  domain: "${GATEWAY_ADDR}"        # CHANGE: from "localhost". Builds HTTPRoute hostnames (api.<domain>, etc.)

  helm:
    sources:
      registry: "nvcr.io"          # OCI registry NVCF charts are pulled from. Change only if you mirror.
      repository: "${REPOSITORY}"  # CHANGE: from "YOUR_ORG/YOUR_TEAM". NGC org/team or mirror path.

  imagePullSecrets:
    - name: nvcr-pull-secret       # CHANGE: from []. Pull secret applied to all workloads (created below).

  image:
    registry: nvcr.io              # Container image registry. Change only if you mirror.
    repository: "${REPOSITORY}"    # CHANGE: from "YOUR_ORG/YOUR_TEAM". NGC org/team or mirror path.

  workerEndpoints:
    nvcfServiceURL: ""             # CHANGE (multi-cluster): "http://api.${GATEWAY_ADDR}". Worker env NVCF_FQDN.
    nvcfGrpcServiceURL: ""         # CHANGE (multi-cluster): "http://worker-api.${GATEWAY_ADDR}". Worker env NVCF_FQDN_GRPC.
    nvcfNatsServiceURL: ""         # CHANGE (multi-cluster): "nats://${GATEWAY_ADDR}:4222". Worker env NVCF_NATS_WORKER_URL.
    essServiceURL: ""              # Empty = in-cluster default. Workers use this to reach the Encrypted Secrets Service.
    nvctServiceURL: ""             # CHANGE (multi-cluster): "http://tasks.${GATEWAY_ADDR}". Worker env NVCT_FQDN.
    nvctGrpcServiceURL: ""         # CHANGE (multi-cluster): "http://worker-tasks.${GATEWAY_ADDR}". Worker env NVCT_FQDN_GRPC.
    invocationServiceURL: ""       # Empty = in-cluster default. Workers use this for the invocation stream address.

  nodeSelectors:
    enabled: false                 # Pin system workloads to labeled node pools. Leave false unless nodes are labeled.
    vault:
      key: nvcf.nvidia.com/workload
      value: vault                 # Node label for the vault pool (applied only when enabled: true).
    cassandra:
      key: nvcf.nvidia.com/workload
      value: cassandra             # Node label for the cassandra pool.
    controlplane:
      key: nvcf.nvidia.com/workload
      value: control-plane         # Node label for the control-plane pool.

  tolerations:
    enabled: false                 # Tolerations for tainted node pools. Leave false unless nodes are tainted.
    all: []                        # Tolerations applied to all system workloads when enabled.

  storageClass: "${STORAGE_CLASS}" # CHANGE: from "". Dynamic-provisioning StorageClass (gp3 on EKS).
  storageSize: "10Gi"              # Per-PVC size. Default works; raise for larger control-plane data (Cassandra).

  observability:
    tracing:
      enabled: false               # OpenTelemetry trace export. Set true plus a collector endpoint to enable.
      collectorEndpoint: ""        # OTLP collector endpoint when tracing is enabled.
      collectorPort: 4317          # OTLP collector port.
      collectorProtocol: http      # OTLP protocol (http or grpc).
    metrics:
      enabled: false               # Prometheus metrics. Set true if you run the Prometheus Operator.

accounts:
  limits:
    maxFunctions: 10               # Max functions per account.
    maxTasks: 10                   # Max tasks per account.
    maxTelemetries: 10             # Max telemetry endpoints per account.
    maxRegistryCreds: 10           # Max registry credentials per account.

nats:
  enabled: true                    # Deploy the NATS messaging layer. Keep true; the control plane depends on it.

cassandra:
  enabled: true                    # Deploy Cassandra. Keep true.
  resourcesPreset: "xlarge"        # CPU/memory preset. Do not use small for cloud installs (OOM on first boot).

certManager:
  enabled: true                    # Install cert-manager for the self-managed PKI. Keep true.

openbao:
  enabled: true                    # Deploy OpenBao (Vault) for secrets. Keep true.
  migrations:
    issuerDiscovery:
      enabled: true                # CHANGE: from false. Discover the cluster OIDC issuer. Required on managed Kubernetes.
  injector:
    replicas: 2                    # OpenBao injector replicas (HA). Set 1 on single-node / minimal pools.

addons:
  lls:
    enabled: false                 # Low Latency Streaming (TURN) addon. Optional.
  llm:
    enabled: false                 # LLM gateway + request router (Stargate). Optional.
    pki:
      enabled: false               # OpenBao-issued QUIC TLS for the request router. Optional.
      allowedDomains: ""           # Required only when llm.pki.enabled: comma-separated DNS suffixes.
      dnsNames: []                 # Required only when llm.pki.enabled: SANs on the issued certificate.

stateMetrics:
  enabled: false                   # State-metrics exporter. Optional.
  serviceMonitor:
    enabled: false                 # ServiceMonitor for the exporter. Requires the Prometheus Operator.

rateLimiter:
  enabled: false                   # Invocation rate limiter. Optional.
  replicaCount: 1                  # Rate limiter replicas (applied only when enabled).

ingress:
  gatewayApi:
    enabled: true                  # Enable Gateway API ingress. Keep true.
    controllerNamespace: envoy-gateway-system  # CHANGE: from "". Namespace of the gateway controller.
    routes:
      nvcfApi:
        routeAnnotations: {}       # Optional per-route annotations for the NVCF API route.
        grpc:
          enabled: false           # CHANGE (multi-cluster): true. Creates a GRPCRoute for worker-facing API gRPC.
          hostnames:
            - "worker-api.${GATEWAY_ADDR}"  # CHANGE (multi-cluster): externally resolvable hostname for the API gRPC route.
      nvctApi:
        routeAnnotations: {}       # Optional annotations for the NVCT API route.
        grpc:
          enabled: false           # CHANGE (multi-cluster): true. Creates a GRPCRoute for worker-facing NVCT gRPC.
          hostnames:
            - "worker-tasks.${GATEWAY_ADDR}"  # CHANGE (multi-cluster): externally resolvable hostname for the NVCT gRPC route.
      apiKeys:
        routeAnnotations: {}       # Optional annotations for the api-keys route.
      invocation:
        routeAnnotations: {}       # Optional annotations for the invocation route.
      llmInvocation:
        routeAnnotations: {}       # Optional annotations for the LLM invocation route.
      grpc:
        routeAnnotations: {}       # Optional annotations for the gRPC route.
      nats:
        enabled: true              # CHANGE: from false. Create the NATS route (the NVCA agent needs it).
        routeAnnotations: {}       # Optional annotations for the NATS route.
      ess:
        enabled: true              # CHANGE: from false. Create the ESS route (the nvcf worker container needs it).
        routeAnnotations: {}       # Optional annotations for the ESS route.
    gateways:
      shared:
        name: nvcf-gateway         # CHANGE: from "". Gateway the HTTP routes attach to.
        namespace: envoy-gateway   # CHANGE: from "". Namespace of that Gateway.
      grpc:
        name: nvcf-gateway         # CHANGE: from "". Gateway the gRPC route attaches to.
        namespace: envoy-gateway   # CHANGE: from "".
      nats:
        name: nvcf-gateway         # CHANGE: from "". Gateway the NATS route attaches to (when routes.nats.enabled).
        namespace: envoy-gateway   # CHANGE: from "".
        listenerName: nats         # Gateway listener name for NATS. Keep as nats.
```

<Info>
Multi-cluster worker endpoints and GRPCRoutes

In single-cluster deployments, leave `workerEndpoints` empty and `grpc.enabled`
false. Worker pods resolve control-plane services through in-cluster DNS.

In multi-cluster deployments, worker pods run on a separate compute cluster and
cannot resolve in-cluster service names from the control-plane cluster. Set each
`workerEndpoints` field to an externally resolvable address (the Gateway load
balancer hostname with a per-service route hostname). Enable the `nvcfApi.grpc`
and `nvctApi.grpc` GRPCRoutes so workers can reach the API and NVCT gRPC
endpoints through the Gateway.

The `selfManaged.*Override` values in the compute-plane environment file
configure the NVCA agent's own connections to the control plane. The
`workerEndpoints` values configure the URLs that the control plane advertises
into launched worker pods. Both layers are required for multi-cluster function
execution.
</Info>

### Create the registry pull secret

The control-plane charts reference an image pull secret named `nvcr-pull-secret`
in each namespace. Create it in every control-plane namespace:

```bash
for ns in cassandra-system nats-system nvcf api-keys ess sis vault-system cert-manager; do
  kubectl --context "${CONTROL_PLANE_CONTEXT}" create namespace "${ns}" \
    --dry-run=client -o yaml | kubectl --context "${CONTROL_PLANE_CONTEXT}" apply -f -
  kubectl --context "${CONTROL_PLANE_CONTEXT}" create secret docker-registry nvcr-pull-secret \
    --docker-server=nvcr.io --docker-username='$oauthtoken' --docker-password="${NGC_API_KEY}" \
    -n "${ns}" --dry-run=client -o yaml | kubectl --context "${CONTROL_PLANE_CONTEXT}" apply -f -
done
```

### Create the bundle secrets file

The control-plane bundle reads a secrets file for the OpenBao migration and API
account bootstrap. Create it from the template and set the base64 dockerconfig
credential:

```bash
cp secrets/secrets.yaml.template "secrets/${HELMFILE_ENV}-secrets.yaml"

DOCKER_CRED_B64=$(printf '%s' '$oauthtoken:'"${NGC_API_KEY}" | base64 | tr -d '\n')
# Replace every REPLACE_WITH_BASE64_DOCKER_CREDENTIAL in the secrets file with ${DOCKER_CRED_B64}.
sed -i.bak "s|REPLACE_WITH_BASE64_DOCKER_CREDENTIAL|${DOCKER_CRED_B64}|g" \
  "secrets/${HELMFILE_ENV}-secrets.yaml"
rm "secrets/${HELMFILE_ENV}-secrets.yaml.bak"
```

<Warning>
Do not commit the populated secrets file or the environment file. They contain
cluster-specific and credential material.
</Warning>

## Step 3: Install the control plane

Run from the `nvcf-self-managed-stack` bundle directory:

```bash
cd <path to nvcf-self-managed-stack>/
kubectl config use-context "${CONTROL_PLANE_CONTEXT}"
make install HELMFILE_ENV="${HELMFILE_ENV}"
```

Verify the releases are deployed:

```bash
helm list --all-namespaces --kube-context "${CONTROL_PLANE_CONTEXT}"
```

Expected releases include `nats`, `cert-manager`, `openbao-server`, `cassandra`,
`api-keys`, `sis`, `api`, `nvct-api`, `invocation-service`, `grpc-proxy`,
`ess-api`, `notary-service`, `admin-issuer-proxy`, `reval`,
`nats-auth-callout-service`, and `ingress`.

Confirm `global.domain` propagated into the API HTTPRoute hostname:

```bash
kubectl --context "${CONTROL_PLANE_CONTEXT}" get httproute nvcf-api -n envoy-gateway \
  -o jsonpath='{.spec.hostnames[0]}'
# Expected: api.${GATEWAY_ADDR}
```

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

This is where single-cluster and multi-cluster diverge. Pick the matching
section. Run both from the source repository root.

```bash
cd <path-to-nvcf-repository>
```

Create the compute-plane environment file for either topology. The compute-plane
Makefile reads
`deploy/stacks/nvcf-compute-plane/environments/${HELMFILE_ENV}.yaml`, and the
`selfManaged` values tell the NVCA agent how to reach the control plane. The
sections below use different cluster targets and endpoint values.

```bash
cp deploy/stacks/nvcf-compute-plane/environments/base.yaml \
  "deploy/stacks/nvcf-compute-plane/environments/${HELMFILE_ENV}.yaml"
```

Set these keys in
`deploy/stacks/nvcf-compute-plane/environments/${HELMFILE_ENV}.yaml`:

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

Then follow the matching subsection below.

### Single-cluster

The GPU cluster is the same cluster as the control plane, so registration uses
the current context. Create the pull secret in `nvca-operator`; the operator
propagates it to the managed namespaces after installation:

```bash
kubectl --context "${CONTROL_PLANE_CONTEXT}" create namespace nvca-operator \
  --dry-run=client -o yaml | kubectl --context "${CONTROL_PLANE_CONTEXT}" apply -f -
kubectl --context "${CONTROL_PLANE_CONTEXT}" create secret docker-registry nvcr-pull-secret \
  --docker-server=nvcr.io --docker-username='$oauthtoken' --docker-password="${NGC_API_KEY}" \
  -n nvca-operator --dry-run=client -o yaml | kubectl --context "${CONTROL_PLANE_CONTEXT}" apply -f -
```

Register, then install:

```bash
kubectl config use-context "${CONTROL_PLANE_CONTEXT}"

make -C deploy/stacks/nvcf-compute-plane register-cluster \
  CLUSTER_NAME="${CLUSTER_NAME}" \
  NCA_ID=nvcf-default \
  CLUSTER_REGION="${CLUSTER_REGION}" \
  ICMS_URL="http://${GATEWAY_ADDR}" \
  NVCF_CLI="${NVCF_CLI}" \
  NVCF_CLI_CONFIG="${NVCF_CLI_CONFIG}"

make -C deploy/stacks/nvcf-compute-plane install \
  CLUSTER_NAME="${CLUSTER_NAME}" \
  HELMFILE_ENV="${HELMFILE_ENV}" \
  NVCF_CLI="${NVCF_CLI}" \
  NVCF_CLI_CONFIG="${NVCF_CLI_CONFIG}"
```

### Multi-cluster

The NVCA operator installs on a separate compute cluster. Two extra concerns:

1. Registration must discover the OIDC issuer and JWKS from the compute cluster,
   not the control-plane cluster. Switch the context to the compute cluster and
   pass a compute-scoped kubeconfig to `register-cluster`.
2. The compute-plane environment file must carry the control-plane service URLs
   and Host headers so the agent on the compute cluster can reach the control
   plane through the Gateway.

<Warning>
Register with the compute cluster context active. The registration step probes
the current context for its JWKS. If the control-plane context is active, the
control-plane JWKS is recorded for the compute cluster, and the compute agent
then fails authentication at runtime with `Signed JWT rejected: no matching
key(s) found`.
</Warning>

You already created the compute-plane environment file with the `selfManaged`
values above. Create the pull secret in `nvca-operator` on the compute cluster;
the operator propagates it to the managed namespaces after installation. Also
create a compute-scoped kubeconfig:

```bash
kubectl --context "${COMPUTE_CONTEXT}" create namespace nvca-operator \
  --dry-run=client -o yaml | kubectl --context "${COMPUTE_CONTEXT}" apply -f -
kubectl --context "${COMPUTE_CONTEXT}" create secret docker-registry nvcr-pull-secret \
  --docker-server=nvcr.io --docker-username='$oauthtoken' --docker-password="${NGC_API_KEY}" \
  -n nvca-operator --dry-run=client -o yaml | kubectl --context "${COMPUTE_CONTEXT}" apply -f -

kubectl --context "${COMPUTE_CONTEXT}" config view --raw --minify --flatten > compute-kubeconfig.yaml
export COMPUTE_KUBECONFIG="$(pwd)/compute-kubeconfig.yaml"
```

Register with the compute context active, then install onto the compute cluster:

```bash
kubectl config use-context "${COMPUTE_CONTEXT}"

make -C deploy/stacks/nvcf-compute-plane register-cluster \
  CLUSTER_NAME="${CLUSTER_NAME}" \
  NCA_ID=nvcf-default \
  CLUSTER_REGION="${CLUSTER_REGION}" \
  ICMS_URL="http://${GATEWAY_ADDR}" \
  KUBECONFIG_FILE="${COMPUTE_KUBECONFIG}" \
  NVCF_CLI="${NVCF_CLI}" \
  NVCF_CLI_CONFIG="${NVCF_CLI_CONFIG}"

make -C deploy/stacks/nvcf-compute-plane install \
  CLUSTER_NAME="${CLUSTER_NAME}" \
  HELMFILE_ENV="${HELMFILE_ENV}" \
  KUBECONFIG_FILE="${COMPUTE_KUBECONFIG}" \
  NVCF_CLI="${NVCF_CLI}" \
  NVCF_CLI_CONFIG="${NVCF_CLI_CONFIG}"
```

<Info>
`make register-cluster` runs `nvcf-cli init` (mints the admin token) and then
`cluster register`, and writes
`registration/${CLUSTER_NAME}-register-values.yaml`. `make install` consumes
that file. If you skip `register-cluster`, `make install` fails with a
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
```

Confirm that the operator propagated the pull secret to `nvca-system`:

```bash
kubectl --context "${COMPUTE_CONTEXT}" get secret nvcr-pull-secret -n nvca-system
```

The agent reaching `healthy` confirms registration and the Host-header wiring
are correct.

## Single-cluster vs multi-cluster

| Concern | Single-cluster | Multi-cluster |
| --- | --- | --- |
| Clusters | One cluster for everything | Control plane on one cluster, NVCA on a separate GPU cluster |
| Gateway | On the only cluster | On the control-plane cluster only |
| Worker endpoints | Empty (in-cluster DNS) | Set to externally resolvable Gateway hostnames |
| Worker GRPCRoutes | Disabled | Enabled (`nvcfApi.grpc`, `nvctApi.grpc`) |
| Control-plane env file | Sets the three NVCA Host-header overrides | Same, plus `workerEndpoints` and worker GRPCRoutes |
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

See [Troubleshooting](./troubleshooting.md) for more.
