# Create and run a task

Standard flow: mint API keys -> task CREATE -> monitor -> retrieve results -> cleanup.

## 1. Mint API keys

The admin token from `init` does not carry task invocation scope. Generate API keys:

```sh
nvcf-cli api-key generate --description="task smoke test" --expires-in=8h
# -> function key: nvapi-...
# -> task key: nvapi-...
```

Both keys are saved to CLI state. To mint only the task key:

```sh
nvcf-cli api-key generate --for task --description="task smoke test" --expires-in=8h
```

To validate each key immediately after minting:

```sh
nvcf-cli api-key generate --description="task smoke test" --expires-in=8h --validate
# -> function API key validation passed!
# -> task API key validation passed!
```

Never echo the key value (nvapi-...) into chat or logs. Report only that the key was minted, its description, and its expiry — never the value itself.

### Fresh session pre-flight

Before running any `task` command in a fresh agent session, verify credentials:

1. Check whether `NVCF_API_KEY` is set:
   ```sh
   printenv NVCF_API_KEY > /dev/null 2>&1 && echo set || echo unset
   ```
2. If unset, read the stored key expiry without printing the value:
   ```sh
   python3 -c "import json,pathlib; s=json.loads(pathlib.Path('~/.nvcf-cli.state').expanduser().read_text()); print(s.get('nvctApiKeyExpiration','missing'))"
   ```
3. If the expiry is in the past or missing, regenerate:
   ```sh
   nvcf-cli refresh
   nvcf-cli api-key generate --for task --description="task-key" --expires-in=24h --validate
   ```
4. Inject the stored key into the subprocess env without logging the value. Use a shell subshell or env prefix — never print the key into chat.

Error classification:
- "missing authentication credentials" or exit 2: key not injected; repeat pre-flight.
- HTTP 403: key expired or wrong audience; regenerate.
- Empty task list: valid; no tasks exist yet.

## 2. Task CREATE: container

Define the task in a JSON file. Minimum viable container task:

```json
{
  "name": "smoke-container-task",
  "gpuSpecification": {
    "gpu": "H100",
    "instanceType": "NCP.GPU.H100_1x",
    "backend": "ncp-local"
  },
  "containerImage": "nvcr.io/nvidia/nemo:latest",
  "containerArgs": "python -c \"print('hello from task')\"",
  "resultHandlingStrategy": "NONE"
}
```

```sh
nvcf-cli task create --input-file=task-container.json
# -> Task ID: <task_id>
```

Capture the task ID from the output. State persistence can fail silently, so pass
`<taskId>` explicitly on subsequent commands rather than relying on saved state.

Equivalent CLI flags form for quick ad-hoc runs:

```sh
nvcf-cli task create \
  --name=smoke-container-task \
  --gpu=H100 \
  --instance-type=NCP.GPU.H100_1x \
  --backend=ncp-local \
  --image=nvcr.io/nvidia/nemo:latest \
  --container-args="python -c 'print(hello from task)'" \
  --result-strategy=NONE
```

To target a specific compute cluster:

```sh
nvcf-cli task create --input-file=task-container.json --clusters=my-cluster
```

To inject environment variables or secrets, either pass flags:

```sh
nvcf-cli task create --input-file=task-container.json \
  --container-env MY_ENV=value \
  --secrets MY_SECRET=s3cr3t
```

or set them in the JSON file:

```json
{
  "containerEnvironment": [
    {"key": "MY_ENV", "value": "value"}
  ],
  "secrets": [
    {"name": "MY_SECRET", "value": "s3cr3t"}
  ]
}
```

Secrets are encrypted at rest and not returned in list or get responses by default; do not log them.

## 3. Task CREATE: Helm

For Helm-type tasks, replace `containerImage` with `helmChart`:

```json
{
  "name": "smoke-helm-task",
  "gpuSpecification": {
    "gpu": "H100",
    "instanceType": "NCP.GPU.H100_1x",
    "backend": "ncp-local"
  },
  "helmChart": "https://helm.ngc.nvidia.com/<org>/charts/example-task-0.1.0.tgz",
  "resultHandlingStrategy": "NONE"
}
```

```sh
nvcf-cli task create --input-file=task-helm.json
# -> Task ID: <task_id>
```

`helmChart` and `containerImage` are mutually exclusive.

## 3b. Update secrets on a running task

```sh
nvcf-cli task update-secrets <task_id> --secrets NAME=value
```

`update-secrets` merges secrets by name: supplied secrets are added or updated, and existing secrets not included in the request are preserved. Do not echo secret values into chat.

## 4. Monitor

Poll task state:

```sh
nvcf-cli task get <task_id>
# -> id: <task_id>
# -> status: RUNNING
```

Or use the task ID saved to state from the most recent `task create`:

```sh
nvcf-cli task get
```

Status values: `QUEUED`, `LAUNCHED`, `RUNNING`, `COMPLETED`, `ERRORED`, `CANCELED`, `EXCEEDED_MAX_RUNTIME_DURATION`, `EXCEEDED_MAX_QUEUED_DURATION`.

Inspect lifecycle events for progress details or failure context:

```sh
nvcf-cli task events <task_id>
```

If the task is stuck in `QUEUED`, check that a compute cluster with a matching GPU SKU
is registered and healthy (`nvcf-cli self-hosted status`).

If the task moves to `ERRORED`, the events output contains the failure reason. For
container-level detail, inspect the task workload pods on the compute-plane cluster with
`kubectl` against the appropriate namespace and context.

## 5. Retrieve results

For tasks with `resultHandlingStrategy: NONE`, results are empty by design:

```sh
nvcf-cli task results <task_id>
# -> {"results": []}
```

For tasks with `resultHandlingStrategy: UPLOAD`, results upload to the location named in
`resultsLocation`. The task's credentials must have write access to that location. After
the task reaches `COMPLETED`:

```sh
nvcf-cli task results <task_id>
```

Note: result upload is not yet supported in this release.

## 6. Cleanup

Cancel a running task:

```sh
nvcf-cli task cancel <task_id>
```

Delete the task record after it reaches a terminal state:

```sh
nvcf-cli task delete <task_id>
```

Stop and confirm with the user before running task delete. State the task ID and its current status. Do not proceed even when the user's prompt already requested deletion — always get an explicit last-look confirmation. Deletion is permanent and cannot be undone.

To list all tasks for the current owner (useful before bulk cleanup):

```sh
nvcf-cli task list
nvcf-cli task list --status=COMPLETED
```

## Notes

- GPU family and instance type must match a registered compute cluster SKU. Mismatch
  causes the task to remain `QUEUED` indefinitely. Use `nvcf-cli cluster list` to check
  registered SKUs.
- For container images on private registries, ensure the compute-plane cluster's
  image-credential-helper is configured for the registry host.
