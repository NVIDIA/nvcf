# Task Simple Sample

Minimal NVCT task that writes progress updates to `${NVCT_PROGRESS_FILE_PATH}` until it reaches 100 percent. Use it to validate task registration, invocation, and result handling on a self-hosted NVCF cluster.

## Build the sample container

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t task-simple-sample .
```

Push the image to an OCI registry your self-hosted NVCF cluster can access and register pull credentials with `nvcf-cli registry-credential add`. See [examples/README.md](../../README.md#publishing-container-images) for the full flow.

## Run the sample locally

```bash
docker run --rm -v ${PWD}:/tmp/output -e NVCT_RESULTS_DIR=/tmp/output task-simple-sample
```

The container writes progress JSON into `/tmp/output/progress` and terminates once it reaches 100 percent.

## Launch on self-hosted NVCF

Resolve the cluster gateway and generate an invocation API key via `nvcf-cli`:

```bash
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
export NVCF_API_KEY=$(nvcf-cli api-key generate --description "task-simple-sample" --json | jq -r .apiKey)
export ORG_ID=<your-org-id>
```

Submit the task through the NVCT API, routing with the `Host` header:

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/v2/orgs/${ORG_ID}/nvct/tasks" \
  --header "Host: api.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${NVCF_API_KEY}" \
  --header "Content-Type: application/json" \
  --data '{
    "name": "task-simple-sample",
    "containerImage": "<your-registry>/<namespace>/task-simple-sample:<tag>",
    "gpuSpecification": {
      "gpu": "T10",
      "instanceType": "g6.full",
      "backend": "GFN"
    },
    "maxRuntimeDuration": "PT1H",
    "maxQueuedDuration": "PT2H",
    "terminationGracePeriodDuration": "PT15M",
    "resultHandlingStrategy": "NONE"
  }'
```

The response contains the task ID. Poll events and fetch results with:

```bash
TASK_ID=<task-id-from-response>

curl --url "http://${GATEWAY_ADDR}/v2/orgs/${ORG_ID}/nvct/tasks/${TASK_ID}/events" \
  --header "Host: api.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${NVCF_API_KEY}"

curl --url "http://${GATEWAY_ADDR}/v2/orgs/${ORG_ID}/nvct/tasks/${TASK_ID}/results" \
  --header "Host: api.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${NVCF_API_KEY}"
```

## Progress file format

Each task container writes periodic updates to `${NVCT_PROGRESS_FILE_PATH}` so that NVCF can track completion and surface progress to the caller.

```json
{
    "taskId": "579ad430-34b9-4a6e-9537-a060db4a9e6c",
    "percentComplete": 20,
    "name": "ckpt-step-2000",
    "metadata": {
        "step-number": 2000,
        "token_accuracy": 0.874
    },
    "lastUpdatedAt": "2025-01-02T15:04:05.999999999Z07:00"
}
```

- `taskId`: task ID issued by NVCF.
- `percentComplete`: integer completion percentage between 1 and 100.
- `metadata`: optional key-value pairs the container wants attached to the progress record.
- `name`: directory used when the `UPLOAD` result-handling strategy is selected. Length 1 to 190 characters. Allowed characters: `[0-9a-zA-Z!-_.*()]`. Prefixes `./` and `../` are not allowed.
- `lastUpdatedAt`: ISO 8601 timestamp. Must be updated at least every three minutes to signal that the task is still making progress.
