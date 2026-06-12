# Container-Based Task Creation

A container task runs a Docker image on a GPU instance until the process
exits. Use this approach for training jobs, batch inference, data processing,
or any workload that runs to completion.

## Container requirements

NVCT does not impose a server or health check requirement. The container only
needs to:

- Perform its workload.
- Exit with code 0 on success, or a non-zero code on failure.

GPU drivers and CUDA libraries are available on the host. Use an image based
on an appropriate CUDA base image for your workload.

## Creating a container task

```bash
# Minimal example using CLI flags
nvcf-cli task create \
  --name my-training-job \
  --gpu H100 \
  --instance-type GPU.H100_1x \
  --image my-registry/training:latest

# With arguments, environment variables, and secrets
nvcf-cli task create \
  --name my-training-job \
  --gpu H100 \
  --instance-type GPU.H100_1x \
  --image my-registry/training:latest \
  --container-args "--epochs 10 --batch-size 32" \
  --container-env DATASET_PATH=/data/train \
  --container-env LOG_LEVEL=info \
  --secrets NGC_API_KEY=nvapi-... \
  --max-runtime PT4H \
  --result-strategy UPLOAD \
  --results-location my-org/my-team/my-model

# From a JSON file (recommended for repeatable configurations)
nvcf-cli task create --input-file task.json
```

## Example JSON configuration

```json
{
  "name": "my-training-job",
  "gpuSpecification": {
    "gpu": "H100",
    "instanceType": "GPU.H100_1x",
    "backend": "GFN"
  },
  "containerImage": "my-registry/training:latest",
  "containerArgs": "--epochs 10 --batch-size 32",
  "containerEnvironment": [
    {"key": "DATASET_PATH", "value": "/data/train"},
    {"key": "LOG_LEVEL", "value": "info"}
  ],
  "maxRuntimeDuration": "PT4H",
  "maxQueuedDuration": "PT72H",
  "resultHandlingStrategy": "UPLOAD",
  "resultsLocation": "my-org/my-team/my-model",
  "secrets": [
    {"name": "NGC_API_KEY", "value": "nvapi-..."}
  ]
}
```

## GPU specification

| Field | Description |
| --- | --- |
| `gpu` | GPU name, e.g. `H100`, `A100` |
| `instanceType` | Instance type, e.g. `GPU.H100_1x`, `GPU.A100_8x` |
| `backend` | Backend or CSP (optional) |
| `clusters` | Specific cluster names to target (optional) |

## Runtime limits

| Field | Format | Default |
| --- | --- | --- |
| `maxRuntimeDuration` | ISO 8601 duration, e.g. `PT4H30M` | None |
| `maxQueuedDuration` | ISO 8601 duration | `PT72H` |
| `terminationGracePeriodDuration` | ISO 8601 duration | `PT1H` |

A task that exceeds `maxRuntimeDuration` moves to
`EXCEEDED_MAX_RUNTIME_DURATION` status. A task that is not scheduled within
`maxQueuedDuration` moves to `EXCEEDED_MAX_QUEUED_DURATION` status.

## Secrets

Secrets are passed to the container as environment variables. Provide them via
`--secrets NAME=value` on the CLI or as a `secrets` array in the JSON file.
Secret values are stored encrypted and are not returned by default in task
detail responses.

To replace secrets on a running task:

```bash
nvcf-cli task update-secrets --secrets NEW_KEY=new-value
```

## Model and resource artifacts

Attach model or resource artifacts to a task using the `--models` and
`--resources` flags (format: `name:version:uri`) or via the JSON `models` and
`resources` arrays. These are made available to the container at runtime.

## Result handling

Note: result upload is not yet supported in this release.

When `resultHandlingStrategy` is `UPLOAD`, the task uploads outputs to the
registry location specified in `resultsLocation`. After the task completes,
retrieve the results:

```bash
nvcf-cli task results
```

## Monitoring a task

```bash
# Check status
nvcf-cli task get

# Stream lifecycle events
nvcf-cli task events
```
