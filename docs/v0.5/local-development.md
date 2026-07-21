# Local Development (k3d)

Run the full NVCF self-hosted control plane on your laptop using
[k3d](https://k3d.io/) for development, testing, or demos.

<Info>
This setup is for **local development only**. It uses fake GPUs, a single Cassandra
replica, and ephemeral storage. Do not use this for production workloads.

</Info>

## Assumptions

This guide assumes:

- **Helm charts** are pulled from the NGC registry (`nvcr.io/0833294136851237/nvcf-ncp-staging`)
- **Container images** are pulled from the same NGC registry
- **Image pull secrets** are configured in the environment YAML using `imagePullSecrets` to authenticate with NGC

If you are using a different registry (e.g., Amazon ECR, a private Harbor instance, or a
local mirror), update the `helm.sources` and `image` sections in the environment file
and adjust the pull secret configuration accordingly. See [self-hosted-image-mirroring](./image-mirroring.md)
for details on mirroring artifacts to other registries.

<Tip>
A ready-to-use k3d configuration and setup script is available in the
[nv-cloud-function-helpers](https://github.com/NVIDIA/nvcf/tree/main/examples/function-samples)
repository. Clone it and run `./setup.sh` to create the cluster with all prerequisites,
then skip to [Deploy the NVCF Stack].

</Tip>

### Prerequisites

Install the following tools:

- [Docker](https://www.docker.com/get-started) (running)
- [k3d](https://k3d.io/#installation) v5.x or later
- `kubectl`
- `helm` >= 3.12
- `helmfile` >= 1.1.0, < 1.2.0
- `helm-diff` plugin (`helm plugin install https://github.com/databus23/helm-diff`)
- **NGC API Key** from [ngc.nvidia.com](https://ngc.nvidia.com) with access to the NVCF chart/image registry

### Step 1: Create the k3d Cluster

Save the following configuration as `k3d-config.yaml`:

```yaml
apiVersion: k3d.io/v1alpha5
kind: Simple
metadata:
  name: ncp-local

image: rancher/k3s:v1.30.2-k3s2
servers: 1
agents: 5

ports:
  - port: 8080:80
    nodeFilters:
      - loadbalancer
  - port: 8443:443
    nodeFilters:
      - loadbalancer

options:
  k3d:
    wait: true
  k3s:
    extraArgs:
      - arg: "--disable=traefik"
        nodeFilters:
          - server:*
    nodeLabels:
      - label: run.ai/simulated-gpu-node-pool=default
        nodeFilters:
          - agent:3
          - agent:4
      - label: nvidia.com/gpu.family=hopper
        nodeFilters:
          - agent:3
          - agent:4
      - label: nvidia.com/gpu.machine=NVIDIA-DGX-H100
        nodeFilters:
          - agent:3
          - agent:4
      - label: nvidia.com/cuda.driver.major=535
        nodeFilters:
          - agent:3
          - agent:4
```

This creates a 6-node cluster: 1 server (control plane) and 5 agents. Agents 3 and 4 are
pre-labeled for the fake GPU operator. Traefik is disabled because NVCF uses Envoy Gateway.

Create the cluster:

```bash
k3d cluster create --config k3d-config.yaml
```

Verify:

```bash
kubectl get nodes
# Expected: 6 nodes (1 server + 5 agents), all Ready
```

### Step 2: Install the Fake GPU Operator

The fake GPU operator simulates GPU resources on the pre-labeled nodes so the NVCA agent
can discover them. See [fake-gpu-operator](./fake-gpu-operator.md) for full details.

```bash
# Install KWOK (required by the fake GPU operator)
kubectl apply -f https://github.com/kubernetes-sigs/kwok/releases/download/v0.7.0/kwok.yaml
kubectl wait --for=condition=Available deployment/kwok-controller -n kube-system --timeout=60s

# Install the fake GPU operator
helm repo add fake-gpu-operator \
  https://runai.jfrog.io/artifactory/api/helm/fake-gpu-operator-charts-prod --force-update

helm upgrade -i gpu-operator fake-gpu-operator/fake-gpu-operator \
  -n gpu-operator --create-namespace \
  --set 'topology.nodePools.default.gpuCount=8' \
  --set 'topology.nodePools.default.gpuProduct=NVIDIA-H100-80GB-HBM3' \
  --set 'topology.nodePools.default.gpuMemory=81559'
```

Verify fake GPUs appear on the labeled nodes:

```bash
kubectl get nodes -o custom-columns="NAME:.metadata.name,GPU:.status.allocatable.nvidia\.com/gpu"
# Agents 3 and 4 should show GPU: 8
```

### Step 3: Install CSI SMB Driver

The CSI SMB driver is required for NVCA shared model cache storage:

```bash
helm repo add csi-driver-smb \
  https://raw.githubusercontent.com/kubernetes-csi/csi-driver-smb/master/charts

helm install csi-driver-smb csi-driver-smb/csi-driver-smb \
  -n kube-system --version v1.17.0
```

### Deploy the NVCF Stack

With the cluster ready, follow the [helmfile-installation](./helmfile-installation.md) guide. The steps
below call out the local-specific differences for each step.

## Step 1 (Ingress)

Follow as documented, but **skip the cloud-provider annotations** on the Gateway resource.
k3d handles LoadBalancer services automatically via its built-in `klipper-lb`.

## Step 2 (Environment file)

Create a local development environment file from the template below
([local-dev-env.yaml](https://raw.githubusercontent.com/NVIDIA/nvcf/main/docs/v0.5/samples/configs/local-dev-env.yaml)).
Save it as `environments/<name>.yaml` (e.g., `environments/my-local.yaml`) in your
`nvcf-self-managed-stack` directory.

<Accordion title="Local Development Environment Template">
</Accordion>
```yaml title="environments/my-local.yaml"
# NVCF Self-Hosted Local Development Environment
# For use with k3d clusters. See the Local Development guide for setup instructions.
#
# Save this file as environments/<name>.yaml in your nvcf-self-managed-stack directory.
# Create a matching secrets/<name>-secrets.yaml file with your registry credentials.
# Deploy with: HELMFILE_ENV=<name> helmfile sync

global:
  # Domain for local access (routes use .localhost TLD)
  domain: "localhost"

  # Helm chart registry (where helmfile pulls OCI charts from)
  helm:
    sources:
      registry: nvcr.io
      repository: 0833294136851237/nvcf-ncp-staging

  # Container image registry (where Kubernetes pulls images from)
  image:
    registry: nvcr.io
    repository: 0833294136851237/nvcf-ncp-staging

  # Pull secret created by create-nvcr-pull-secrets.sh (run once before deploying)
  imagePullSecrets:
    - name: nvcr-pull-secret

  # Disable node selectors for local development (pods schedule on any node)
  nodeSelectors:
    enabled: false

  # k3d uses the local-path StorageClass by default
  storageClass: local-path
  storageSize: 2Gi

  observability:
    tracing:
      enabled: false
      collectorEndpoint: ""
      collectorPort: 4317
      collectorProtocol: http

# Single Cassandra replica for local development
cassandra:
  enabled: true
  replicaCount: 1
  jvm:
    # Fast startup options -- only safe with a single replica.
    # Do NOT use these settings with multiple replicas.
    extraOpts: "-Dcassandra.superuser_setup_delay_ms=100 -Dcassandra.gossip_settle_min_wait_ms=1000"

nats:
  enabled: true

openbao:
  enabled: true
  migrations:
    issuerDiscovery:
      enabled: true

# Gateway configuration matching the standard control plane installation Step 1
ingress:
  gatewayApi:
    enabled: true
    controllerNamespace: envoy-gateway-system
    routes:
      nvcfApi:
        routeAnnotations: {}
      apiKeys:
        routeAnnotations: {}
      invocation:
        routeAnnotations: {}
      grpc:
        routeAnnotations: {}
    gateways:
      shared:
        name: nvcf-gateway
        namespace: envoy-gateway
        listenerName: http
      grpc:
        name: nvcf-gateway
        namespace: envoy-gateway
        listenerName: tcp
```


This template is pre-configured for local development:

- **Storage**: `local-path` (2Gi volumes, the default k3d StorageClass)
- **Cassandra**: Single replica with fast startup JVM options
- **Node selectors**: Disabled (pods schedule on any available node)
- **Registry**: `nvcr.io/0833294136851237/nvcf-ncp-staging`
- **Gateway**: `nvcf-gateway` in `envoy-gateway` namespace (matches Step 1)
- **Domain**: `localhost`
- **imagePullSecrets**: Pre-configured to reference `nvcr-pull-secret` (created in Step 4)

## Step 3 (Secrets)

Create `secrets/<name>-secrets.yaml` (e.g., `secrets/my-local-secrets.yaml`) from
the template in the control plane guide. The file name must match your environment name.
Fill in your NGC base64-encoded credentials for the NGC org you'll be deploying function images from:

```bash
echo -n '$oauthtoken:YOUR_NGC_API_KEY' | base64
```

## Step 4 (Pull secrets)

Run the helper script to create the `nvcr-pull-secret` Kubernetes secret in all NVCF namespaces:

```bash
export NGC_API_KEY="<your-ngc-api-key>"
bash samples/scripts/create-nvcr-pull-secrets.sh
```

The environment file template from Step 2 already references this secret via `imagePullSecrets`.

## Step 5 (Deploy)

Authenticate helm and deploy using your environment name:

```bash
helm registry login nvcr.io -u '$oauthtoken' -p "$NGC_API_KEY"
HELMFILE_ENV=<name> helmfile sync
```

<Note>
Replace `<name>` with the name you chose for your environment file (e.g., `my-local`).

</Note>

## Step 6 (Verify)

Check that all pods are running:

```bash
kubectl get pods -A -o wide
# All pods should be Running or Completed

helm list -A
# All releases should show STATUS: deployed
```

Verify the NVCA agent discovered the fake GPUs:

```bash
kubectl get nvcfbackends -n nvca-operator
# Expected: nvcf-default   healthy

kubectl get nvcfbackends -n nvca-operator -o jsonpath='{.items[0].status.gpuUsage}' | python3 -m json.tool
# Expected: {"H100": {"available": 16, "capacity": 16}}
```

Verify API connectivity using the `.localhost` routing (not the Gateway address, which
is cluster-internal on k3d):

```bash
# Generate an admin token
export NVCF_TOKEN=$(curl -s -X POST "http://api-keys.localhost:8080/v1/admin/keys" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['value'])")

echo "Token: ${NVCF_TOKEN:0:20}..."

# List functions (should return empty)
curl -s "http://api.localhost:8080/v2/nvcf/functions" \
  -H "Authorization: Bearer ${NVCF_TOKEN}" | python3 -m json.tool
# Expected: {"functions": []}
```

<Note>
The standard control plane verification commands use the Gateway address from
`kubectl get gateway`. On k3d this returns a cluster-internal IP that is not reachable
from the host. Use `localhost:8080` with `.localhost` hostnames instead, as shown above.

</Note>

### Accessing Routes Locally

NVCF routes use the `.localhost` top-level domain, which resolves to `127.0.0.1`
automatically on most systems. Access services via the k3d load balancer on port 8080:

- `http://api.localhost:8080` -- NVCF API
- `http://api-keys.localhost:8080` -- API Keys service
- `http://invocation.localhost:8080` -- Function invocation

If `.localhost` does not resolve automatically, add entries to `/etc/hosts`:

```text
127.0.0.1 api.localhost
127.0.0.1 api-keys.localhost
127.0.0.1 invocation.localhost
```

<Note>
Wildcard subdomains (e.g., `<function-id>.invocation.localhost`) cannot be added to
`/etc/hosts`. For local testing with dynamic function IDs, add specific entries or use
a local DNS resolver such as `dnsmasq`.

</Note>

### Teardown

```bash
# Remove the NVCF stack (use your environment name)
HELMFILE_ENV=<name> helmfile destroy

# Delete the k3d cluster
k3d cluster delete ncp-local
```

### Limitations

- **Fake GPUs** -- Function containers will be scheduled and deployed but cannot execute
  actual GPU workloads.
- **Single Cassandra replica** -- No high availability. Data may be lost on pod restart.
- **Ephemeral storage** -- `local-path` volumes are deleted when the cluster is destroyed.
- **Not suitable for performance testing** -- Resource constraints of a laptop do not
  represent production environments.
