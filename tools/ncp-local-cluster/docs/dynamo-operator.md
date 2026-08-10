# Dynamo Operator: Local Self-Hosted Development

Install KAI Scheduler, Grove, and the NVIDIA Dynamo Operator into the local k3d self-hosted
NVCF development cluster, configure NVCA to support them, and create and deploy a function
backed by a DynamoGraphDeployment.

This guide assumes the local k3d cluster and NVCF stack are already running.
See [the ncp-local cluster README](../README.md) for k3d setup and
[docs/dev/local-development.md](../../../docs/dev/local-development.md) for the
NVCF stack deployment.

[KAI Scheduler](https://github.com/kai-scheduler/KAI-Scheduler) provides advanced GPU-aware bin-packing and gang scheduling for NVCF workloads.
[Grove](https://github.com/ai-dynamo/grove) adds topology-aware multi-node gang scheduling and is a required dependency of the Dynamo
Operator: it creates PodCliqueSets via the `grove.io` API during `DynamoGraphDeployment`
reconciliation. Neither KAI Scheduler nor Grove is exposed as a first-class operator through the
NVCF API; both are cluster infrastructure that the Dynamo Operator relies on.

## Prerequisites

- A running ncp-local cluster with the NVCF stack deployed. See [the ncp-local cluster README](../README.md) and [docs/dev/local-development.md](../../../docs/dev/local-development.md).
- A Helm chart containing one or more Dynamo Operator manifests, like a `DynamoGraphDeployment`. You may use the [Dynamo example](../../../examples/function-samples/helmchart-samples/dynamo-operator-sample).

## 1. Install KAI Scheduler

Follow the [KAI Scheduler installation guide](/docs/user/cluster-management/kai-scheduler.md).

**Note:** topology-aware scheduling with Dynamo requires Grove v0.14.0 or later,
so ensure that version is set instead of the default version if you want to test that behavior.

### Verify the KAI Scheduler install

```bash
# Scheduler pods running
kubectl get pods -n kai-scheduler

# Queue CRD installed and both queues created
kubectl get queues.scheduling.run.ai
# Expected: default-parent-queue, default-queue

# Confirm queue hierarchy (default-queue must reference default-parent-queue)
kubectl get queue default-queue -o jsonpath='{.spec.parentQueue}'
```

## 2. Install Grove

Grove is published at `oci://ghcr.io/ai-dynamo/grove/grove-charts` and requires no credentials.
It installs into the `grove-operator` namespace. Upstream install docs are [here](https://github.com/ai-dynamo/grove/blob/main/docs/installation.md),
for more configuration options.

Save the following as `grove-values.yaml`:

```yaml
replicaCount: 1

image:
  tag: "v0.1.0-alpha.8"

resources:
  limits:
    memory: 1Gi
  requests:
    cpu: 50m
    memory: 128Mi

config:
  logLevel: debug
  logFormat: json
  topologyAwareScheduling:
    # Enable this if you want to test topology-aware scheduling with KAI/Grove.
    enabled: false
  network:
    autoMNNVLEnabled: false

# Keep false to prevent Helm from overwriting the operator's auto-generated cert secret
# on every upgrade sync. The operator creates the secret itself if absent.
webhookServerSecret:
  enabled: false

# Runs an init container that applies CRDs via server-side apply on every pod startup,
# covering both initial install and upgrades (Helm does not upgrade CRDs automatically).
crdInstaller:
  enabled: true
```

Install:

```bash
helm upgrade --install grove \
  oci://ghcr.io/ai-dynamo/grove/grove-charts:v0.1.0-alpha.8 \
  -n grove-operator --create-namespace \
  -f grove-values.yaml
```

### Verify the Grove install

```bash
# Operator pod running
kubectl get pods -n grove-operator -l app.kubernetes.io/name=grove-operator

# All five Grove CRDs established
kubectl get crd | grep -E "grove\.io|scheduler\.grove\.io"
# Expected output includes:
#   clustertopologies.grove.io
#   podcliques.grove.io
#   podcliquescalinggroups.grove.io
#   podcliquesets.grove.io
#   podgangs.scheduler.grove.io

# Webhooks registered
kubectl get validatingwebhookconfigurations,mutatingwebhookconfigurations | grep grove

# Leader election lease held
kubectl get lease grove-operator-leader-election -n grove-operator \
  -o jsonpath='{.spec.holderIdentity}'
```

## 3. Install the Dynamo Operator

The chart installs the operator, its CRDs, and a bundled NATS instance. KAI Scheduler and Grove
are required but installed separately and referenced here by setting their `install` flags to
`false`.

Save the following as `dynamo-operator-values.yaml`:

```yaml
dynamo-operator:
  controllerManager:
    manager:
      image:
        repository: nvcr.io/nvidia/ai-dynamo/kubernetes-operator
        tag: "1.0.2"
    resources:
      limits:
        memory: 2Gi
      requests:
        cpu: 512m
        memory: 1Gi
    replicas: 1
  upgradeCRD: true
  dynamo:
    dockerRegistry:
      existingSecretName: ngc-registry
  webhook:
    failurePolicy: Fail
    certManager:
      enabled: false
  namespaceRestriction:
    enabled: false

nats:
  enabled: true

global:
  etcd:
    enabled: false
    install: false
  grove:
    enabled: true
    install: false
  kai-scheduler:
    enabled: true
    install: false
```

Install the operator:

```bash
helm upgrade --install dynamo \
  "https://helm.ngc.nvidia.com/nvidia/ai-dynamo/charts/dynamo-platform-1.0.2.tgz" \
  -n dynamo-system --create-namespace \
  -f dynamo-operator-values.yaml
```

### Verify the Dynamo Operator install

```bash
# Operator pod running
kubectl get pods -n dynamo-system -l app.kubernetes.io/component=operator

# CRDs installed
kubectl get crds | grep nvidia.com

# Leader election lease present
kubectl get lease -n kube-system | grep dynamo

# Webhooks registered
kubectl get mutatingwebhookconfigurations,validatingwebhookconfigurations | grep dynamo

# Operator metrics reachable
kubectl port-forward svc/dynamo-metrics 8080 -n dynamo-system &
curl -sk https://localhost:8080/metrics | head -20
```

## 4. Configure NVCA

Several changes are required in the nvca-operator Helm values:

1. Enable the `KAIScheduler` feature gate so NVCA annotates new workload pods with
   `kai.scheduler/queue` and `schedulerName: kai-scheduler`, and performs health checks against
   the queue hierarchy.
2. Add the `DynamoOperatorSupport` cluster attribute so ICMS routes Dynamo function deployments
   to this cluster.
3. Extend the validation policy with all Dynamo CRD group and resource names so NVCA gains RBAC
   to manage them and the NVCA mutating webhook can intercept objects in operator-managed namespaces.

The `agentConfig.mergeConfig` field is a freeform YAML string merged into the NVCA runtime
config. The `allowedExtraKubernetesTypes` list drives generation of the `allowed-extra-types`
ClusterRole and ClusterRoleBinding by the nvca-operator chart.

The `featureGateValues` list replaces the list in the base values file when provided as an
overlay, so the full set of required feature gates must be specified together.

Add these values as an additional values file in the relevant helmfile release:

```yaml
selfManaged:
  featureGateValues: ["DynamicGPUDiscovery", "SelfHosted", "KAIScheduler"]
  clusterAttributes: ["DynamoOperatorSupport"]

agentConfig:
  mergeConfig: |-
    cluster:
      validationPolicy:
        name: Unrestricted
        allowedExtraKubernetesTypes:
        - group: nvidia.com
          version: v1alpha1
          kind: DynamoGraphDeployment
          resource: dynamographdeployments
        - group: nvidia.com
          version: v1alpha1
          kind: DynamoComponentDeployment
          resource: dynamocomponentdeployments
        - group: nvidia.com
          version: v1alpha1
          kind: DynamoCheckpoint
          resource: dynamocheckpoints
        - group: nvidia.com
          version: v1alpha1
          kind: DynamoGraphDeploymentRequest
          resource: dynamographdeploymentrequests
        - group: nvidia.com
          version: v1alpha1
          kind: DynamoGraphDeploymentScalingAdapter
          resource: dynamographdeploymentscalingadapters
        - group: nvidia.com
          version: v1alpha1
          kind: DynamoModel
          resource: dynamomodels
        - group: nvidia.com
          version: v1alpha1
          kind: DynamoWorkerMetadata
          resource: dynamoworkermetadatas
```

Now rerun `helmfile apply`.

### Verify the NVCA configuration

```bash
# KAIScheduler feature gate present in the NVCFBackend ConfigMap
kubectl get configmap -n nvca-system -o yaml | grep KAIScheduler

# Cluster attribute registered
kubectl get configmap -n nvca-system -o yaml | grep DynamoOperatorSupport

# Extra RBAC ClusterRole created for Dynamo and Grove CRDs
kubectl get clusterrole | grep allowed-extra-types
kubectl get clusterrole nvca-operator-allowed-extra-types -o yaml
```

## 5. Create and deploy a Dynamo function

### Get an admin token

```bash
export NVCF_TOKEN=$(curl -s -X POST "http://api-keys.localhost:8080/v1/admin/keys" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['value'])")
```

### Create the function

Create the function with your customized function configuration.

```bash
curl -s -X POST "http://api.localhost:8080/v2/nvcf/functions" \
  -H "Authorization: Bearer ${NVCF_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "dynamo-test",
    ...
  }'
```

Save the `function.id` and `function.versionId` from the response:

```bash
export FUNCTION_ID=<id from response>
export VERSION_ID=<versionId from response>
```

### Deploy the function

Deploy the function with your customized deployment specification(s).

```bash
curl -s -X POST \
  "http://api.localhost:8080/v2/nvcf/deployments/functions/${FUNCTION_ID}/versions/${VERSION_ID}" \
  -H "Authorization: Bearer ${NVCF_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "deploymentSpecifications": [
      {
       ...
      }
    ]
  }'
```


### Monitor instance status

```bash
# Instance status via NVCF API
curl -s \
  "http://api.localhost:8080/v2/nvcf/deployments/functions/${FUNCTION_ID}/versions/${VERSION_ID}" \
  -H "Authorization: Bearer ${NVCF_TOKEN}"

# MiniService phase in the cluster
kubectl get miniservice -A -w
```

The instance phases in order: Installing, Installed, Running. If the phase stalls in Installing,
check all three operator log streams:

```bash
kubectl logs -n dynamo-system -l app.kubernetes.io/component=operator --tail=50
kubectl logs -n grove-operator -l app.kubernetes.io/name=grove-operator --tail=50
kubectl logs -n kai-scheduler --tail=50
```
