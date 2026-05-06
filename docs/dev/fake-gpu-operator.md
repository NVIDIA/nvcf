# Fake GPU Operator (Development / Testing)

For development, staging, load testing, or CI environments that lack physical NVIDIA GPUs,
you can install a fake GPU operator to simulate GPU resources on cluster nodes. This allows
the NVCA agent to discover GPUs and manage function deployments without actual GPU hardware.

<Info>
The fake GPU operator is for **non-production use only**. For production deployments with
real GPUs, install the
[NVIDIA GPU Operator](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/).

</Info>

## Prerequisites

- A running Kubernetes cluster with `kubectl` access
- `helm` >= 3.12

### Install KWOK

The fake GPU operator depends on [KWOK (Kubernetes Without Kubelet)](https://kwok.sigs.k8s.io/)
to manage simulated GPU device plugins on nodes. Install KWOK before the fake GPU operator:

```bash
kubectl apply -f https://github.com/kubernetes-sigs/kwok/releases/download/v0.7.0/kwok.yaml
```

Verify the KWOK controller is running:

```bash
kubectl get pods -n kube-system -l app=kwok-controller
# Expected: kwok-controller-...   1/1   Running
```

<Note>
The KWOK install may produce a FlowSchema error
(`creation or update of FlowSchema object ... is not allowed`).
This is non-critical and can be safely ignored.

</Note>

## Installation

Add the RunAI helm repository and install the fake GPU operator:

```bash
helm repo add fake-gpu-operator \
  https://runai.jfrog.io/artifactory/api/helm/fake-gpu-operator-charts-prod \
  --force-update

helm upgrade -i gpu-operator fake-gpu-operator/fake-gpu-operator \
  -n gpu-operator --create-namespace \
  --set 'topology.nodePools.default.gpuCount=8' \
  --set 'topology.nodePools.default.gpuProduct=NVIDIA-H100-80GB-HBM3'
```

This configures one node pool named `default` with 8 simulated H100 GPUs per node.

<Warning>
**topology.nodePools must be a map, not an array.**

Using array index syntax (`--set 'topology.nodePools[0].gpuCount=8'`) will create a
YAML array instead of a map and cause the status-updater to fail with:

```
yaml: unmarshal errors: cannot unmarshal !!seq into map[string]topology.NodePoolTopology
```

Always use named keys: `topology.nodePools.default.gpuCount=8`.

</Warning>

## Node Labeling

The fake GPU operator watches for nodes with the label
`run.ai/simulated-gpu-node-pool=<pool-name>` and patches their status to advertise
fake `nvidia.com/gpu` extended resources. You must label the nodes that should receive
simulated GPUs:

```bash
kubectl label node <node-name> run.ai/simulated-gpu-node-pool=default
```

The pool name (`default`) must match a key in `topology.nodePools` from the helm install.

### GPU Metadata Labels (Optional)

The NVCA agent uses several GPU metadata labels for dynamic discovery. On real GPU nodes
these are set by the NVIDIA GPU Operator. To suppress warnings from NVCA on fake GPU nodes,
add the following labels:

```bash
kubectl label node <node-name> \
  nvidia.com/gpu.family=hopper \
  nvidia.com/gpu.machine=NVIDIA-DGX-H100 \
  nvidia.com/cuda.driver.major=535 \
  --overwrite
```

Adjust the values to match the GPU product you configured (e.g., `ampere` for A100,
`ada` for L40S).

## RuntimeClass Ownership Conflict

The fake GPU operator chart creates `RuntimeClass/nvidia` and several namespaced resources in `gpu-operator`. Helm fails with `invalid ownership metadata` if one of those objects already exists and is not owned by release `gpu-operator` in namespace `gpu-operator`.

For local k3d development, use the recovery workflow in `examples/self-hosted-local-development/README.md`. For manual chart debugging, inspect ownership before deleting anything. If another Helm release owns the resource, remove that release instead of deleting the resource directly.

## Verification

Check that the fake GPU operator pods are running:

```bash
kubectl get pods -n gpu-operator
# Expected: 3 pods Running (topology-server, status-updater, kwok-gpu-device-plugin)
```

Confirm that labeled nodes now advertise GPU resources:

```bash
kubectl get nodes -o custom-columns="NAME:.metadata.name,GPU:.status.allocatable.nvidia\.com/gpu"
# Labeled nodes should show the configured GPU count (e.g., 8)
```

If GPUs do not appear, verify the node has the `run.ai/simulated-gpu-node-pool=default`
label and that the status-updater pod is not in an error state.

## Integration with NVCF

### Recommended Installation Order

For the smoothest experience, install the fake GPU operator **before** running
`helmfile sync`. This way the NVCA agent discovers GPUs on its first boot and no
re-registration is needed.

The recommended sequence is:

1. Install KWOK
2. Install fake-gpu-operator and label target nodes
3. Verify `nvidia.com/gpu` appears in node allocatable resources
4. Proceed with the [control-plane installation](./helmfile-installation)

### If Installed After the Control Plane

If you add the fake GPU operator to a cluster that already has NVCF deployed, the NVCA
agent will be crash-looping because it cannot find GPUs. After installing the fake GPU
operator and verifying GPUs appear on nodes, re-register the cluster and restart the
operator:

```bash
# Re-run the cluster bootstrap
kubectl exec -n nvca-operator deploy/nvca-operator -c nvca-operator -- \
  /usr/bin/nvca-self-managed bootstrap --system-namespace nvca-operator

# Restart the operator (it caches cluster IDs at startup)
kubectl rollout restart deployment nvca-operator -n nvca-operator
kubectl rollout status deployment nvca-operator -n nvca-operator --timeout=120s
```

The operator restart will re-run the bootstrap init container, recreate the NVCFBackend
resource, and spawn a fresh NVCA agent pod that discovers the simulated GPUs.

For details on the bootstrap process, see [Self-Managed Clusters](cluster-management/self-managed) (Manual Cluster
Registration).

## Customization

### GPU Count and Product

Adjust the GPU count, product name, and memory per node pool:

```bash
helm upgrade gpu-operator fake-gpu-operator/fake-gpu-operator \
  -n gpu-operator \
  --set 'topology.nodePools.default.gpuCount=4' \
  --set 'topology.nodePools.default.gpuProduct=NVIDIA-A100-SXM4-80GB' \
  --set 'topology.nodePools.default.gpuMemory=81920'
```

### Multiple Node Pools

Define multiple pools with different GPU configurations by using different map keys:

```bash
helm upgrade gpu-operator fake-gpu-operator/fake-gpu-operator \
  -n gpu-operator \
  --set 'topology.nodePools.h100-pool.gpuCount=8' \
  --set 'topology.nodePools.h100-pool.gpuProduct=NVIDIA-H100-80GB-HBM3' \
  --set 'topology.nodePools.a100-pool.gpuCount=4' \
  --set 'topology.nodePools.a100-pool.gpuProduct=NVIDIA-A100-SXM4-80GB'
```

Then label nodes with the corresponding pool name:

```bash
kubectl label node <h100-node> run.ai/simulated-gpu-node-pool=h100-pool
kubectl label node <a100-node> run.ai/simulated-gpu-node-pool=a100-pool
```

## Teardown

To remove the fake GPU operator and all simulated GPU resources:

```bash
# Remove the fake GPU operator
helm uninstall gpu-operator -n gpu-operator
kubectl delete namespace gpu-operator --ignore-not-found

# Remove KWOK
kubectl delete -f https://github.com/kubernetes-sigs/kwok/releases/download/v0.7.0/kwok.yaml

# Remove the node labels (for each labeled node)
kubectl label node <node-name> run.ai/simulated-gpu-node-pool-
kubectl label node <node-name> nvidia.com/gpu.product-
kubectl label node <node-name> nvidia.com/gpu.family-
kubectl label node <node-name> nvidia.com/gpu.machine-
kubectl label node <node-name> nvidia.com/cuda.driver.major-
```

After removing the fake GPU operator, the NVCA agent will lose GPU visibility and begin
crash-looping. Either install a real GPU Operator with physical GPUs or uninstall the
NVCA operator.
