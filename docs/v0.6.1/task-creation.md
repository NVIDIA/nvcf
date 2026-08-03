# Task Creation

NVIDIA Cloud Tasks (NVCT) runs GPU-backed batch jobs. A task runs to
completion on a reserved GPU instance and optionally uploads results to a
model registry location when it finishes.

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
set `resultHandlingStrategy` to `NONE` so the task runs without registry upload.

## Authentication

Task commands require their own API key separate from the function API key.
Run `nvcf-cli api-key generate` after `nvcf-cli init` to mint both keys in
one step. See [CLI](./cli.md#generate-api-keys) for details.
