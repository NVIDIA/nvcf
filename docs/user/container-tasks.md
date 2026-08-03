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
  --secrets S3_ACCESS_KEY=... \
  --max-runtime PT4H \
  --result-strategy NONE

# From a JSON file (recommended for repeatable configurations)
nvcf-cli task create --input-file task.json
```

## Example JSON configuration

```json
{
  "name": "my-training-job",
  "description": "Fine-tuning run for Q4 experiment",
  "tags": ["training", "q4"],
  "gpuSpecification": {
    "gpu": "H100",
    "instanceType": "GPU.H100_1x"
  },
  "containerImage": "my-registry/training:latest",
  "containerArgs": "--epochs 10 --batch-size 32",
  "containerEnvironment": [
    {"key": "DATASET_PATH", "value": "/data/train"},
    {"key": "LOG_LEVEL", "value": "info"}
  ],
  "maxRuntimeDuration": "PT4H",
  "maxQueuedDuration": "PT72H",
  "resultHandlingStrategy": "NONE",
  "secrets": [
    {"name": "S3_ACCESS_KEY", "value": "..."},
    {"name": "S3_SECRET_KEY", "value": "..."}
  ]
}
```

## Environment variables

The task system injects the following environment variables into the container:

| Variable | Description |
| --- | --- |
| `NVCT_TASK_ID` | Unique task ID; useful for including in logs |
| `NVCT_TASK_NAME` | Task name |
| `NVCT_NCA_ID` | NCA ID that owns the task |
| `NVCT_PROGRESS_FILE_PATH` | Absolute path where the container must write the progress file |
| `NVCT_RESULTS_DIR` | Root directory under which the container creates result subdirectories |

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

`maxRuntimeDuration` has no default or maximum: omit it and the task runs with
no time limit.

## Secrets

Secrets are delivered to the container as a JSON file at
`/var/secrets/secrets.json`. The file has the form:

```json
{
  "SECRET_NAME": "secret-value",
  "ANOTHER_SECRET": "another-value"
}
```

Provide secrets via `--secrets NAME=value` on the CLI or as a `secrets` array
in the JSON file. Secret values are stored encrypted and are not returned by
default in task detail responses.

To rotate secrets on a running task, use `update-secrets`. The file at
`/var/secrets/secrets.json` is refreshed automatically with the new values:

```bash
nvcf-cli task update-secrets --secrets NEW_KEY=new-value
```

## Model and resource artifacts

Attach model or resource artifacts to a task using the `--models` and
`--resources` flags (format: `name:version:uri`) or via the JSON `models` and
`resources` arrays. The task system downloads and mounts them before the
container starts:

- Models are available at `/config/models/{modelName}`
- Resources are available at `/config/resources/{resourceName}`

## Progress file

The progress file is the primary mechanism for signaling task progress and
completion. The task system reads it to determine whether the container is alive
and when to transition the task to `COMPLETED`.

The container must write a JSON object to `NVCT_PROGRESS_FILE_PATH`:

```json
{
  "taskId": "579ad430-34b9-4a6e-9537-a060db4a9e6c",
  "percentComplete": 42,
  "name": "checkpoint-step-1000",
  "metadata": {
    "step": 1000,
    "loss": 0.312
  },
  "lastUpdatedAt": "2025-01-02T15:04:05.999999999Z"
}
```

Field requirements:

| Field | Required | Description |
| --- | --- | --- |
| `taskId` | Yes | Task ID from `NVCT_TASK_ID` |
| `percentComplete` | Yes | Integer 1-100; must be monotonically increasing |
| `name` | Yes | Name of the current result; 1-190 characters, allowed characters: letters, digits, `!`, `-`, `_`, `.`, `*`, `'`, `(`, `)`, no `./` or `../` prefix |
| `metadata` | No | Arbitrary key-value pairs surfaced in task details |
| `lastUpdatedAt` | Yes | RFC3339Nano timestamp; must be refreshed at least every 3 minutes |

**Heartbeat:** If `lastUpdatedAt` falls more than 5 minutes behind the current
time and `percentComplete` is not 100, the task moves to `ERRORED`.

**Completion:** Set `percentComplete` to `100` when the workload finishes. That
write transitions the task to `COMPLETED`. A container that exits without
writing `100` will not reach `COMPLETED`.

**Intermediate results:** For checkpoints or partial outputs, write
`percentComplete` between 1 and 99 with a unique `name` for each checkpoint.
Do not write to a result directory after updating `name` to a new value in the
progress file.

**Atomic writes:** Write to a temporary file and rename it over
`NVCT_PROGRESS_FILE_PATH` to avoid the task system reading a partial file.
In Python: `os.rename(tmp_path, progress_path)`.

## Result handling

Note: result upload to a registry is not yet supported in this release. Set
`resultHandlingStrategy` to `NONE`.

With `NONE`, the container is responsible for delivering its own outputs --
for example, writing to a volume or pushing to external storage using
credentials from task secrets. The task system does not upload anything but
still tracks progress through the progress file.

After the task completes, list any result names the container reported:

```bash
nvcf-cli task results
```

## Monitoring a task

```bash
# Check status and progress
nvcf-cli task get

# Stream lifecycle events
nvcf-cli task events
```

A `task get` response includes `percentComplete` and `healthInfo` (the GPU type,
instance type, and any error message from the platform) in addition to the
lifecycle status.

To stop a running or queued task:

```bash
# Stop execution gracefully; task moves to CANCELED
nvcf-cli task cancel

# Remove the task and its secrets; allows the container to finish within
# terminationGracePeriodDuration before the instance is terminated
nvcf-cli task delete
```
