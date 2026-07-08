# Self-Managed Clusters

GPU clusters are registered with the NVCF control plane using the NVCA Operator. The operator
creates its configuration locally via Kubernetes ConfigMaps and authenticates through the
local OpenBao (Vault) instance.

<Info>
A running NVCF control plane (SIS, OpenBao, NATS, Cassandra, and all core services) is
required. Install the control plane using either the standalone Helm chart installation or
the Helmfile-based installation before proceeding.

</Info>

## Prerequisites

Before installing the NVCA Operator, ensure the following prerequisites are met:

- The [control plane](../helmfile-installation.md) is installed and all core services are running.

- The [NVIDIA GPU Operator](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/getting-started.html) is installed on the GPU cluster. The GPU Operator manages the NVIDIA drivers, device plugin, and GPU feature discovery required for workload scheduling. For development or testing environments without physical GPUs, see [fake-gpu-operator](../fake-gpu-operator.md).

- The [KAI Scheduler](./kai-scheduler.md) is installed on the GPU cluster. KAI Scheduler is required for optimized AI workload scheduling and bin-packing of GPU resources.

- `GPU Workload Components` must be available in a user-managed registry that your Kubernetes cluster can access. See `GPU Workload Components` under [self-hosted-artifact-manifest](../manifest.md) for necessary artifacts and [self-hosted-image-mirroring](../image-mirroring.md) for mirroring instructions.

- The [SMB CSI driver](https://github.com/kubernetes-csi/csi-driver-smb) (`smb.csi.k8s.io`) must be installed on the GPU cluster. It is required for NVCA shared model cache storage (samba sidecar). Install it with:

  ```bash
  helm repo add csi-driver-smb \
    https://raw.githubusercontent.com/kubernetes-csi/csi-driver-smb/master/charts

  helm install csi-driver-smb csi-driver-smb/csi-driver-smb \
    -n kube-system --version v1.17.0
  ```

## How It Works

When `ngcConfig.clusterSource` is set to `self-managed`, the NVCA Operator uses a local
bootstrap process:

1. The Helm chart creates a **local ConfigMap** (`nvcfbackend-self-managed`) with cluster
   configuration from your values file (name, region, capabilities, SIS endpoint).
2. The `ngcConfig.serviceKey` field is required by the chart schema but **not used**. Set it
   to any non-empty string.
3. An **init container** (`nvca-self-managed-bootstrap`) runs before the operator starts.
   It reads the vault-injected SIS JWT token and registers the cluster with the local SIS
   service, writing the resulting `clusterId` and `clusterGroupId` to the
   `nvca-cluster-registration` ConfigMap.
4. The **operator** starts with the cluster already registered. It reads the IDs from the
   ConfigMap and creates the NVCFBackend resource.
5. The **NVCA agent** pod is created by the operator. It authenticates with SIS using a
   vault-injected token and begins managing GPU workloads.
6. Secrets (Cassandra credentials, registry pull secrets) are injected by the **OpenBao vault
   agent** sidecar, which authenticates using Kubernetes service account JWT tokens against
   the local OpenBao instance.

## Installing the NVCA Operator

| **Chart** | `helm-nvca-operator` |
| --- | --- |
| **Version** | `1.6.6` |
| **Namespace** | `nvca-operator` |
| **Depends on** | All control plane services and gateway must be running |

### Configuration

Create `nvca-operator-values.yaml` ([download template](samples/nvca-operator-values.yaml)):

<Accordion title="nvca-operator-values.yaml">
</Accordion>
```yaml title="nvca-operator-values.yaml"
# NVCA Operator values for standalone self-managed installation
# Replace <REGISTRY> and <REPOSITORY> with your container registry settings.

# Operator image
image:
  repository: "<REGISTRY>/<REPOSITORY>/nvca-operator"
  tag: "2.52.1"

# NVCA agent image
nvcaImage:
  repositoryOverride: "<REGISTRY>/<REPOSITORY>/nvca"
  
generateImagePullSecret: false
# Image pull secret for private registries. Create the secret in the nvca-operator
# namespace, then uncomment the lines below. The chart passes this to both the
# operator and all NVCA agent pods (including samba and image-credential-helper).
# imagePullSecrets:
#   - name: nvcr-pull-secret

# NGC configuration -- self-managed mode does not use NGC cloud services.
# The serviceKey is not used but the field is required by the chart.
ngcConfig:
  clusterSource: self-managed
  serviceKey: "not-used"

# Self-managed backend configuration
selfManaged:
  nvcaVersion: "2.52.1" # NVCA agent version to deploy
  featureGateValues: ["DynamicGPUDiscovery", "SelfHosted", "KAIScheduler"]
  imageCredHelper:
    imageRepository: "<REGISTRY>/<REPOSITORY>/nvcf-image-credential-helper"
  sharedStorage:
    imageRepository: "<REGISTRY>/<REPOSITORY>/samba"
# Uncomment for node selectors
# nodeSelector:
#   key: nvcf.nvidia.com/workload
#   value: control-plane
```

Replace all `<REGISTRY>` and `<REPOSITORY>` placeholders with your registry settings.

Key values:

| `ngcConfig.clusterSource` | Must be `self-managed` for self-hosted deployments |
| --- | --- |
| `ngcConfig.serviceKey` | Required by the chart but not used. Set to any non-empty string. |
| `selfManaged.nvcaVersion` | The NVCA agent container version to deploy |
| `selfManaged.featureGateValues` | Feature flags. `DynamicGPUDiscovery` enables automatic GPU detection. `SelfHosted` enables local vault-based auth. |
| `selfManaged.imageCredHelper` | Image credential helper sidecar (enables function pods to pull from private registries) |
| `selfManaged.sharedStorage` | Samba sidecar for shared model cache storage |

If you are using node selectors, uncomment the `nodeSelector` section.

<Note>
For the full list of available feature flags and how to set or modify them, see
[managing-feature-flags](./configuration.md).

</Note>

### Image Pull Secrets

The NVCA operator, NVCA agent, samba sidecar, and image-credential-helper all pull
container images from the registry configured in your values file. If that registry is
private, you must create a Kubernetes pull secret and reference it in the Helm values
so that all pods can authenticate.

<Note>
The chart provides a `generateImagePullSecret` option, but this does not work in
self-managed mode. It generates a pull secret from `ngcConfig.serviceKey`, which is
set to a dummy value in self-managed deployments. Use `imagePullSecrets` with a
pre-existing secret instead.

</Note>

**1. Create the pull secret** in the `nvca-operator` and `nvcf-backend` namespaces:

```bash
for ns in nvca-operator nvcf-backend; do
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
  kubectl create secret docker-registry nvcr-pull-secret \
    --docker-server=<REGISTRY> \
    --docker-username='$oauthtoken' \
    --docker-password="$REGISTRY_PASSWORD" \
    --namespace="$ns" \
    --dry-run=client -o yaml | kubectl apply -f -
done
```

Replace `<REGISTRY>` with your container registry (e.g., `nvcr.io`). For non-NGC
registries, replace `--docker-username` and `--docker-password` with your registry
credentials. For NGC (`nvcr.io`), `$REGISTRY_PASSWORD` is your NGC Personal Key or
API Key.

The secret is needed in both namespaces because the operator and agent pods run in
`nvca-operator`, while deployed function pods (which include the samba and
image-credential-helper sidecars) run in `nvcf-backend`.

**2. Reference the secret in your values file.** Add the following to
`nvca-operator-values.yaml`:

```yaml
imagePullSecrets:
  - name: nvcr-pull-secret
```

The chart passes this secret to both the operator deployment and all NVCA agent pods it
creates. The operator also propagates it to function pods in the `nvcf-backend`
namespace.

### Install

```bash
helm upgrade --install nvca-operator \
  oci://${REGISTRY}/${REPOSITORY}/helm-nvca-operator \
  --namespace nvca-operator --create-namespace \
  --wait --timeout 10m \
  -f nvca-operator-values.yaml \
  --version 1.6.6
```

During installation, the chart will:

1. Create the operator deployment with vault agent annotations for OpenBao auth
2. Run the `nvca-self-managed-bootstrap` init container to register the cluster with SIS
3. Start the operator, which creates the NVCFBackend and NVCA agent deployment

### Verify

Check the operator pod is running (4 containers: operator, nvca-mirror, nvca-bootstrap-watch, vault-agent):

```bash
kubectl get pods -n nvca-operator

# Expected:
# NAME                             READY   STATUS    RESTARTS   AGE
# nvca-operator-...                4/4     Running   0          1m
```

Check the bootstrap init container completed successfully:

```bash
kubectl logs -n nvca-operator -c nvca-self-managed-bootstrap \
  -l app.kubernetes.io/name=nvca-operator --tail=5

# Expected: "Bootstrap completed successfully" with cluster_id and cluster_group_id
```

Check the cluster registration ConfigMap has IDs populated:

```bash
kubectl get cm nvca-cluster-registration -n nvca-operator \
  -o jsonpath='{.data}'

# Expected: {"clusterGroupId":"<uuid>","clusterId":"<uuid>"}
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

### Verify Workload Scheduling

**1. Set up environment variables:**

```bash
# Get the Gateway address (from Step 1)
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
echo "Gateway Address: $GATEWAY_ADDR"
```

**2. Generate an admin token:**

```bash
# Generate an admin API token
export NVCF_TOKEN=$(curl -s -X POST "http://${GATEWAY_ADDR}/v1/admin/keys" \
  -H "Host: api-keys.${GATEWAY_ADDR}" \
  | grep -o '"value":"[^"]*"' | cut -d'"' -f4)

echo "Token generated: ${NVCF_TOKEN:0:20}..."
```

**3. Create, deploy, and invoke a test function:**

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

# Generate an API key for invocation (note the required scopes, the NVCF API Open API Spec under "API" page has all scopes documented per endpoint)
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

</Note>

You can also use the **NVCF CLI** for easier function management:

- **Create, deploy, and invoke functions** with simple commands
- **Create or update registry credentials** without manual API calls

See [self-hosted-cli](../cli.md) for installation and usage instructions.

## Manual Cluster Registration

The cluster bootstrap runs automatically as an init container during `helm install`. If
you need to re-run registration (for example, after a failed install or to update the
cluster registration), you can invoke the bootstrap CLI directly from the running operator
pod:

```bash
kubectl exec -n nvca-operator deploy/nvca-operator -c nvca-operator -- \
  /usr/bin/nvca-self-managed bootstrap --system-namespace nvca-operator
```

This command:

- Reads the cluster config from the `nvcfbackend-self-managed` ConfigMap
- Authenticates with SIS using the vault-injected JWT token
- Registers the cluster (or re-discovers an existing registration)
- Updates the `nvca-cluster-registration` ConfigMap with the cluster IDs

After manual registration, **restart the operator** so it re-reads the updated ConfigMap.
The operator caches the cluster IDs at startup and does not watch the bootstrap ConfigMap
for changes, so a restart is required:

```bash
kubectl rollout restart deployment nvca-operator -n nvca-operator
```

Wait for the operator to come back up (the bootstrap init container will run again and
confirm the existing registration), then verify the agent starts successfully:

```bash
kubectl rollout status deployment nvca-operator -n nvca-operator --timeout=120s
kubectl get pods -n nvca-system
```

<Warning>
Simply annotating the NVCFBackend to force a rollout is **not sufficient** after manual
registration. The operator must be restarted to pick up the new cluster IDs from the
ConfigMap.

</Warning>

## Uninstalling

To fully remove the NVCA Operator and all associated resources:

<Info>
If functions are currently deployed on the cluster (pods in the `nvcf-backend` namespace),
undeploy them through the NVCF API or CLI **before** uninstalling the operator. Attempting
to delete NVCA while function pods are running can cause finalizers to block namespace
deletion. If you encounter stuck resources, see [Handling Stuck Resources] below.

</Info>

1. **Delete the NVCFBackend resource** (triggers operator-managed cleanup of the agent
   deployment, NVCA system pods, and related resources):

   ```bash
   kubectl delete nvcfbackends --all -n nvca-operator --timeout=60s
   ```

2. **Verify the agent namespace is clean** before proceeding:

   ```bash
   kubectl get pods -n nvca-system

   # Expected: "No resources found in nvca-system namespace."
   ```

3. **Uninstall the Helm release**:

   ```bash
   helm uninstall nvca-operator -n nvca-operator
   ```

   <Note>
   Helm will report that the `nvca-cluster-registration` ConfigMap was
   kept due to resource policy. This is intentional — it preserves the cluster registration
   IDs so that a reinstall can reuse them. Delete it manually if you want a completely
   clean removal.
   </Note>

4. **Clean up the retained ConfigMap** (optional — skip if you plan to reinstall):

   ```bash
   kubectl delete cm nvca-cluster-registration -n nvca-operator --ignore-not-found
   ```

5. **Delete CRDs** (removes all NVCFBackend, MiniService, and StorageRequest custom resources
   cluster-wide):

   ```bash
   kubectl delete crd \
     nvcfbackends.nvcf.nvidia.io \
     miniservices.nvca.nvcf.nvidia.io \
     storagerequests.nvca.nvcf.nvidia.io \
     --ignore-not-found
   ```

6. **Delete namespaces**:

   ```bash
   kubectl delete namespace nvca-operator nvca-system nvca-modelcache-init --ignore-not-found
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

- **Bootstrap init container failed**: Check the bootstrap logs to see why registration
  failed:

  ```bash
  kubectl logs -n nvca-operator -c nvca-self-managed-bootstrap \
    -l app.kubernetes.io/name=nvca-operator
  ```

- **ConfigMap shows empty cluster IDs after install**: The vault token may not have been
  available when the init container ran. Run the bootstrap manually (see
  [Manual Cluster Registration] above).

- **Operator pod not starting**: Check the operator logs:

  ```bash
  kubectl logs -n nvca-operator -l app.kubernetes.io/name=nvca-operator -c nvca-operator --tail=100
  ```

- **NVCA agent pod not created**: The operator creates the agent pod via the NVCFBackend
  resource. Check the operator logs for reconciliation errors:

  ```bash
  kubectl describe nvcfbackends -n nvca-operator
  ```

- **Agent fails to register with SIS (HTTP 401)**: Check the bootstrap registration
  ConfigMap for populated cluster IDs. If they are empty, run the bootstrap manually.
  Also verify the vault agent sidecar on the agent pod is running and rendering the
  secrets file:

  ```bash
  kubectl logs -n nvca-system -l app.kubernetes.io/name=nvca -c vault-agent --tail=10
  ```

- **Vault agent sidecar failing**: The agent pod needs to authenticate with OpenBao. Verify
  the vault system is healthy:

  ```bash
  kubectl exec -n vault-system openbao-server-0 -- bao status
  ```

- **No GPUs discovered**: Ensure the GPU Operator is installed and GPU nodes have the
  `nvidia.com/gpu` resource advertised:

  ```bash
  kubectl get nodes -o custom-columns="NAME:.metadata.name,GPU:.status.allocatable.nvidia\.com/gpu"
  ```
