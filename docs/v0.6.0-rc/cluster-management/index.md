# GPU Cluster Setup

<Warning>
Customers on NVCA versions older than 2.51.0 must upgrade to 2.51.0 before upgrading to 3.x to avoid workload and GPU accounting issues. See [Upgrade Advisory](#upgrade-advisory) below for the recommended upgrade path.

</Warning>

The **NVIDIA Cluster Agent (NVCA)** connects GPU clusters to the NVCF control plane, enabling them to act as deployment targets for Cloud Functions. NVCA is a function deployment orchestrator that registers a cluster's GPU resources, communicates with the control plane, and manages the lifecycle of function deployments on GPU nodes.

For a fresh install, use the [Quickstart](../quickstart.md). The one-click CLI flow can register a GPU cluster as part of the install. Use this section for manual cluster registration, standalone NVCA installation, and day-two cluster configuration.

After installing NVCA on a cluster:

- The registered cluster will show as a deployment option in the `GET /v2/nvcf/clusterGroups` API response.
- Any functions under the cluster's authorized NCA IDs can now deploy on the cluster.

## Authentication and Keys

| Key Type | Description |
| --- | --- |
| NVCF API Key (NAK) | Used by NVCA to authenticate with the control plane. See [self-hosted-api](../api.md) for details on API key generation. |

## Prerequisites

- Access to a Kubernetes cluster including GPU-enabled nodes ("GPU cluster")

  - The cluster must have a compatible version of [Kubernetes](https://kubernetes.io/releases/).

  - The cluster must have the [NVIDIA GPU Operator](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/getting-started.html#operator-install-guide) installed.

    - If your cloud provider *does not* support the NVIDIA GPU Operator, [Manual Instance Configuration](./configuration.md) is possible, but **not recommended** due to lack of maintainability.
    - To get the most out of clusters with multi-node NVLink ([MNNVL](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/dra-cds.html#dra-docs-compute-domains)) GPUs like [GB200](https://www.nvidia.com/en-us/data-center/gb200-nvl72/), the [NVIDIA GPU DRA driver](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/dra-intro-install.html) must be installed. See the [nvlink-optimized-clusters](./configuration.md) for details.
    - For development or testing environments **without physical GPUs**, install the [fake-gpu-operator](../fake-gpu-operator.md) instead.

- Registering the cluster requires `kubectl` and `helm` installed.

- The user registering the cluster must have the `cluster-admin` role privileges to install the NVIDIA Cluster Agent Operator (`nvca-operator`).

### Supported Kubernetes Versions

- Any official supported Kubernetes version

## Upgrade Advisory

<Warning>
Customers on NVCA versions older than 2.51.0 must upgrade to 2.51.0 before upgrading to 3.x.

NVCA 2.51.0 includes critical fixes for workload lifecycle management and GPU capacity accounting.
Skipping this version may result in:

- Incorrect GPU resource accounting on the cluster
- Workload pods not being properly tracked or cleaned up
- Cluster capacity reporting mismatches with the control plane

Recommended upgrade path:

| Current Version | Action |
| --- | --- |
| 2.50.5 or older | Upgrade to **2.51.0** first, verify cluster health, then upgrade to 3.0+ |
| 2.51.0 | Upgrade directly to 3.0+ |

To check your current NVCA version:

```bash
kubectl get nvcfbackend -n nvca-operator
```

The `VERSION` column shows the currently deployed NVCA version.

To upgrade the NVCA operator:

```bash
helm upgrade nvca-operator -n nvca-operator \
  --reuse-values \
  --wait \
  "${CHART_URL}" \
  --username='$oauthtoken' \
  --password="$(helm get values -n nvca-operator nvca-operator -o json | jq -r '.ngcConfig.serviceKey')" \
  --set helmManaged.nvcaVersion="2.51.0"
```

After upgrading to 2.51.0, verify the cluster is healthy before proceeding to 3.x:

```bash
kubectl get nvcfbackend -n nvca-operator
# Verify VERSION shows 2.51.0 and HEALTH shows healthy
```

</Warning>

### Considerations

- The NVIDIA Cluster Agent currently only supports caching if the cluster is enabled with `StorageClass` configurations. If the "Caching Support" capability is enabled, the agent will make the best effort by attempting to detect storage during deployments and fall back on non-cached workflows.
- Each function and task requires several infrastructure containers be deployed alongside workload containers. These infrastructure containers collectively need 6 CPU cores and 8 Gi of system memory to execute. Each GPU node must have at least this many resources, ideally significantly more for workload resource usage.
