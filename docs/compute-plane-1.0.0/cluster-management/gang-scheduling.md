# Gang Scheduling

Gang scheduling holds a group of Pods until the scheduler can place every
required member. This prevents a partial deployment from consuming GPUs while
the remaining Pods stay `Pending`.

NVCF Helm functions and tasks may use custom resources for several components
for gang-scheduled workloads:

- [KAI Scheduler](https://github.com/kai-scheduler/KAI-Scheduler) provides
  queueing, resource allocation, and atomic placement through `PodGroup`
  resources.
- [Grove](https://github.com/NVIDIA/grove) represents related workload roles as
  `PodCliqueSet`, `PodClique`, `PodCliqueScalingGroup`, and `PodGang`
  resources. Grove delegates placement to KAI Scheduler.
- [Dynamo](https://docs.nvidia.com/dynamo/v1.2.1/kubernetes-deployment/scale/grove)
  describes inference services such as frontend, prefill, and decode workers
  in a `DynamoGraphDeployment`. The Dynamo operator uses Grove to orchestrate
  those services.

The NVCF Cluster Agent (NVCA) admits the KAI, Grove, and Dynamo resources in
function Helm charts when their compute plane add-ons are enabled.
NVCA also uses KAI implicitly when the `KAIScheduler` feature flag is enabled
to binpack workloads.

For more background, see the upstream
[KAI gang scheduling guide](https://github.com/kai-scheduler/KAI-Scheduler/blob/main/docs/batch/README.md)
and [Grove core concepts](https://github.com/NVIDIA/grove/blob/main/docs/user-guide/01_core-concepts/01_overview.md).

## Enable gang scheduling

Enable KAI Scheduler, Grove or Dynamo when functions use their custom resources:

```yaml
addons:
  kaiScheduler:
    enabled: true
  groveOperator:
    enabled: true
  dynamoOperator:
    enabled: true
```

The compute plane installs the components in dependency order:

1. KAI Scheduler
2. Grove
3. Dynamo

The same add-ons configure NVCA:

- KAI adds the `KAIScheduler` feature gate and permits `PodGroup`.
- Grove permits `PodCliqueSet`, `PodClique`, `PodCliqueScalingGroup`, and
  `PodGang`. Grove requires KAI Scheduler to be enabled.
- Dynamo adds the `DynamoOperatorSupport` feature gate and permits Dynamo
  custom resources. Dynamo requires Grove to be enabled.

See [KAI Scheduler](./kai-scheduler.md) for queue configuration and the
standalone installation path.

## Gang schedule a StatefulSet with KAI

Use a StatefulSet with parallel Pod management for a direct KAI workload:

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: distributed-worker
spec:
  podManagementPolicy: Parallel
  replicas: 2
  selector:
    matchLabels:
      app: distributed-worker
  template:
    metadata:
      labels:
        app: distributed-worker
    spec:
      containers:
        - name: worker
          image: <worker-image>
```

When the `KAIScheduler` feature is enabled, NVCA assigns the KAI scheduler and
queue to the Pod template. KAI creates one `PodGroup` for the StatefulSet and
waits until every replica can be placed.

Add topology annotations only when the gang must also fit in a specific
hardware domain. See [Topology-Aware Scheduling](./topology-aware-scheduling.md).

<Warning>
KAI creates a separate `PodGroup` for each Deployment replica. Use a
StatefulSet when all replicas must be scheduled as one gang.
</Warning>

The
[multi-node Helm function sample](https://github.com/NVIDIA/nvcf/tree/main/examples/function-samples/helmchart-samples/multi-node-helm-function-test)
contains optional KAI topology annotations for gangs that also need GPU clique
placement.

## Gang schedule a Dynamo workload with Grove

A `DynamoGraphDeployment` groups inference roles in one resource. A multinode
service tells Dynamo and Grove that each replica needs multiple Pods:

```yaml
apiVersion: nvidia.com/v1alpha1
kind: DynamoGraphDeployment
metadata:
  name: myllm
spec:
  services:
    VllmDecodeWorker:
      componentType: worker
      replicas: 1
      multinode:
        nodeCount: 2
      resources:
        limits:
          gpu: "4"
```

The Dynamo operator converts the service into Grove resources. Grove groups the
Pods, and KAI reserves and places the complete group. Function authors do not explicitly
create `PodGang` or `PodGroup` resources for a `DynamoGraphDeployment`.

See the [Dynamo Operator sample](https://github.com/NVIDIA/nvcf/tree/main/examples/function-samples/helmchart-samples/dynamo-operator-sample)
for a disaggregated frontend, prefill, and decode workload. See the upstream
[Dynamo multinode guide](https://docs.nvidia.com/dynamo/v1.2.1/kubernetes-deployment/scale/multinode-deployments)
and [Grove quickstart](https://github.com/NVIDIA/grove/blob/main/docs/quickstart.md)
for advanced workload configuration.

## Verify gang scheduling (Cluster admins only)

Check that the operators are running:

```bash
kubectl get pods -n kai-scheduler
kubectl get pods -n grove-system
kubectl get pods -n dynamo-system
```

Inspect the scheduling resources for a deployed function:

```bash
kubectl get podgroups.scheduling.run.ai -A
kubectl get podgangs.scheduler.grove.io -A
kubectl get podcliquesets.grove.io -A
```

If a gang remains `Pending`, describe its Pods and KAI `PodGroup`. Common
causes are insufficient free GPUs, queue limits, or topology constraints that
no available domain can satisfy.

```bash
kubectl describe pod <pod-name> -n <namespace>
kubectl describe podgroup <pod-group-name> -n <namespace>
```
