# Helm-Based Function Creation

Cloud functions support helm-based functions for orchestration across multiple containers.

## Prerequisites

<Warning>
Ensure that your helm charts version does not contain `-` For example `v1` is ok but `v1-test` will cause issues.

</Warning>

1. The Helm chart must define a `mini-service` container as the inference entry point.
2. The name of this service in your helm chart should be supplied by setting `helmChartServiceName` during the function definition. This allows Cloud Functions to communicate and make inference requests to the "mini-service" endpoint.

<Warning>
The `servicePort` defined within the helm chart should be used as the `inferencePort` supplied during function creation. Otherwise, Cloud Functions will not be able to reach the "mini-service".

</Warning>

3. Ensure you have pushed your helm chart to your OCI container registry.

## Pull Secret Management

All Pod specs in your helm chart will be updated with pull secrets at runtime, so any images are authorized to pull automatically. No other configuration is needed.

## Create a Helm-based Function

1. Ensure your helm chart is uploaded to your registry and adheres to the [helm-prereq](./helm-functions.md) listed above.

2. Create the function:

   - Include the following additional parameters in the function definition:
     - `helmChart`
     - `helmChartServiceName`

   - The `helmChart` property should be set to the OCI URL of the helm chart that will deploy the "mini-service". The helm chart URL should follow the format: `oci://${REGISTRY}/${REPOSITORY}/charts/$NAME-X.Y.Z.tgz`. The chart name should not contain `-` in the version string.

   - NVCF uses `helmChartServiceName` for readiness checks and function
     metrics. It must match the service that exposes the `mini-service` entry
     point. Templated service names are not supported.

   - Important: The Helm chart name should not contain underscores or other special symbols, as that may cause issues during deployment.

### API example

Please see our [sample helm chart used](https://github.com/NVIDIA/nvcf/tree/main/examples/function-samples/helmchart-samples/inference-test-sample) in this example for reference.

Below is an example function creation API call creating a helm-based function:

```bash
curl -s -X POST "http://${GATEWAY_ADDR}/v2/nvcf/functions" \
    -H "Host: api.${GATEWAY_ADDR}" \
    -H "Authorization: Bearer $NVCF_TOKEN" \
    -H 'accept: application/json' \
    -H 'Content-Type: application/json' \
    -d '{
    "name": "function_name",
    "inferenceUrl": "v2/models/model_name/versions/model_version/infer",
    "inferencePort": 8001,
    "helmChart": "oci://'${REGISTRY}'/'${REPOSITORY}'/charts/inference-test-1.0.tgz",
    "helmChartServiceName": "service_name",
    "apiBodyFormat": "CUSTOM"
}'
```

<Note>
For gRPC-based functions, set `"inferenceURL" : "/gRPC"`. This signals to Cloud Functions that the function is using gRPC protocol and is not expected to have a `/gRPC` endpoint exposed for inferencing requests.

</Note>

3. Proceed with function deployment and invocation normally.

### Multi-node helm deployment

To create a multi-node helm deployment, you need to use the following format for the `instanceType`:
`<CSP>.GPU.<GPU_NAME>_<number of gpus per node>x[.x<number of nodes>]`. For example, `DGXC.GPU.L40S_1x` is a single L40S instance while `ON-PREM.GPU.B200_8x.x2` is two full nodes of 8-way B200.

A sample helm chart for a multi-node deployment can be found [in the multi-node helm example](https://github.com/NVIDIA/nvcf/tree/main/examples/function-samples/helmchart-samples/multi-node-helm-function-test/).

#### Multi-node NVLink scheduling

<Note>
The compute cluster must have the `NVLinkOptimized` attribute. See
[NVLink-optimized clusters](./cluster-management/configuration.md#nvlink-optimized-clusters).
</Note>

NVCF can place the Pods from one multi-node Helm function in a single NVLink
GPU clique. Use [Gang Scheduling](./cluster-management/gang-scheduling.md) when every Pod
must be placed atomically. Use
[Topology-Aware Scheduling](./cluster-management/topology-aware-scheduling.md)
to place that gang in one GPU clique. The guides include examples for direct
KAI StatefulSets and workloads managed through Grove and Dynamo.
NVCA will create a
[`ComputeDomain`](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/dra-cds.html)
to connect GPU workload Pods through IMEX.

<Note>
Each GPU-enabled Pod must request a full node of GPUs.
</Note>

#### Legacy NVCA NVLink partition annotation

<Warning>
NVLink partition placement through these legacy affinity rules is best-effort
without KAI Scheduler or Grove topology-aware scheduling. The rules do not
provide atomic gang placement. Concurrent Pods can initially land in different
cliques, and distinct logical groups can land in the same clique. Use
[Gang Scheduling](./cluster-management/gang-scheduling.md) with
[Topology-Aware Scheduling](./cluster-management/topology-aware-scheduling.md)
when the workload requires all Pods to fit and start in a specific topology
domain.
</Warning>

For charts that do not use KAI Scheduler or Grove topology constraints, NVCA
supports this legacy Pod template annotation:

```yaml
spec:
  template:
    metadata:
      annotations:
        dra.nvcf.nvidia.io/required-nvlink-domain-index: "0"
```

Set the annotation on the Pod template for a Deployment, ReplicaSet,
StatefulSet, Job, or CronJob. Set it on `metadata.annotations` for a standalone
Pod. Setting it only on the controller object's metadata does not pass it to
the Pods.

The value is a logical partition index within the function. It is not the
value of the `nvidia.com/gpu.clique` node label and does not select a physical
rack or clique:

- Pods with the same value form one logical placement group.
- Pods with different values get different logical group labels. This does not
  require the groups to use different GPU cliques.
- Pods without the annotation share a default logical group.

On an NVLink-optimized cluster, NVCA mutates each admitted Pod as follows:

- It adds the generated `dra.nvcf.nvidia.io/nvlink-domain-partition` label.
  Do not set this label in the chart.
- For an annotated Pod, it adds required Pod affinity so Pods in the same
  logical group use one value of the `nvidia.com/gpu.clique` topology key.
- For an unannotated Pod, it adds preferred Pod affinity with weight 100. The
  scheduler can spread these Pods when it cannot satisfy the preference.
- It requires placement on a node that has the `nvidia.com/gpu.clique` label.
- It adds the function's `ComputeDomain` resource claim to containers that
  request `nvidia.com/gpu`, `nvidia.com/pgpu`, `nvidia.com/gpu.shared`, or an
  `nvidia.com/mig-*` resource.

## Limitations

When using Helm Charts to deploy a function, the following limitations need to be taken into consideration.

### 1. Asset caching

- For any downloads (such as of assets or models) occurring within your function's containers, download size is limited by the disk space on the node.

### 2. Inference

Progress/partial response reporting is not supported, including any additional artifacts generated during inferencing. Consider opting for HTTP streaming or gRPC bidirectional support.

### 3. Security Constraints

Helm charts must conform to certain security standards to be deployable as a function.
This means that certain helm and Kubernetes features are restricted in NVCF backends.
NVCF will process your helm chart on function creation, then later on deployment with your Helm values and other deployment metadata,
to ensure standards are enforced.

NVCF may automatically modify certain objects in your chart so they conform to these standards;
it will only do so if modification will not break your chart when it is installed in the targeted backend.
Possible areas amenable to modification will be noted in the restrictions section below.
Any standard that cannot be enforced by modification will result in error(s) during function creation.

#### Restrictions

- Supported k8s artifacts under Helm Chart Namespace are listed below; others will be rejected:
  - ConfigMaps
  - Secrets
  - Services (only `type: ClusterIP` or none)
  - Deployments
  - ReplicaSets
  - StatefulSets
  - Jobs
  - CronJobs
  - Pods
  - ServiceAccounts
  - Roles
  - Rolebindings
  - PersistentVolumeClaims

- A rendered Helm chart may contain a maximum of 300 of the aforementioned objects.

- The only allowed Pod or Pod template volume types are:
  - `configMap`
  - `secret`
  - `projected.sources.*` of any of the above
  - `persistentVolumeClaim`
  - `emptyDir`

- No [chart hooks](https://helm.sh/docs/topics/charts_hooks/) are allowed; if specified in the chart, they will not be executed.

<Note>
CustomResourceDefinitions in helm charts will be skipped on installation. There is no need to modify your chart to remove them from `helm template` output for NVCF.

</Note>

Helm charts _should_ conform to these additional security standards. While not enforced now, they will be at a later date.

- All containers have [resource limits](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#requests-and-limits) for at least `cpu` and `memory` (and `nvidia.com/gpu`, `ephemeral-storage` if required for certain containers).
- All Pod's and resources that define a Pod template conform to the Kubernetes Pod Security Standards [Baseline](https://kubernetes.io/docs/concepts/security/pod-security-standards/#baseline) and [Restricted](https://kubernetes.io/docs/concepts/security/pod-security-standards/#restricted) policies.
- Pod and container `securityContext`'s conform to these parameters:
  - `automountServiceAccountToken` must be unset or set to `false`
  - `runAsNonRoot` must be explicitly set to `true`
  - `hostIPC`, `hostPID`, and `hostNetwork` must be unset or set to `false`
  - No privilege escalation, root capabilities, or non-default Seccomp, AppArmor, or SELinux profiles are allowed.
    See the [Baseline](https://kubernetes.io/docs/concepts/security/pod-security-standards/#baseline)
    and [Restricted](https://kubernetes.io/docs/concepts/security/pod-security-standards/#restricted)
    Pod security standards for fields that cannot be explicitly set.

## Helm Chart Overrides

To override keys in your helm chart `values.yml`, you can provide the `configuration` parameter and supply corresponding key-value pairs in JSON format which you would like to be overridden when the function is deployed.

```bash
curl -s -X POST "http://${GATEWAY_ADDR}/v2/nvcf/deployments/functions/${FUNCTION_ID}/versions/${FUNCTION_VERSION_ID}" \
 -H "Host: api.${GATEWAY_ADDR}" \
 -H "Authorization: Bearer $NVCF_TOKEN" \
 -H 'accept: application/json' \
 -H 'Content-Type: application/json' \
 -d '{
     "deploymentSpecifications": [{
         "gpu": "L40",
         "backend": "nvcf-default",
         "maxInstances": 2,
         "minInstances": 1,
         "configuration": {
         "key_one": "<value>",
         "key_two": { "key_two_subkey_one": "<value>", "key_two_subkey_two": "<value>" }
     ...
     }]
 }'
```
