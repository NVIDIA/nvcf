# Helm-Based Task Creation

A Helm task deploys a Helm chart onto a GPU instance for the duration of the
job. Use this approach when your workload requires multiple coordinated
containers or a more complex Kubernetes resource configuration than a single
container image allows.

## Prerequisites

Before creating a Helm task:

- Chart version strings must not contain hyphens. For example, `v1` is valid;
  `v1-test` causes installation failures.
- The task system injects the following values before rendering the chart,
  so templates can reference them as `.Values.nvctTaskId` etc. without any
  other setup. Declaring them with empty defaults in `values.yaml` is
  recommended so templates compile cleanly during local development:

  | Key | Description |
  | --- | --- |
  | `nvctNcaId` | NCA ID that owns the task |
  | `nvctTaskId` | Unique task ID |
  | `nvctTaskName` | Task name |
  | `nvctResultsDir` | Root directory for result subdirectories |
  | `nvctProgressFilePath` | Path where the chart must write the progress file |

  Minimal `values.yaml`:

  ```yaml
  nvctNcaId: ""
  nvctTaskId: ""
  nvctTaskName: ""
  nvctResultsDir: ""
  nvctProgressFilePath: ""
  ```

- Pull secrets are attached to the default ServiceAccount at runtime. Pods
  that use the default ServiceAccount can pull images without any pull-secret
  configuration in the chart. For private registries, register credentials
  first with `nvcf-cli registry-credential add` -- see [CLI](./cli.md#registry-credentials-commands)
  for details.

## Creating a Helm task

```bash
# Using CLI flags
nvcf-cli task create \
  --name my-helm-job \
  --gpu H100 \
  --instance-type GPU.H100_1x \
  --helm-chart my-registry/charts/my-job:1.0.0

# From a JSON file
nvcf-cli task create --input-file helm-task.json
```

## Example JSON configuration

```json
{
  "name": "my-helm-job",
  "gpuSpecification": {
    "gpu": "H100",
    "instanceType": "GPU.H100_1x",
    "helmValidationPolicy": {
      "name": "Default"
    }
  },
  "helmChart": "my-registry/charts/my-job:1.0.0",
  "maxRuntimeDuration": "PT8H",
  "resultHandlingStrategy": "NONE",
  "secrets": [
    {"name": "S3_ACCESS_KEY", "value": "..."},
    {"name": "S3_SECRET_KEY", "value": "..."}
  ]
}
```

## Progress updates

The chart must write to `nvctProgressFilePath` throughout the job. See
[Container-Based Task Creation](./container-tasks.md#progress-file) for the
progress file schema and heartbeat requirements.

Because Kubernetes may restart containers, use a Job with `backoffLimit: 0` and
`restartPolicy: Never` for the workload container. A restarted container that
writes a lower `percentComplete` than a previous write causes a task failure --
the value must be non-decreasing across all writes.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ .Release.Name }}
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: task
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
```

## Security constraints

The supported object types below apply to all policies. The remaining rules
(volume types, object count, hooks, CRDs) apply only when using the `Default`
policy; `Unrestricted` bypasses those checks.

Supported Kubernetes object types:

- ConfigMap
- Secret
- Service (type `ClusterIP` or headless only)
- Deployment, ReplicaSet, StatefulSet
- Job, CronJob
- Pod
- ServiceAccount

The rendered chart may contain at most 300 objects across these types.

Allowed Pod volume types:

- `configMap`
- `secret`
- `projected` (sources of the above types)
- `emptyDir`

Helm chart hooks are not executed and are ignored if present.

CustomResourceDefinitions in the chart are skipped on installation.

## Helm validation policy

The `helmValidationPolicy` field controls which Kubernetes resource types the
chart is permitted to create. It is nested inside `gpuSpecification`.

| Policy name | Description |
| --- | --- |
| `Default` | Allows standard Kubernetes workload types |
| `Unrestricted` | Allows any resource type |

To permit additional resource types beyond the default set, supply them in
`extraKubernetesTypes`:

```json
"helmValidationPolicy": {
  "name": "Default",
  "extraKubernetesTypes": [
    {"group": "apps", "version": "v1", "kind": "DaemonSet"}
  ]
}
```

## Differences from container tasks

| | Container task | Helm task |
| --- | --- | --- |
| Entry point | `containerImage` + optional `containerArgs` | `helmChart` |
| Multi-container | No | Yes |
| Resource control | Via `gpuSpecification` | Via Helm chart values and `helmValidationPolicy` |
| `containerEnvironment` | Supported | Not applicable |

Runtime limits, secrets, result handling, and monitoring work the same way as
container tasks. Note: result upload is not yet supported in this release. See [Container-Based Task Creation](./container-tasks.md) for
details on those fields.
