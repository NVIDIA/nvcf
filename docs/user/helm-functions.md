# Helm-Based Function Creation

Cloud functions support helm-based functions for orchestration across multiple containers.

## Prerequisites

<Warning>
Ensure that your helm charts version does not contain `-` For example `v1` is ok but `v1-test` will cause issues.

</Warning>

1. The helm chart **must have a "mini-service" container defined, which will be used as the inference entry point.**
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

   - The `helmChartServiceName` is used for checking if the "mini-service" is ready for inference and is also scraped for function metrics. At this time, templatized service names are not supported. **This must match the service name of your "mini-service" with the exposed entry point port.**

   - Important: The Helm chart name should not contain underscores or other special symbols, as that may cause issues during deployment.

### Example creation via API

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

## Multi-node Helm deployment

To create a multi-node helm deployment, you need to use the following format for the `instanceType`:
`<CSP>.GPU.<GPU_NAME>_<number of gpus per node>x[.x<number of nodes>]`. For example, `DGXC.GPU.L40S_1x` is a single L40S instance while `ON-PREM.GPU.B200_8x.x2` is two full nodes of 8-way B200.

A sample helm chart for a multi-node deployment can be found [in the multi-node helm example](https://github.com/NVIDIA/nvcf/tree/main/examples/function-samples/helmchart-samples/multi-node-helm-function-test/).

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

## Instance health

Cloud Functions uses "holistic" Helm function health checking: on every reconcile,
NVCA evaluates the health of every object applied from your Helm chart and their children
(ex. a Deployment, its active ReplicaSet, and its replica Pods) plus infrastructure Pods.
If any single object reaches a terminal bad state, the whole function instance is marked failed.
NVCA then reports the failing object's status and events for debugging and re-creates the instance.

An instance transitions through these phases:

- Scheduling: one or more Pods have not yet been assigned to a node.
- Starting: all Pods are scheduled and NVCA is waiting on readiness.
- Running: all Helm chart objects and the worker Pod report healthy or ready.

Because health depends on all chart objects, a Pod that is not the inference
worker (for example a sidecar Deployment or an init Job) can fail the instance
if it enters a terminal state. Use [`StatusByWorkerReadiness`](#use-worker-readiness-for-function-health)
if only the inference worker's readiness should determine instance health.

### Timeouts

The following timeouts govern when a still-unhealthy object fails the instance.
The clock for each timeout starts when the described condition is first
observed, measured from the Pod launch time unless noted. Values are NVCA
defaults. Only Worker Degradation Period is operator-configurable, through the
`Worker Degradation Period` setting in [NVCA Configuration](./cluster-management/configuration.md).

| Timeout | Default | Cause (when the clock starts) | Effect (when exceeded) |
| --- | --- | --- | --- |
| Pod scheduling | 10 minutes | A Pod cannot be scheduled onto a node, for example no node satisfies its resources or affinity. | The Pod is marked terminal (`PodStuckScheduling`) and the instance fails and is re-created. For tasks, the task's max queued duration is used instead of this default. |
| Image pull error | 1 minute | A scheduled Pod reports `ErrImagePull` or `ImagePullBackOff` on any container or init container. | The Pod is marked terminal (`ImagePullIssues`) and the instance fails. Check registry credentials and image references. |
| Container restart loop | 10 minutes | A container or init container restarts at least 3 times. | The Pod is marked terminal (stuck initializing, containers in restart loop) and the instance fails. |
| Init container stuck | 2 hours | A Pod's init containers have not completed (the Pod is not yet `Initialized`). | The Pod is marked terminal (init container stuck) and the instance fails. |
| Worker startup | 2 hours | A running Pod has not become ready during its initial startup, before it has ever reported ready. | The Pod is considered degraded and the instance fails. |
| Worker Degradation Period | 30 minutes | A Pod that had been ready becomes not ready (containers report not ready) after being initialized. | The Pod is considered degraded, the instance is marked degraded, and NVCA kills and re-creates it. Operator-configurable. |
| Pending timeout (max running) | 3 hours | Objects remain pending, that is not all objects reach ready, since the instance health condition first went unhealthy. | The instance fails with a pending timeout. |
| Failing objects backoff | 90 seconds | An object reports a transient failure, for example `FailedMount` or `FailedAttachVolume`. | NVCA requeues and retries every 30 seconds for up to 90 seconds. If the object is still failing after 90 seconds, the instance fails. |

Some conditions fail an instance immediately, without waiting for a timeout:

- A Pod that enters the `Failed` phase, including admission rejection
  (`UnexpectedAdmissionError`).
- A Pod with restart policy `Never` whose non-restartable container terminates
  with a non-zero exit code is treated as degraded right away, regardless of the
  Worker Degradation Period.
- A controller object (Deployment, ReplicaSet, StatefulSet, Job) that reports a
  terminal condition such as `ProgressDeadlineExceeded`, a replica-failure
  condition, or a failed Job.
- A warning event that indicates a terminal error, such as `FailedCreate`,
  `FailedUpdate`, `ReplicaSetCreateError`, or a `forbidden` error, on a tracked
  object.

### Use worker readiness for function health

As described above, an unhealthy object, ex. a Pod, applied by the Helm chart can fail the function
instance. This behavior may not be desirable if some objects in your chart are expected to fail or are not critical
to serving inference. When your function is configured with the `StatusByWorkerReadiness` feature flag, the instance health check performed by NVCF becomes the sole determinant of instance health.
The flag is read from the chart at install time to configure the function instance.

Add `templates/nvcf-workload-config.yaml` to the function chart:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: nvcf-workload-config
data:
  config.yaml: |
    featureFlags:
      StatusByWorkerReadiness: true
```

The ConfigMap **must** be named `nvcf-workload-config` and the `config.yaml` key **must** exist.
Cloud Functions reads this configuration from the chart and does not create the ConfigMap
in the instance namespace.

Package and push the chart, then create and deploy the function:

```bash
helm package ./my-chart
helm push ./my-chart-1.0.0.tgz \
  oci://${REGISTRY}/${REPOSITORY}/charts

./nvcf-cli function create \
  --name my-helm-function \
  --helm-chart \
    oci://${REGISTRY}/${REPOSITORY}/charts/my-chart-1.0.0.tgz \
  --helm-chart-service my-inference-service \
  --inference-url /infer \
  --inference-port 8000 \
  --health-uri /health/ready \
  --health-port 8000

./nvcf-cli function deploy create \
  --instance-type NCP.GPU.H100_1x \
  --gpu H100 \
  --min-instances 1 \
  --max-instances 1
```

The function reaches `RUNNING` when the instance health endpoint `/health/ready` returns HTTP `200`,
indicating readiness. With this flag enabled:

- Other chart objects' statuses, including the inference Deployment and Service, are
  informational only. Their statuses are sent in logs but do not gate `RUNNING`.
- If a non-worker Pod goes down while the instance health endpoint reports ready, the
  instance remains `RUNNING`. Cloud Functions reports the unhealthy object's status for
  debugging, and Kubernetes can replace it.
- If the health endpoint does not report ready, the instance is marked degraded
  until it reports ready again or 30 minutes have passed (Worker Degradation Period, see [NVCA Configuration](./cluster-management/configuration.md)),
  after which the instance is killed and re-created by NVCF.

Without the ConfigMap, or with the flag set to `false`, Cloud Functions uses standard health behavior.

See also the optional `statusByWorkerReadiness` value in
[examples/function-samples/helmchart-samples/inference-test-sample](https://github.com/NVIDIA/nvcf/tree/main/examples/function-samples/helmchart-samples/inference-test-sample).
