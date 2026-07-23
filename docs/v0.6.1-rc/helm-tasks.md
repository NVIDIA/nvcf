# Helm-Based Task Creation

A Helm task deploys a Helm chart onto a GPU instance for the duration of the
job. Use this approach when your workload requires multiple coordinated
containers or a more complex Kubernetes resource configuration than a single
container image allows.

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
    "backend": "GFN",
    "helmValidationPolicy": {
      "name": "Default"
    }
  },
  "helmChart": "my-registry/charts/my-job:1.0.0",
  "maxRuntimeDuration": "PT8H",
  "resultHandlingStrategy": "UPLOAD",
  "resultsLocation": "my-org/my-team/my-model",
  "secrets": [
    {"name": "NGC_API_KEY", "value": "nvapi-..."}
  ]
}
```

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
    {"group": "batch", "version": "v1", "kind": "CronJob"}
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
