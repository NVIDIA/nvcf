# Multi-Tenancy

This page describes the supported multi-tenant invocation models and the isolation guarantees available at each layer in self-managed NVCF deployments.

<Warning>
NVCF does **not** provide hardware-level GPU isolation. Multiple workloads from different tenants may share the same physical GPU unless explicit node-level isolation is configured. See [Isolation Options by Layer](#isolation-options-by-layer) below for available controls.
</Warning>

## Supported Invocation Models

NVCF supports two forms of multi-tenant invocation.

### Multi-Tenant Invocation with Dedicated Endpoint

A function is deployed with a private or access-controlled endpoint. Each invoking tenant receives backend capacity that is logically or physically isolated from other tenants.

**Characteristics:**

- Helm-deployed functions run in a dedicated Kubernetes namespace, isolated from other functions at the namespace boundary. Custom container functions run in the shared `nvcf-backend` namespace and do not receive namespace-level isolation.
- When [Account Isolation](./configuration.md#account-isolated-clusters) is enabled on the cluster, workload pods are scheduled on nodes exclusively reserved for that NCA account — no other account's pods share those nodes.
- Network policies restrict pod-to-pod communication across namespaces (subject to the cluster's CNI supporting Kubernetes NetworkPolicy).

**Best for:** Tenants with strict data separation requirements who can accept higher resource cost in exchange for stronger isolation.

### Multi-Tenant Invocation with Shared Endpoint

A function is deployed with a publicly accessible or multi-tenant endpoint. Multiple tenants invoke the same function endpoint and backend instances may be co-located on the same nodes.

**Characteristics:**

- Helm-deployed functions run in separate Kubernetes namespaces. Custom container functions run in the shared `nvcf-backend` namespace with no namespace-level isolation.
- Node-level isolation is not guaranteed by default; workloads from different tenants may be bin-packed onto the same node.
- Network policies still restrict cross-namespace egress (subject to CNI support).

**Best for:** Tenants who prioritize GPU utilization efficiency over strict separation, and whose workloads do not process sensitive data.

<Note>
NVCF supports the two invocation models described above. Other multi-tenancy configurations are outside the scope of this documentation.
</Note>

## Isolation Options by Layer

The table below summarizes the isolation available at each layer and how to enable it.

| Layer | Default Behavior | Stronger Option | How to Enable |
|---|---|---|---|
| **Namespace** | Helm-deployed functions run in a dedicated Kubernetes namespace; custom container functions run in the shared `nvcf-backend` namespace | — | Namespace isolation is automatic for helm deployments; not available for custom container deployments |
| **Network** | Egress and cross-namespace traffic controlled by NetworkPolicy | Custom policies via `nvca-namespace-networkpolicies` configmap | See [Network Configuration](./configuration.md#network-configuration) |
| **Node (function-level)** | Functions and tasks may share nodes | One active workload instance per node via k8s scheduling constraints | Enable `HostIsolation` cluster attribute — see [Host-Isolated Clusters](./configuration.md#host-isolated-clusters) |
| **Node (account-level)** | Multiple NCA accounts may share nodes | Hard anti-affinity: pods from different NCA accounts are never co-located via k8s scheduling constraints | Enable `AccountIsolation` cluster attribute — see [Account-Isolated Clusters](./configuration.md#account-isolated-clusters) |
| **Container runtime** | Standard container runtime (runc) | Kata Containers — each pod runs inside a lightweight VM | Enable `KataRuntimeIsolation` cluster attribute — see [Kata Container-Isolated Workloads](./configuration.md#kata-container-isolated-workloads) |
| **Hardware (GPU)** | Shared GPU; no hardware isolation | **Not supported by NVCF** — hardware-level GPU isolation is the infrastructure provider's responsibility | — |

<Warning>
`HostIsolation` and `AccountIsolation` are mutually exclusive. Enabling both on the same cluster is not supported. Choose one based on your isolation boundary: function-level (`HostIsolation`) or account-level (`AccountIsolation`).
</Warning>


## Hardware Isolation — Not Supported

NVCF does not provide hardware-level GPU isolation between tenants. Specifically:

- Workloads from different tenants may execute on the same physical GPU at the same time unless `AccountIsolation` is configured at the node level.
- GPU memory from one workload is not cryptographically isolated from another workload sharing the same device.
- NVIDIA Multi-Instance GPU (MIG) partitioning can be configured outside NVCF to partition a physical GPU, but NVCF does not orchestrate or guarantee MIG assignments per tenant.

If hardware isolation is a hard requirement, the recommended approach is `AccountIsolation`, which ensures tenants are on separate nodes and therefore on physically separate GPUs, without requiring MIG. Physical GPU separation at the hardware level remains the responsibility of the infrastructure provider.
