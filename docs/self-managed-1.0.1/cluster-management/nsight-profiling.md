# Nsight Profiling

NVIDIA Nsight Operator can inject NVIDIA Nsight Systems profiling support into
NVCF function pods that run on a self-hosted GPU cluster. Use it after the NVCA
Operator is installed and the cluster can deploy and invoke functions.

Nsight Operator injects profiling only into newly admitted pods. To enable
profiling for NVCF functions, label the workload namespace before creating or
recreating the function pods.

## Prerequisites

- A self-hosted NVCF control plane with a registered GPU cluster.
- A healthy NVCA Operator and NVCA agent. See [Self-Managed Clusters](./self-managed.md).
- The NVIDIA GPU Operator installed on the GPU cluster.
- `kubectl` and Helm access with permissions to install cluster-scoped resources.
- An external S3-compatible bucket for Nsight profiling results.
- The NVIDIA Nsight Operator resources bundle, including `nsight_operator.py`,
  installed on the workstation that will control profiling sessions.

Install the Python dependencies from the unpacked Nsight Operator resources
bundle:

```bash
python3 -m pip install -r requirements.txt
```

## Storage

Use external S3-compatible storage for repeatable NVCF profiling runs. The
stock Nsight Operator chart can deploy an in-cluster MinIO instance, but that
default is intended for short-lived experiments. External object storage keeps
captures available after Kubernetes pod restarts and makes downloads
independent of the cluster lifecycle.

Create a Kubernetes Secret in the Nsight Operator namespace. Replace the
placeholder values with credentials for your storage provider.

```yaml title="nsight-s3-secret.yaml"
apiVersion: v1
kind: Secret
metadata:
  name: nsight-s3-credentials
  namespace: nsight-operator
type: Opaque
stringData:
  storage-config.yaml: |
    storage_type: s3
    bucket_name: <profiling-results-bucket>
    aws_access_key_id: <access-key-id>
    aws_secret_access_key: <secret-access-key>
    region_name: <region>
    endpoint_url: <s3-compatible-endpoint-url>
    local_cache_dir: /tmp/nsight-s3-cache
```

Apply the namespace and Secret:

```bash
kubectl create namespace nsight-operator --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f nsight-s3-secret.yaml
```

If your storage provider does not require `endpoint_url`, omit it from
`storage-config.yaml`.

## Install Nsight Operator

The profiling-only profile enables coordinator mode for on-demand captures,
uses external S3 storage, disables operator-managed MinIO, and disables OTLP
trace mirroring.

```yaml title="nsight-values-profile-only.yaml"
cloudStorage:
  enabled: true
  minio:
    enabled: false
  secretRef:
    name: nsight-s3-credentials

nsight-injector:
  nsightToolConfig:
    coordinator: true
    otlpMirroringEnabled: false
    nsightToolArgs: "-t cuda,nvtx,osrt --python-sampling=true --cuda-graph-trace=node"

nsight-otel-collector:
  enabled: false

otlpProxyConfig:
  enabled: false
```

Install the chart:

```bash
helm upgrade --install nsight-operator \
  --wait \
  --namespace nsight-operator \
  --create-namespace \
  --values nsight-values-profile-only.yaml \
  https://helm.ngc.nvidia.com/nvidia/devtools/charts/nsight-operator-26.2.2.tgz
```

To also enable OTLP trace mirroring, use this values file instead:

```yaml title="nsight-values-with-otlp.yaml"
cloudStorage:
  enabled: true
  minio:
    enabled: false
  secretRef:
    name: nsight-s3-credentials

nsight-injector:
  nsightToolConfig:
    coordinator: true
    otlpMirroringEnabled: true
    nsightToolArgs: "-t cuda,nvtx,osrt --python-sampling=true --cuda-graph-trace=node"

nsight-otel-collector:
  enabled: true

otlpProxyConfig:
  enabled: true
```

Verify that the Nsight Operator components are running:

```bash
kubectl get pods -n nsight-operator
kubectl get nsightcloudstorageconfigs -A
kubectl get nsightgateways -A
kubectl get nsightcoordinators -A
```

## Enable Profiling Manually

Container functions run in the shared `nvcf-backend` namespace. Label that
namespace before deploying or recreating container function pods:

```bash
kubectl label namespace nvcf-backend \
  nvidia-nsight-profile=enabled \
  --overwrite
```

Helm functions run in dedicated namespaces created by NVCA. After deploying a
Helm function, find the function namespace and label it:

```bash
kubectl get namespaces \
  -l nvca.nvcf.nvidia.io/workload-instance-type=miniservice

kubectl label namespace <helm-function-namespace> \
  nvidia-nsight-profile=enabled \
  --overwrite
```

Existing pods are not injected retroactively. Recreate function pods after
adding the label. For container functions, delete the affected pod and allow
NVCA to recreate it:

```bash
kubectl get pods -n nvcf-backend
kubectl delete pod -n nvcf-backend <function-pod-name>
```

For Helm functions, redeploy the function or restart the workload resource in
the function namespace:

```bash
kubectl get deploy,statefulset,pod -n <helm-function-namespace>
kubectl rollout restart deployment/<deployment-name> -n <helm-function-namespace>
```

## Optional Kyverno Automation

Kyverno can label new NVCF workload namespaces as they are created. This covers
container functions through `nvcf-backend` when the namespace is created after
the policy exists, and Helm functions through their dedicated NVCA-created
namespaces.

Install Kyverno before applying this policy. Then create the ClusterPolicy:

```yaml title="nsight-label-nvcf-namespaces.yaml"
apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: label-nvcf-namespaces-for-nsight
spec:
  background: true
  rules:
    - name: add-nsight-profile-label
      match:
        any:
          - resources:
              kinds:
                - Namespace
              selector:
                matchExpressions:
                  - key: nvca.nvcf.nvidia.io/workload-instance-type
                    operator: In
                    values:
                      - pod_spec
                      - miniservice
      mutate:
        patchStrategicMerge:
          metadata:
            labels:
              nvidia-nsight-profile: enabled
```

Apply the policy:

```bash
kubectl apply -f nsight-label-nvcf-namespaces.yaml
```

Verify namespace labels:

```bash
kubectl get namespace nvcf-backend --show-labels
kubectl get namespaces \
  -l nvca.nvcf.nvidia.io/workload-instance-type \
  --show-labels
```

If `nvcf-backend` or existing Helm function namespaces were created before the
policy, label them manually or use Kyverno mutate-existing features in your
cluster policy. Recreate existing function pods after the label is present.

## Run a Capture

Use this flow with an existing Gemma-based LLM function, or create one using the
[LLM Gateway](../llm-gateway.md#function-configuration) function configuration
pattern. Keep the model name aligned with the function's configured
`models[].name` value.

Set the function and invocation variables:

```bash
export GATEWAY_ADDR=<gateway-address>
export FUNCTION_ID=<function-id>
export FUNCTION_VERSION_ID=<function-version-id>
export NVCF_TOKEN=<management-token>
export NVCF_INVOKE_KEY=<invocation-key>
export NVCF_BACKEND=<cluster-group-name>
export NVCF_GPU=<gpu-name>
export NVCF_INSTANCE_TYPE=<instance-type>
export GEMMA_MODEL_NAME=<gemma-model-name>
```

Deploy the function with a single GPU if it is not already deployed:

```bash
curl -s -X POST \
  "http://${GATEWAY_ADDR}/v2/nvcf/deployments/functions/${FUNCTION_ID}/versions/${FUNCTION_VERSION_ID}" \
  -H "Host: api.${GATEWAY_ADDR}" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${NVCF_TOKEN}" \
  -d '{
    "deploymentSpecifications": [
      {
        "backend": "'"${NVCF_BACKEND}"'",
        "gpu": "'"${NVCF_GPU}"'",
        "instanceType": "'"${NVCF_INSTANCE_TYPE}"'",
        "minInstances": 1,
        "maxInstances": 1
      }
    ]
  }' | jq .
```

Wait until the function pod is running and has been recreated after the Nsight
label was applied:

```bash
kubectl get pods -n nvcf-backend
kubectl get pods -A -l function-id="${FUNCTION_ID}"
```

Configure the Nsight CLI. `autoconfigure` discovers the Nsight Gateway and
storage settings from the cluster.

```bash
python3 nsight_operator.py autoconfigure -n nsight-operator
```

Start a profiling session:

```bash
python3 nsight_operator.py session-begin --title gemma-nvcf-smoke
python3 nsight_operator.py profiler-start
```

Invoke the model through the NVCF LLM route while profiling is active:

```bash
for i in 1 2 3; do
  curl -s -X POST "http://${GATEWAY_ADDR}/v1/chat/completions" \
    -H "Host: llm.invocation.${GATEWAY_ADDR}" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer ${NVCF_INVOKE_KEY}" \
    -d '{
      "model": "'"${FUNCTION_ID}/${GEMMA_MODEL_NAME}"'",
      "messages": [
        {
          "role": "user",
          "content": "Write a short paragraph about GPU profiling."
        }
      ],
      "max_tokens": 128,
      "temperature": 0.2
    }'
  echo
done
```

Stop and close the session:

```bash
python3 nsight_operator.py profiler-stop
python3 nsight_operator.py session-end
```

List and download the report:

```bash
python3 nsight_operator.py ls
python3 nsight_operator.py download --output-dir ./nsight-profiles
```

The downloaded directory should contain one or more `.nsys-rep` files. Open the
reports with NVIDIA Nsight Systems.

## Troubleshooting

### Pods are not injected

Check the namespace label:

```bash
kubectl get namespace <namespace> --show-labels
```

The namespace must have `nvidia-nsight-profile=enabled` before the pod is
created. If the pod already existed, recreate it. Also check the Nsight Operator
logs and verify that the process name matches the configured
`injectionIncludePatterns`.

```bash
kubectl logs -n nsight-operator -l app.kubernetes.io/name=nsight-operator --tail 100
kubectl describe pod -n <namespace> <pod-name>
```

### Downloads fail

Verify that the storage Secret exists and that the bucket credentials can write
and read profiling reports:

```bash
kubectl get secret nsight-s3-credentials -n nsight-operator
kubectl get nsightcloudstorageconfigs -n nsight-operator -o yaml
```

If `nsight_operator.py configure` was used instead of `autoconfigure`, configure
storage access separately before running `download`.

### The Nsight Gateway is not reachable

Check the Nsight Gateway service:

```bash
kubectl get svc -n nsight-operator
kubectl get nsightgateways -n nsight-operator -o yaml
```

For a ClusterIP service, run `nsight_operator.py autoconfigure -n nsight-operator`
from a workstation with `kubectl` access. The CLI can set up port forwarding for
the gateway.

### GPU metrics collectors conflict

Some GPU profiling and metrics collectors use the same low-level GPU interfaces.
If profiling fails after injection, check whether another DCGM or GPU metrics
collector is running on the node. Temporarily disable the conflicting collector
or profile in a maintenance window.

## See Also

- [Self-Managed Clusters](./self-managed.md)
- [LLM Gateway](../llm-gateway.md)
- [Generic HTTP Function Invocation](../generic-http-function-invocation.md)
- [NVIDIA Nsight Operator Installation Guide](https://docs.nvidia.com/nsight-operator/InstallationGuide/index.html)
- [NVIDIA Nsight Operator User Guide](https://docs.nvidia.com/nsight-operator/UserGuide/index.html)
- [NVIDIA Nsight Operator CRD Reference](https://docs.nvidia.com/nsight-operator/CRDReference/index.html)
