# Infrastructure Sizing

<Info>
The recommendations on this page are approximate starting points. The node
types and storage classes are AWS examples. For another cloud service provider
(CSP) or on-premises Kubernetes environment, choose equivalent node shapes and
storage classes that provide at least the listed vCPU, memory, storage, zone
spread, and GPU compatibility characteristics. Actual requirements depend on
workload characteristics, function count, and request concurrency. Use the
[self-managed-grpc-load-test](../g-rpc-load-testing) and
[self-managed-http-load-test](../http-load-testing) guides to validate
throughput and tune your control plane accordingly.

</Info>

## Infrastructure Components

The self-hosted NVCF control plane uses dedicated node pools, each isolated by a
Kubernetes node selector label. Separating workloads onto dedicated pools
prevents resource contention and simplifies scaling.

The table below lists the stateful infrastructure workloads deployed by the
helmfile stack. These are the core dependencies that must be running before any
NVCF services can start.

| Service | Namespace | Kind | Replicas | Node Selector | Notes |
| --- | --- | --- | --- | --- | --- |
| Cassandra | `cassandra-system` | StatefulSet | 3 | `nvcf.nvidia.com/workload=cassandra` | Wraps the Bitnami Cassandra chart. Configurable via `cassandra.replicaCount`. |
| NATS | `nats-system` | StatefulSet | 3 | `nvcf.nvidia.com/workload=control-plane` | Wraps the upstream `nats-io/k8s` chart with clustering and JetStream enabled. Each replica has its own PVC for file storage. |
| OpenBao Server | `vault-system` | StatefulSet | 3 | `nvcf.nvidia.com/workload=vault` | HA Raft cluster for secrets management. Configured via `openbao.server.ha.replicas`. |
| OpenBao Injector | `vault-system` | Deployment | 2 | `nvcf.nvidia.com/workload=vault` | Sidecar injector for Vault agent containers. Upstream chart defaults to 1; overridden to 2 in `global.yaml.gotmpl`. |
| GPU Workers | *(operator-managed)* | *(varies)* | Workload-dependent | `nvcf.nvidia.com/workload=gpu` | Function instances requiring GPU acceleration. Sized independently from the control plane. See [GPU Worker Nodes](#gpu-worker-nodes). |

<Info>
Each Cassandra replica must run on its own node to ensure high
availability. Do not co-locate multiple Cassandra pods on the same node.

</Info>

<Note>
Node selectors are only applied when `global.nodeSelectors.enabled` is set
to `true` in the environment file. When disabled, all workloads are
scheduled without placement constraints.

</Note>

## Sizing Tiers

The tables below cover control-plane infrastructure only. GPU worker nodes
are sized independently based on your inference workloads. See
[GPU Worker Nodes](#gpu-worker-nodes).

### Development

For local development of the stack or functions, CI pipelines, or quick demos,
you can run the entire NVCF stack on a single machine using k3d. This setup
uses a single Cassandra replica, fake GPUs, and ephemeral `local-path` storage.

See [local-development](../local-development) for full step-by-step instructions.

### Staging / Demo

A minimal deployment for validating the stack, running demos, or developing
functions. All NVCF services can be co-located on one or two nodes with no
dedicated node selectors.

| Node Pool | AWS Example Node Type | vCPU | Memory | Nodes |
| --- | --- | --- | --- | --- |
| All services (co-located) | `m6i.4xlarge` or equivalent | 16 | 64 GiB | 1 |
| Split across | `m6i.2xlarge` or equivalent | 8 | 32 GiB | 2 |

Storage: Approximately 50 GB AWS EBS `gp3`, or equivalent CSI-backed block
storage for your CSP.

Use this tier for:

- Initial evaluation and proof-of-concept deployments
- CI/CD pipelines and automated testing
- Demo environments

<Tip>
You can also run the full stack on your laptop using Kind or k3d. See
[local-development](../local-development) for instructions.

</Tip>

### Minimal High Availability

The recommended minimum for production workloads. Core infrastructure
(Cassandra, OpenBao) is fully redundant; the control plane has enough capacity
for moderate function counts.

| Node Pool | AWS Example Node Type | vCPU | Memory | Nodes |
| --- | --- | --- | --- | --- |
| Control Plane | `m5.2xlarge` or equivalent | 8 | 32 GiB | 2 |
| Cassandra | `m5.2xlarge` or equivalent | 8 | 32 GiB | 3 |
| OpenBao | `m5.2xlarge` or equivalent | 8 | 32 GiB | 2 |

Total: 7 control-plane nodes

Storage: Approximately 500 GB AWS EBS `gp3`, or equivalent CSI-backed block
storage, across Cassandra and OpenBao volumes.

Use this tier for:

- Production workloads with up to ~2,000 registered functions
- Environments where core infrastructure HA is required
- Moderate request concurrency

### Production (Full HA)

Designed for high-throughput production deployments with a large number of
concurrent functions and full high availability across every component.

| Node Pool | AWS Example Node Type | vCPU | Memory | Nodes |
| --- | --- | --- | --- | --- |
| Control Plane | `m5.4xlarge` or equivalent | 16 | 64 GiB | 3 to 5 |
| Cassandra | `r5.2xlarge` or equivalent memory-optimized node | 8 | 64 GiB | 3 |
| OpenBao | `m5.xlarge` or equivalent | 4 | 16 GiB | 3 |

Total: 9 to 11 control-plane nodes

Storage: Approximately 500 GB AWS EBS `gp3`, or equivalent CSI-backed block
storage, across Cassandra and OpenBao volumes.

Use this tier for:

- High-throughput production with more than 2,000 concurrent functions
- Full HA required for all components including OpenBao
- Deployments serving multiple teams or tenants

<Note>
The Production tier uses `r5.2xlarge` (memory-optimized) for Cassandra to
provide additional headroom for large datasets and compaction. For the control
plane, `m5.4xlarge` provides double the CPU and memory per node compared to
the Minimal HA tier, reducing the number of nodes needed while increasing
per-node headroom.

</Note>

## GPU Worker Nodes

GPU worker nodes are not part of the control-plane tiers above. They are
sized based on your inference workloads and can be added to any tier.

Self-hosted NVCF supports any GPU instance type compatible with the
[NVIDIA GPU Operator](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/).

GPU requirements:

- NVIDIA GPU Operator must be installed
- NVIDIA device plugin for Kubernetes
- Physical GPU hardware on worker nodes

For development and testing environments without GPUs, install the fake GPU
operator to simulate GPU resources. See [fake-gpu-operator](../fake-gpu-operator) for
instructions.

## Storage Recommendations

| Component | Default | Production Recommendation |
| --- | --- | --- |
| Cassandra | 20 Gi per replica | 50 to 100 Gi |
| OpenBao | 20 Gi | 20 to 50 Gi |
| NATS JetStream | 20 Gi | 50 to 100 Gi (depending on message volume) |
| Control Plane Services | 1 to 10 Gi each | Defaults are typically sufficient |

Storage sizes are configurable via the `storageSize` value in your environment
file. See [helmfile-installation](./helmfile-installation.md) for details.

<Note>
Some cloud providers have minimum PVC size requirements. For example, AWS EBS
`gp3` volumes have a 1 Gi minimum.

</Note>

## Scaling Beyond Defaults

The default control-plane resource sizing shipped with the helmfile stack is
designed to handle approximately 100 concurrent users. If you need higher
throughput:

1. Benchmark your deployment using the [self-managed-grpc-load-test](../g-rpc-load-testing)
   or [self-managed-http-load-test](../http-load-testing) guide. Start with
   `--vus 100` and increase gradually.
2. Scale node pools independently. Cassandra, OpenBao, and control-plane
   pools can each be scaled without affecting the others.
3. Increase pod resources for specific services by adding `values:` blocks
   in the helmfile release definitions. See [helmfile-installation](./helmfile-installation.md)
   for override examples.

<Note>
- [Quickstart](./quickstart.md): One-click fresh installation walkthrough
- [self-managed-grpc-load-test](../g-rpc-load-testing): Validate control-plane throughput
- [self-managed-http-load-test](../http-load-testing): Validate HTTP invocation throughput

</Note>
