# Self-Managed Clusters

GPU clusters are registered with the NVCF control plane and managed by the NVCA
Operator. Use the compute-plane Makefile in
`deploy/stacks/nvcf-compute-plane` for this workflow. It registers one GPU
cluster at a time with `nvcf-cli`, then installs the operator with the cluster
identity returned by registration. The operator reads that identity from a
local ConfigMap and authenticates through the local OpenBao (Vault) instance.

<Info>
A running NVCF control plane (SIS, OpenBao, NATS, Cassandra, and all core
services) is required. The [Quickstart](../quickstart.md) can install the
control plane and register a GPU cluster in one flow. Use this page when you
need to install or operate the NVCA Operator after using the Helmfile
installation path.

</Info>

Clone the public repository and run the compute-plane commands from its root:

```bash
git clone https://github.com/nvidia/nvcf.git
cd nvcf
```

## Prerequisites

Before installing the NVCA Operator, ensure the following prerequisites are met:

- The [control plane](../helmfile-installation.md) is installed and all core services are running.

- The [NVIDIA GPU Operator](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/getting-started.html) is installed on the GPU cluster. The GPU Operator manages the NVIDIA drivers, device plugin, and GPU feature discovery required for workload scheduling. For development or testing environments without physical GPUs, see [fake-gpu-operator](../fake-gpu-operator.md).

- (Optional) The [KAI Scheduler](./kai-scheduler.md) can be installed on the GPU cluster for optimized AI workload scheduling and bin-packing of GPU resources. It is required only when you enable the `KAIScheduler` feature flag. See [NVCA Configuration](./configuration.md).

- `GPU Workload Components` must be available in a user-managed registry that your Kubernetes cluster can access. See `GPU Workload Components` under [self-hosted-artifact-manifest](../manifest.md) for necessary artifacts and [self-hosted-image-mirroring](../image-mirroring.md) for mirroring instructions.

- `nvcf-cli` is available on the deployment machine. The compute-plane Makefile
  calls it during cluster registration. See [self-hosted-cli](../cli.md) for
  CLI installation and configuration.

- The [SMB CSI driver](https://github.com/kubernetes-csi/csi-driver-smb) (`smb.csi.k8s.io`) must be installed on the GPU cluster. It is required for NVCA shared model cache storage (samba sidecar). Install it with:

  ```bash
  helm repo add csi-driver-smb \
    https://raw.githubusercontent.com/kubernetes-csi/csi-driver-smb/master/charts

  helm install csi-driver-smb csi-driver-smb/csi-driver-smb \
    -n kube-system --version v1.17.0
  ```

## How It Works

In self-managed mode, the cluster identity comes from the CLI registration
step, and the operator consumes it:

1. You configure the compute-plane Helmfile environment with registry settings
   and control-plane service URLs.
2. From the repository root, you run
   `make -C deploy/stacks/nvcf-compute-plane register-cluster` (see
   [Register the cluster](#register-the-cluster)). The target runs
   `nvcf-cli init`, then `nvcf-cli cluster register`. The CLI discovers the GPU
   cluster's OIDC issuer and public JWKS, records them with the control plane
   (ICMS), and writes the `clusterID`, `clusterGroupID`, identity source, and
   service URLs to a registration values file.
3. From the repository root, you run
   `make -C deploy/stacks/nvcf-compute-plane install`. Helmfile installs
   `helm-nvca-operator` with the compute-plane environment and registration
   values for that cluster.
4. The Helm chart renders a local ConfigMap (`nvcfbackend-self-managed`) holding the cluster
   identity and the SIS, ReVal, and NATS endpoints (plus any host-header overrides). The
   operator reads that ConfigMap and creates the NVCFBackend resource.
5. The operator creates the NVCA agent pod. The agent authenticates to the control plane
   with a projected service account token (PSAT), which the control plane validates against
   the issuer and JWKS registered in step 1, and begins managing GPU workloads.
6. Runtime secrets are injected by the OpenBao vault-agent sidecar, which
   authenticates using Kubernetes service account JWT tokens against the local
   OpenBao instance. Kubernetes image pull secrets are configured separately in
   the compute-plane environment and namespaces.

## Configure the compute plane

Create the environment file from the repository root. It provides the chart
registry, image registry, image pull secret name, and control-plane service URLs
used by the NVCA operator.

```bash
cd path/to/nvcf
touch deploy/stacks/nvcf-compute-plane/environments/<environment-name>.yaml
```

Use service URLs that are reachable from the GPU cluster. For a single
load-balancer-fronted Gateway with hostname-based routing, the agent should dial
the load balancer address and send route-matching host headers:

```yaml title="deploy/stacks/nvcf-compute-plane/environments/<environment-name>.yaml"
global:
  helm:
    sources:
      registry: <your-chart-registry>
      repository: <your-chart-repository>

  image:
    registry: <your-image-registry>
    repository: <your-image-repository>

  imagePullSecrets:
    - name: nvcr-pull-secret

  nvcaOperator:
    selfManaged:
      icmsServiceURL: "http://<GATEWAY_ADDR>"
      icmsServiceHostHeaderOverride: "sis.<STACK_DOMAIN>"
      revalServiceURL: "http://<GATEWAY_ADDR>"
      revalServiceHostHeaderOverride: "reval.<STACK_DOMAIN>"
      natsURL: "nats://<GATEWAY_ADDR>:4222"
      natsHostOverride: "nats.<STACK_DOMAIN>"
```

If your GPU cluster can resolve per-service DNS names directly, set the service
URLs to those names and omit the host-header override fields.

Host-header overrides require `helm-nvca-operator` 1.12.0 or later. With proper
per-service DNS and TLS (see [gateway-routing](../gateway-routing.md)), the
service hostnames resolve directly and no host-header overrides are needed.

## Register the cluster

Register the GPU cluster with the control plane before installing the operator.
The `nvcf-cli` discovers the cluster's OIDC issuer and JWKS and records them
with the control plane, then returns the Helm values the operator needs. See
[self-hosted-cli](../cli.md) for CLI installation and configuration, and the
[Cluster Registration](../cli.md#cluster-registration) reference for full flag
and output details.

<Note>
The control plane must expose its issuer for the CLI to discover. The Helmfile
installation path enables this with
`openbao.migrations.issuerDiscovery.enabled: true`.

</Note>

Create a CLI config that can reach the control-plane gateway. The
`register-cluster` target runs `nvcf-cli init` before `cluster register`, so the
config must include the API Keys endpoint and host-header overrides:

```yaml title="nvcf-cli-gpu-register.yaml"
base_http_url: "http://<GATEWAY_ADDR>"
invoke_url: "http://<GATEWAY_ADDR>"
api_keys_service_url: "http://<GATEWAY_ADDR>"
icms_url: "http://<GATEWAY_ADDR>"

api_keys_host: "api-keys.<STACK_DOMAIN>"
api_host: "api.<STACK_DOMAIN>"
icms_host: "sis.<STACK_DOMAIN>"
invoke_host: "invocation.<STACK_DOMAIN>"

api_keys_service_id: "nvidia-cloud-functions-ncp-service-id-aketm"
api_keys_issuer_service: "nvcf-api"
api_keys_owner_id: "svc@nvcf-api.local"
client_id: "<nca-id>"
```

Run registration against the GPU cluster kubeconfig. This is required for
multi-cluster installs because the CLI discovers the OIDC issuer and JWKS from
the target Kubernetes cluster.

```bash
make -C deploy/stacks/nvcf-compute-plane register-cluster \
  CLUSTER_NAME=<gpu-cluster-name> \
  NCA_ID=<nca-id> \
  CLUSTER_REGION=<region> \
  ICMS_URL="http://<GATEWAY_ADDR>" \
  KUBECONFIG_FILE=<gpu-cluster-kubeconfig> \
  NVCF_CLI=<path-to-nvcf-cli> \
  NVCF_CLI_CONFIG=<path-to-nvcf-cli-gpu-register.yaml>
```

The target writes
`registration/<gpu-cluster-name>-register-values.yaml`. It carries the cluster
identity and endpoints:

```yaml
clusterID: <uuid>
clusterGroupID: <uuid>
ncaID: <nca-id>
region: <region>
selfManaged:
  identitySource: psat
  icmsServiceURL: "http://<GATEWAY_ADDR>"
  revalServiceURL: "http://<GATEWAY_ADDR>"
  natsURL: "nats://<GATEWAY_ADDR>:4222"
```

The `template`, `install`, and `apply` targets copy this file into `out/` before
running Helmfile.

## Installing the NVCA Operator

| Chart | `helm-nvca-operator` |
| --- | --- |
| Version | `1.12.6` |
| Namespace | `nvca-operator` |
| Depends on | All control-plane services and gateway must be running |

The compute-plane Helmfile passes these values to the chart:

| Value | Source |
| --- | --- |
| Cluster identity | `registration/<gpu-cluster-name>-register-values.yaml` |
| Operator, agent, image credential helper, and shared storage image repositories | `global.image.*` in `environments/<environment-name>.yaml` |
| Control-plane service URLs and host-header overrides | `global.nvcaOperator.selfManaged.*` |
| Image pull secrets | `global.imagePullSecrets` |

Use `global.nvcaOperator.nodeSelector`, `global.nvcaOperator.tolerations`, and
`global.nvcaOperator.agent.*` in the compute-plane environment when you need to
place the operator, agent, or workloads on specific nodes.

<Warning>
The `KAIScheduler` feature flag is optional. Enable it only if the
[KAI Scheduler](./kai-scheduler.md) is installed on the GPU cluster. The flag has no effect,
and GPU workload scheduling will not work as expected, if KAI Scheduler is not present. Omit
`KAIScheduler` from `featureGateValues` when KAI Scheduler is not installed.

</Warning>

<Note>
For the full list of available feature flags and how to set or modify them, see
[managing-feature-flags](./configuration.md).

</Note>

### Node inotify limits

The NVCA operator and agent use file watchers for ConfigMap and Secret reconciliation.
Some node images set `fs.inotify.max_user_instances` to `128`, which can be too low for
nodes running the full NVCF stack. When the node exhausts inotify instances, NVCA logs
errors such as `failed to create watcher` and `too many open files`. Function creation
or deployment requests that wait for NVCA reconciliation can then return HTTP 500 or
time out.

Before installing the operator, set higher inotify limits on every node. The following
DaemonSet sets the values on current nodes and on nodes added later:

```yaml title="inotify-tuner.yaml"
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: node-inotify-tuner
  namespace: kube-system
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: node-inotify-tuner
  template:
    metadata:
      labels:
        app.kubernetes.io/name: node-inotify-tuner
    spec:
      hostPID: true
      tolerations:
        - operator: Exists
      priorityClassName: system-node-critical
      terminationGracePeriodSeconds: 1
      initContainers:
        - name: set-sysctl
          image: busybox:1.36
          securityContext:
            privileged: true
          command:
            - sh
            - -c
            - |
              set -e
              mkdir -p /host/etc/sysctl.d
              {
                echo 'fs.inotify.max_user_instances=8192'
                echo 'fs.inotify.max_user_watches=524288'
              } > /host/etc/sysctl.d/99-inotify.conf
              nsenter -t 1 -m -u -i -n -p -- sysctl -w fs.inotify.max_user_instances=8192
              nsenter -t 1 -m -u -i -n -p -- sysctl -w fs.inotify.max_user_watches=524288
          volumeMounts:
            - name: host
              mountPath: /host
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.9
          resources:
            requests:
              cpu: "1m"
              memory: "1Mi"
            limits:
              cpu: "10m"
              memory: "16Mi"
      volumes:
        - name: host
          hostPath:
            path: /
```

Apply the DaemonSet and wait for it to run on every node:

```bash
kubectl apply -f inotify-tuner.yaml
kubectl -n kube-system rollout status ds/node-inotify-tuner --timeout=5m
```

If your cluster cannot pull public images, mirror `busybox:1.36` and
`registry.k8s.io/pause:3.9` to your registry and update the image fields before applying
the DaemonSet.

### Image Pull Secrets

The NVCA operator, NVCA agent, samba sidecar, and image-credential-helper all pull
container images from the registry configured in your compute-plane environment
file. If that registry is private, create the Kubernetes pull secret before
installing the operator.

<Warning>
Use a pre-existing pull secret through `global.imagePullSecrets`. Do not use
`generateImagePullSecret`; it does not work in self-managed mode.

</Warning>

Create the secret in the GPU cluster namespaces used by the operator and its
managed resources:

```bash
for ns in nvca-operator nvca-system nvcf-backend; do
  kubectl --kubeconfig <gpu-cluster-kubeconfig> \
    create namespace "$ns" --dry-run=client -o yaml | kubectl --kubeconfig <gpu-cluster-kubeconfig> apply -f -
  kubectl --kubeconfig <gpu-cluster-kubeconfig> \
    create secret docker-registry nvcr-pull-secret \
    --docker-server=${REGISTRY} \
    --docker-username='$oauthtoken' \
    --docker-password="$REGISTRY_PASSWORD" \
    --namespace="$ns" \
    --dry-run=client -o yaml | kubectl --kubeconfig <gpu-cluster-kubeconfig> apply -f -
done
```

Replace `${REGISTRY}` with your container registry (e.g., `nvcr.io`). For non-NGC
registries, replace `--docker-username` and `--docker-password` with your registry
credentials. For NGC (`nvcr.io`), `$REGISTRY_PASSWORD` is your NGC Personal Key or
API Key.

Reference the secret in your compute-plane environment:

```yaml
global:
  imagePullSecrets:
    - name: nvcr-pull-secret
```

Helmfile passes this value to the operator chart. The operator adds the pull
secret reference to the pods it manages. Pre-creating the secret in
`nvca-system` and `nvcf-backend` prevents startup races for operator-managed
resources that pull private images.

### Install

Install with the compute-plane Makefile. The command copies
`registration/<gpu-cluster-name>-register-values.yaml` into `out/`, then runs
Helmfile against the GPU cluster kubeconfig:

```bash
make -C deploy/stacks/nvcf-compute-plane install \
  CLUSTER_NAME=<gpu-cluster-name> \
  HELMFILE_ENV=<environment-name> \
  NCA_ID=<nca-id> \
  KUBECONFIG_FILE=<gpu-cluster-kubeconfig>
```

During installation, Helmfile will:

1. Create the operator deployment with vault agent annotations for OpenBao auth.
2. Render the `nvcfbackend-self-managed` ConfigMap from the register values (cluster
   identity, SIS/ReVal/NATS endpoints, and any host-header overrides).
3. Start the operator, which reads the ConfigMap and creates the NVCFBackend and NVCA agent
   deployment.

### Verify

Check the operator pod is running. The pod runs the operator, the `nvca-mirror` sidecar, and
the OpenBao vault-agent sidecar (and a `cluster-validator` init container when network
validation is enabled):

```bash
kubectl get pods -n nvca-operator

# Expected:
# NAME                             READY   STATUS    RESTARTS   AGE
# nvca-operator-...                Running 0          1m
```

Check the cluster identity ConfigMap was rendered from the register values:

```bash
kubectl get cm nvcfbackend-self-managed -n nvca-operator \
  -o jsonpath='{.data.cluster-dto\.yaml}'

# Expected: cluster-dto.yaml with non-empty clusterId and clusterGroupId
```

Check the NVCFBackend resource was created:

```bash
kubectl get nvcfbackends -n nvca-operator

# Expected: one NVCFBackend resource with version and health status
```

Check the NVCA agent pod is running (the operator creates this automatically):

```bash
kubectl get pods -n nvca-system

# Expected:
# NAME                    READY   STATUS    RESTARTS   AGE
# nvca-...                3/3     Running   0          2m
```

<Note>
The NVCA agent pod has 3 containers: the agent, the admission webhook, and the OpenBao vault agent sidecar.
Both should show `Running`. If the vault agent sidecar is in `CrashLoopBackOff`, verify
that OpenBao is healthy and the migration jobs completed successfully.

</Note>

Verify GPU discovery:

```bash
kubectl get nvcfbackends -n nvca-operator -o jsonpath='{.items[0].status}' | python3 -m json.tool

# Look for GPU information in the status output
```

### Optional Nsight Profiling

After NVCA is healthy, you can install NVIDIA Nsight Operator on the GPU cluster
to collect NVIDIA Nsight Systems reports from function pods. See
[Nsight Profiling](./nsight-profiling.md) for external S3 storage setup,
namespace labeling, Kyverno automation, and capture commands.

### Verify Workload Scheduling

Use this check only when worker pods on the GPU cluster can reach the
control-plane worker endpoints used by NVCF. For multi-cluster EKS installs,
first validate the GPU cluster by checking `nvca-operator` rollout and
`NVCFBackend` agent health. Function deployment and invocation are valid
acceptance checks only after the worker endpoints are reachable from the GPU
cluster.

1. Set up environment variables:

```bash
# Get the Gateway address (from Step 1)
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
echo "Gateway Address: $GATEWAY_ADDR"
```

2. Generate an admin token:

```bash
# Generate an admin API token
export NVCF_TOKEN=$(curl -s -X POST "http://${GATEWAY_ADDR}/v1/admin/keys" \
  -H "Host: api-keys.${GATEWAY_ADDR}" \
  | grep -o '"value":"[^"]*"' | cut -d'"' -f4)

echo "Token generated: ${NVCF_TOKEN:0:20}..."
```

3. Create, deploy, and invoke a test function:

```bash
# Create a test function
# Replace <YOUR_REGISTRY>/<YOUR_REPO> with your container registry
# This should match the registry you set in the secrets file
curl -s -X POST "http://${GATEWAY_ADDR}/v2/nvcf/functions" \
  -H "Host: api.${GATEWAY_ADDR}" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${NVCF_TOKEN}" \
  -d '{
    "name": "my-echo-function",
    "inferenceUrl": "/echo",
    "healthUri": "/health",
    "inferencePort": 8000,
    "containerImage": "<YOUR_REGISTRY>/<YOUR_REPO>/load_tester_supreme:0.0.8"
  }' | jq .

# Extract function and version IDs from the response
export FUNCTION_ID=<function-id-from-response>
export FUNCTION_VERSION_ID=<version-id-from-response>

# Deploy the function
# Adjust instanceType and gpu based on your cluster configuration
# Instance Type Examples: NCP.GPU.A10G_1x, NCP.GPU.H100_1x, NCP.GPU.L40S_1x, etc.
# GPU Examples: A10G, H100, L40S, etc.
curl -s -X POST "http://${GATEWAY_ADDR}/v2/nvcf/deployments/functions/${FUNCTION_ID}/versions/${FUNCTION_VERSION_ID}" \
  -H "Host: api.${GATEWAY_ADDR}" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${NVCF_TOKEN}" \
  -d '{
    "deploymentSpecifications": [
      {
        "instanceType": "NCP.GPU.A10G_1x",
        "backend": "nvcf-default",
        "gpu": "A10G",
        "maxInstances": 1,
        "minInstances": 1
      }
    ]
  }' | jq .

# Generate an API key for invocation. See the API scope reference for endpoint-specific scope requirements.
# Set expiration to 1 day from now (required field)
EXPIRES_AT=$(date -u -v+1d '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -d '+1 day' '+%Y-%m-%dT%H:%M:%SZ')
SERVICE_ID="nvidia-cloud-functions-ncp-service-id-aketm"

export API_KEY=$(curl -s -X POST "http://${GATEWAY_ADDR}/v1/keys" \
  -H "Host: api-keys.${GATEWAY_ADDR}" \
  -H "Content-Type: application/json" \
  -H "Key-Issuer-Service: nvcf-api" \
  -H "Key-Issuer-Id: ${SERVICE_ID}" \
  -H "Key-Owner-Id: test@nvcf-api.local" \
  -d '{
    "description": "test invocation key",
    "expires_at": "'"${EXPIRES_AT}"'",
    "authorizations": {
      "policies": [{
        "aud": "'"${SERVICE_ID}"'",
        "auds": ["'"${SERVICE_ID}"'"],
        "product": "nv-cloud-functions",
        "resources": [
          {"id": "*", "type": "account-functions"},
          {"id": "*", "type": "authorized-functions"}
        ],
        "scopes": ["invoke_function", "list_functions", "queue_details", "list_functions_details"]
      }]
    },
    "audience_service_ids": ["'"${SERVICE_ID}"'"]
  }' | jq -r '.value')

echo "API Key: ${API_KEY:0:20}..."

# Wait for deployment to be ready (list functions to see status), then invoke the function
# Uses wildcard subdomain routing: <function-id>.invocation.<gateway-addr>
curl -s -X POST "http://${GATEWAY_ADDR}/echo" \
  -H "Host: ${FUNCTION_ID}.invocation.${GATEWAY_ADDR}" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -d '{"message": "hello world", "repeats": 1}'
```

<Note>
The `backend` value should match the cluster group name registered by the NVCA operator.
The `instanceType` and `gpu` values depend on the GPU types available in your cluster.

For invocation, the Host header uses wildcard subdomain routing: `<function-id>.invocation.<gateway-addr>`.
The URL path should match the function's `inferenceUrl` (e.g., `/echo`).
For full HTTP invocation behavior, streaming, and errors, see
[Generic HTTP Function Invocation](../generic-http-function-invocation.md).

</Note>

You can also use the NVCF CLI for easier function management:

- Create, deploy, and invoke functions with simple commands
- Create or update registry credentials without manual API calls

See [self-hosted-cli](../cli.md) for installation and usage instructions.

## Re-registering a cluster

Registration is performed by the CLI, not by the operator. To re-register (for
example, after a failed install, or to refresh the recorded issuer and JWKS),
re-run the compute-plane registration target and then re-apply the operator with
the refreshed values:

```bash
make -C deploy/stacks/nvcf-compute-plane register-cluster \
  CLUSTER_NAME=<gpu-cluster-name> \
  NCA_ID=<nca-id> \
  CLUSTER_REGION=<region> \
  ICMS_URL="http://<GATEWAY_ADDR>" \
  KUBECONFIG_FILE=<gpu-cluster-kubeconfig> \
  NVCF_CLI=<path-to-nvcf-cli> \
  NVCF_CLI_CONFIG=<path-to-nvcf-cli-gpu-register.yaml>

make -C deploy/stacks/nvcf-compute-plane install \
  CLUSTER_NAME=<gpu-cluster-name> \
  HELMFILE_ENV=<environment-name> \
  NCA_ID=<nca-id> \
  KUBECONFIG_FILE=<gpu-cluster-kubeconfig>
```

The registration target rewrites
`registration/<gpu-cluster-name>-register-values.yaml`. The install target
copies that file into `out/` before running Helmfile. The operator reads the
cluster identity at startup, so re-running the install restarts it with the new
values.

## Uninstalling

To fully remove the NVCA Operator and all associated resources:

<Info>
If functions are currently deployed on the cluster (pods in the `nvcf-backend` namespace),
undeploy them through the NVCF API or CLI before uninstalling the operator. Attempting
to delete NVCA while function pods are running can cause finalizers to block namespace
deletion. If you encounter stuck resources, see [Handling Stuck Resources] below.

</Info>

1. Delete the NVCFBackend resource. This triggers operator-managed cleanup of
   the agent deployment, NVCA system pods, and related resources:

   ```bash
   kubectl --kubeconfig <gpu-cluster-kubeconfig> \
     delete nvcfbackends --all -n nvca-operator --timeout=60s
   ```

2. Verify the agent namespace is clean before proceeding:

   ```bash
   kubectl --kubeconfig <gpu-cluster-kubeconfig> get pods -n nvca-system

   # Expected: "No resources found in nvca-system namespace."
   ```

3. Destroy the compute-plane Helmfile release:

   ```bash
   make destroy \
     CLUSTER_NAME=<gpu-cluster-name> \
     HELMFILE_ENV=<environment-name> \
     NCA_ID=<nca-id> \
     KUBECONFIG_FILE=<gpu-cluster-kubeconfig>
   ```

   <Note>
   The cluster identity (`clusterID` and `clusterGroupID`) persists in the control plane
   (ICMS) and in your registration values file. A reinstall reuses it by
   re-applying that file. Pass `--ignore-existing` to `cluster register` if you re-register.

   </Note>

4. Delete CRDs. This removes all NVCFBackend, MiniService, and StorageRequest
   custom resources cluster-wide:

   ```bash
   kubectl --kubeconfig <gpu-cluster-kubeconfig> delete crd \
     nvcfbackends.nvcf.nvidia.io \
     miniservices.nvca.nvcf.nvidia.io \
     storagerequests.nvca.nvcf.nvidia.io \
     --ignore-not-found
   ```

5. Delete namespaces:

   ```bash
   kubectl --kubeconfig <gpu-cluster-kubeconfig> delete namespace \
     nvca-operator nvca-system nvcf-backend nvca-modelcache-init \
     --ignore-not-found
   ```

### Handling Stuck Resources

If step 1 times out and namespaces remain stuck in `Terminating` state, or function pods in
`nvcf-backend` prevent cleanup, use the [force-cleanup-script](../troubleshooting.md). This script removes
finalizers on stuck NVCA resources, force-deletes function pods, and cleans up all NVCA
namespaces.

```bash
# Preview what will be deleted
./force-cleanup-nvcf.sh --dry-run

# Execute the cleanup
./force-cleanup-nvcf.sh
```

<Warning>
The force cleanup script bypasses normal cleanup procedures by removing finalizers. Always
attempt the ordered uninstall steps above first.

</Warning>

For the full script, download link, and detailed usage instructions, see the NVCA Force
Cleanup Script appendix in the self-hosted troubleshooting guide.

## Troubleshooting

- Cluster IDs empty in the ConfigMap after install: The registration values were
  not applied. Confirm
  `registration/<gpu-cluster-name>-register-values.yaml` exists and that its
  `clusterID` and `clusterGroupID` are populated, then re-run `make install`:

  ```bash
  kubectl get cm nvcfbackend-self-managed -n nvca-operator \
    -o jsonpath='{.data.cluster-dto\.yaml}'
  ```

- Operator pod not starting: Check the operator logs:

  ```bash
  kubectl logs -n nvca-operator -l app.kubernetes.io/name=nvca-operator -c nvca-operator --tail=100
  ```

- Operator or agent logs show `failed to create watcher` with `too many open files`:
  Increase the node inotify limits with the `node-inotify-tuner` DaemonSet in
  [Node inotify limits](#node-inotify-limits), then restart the affected pod.

- NVCA agent pod not created: The operator creates the agent pod via the NVCFBackend
  resource. Check the operator logs for reconciliation errors:

  ```bash
  kubectl describe nvcfbackends -n nvca-operator
  ```

- Agent fails to register with SIS (HTTP 401): The control plane could not
  validate the agent's PSAT against the recorded issuer and JWKS. Re-run
  `make register-cluster` for the cluster (see
  [Re-registering a cluster](#re-registering-a-cluster)) so ICMS has the
  current issuer and JWKS. Also verify the vault agent sidecar on the agent pod
  is running and rendering the secrets file:

  ```bash
  kubectl logs -n nvca-system -l app.kubernetes.io/name=nvca -c vault-agent --tail=10
  ```

- Vault agent sidecar failing: The agent pod needs to authenticate with OpenBao. Verify
  the vault system is healthy:

  ```bash
  kubectl exec -n vault-system openbao-server-0 -- bao status
  ```

- No GPUs discovered: Ensure the GPU Operator is installed and GPU nodes have the
  `nvidia.com/gpu` resource advertised:

  ```bash
  kubectl get nodes -o custom-columns="NAME:.metadata.name,GPU:.status.allocatable.nvidia\.com/gpu"
  ```
