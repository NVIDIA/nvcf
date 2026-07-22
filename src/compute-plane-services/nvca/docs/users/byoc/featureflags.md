# NVCA Attributes and Feature Flags

After installing the nvca-operator, edit its `NVCFBackend` object to add feature flags.
Default-enabled feature flags can be _disabled_ by prepending `-` to its name in the `values` list,
ex. `-CachingSupport`.

**Note**: make sure to copy over existing spec feature flag values into the equivalent override values,
since that list overwritten not merged.

Example:

```
$ kubectl edit nvcfbackend -n nvca-operator
...
spec:
  featureGate:
    values:
    - LogPosting                # Existing feature flag
  overrides:
    featureGate:
      values:
      - LogPosting              # Existing feature flag copied over
      - -CachingSupport         # Caching support disabled
      - LowLatencyStreaming
      - BYOObservability
      ...
```

## Attributes

| Name | Default | Description |
| --- | --- | --- |
| KataRuntimeIsolation | false | Forces NVCF workload pods to get a Kata runtime class, and allows "nvidia.com/pgpu" resource types on nodes when parsing node resources |
| HostIsolation | false | Prevents pods from more than one function from running on a given node at once |
| AccountIsolation | false | Prevents pods from different functions belonging to more than one Nvidia Cloud Account from running on a given node at once |
| TimeSlicingGPUEnabled | false | Forces NVCA's node feature handler to permit time-sliced GPUs when parsing node resources |
| PassthroughGPUEnabled | false | Allows "nvidia.com/pgpu" resource types on nodes when parsing node resources |
| OVCSecurityEnforcements | false | Turns on OVC security enforcements outlined by the "SensorRTX Risk Mitigation" SDD |
| NVLinkOptimized | false | Turns on NVLink Optimization on Clusters and related validations on Agent startup |

## Feature Flags

| Name | Default | Description |
| --- | --- | --- |
| LogPosting | false | Post instance logs to ICMS directly |
| CachingSupport | false | Enable NVMesh caching support for Container functions and tasks |
| HelmCachingSupport | false | Enable NVMesh caching support for Helm functions and tasks |
| NVMeshEncryption | false | Enable NVMesh encryption on cache data |
| PeriodicInstanceStatusUpdate | true | Enable periodic syncs with ICMS to reconcile instance state differences |
| HelmRBACEnforcement | true | Enforce RBAC constraints on Helm charts specified by functions |
| DynamicGPUDiscovery | true | Dynamically discover GPUs and instance types on this cluster |
| MultipleGPUTypesAllowed | true | Permit a heterogeneous set of GPUs across nodes in this cluster, ex. L40 and A100 |
| AutoPurgeDegradedWorkers | true | Automatically delete function instances and tasks that have degraded Pods |
| HelmSharedStorage | true | Configure Helm functions and tasks with shared read-only storage for ESS secrets |
| ClusterTargeting | true | Enable targeted cluster queues |
| HelmResourceConstraints | true | Enforce GPU quota adherence on Helm functions and tasks |
| BinPackTenantWorkloads | false | Prefer that pods from the same function or task are scheduled on the same node |
| GXCache | false | Enable GXCache support in NVCA |
| LowLatencyStreaming | true | Enable LLS support in NVCA |
| UseFunctionDeploymentStages | false | Enable container stage transition event logging to Function Deployment Stages service |
| PVCRebind | false | Force cache PVC's to rebind on failure |
| MultiNodeWorkloads | true | Instruct NVCA to send multi-node instance types to ICMS during registration |
| UseFunctionTranslator | true | Use the nvcf-icms-translate translator to generate function manifests instead of ICMS-generated artifacts |
| BYOObservability | false | Enable Bring-your-own observability support in NVCA |
| BYOOFluentBit | false | Enable Bring-your-own observability FluentBit logging sidecar in workload pods |
| ClientMetrics | false | Emit OpenTelemetry semantic-convention metrics for NVCA's outbound dependency clients |
| MaxSQSBatchPull | true | Increase the pull batch size from the SQS queue from 1 to 10 |
| InfraResourceOverhead | false | InfraResourceOverhead enables subtraction of infrastructure resource overhead from instance type resources, potentially removing any instance type that cannot satisfy infrastructure resources |
| EnforceHelmFunctionResourceLimits | false | Enforces resource limits on helm functions via ResourceQuota's. Sets `podSpec.{initContainers,containers}[*].resource.requests = limits` |
| EnforceContainerFunctionResourceLimits | false | Enforces resource limits on container functions via container resource limits. Sets `podSpec.{initContainers,containers}[*].resource.requests = limits` |
| EnforceHelmTaskResourceLimits | false | Enforces resource limits on helm tasks via ResourceQuota's. Sets `podSpec.{initContainers,containers}[*].resource.requests = limits` |
| EnforceContainerTaskResourceLimits | false | Enforces resource limits on container tasks via container resource limits. Sets `podSpec.{initContainers,containers}[*].resource.requests = limits` |
| CordonMaintenance | false | Sets the mode for NVCA to maintenance and only pauses new workloads on the cluster backend |
| CordonAndDrainMaintenance | false | Sets the mode for NVCA to maintenance and evicts existing workloads disruptively on the cluster backend |
| AckTaskRequestAfterPodsScheduled | false | Instructs the agent to only acknowledge ICMS requests with ICMS and delete queue messages after all NVCT task pods have been accepted by the cluster's scheduler |
| SelfHosted | false | Enable Self-Hosted mode |
| GracefulNoGPU | false | Allow NVCA to start and operate without GPUs, pausing queue processing until GPUs become available |
| HelmCustomAnnotations | false | Enable Custom Annotations for Helm workloads |
| KAIScheduler | false | Enables bin-packing support for efficient resource utilization using KAI scheduler |
| HelmAllowCPUNodes | false | Allows CPU-only pods in Helm functions to be scheduled on non-GPU nodes. GPU pods retain required instance-type affinity while CPU-only pods get anti-preference for GPU nodes. Mutually exclusive with HelmResourceConstraints |
| MiniServiceRevisionHistory | true | Enables saving prior helm values as ConfigMaps on each MiniService's values update. |
| AllowWorkloadKubernetesAPIAccess | false | Allows workload pods to access the Kubernetes API. Required for First Class Operator support |
| DynamoOperatorSupport | false | Enables First Class Operator support. The operator must be installed in the cluster and NVCA's validation policy configured with its CRD types. **Note:** enabling this flag automatically enables `AllowWorkloadKubernetesAPIAccess` |
