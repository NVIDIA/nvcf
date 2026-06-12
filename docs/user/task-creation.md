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

Tasks can upload outputs to a model registry location when they complete.
Set `resultHandlingStrategy` to `UPLOAD` and provide a `resultsLocation` in
the form `org/[team/]model-name`. The results are then accessible via
`nvcf-cli task results`.

Set `resultHandlingStrategy` to `NONE` (the default) when the task writes
outputs elsewhere or does not need registry upload.

Note: result upload is not yet supported in this release.

## Authentication

Task commands require their own API key separate from the function API key.
Run `nvcf-cli api-key generate` after `nvcf-cli init` to mint both keys in
one step. See [CLI](./cli.md#generate-api-keys) for details.
