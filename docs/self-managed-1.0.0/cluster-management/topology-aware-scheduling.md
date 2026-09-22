# Topology-Aware Scheduling

Topology-aware scheduling places related Pods in a hardware domain with the
network bandwidth they require. On an NVLink-optimized cluster, NVCF can place
a multi-node function inside one GPU clique instead of spreading its Pods
across slower links.

The scheduling path has four layers:

1. The NVIDIA GPU DRA driver labels nodes with `nvidia.com/gpu.clique`,
   identifying a particular NVLink partition.
2. KAI Scheduler reads a cluster-scoped `Topology` and places a gang in the
   requested domain.
3. Grove maps its workload hierarchy to the KAI topology through a `ClusterTopologyBinding`.
4. Dynamo uses Grove for placement of frontend, prefill, decode, and other
   inference services.

See the
[NVIDIA GPU DRA ComputeDomain guide](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/dra-cds.html),
[KAI topology guide](https://github.com/kai-scheduler/KAI-Scheduler/blob/main/docs/topology/README.md),
[Grove topology guide](https://github.com/NVIDIA/grove/blob/main/docs/user-guide/topology-aware-scheduling.md),
and [Dynamo topology guide](https://docs.nvidia.com/dynamo/v1.4.1/kubernetes-deployment/scale/topology-aware-scheduling)
for component-level details.

Helm functions can also use the legacy
[`dra.nvcf.nvidia.io` partition annotation](../helm-functions.md#legacy-nvca-nvlink-partition-annotation).
That path uses Kubernetes Pod affinity and is best-effort without KAI Scheduler
or Grove topology-aware scheduling. Use the KAI or Grove mechanisms on this
page when clique placement must be coordinated for the complete workload.

## Prerequisites

- Install the
  [NVIDIA GPU DRA driver](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/dra-intro-install.html).
- Request a full node of GPUs for each GPU-enabled Pod in your Helm chart.
  The GPU DRA driver currently supports one GPU-enabled Pod per node in this mode.

See [NVLink-optimized clusters](./configuration.md#nvlink-optimized-clusters)
for cluster registration and GPU request requirements.

### Use KAI Scheduler only

Enable KAI Scheduler in the compute plane stack during installation:

```yaml
addons:
  topologyAwareScheduling:
    enabled: true
  kaiScheduler:
    enabled: true
  groveOperator:
    enabled: false
  dynamoOperator:
    enabled: false
```

Topology levels are ordered from the widest domain to the narrowest domain.
The compute plane automatically creates this KAI resource on installation:

```yaml
apiVersion: kai.scheduler/v1alpha1
kind: Topology
metadata:
  name: nvcf-mnnvl-topology
spec:
  levels:
    - nodeLabel: nvidia.com/gpu.clique
    - nodeLabel: kubernetes.io/hostname
```

Enabling `addons.topologyAwareScheduling` without
`addons.kaiScheduler.enabled` stops Helmfile rendering with an error.

<Note>
The `Topology` name will always be `nvcf-mnnvl-topology` for MNNVL scheduling,
with the `nvidia.com/gpu.clique` node label for placement.
When using a KAI `PodGang` or topology annotations, ensure this name is used.
</Note>

#### Example: place a StatefulSet in one GPU clique

Add the KAI topology annotations to the StatefulSet object:

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  annotations:
    kai.scheduler/topology: "nvcf-mnnvl-topology"
    kai.scheduler/topology-required-placement: "nvidia.com/gpu.clique"
spec:
  podManagementPolicy: Parallel
  replicas: 2
```

KAI waits until all replicas fit in one value of
`nvidia.com/gpu.clique`. This is also a gang-scheduling request. See
[Gang Scheduling](./gang-scheduling.md) for atomic placement behavior and
Grove or Dynamo workloads.

Creating a `Topology` does not change workloads that do not opt in.

## Use Grove and/or Dynamo

Enable the complete scheduling stack during compute stack installation
when functions use Grove or Dynamo:

```yaml
addons:
  topologyAwareScheduling:
    enabled: true
  kaiScheduler:
    enabled: true
  groveOperator:
    enabled: true
  dynamoOperator:
    enabled: true
```

The `topologyAwareScheduling` toggle enables topology handling in Grove and
automatically creates a binding for each configured KAI topology on installation:

```yaml
apiVersion: grove.io/v1alpha1
kind: ClusterTopologyBinding
metadata:
  name: nvcf-mnnvl-topology-binding
spec:
  levels:
    - domain: gpuclique
      key: nvidia.com/gpu.clique
    - domain: hostname
      key: kubernetes.io/hostname
  schedulerTopologyBindings:
    - schedulerName: kai-scheduler
      topologyReference: nvcf-mnnvl-topology
```

See the
[Grove automatic MNNVL guide](https://github.com/NVIDIA/grove/blob/main/docs/user-guide/auto-mnnvl.md)
for more details.

<Note>
The `ClusterTopologyBinding` name will always be `nvcf-mnnvl-topology-binding` for MNNVL scheduling,
with the `gpuclique` domain and `nvidia.com/gpu.clique` key for placement.
When using Grove `PodClique*` or Dynamo `DynamoGraphDeployment*` family types with topology constraints,
ensure these constants are used.
</Note>

### Example: place a Grove workload in one GPU clique

Grove workload authors use the portable `gpuclique` domain from the generated
binding instead of the cluster's node label:

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: distributed-inference
spec:
  replicas: 1
  template:
    topologyConstraint:
      topologyName: nvcf-mnnvl-topology-binding
      pack:
        required: gpuclique
    cliques:
      - name: worker
        spec:
          roleName: worker
          replicas: 2
          minAvailable: 2
          podSpec:
            containers:
              - name: worker
                image: <worker-image>
                resources:
                  limits:
                    nvidia.com/gpu: "4"
```

Grove translates `gpuclique` to `nvidia.com/gpu.clique` and sends the
constraint to KAI. Child `PodClique` or `PodCliqueScalingGroup` constraints
can select the same or a narrower domain. See the
[Grove topology constraint rules](https://github.com/NVIDIA/grove/blob/main/docs/user-guide/topology-aware-scheduling.md#topology-constraint-rules).

### Example: place an entire DynamoGraphDeployment in one GPU clique

This `DynamoGraphDeployment` will create Grove pod cliques for GPU and frontend workers
that get scheduled in a single `gpuclique` domain:

```yaml
apiVersion: nvidia.com/v1alpha1
kind: DynamoGraphDeployment
metadata:
  name: my-llm
spec:
  topologyConstraint:
    topologyProfile: nvcf-mnnvl-topology-binding
    packDomain: gpuclique
  services:
    VllmWorker:
      componentType: worker
      replicas: 2
      envFromSecret: hf-token-secret
      resources:
        limits:
          gpu: "1"
      extraPodSpec:
        mainContainer:
          image: my-image
          command: ["/bin/sh", "-c"]
          args:
            - python3 -m dynamo.vllm --model Qwen/Qwen3-0.6B
    Frontend:
      componentType: frontend
      replicas: 1
      extraPodSpec:
        mainContainer:
          image: my-image
          command: ["/bin/sh", "-c"]
          args:
            - python3 -m dynamo.frontend
```

In general Dynamo creates Grove resources from a `DynamoGraphDeployment`, which Grove
and KAI place. Before adding
topology constraints to a `DynamoGraphDeployment`, see the
[topology-aware scheduling guide for Dynamo 1.4.1](https://docs.nvidia.com/dynamo/v1.4.1/kubernetes-deployment/scale/topology-aware-scheduling)
to check the workload fields and topology resources expected by the compute
plane stack's pinned operator.

## Verify topology resources (Cluster admins only)

Confirm that nodes have GPU clique labels:

```bash
kubectl get nodes -L nvidia.com/gpu.clique
```

Confirm that the KAI topology exists:

```bash
kubectl get topologies.kai.scheduler nvcf-mnnvl-topology
```

When Grove is enabled, confirm that its binding exists:

```bash
kubectl get clustertopologybindings.grove.io \
  nvcf-mnnvl-topology-binding
```

Grove v0.1.0-alpha.12 renamed PodGang resources from legacy base/scaled names
to an epoch-based naming scheme. The Grove operator migrates existing PodGangs
automatically on startup; running Pods are not disrupted, and no action is
required. PodGang names in `kubectl describe podgroup` output may look
different after the upgrade.

If Pods remain `Pending`, verify that one clique has enough free nodes for the
entire gang. Then inspect the Pod and scheduler events:

```bash
kubectl describe pod <pod-name> -n <namespace>
kubectl describe podgroup <pod-group-name> -n <namespace>
```
