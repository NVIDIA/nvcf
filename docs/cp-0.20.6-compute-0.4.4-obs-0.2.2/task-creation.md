# Task Creation

NVIDIA Cloud Tasks (NVCT) runs GPU-backed batch jobs on reserved GPU instances.
Common use cases include fine-tuning models, generating TensorRT engine builds,
and running batch data processing pipelines.

Tasks can be created in one of two ways:

1. Container image
   - Runs any container that executes a workload and exits.
   - The container receives GPU access and any secrets or environment variables
     you configure.
   - See [Container-Based Task Creation](./container-tasks.md).

2. Helm chart
   - Orchestrates multi-container workloads using a Helm chart.
   - Suitable for complex jobs that require multiple coordinated services.
   - See [Helm-Based Task Creation](./helm-tasks.md).

## Task lifecycle

A task moves through the following statuses during its lifetime:

| Status | Description |
| --- | --- |
| `QUEUED` | Task is created and waiting to be scheduled |
| `LAUNCHED` | Task has been scheduled and is starting |
| `RUNNING` | Task is executing |
| `COMPLETED` | Task finished successfully |
| `CANCELED` | Task was canceled by the user |
| `ERRORED` | An error occurred during execution, including a missed heartbeat |
| `EXCEEDED_MAX_RUNTIME_DURATION` | Task exceeded the configured `maxRuntimeDuration` |
| `EXCEEDED_MAX_QUEUED_DURATION` | Task was not scheduled within `maxQueuedDuration` |

## Differences from functions

Tasks and functions both run containers on GPU instances, but they serve
different purposes:

- Functions are long-running inference services that handle repeated invocation
  requests. They stay deployed until explicitly removed.
- Tasks are one-shot batch jobs. A task starts, runs its workload, and exits.
  The lifecycle ends when the container exits or a timeout is reached.

## Result handling

Result upload to a model registry is not supported on self-hosted NVCF in this
release.

`resultHandlingStrategy` defaults to `UPLOAD` when it is omitted, not `NONE`.
Because `UPLOAD` requires registry secrets and a `resultsLocation`, a task
created without those is rejected with a missing-secrets error. On self-hosted,
set `resultHandlingStrategy` to `NONE`.

With the `NONE` strategy, the task system does not upload anything. The
container is responsible for delivering its own outputs -- for example, writing
to a mounted volume or pushing to external storage using credentials supplied as
task secrets.

Even with `NONE`, the container must write to the progress file at
`NVCT_PROGRESS_FILE_PATH` throughout its run:

- Update `lastUpdatedAt` at least every 3 minutes as a heartbeat. If the
  timestamp falls more than 5 minutes behind the current time and
  `percentComplete` has not reached 100, the task moves to `ERRORED`.
- Set `percentComplete` to `100` when the workload completes. That value is
  what transitions the task from `RUNNING` to `COMPLETED`. A container that
  exits cleanly without writing `100` will not reach `COMPLETED`.

See [Container-Based Task Creation](./container-tasks.md#progress-file) for the
full progress file schema and requirements.

## Authentication

Task commands require their own API key separate from the function API key.
Run `nvcf-cli api-key generate` after `nvcf-cli init` to mint both keys in
one step. See [CLI](./cli.md#generate-api-keys) for details.
