# GPU Cluster Setup

The NVIDIA Cluster Agent (NVCA) connects GPU clusters to the NVCF control plane, enabling them to act as deployment targets for Cloud Functions. NVCA is a function deployment orchestrator that registers a cluster's GPU resources, communicates with the control plane, and manages the lifecycle of function deployments on GPU nodes.

For a fresh install, use the [Quickstart](../quickstart.md). The one-click CLI flow can register a GPU cluster as part of the install. Use this section for manual cluster registration, standalone NVCA installation, and day-two cluster configuration.

<Warning>
If you pin NVCA separately from the recommended compute-plane stack, check the current NVCA version before upgrading to NVCA 3.x. Clusters running NVCA 2.51.0 or earlier have version-specific upgrade guidance. See the [0.6.0 upgrade notes](../release-notes/0.6.0.md#upgrade-notes).
</Warning>

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

    - If your cloud provider does not support the NVIDIA GPU Operator, [Manual Instance Configuration](./configuration.md) is possible, but not recommended due to lack of maintainability.
    - To get the most out of clusters with multi-node NVLink ([MNNVL](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/dra-cds.html#dra-docs-compute-domains)) GPUs like [GB200](https://www.nvidia.com/en-us/data-center/gb200-nvl72/), the [NVIDIA GPU DRA driver](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/dra-intro-install.html) must be installed. See the [nvlink-optimized-clusters](./configuration.md) for details.
    - For development or testing environments without physical GPUs, install the [fake-gpu-operator](../fake-gpu-operator) instead.

- Registering the cluster requires `kubectl` and `helm` installed.

- The user registering the cluster must have the `cluster-admin` role privileges to install the NVIDIA Cluster Agent Operator (`nvca-operator`).

### Supported Kubernetes Versions

- Supported versions are the latest Kubernetes minor release and the two prior minor releases (N-2). See official Kubernetes docs for current supported [versions](https://kubernetes.io/releases/version-skew-policy/#supported-versions).

### Considerations

- The NVIDIA Cluster Agent currently only supports caching if the cluster is enabled with `StorageClass` configurations. If the "Caching Support" capability is enabled, the agent will make the best effort by attempting to detect storage during deployments and fall back on non-cached workflows.
- Each function and task requires several infrastructure containers be deployed alongside workload containers. These infrastructure containers collectively need 6 CPU cores and 8 Gi of system memory to execute. Each GPU node must have at least this many resources, ideally significantly more for workload resource usage.
