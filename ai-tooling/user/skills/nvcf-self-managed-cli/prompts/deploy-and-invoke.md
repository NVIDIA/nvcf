# Deploy and invoke a container function

Standard four-step flow: function CREATE → DEPLOY → API key (for invoke scope) → INVOKE.

## 1. Function CREATE

Define the function in a JSON file. Minimum:

Before creating the function, confirm the exact function name and container image with the user. If either value is missing, stop and ask. Do not invent or submit example values for these fields.

```json
{
  "name": "<confirmed-function-name>",
  "containerImage": "<confirmed-container-image>",
  "inferenceUrl": "/echo",
  "inferencePort": 8000,
  "description": "smoke test echo function",
  "functionType": "DEFAULT",
  "apiBodyFormat": "CUSTOM",
  "health": {
    "protocol": "HTTP",
    "uri": "/health",
    "port": 8000,
    "timeout": "PT30S",
    "expectedStatusCode": 200
  }
}
```

```sh
nvcf-cli function create --input-file=create-fn.json
# → Function ID: <fn_id>
# → Version ID: <ver_id>
```

Save the IDs — they're needed for deploy + invoke.

### LLM function variant

For an OpenAI-compatible LLM function, set `functionType: "LLM"` and put route metadata under `models[].llmConfig`:

Before creating the function, confirm the exact function name, container image, and model name with the user. If any value is missing, stop and ask. Do not invent or submit example values for these fields.

```json
{
  "name": "<confirmed-function-name>",
  "containerImage": "<confirmed-container-image>",
  "inferenceUrl": "/",
  "inferencePort": 8000,
  "functionType": "LLM",
  "models": [
    {
      "name": "<confirmed-model-name>",
      "llmConfig": {
        "uris": ["/v1/chat/completions", "/v1/responses", "/v1/embeddings"],
        "routingMethod": "round_robin",
        "tokenRateLimit": "1000-S"
      }
    }
  ]
}
```

Equivalent CLI flag form:

```sh
nvcf-cli function create \
  --name="<confirmed-function-name>" \
  --image="<confirmed-container-image>" \
  --inference-url=/ \
  --inference-port=8000 \
  --function-type=LLM \
  --llm-model='name=<confirmed-model-name>,uris=/v1/chat/completions|/v1/responses|/v1/embeddings,routingMethod=round_robin,tokenRateLimit=1000-S'
```

Use the same deploy step below after the LLM function is created.

Optional model routing update after creation:

```sh
nvcf-cli function update \
  --function-id=<fn_id> \
  --version-id=<ver_id> \
  --llm-model-update='name=<confirmed-model-name>,routingMethod=power_of_two,tokenRateLimit=1000-S'
```

Use this for mutable `routingMethod` and `tokenRateLimit`; create-time `uris` stay in `models[].llmConfig`.
`tokenRateLimit` supports positive integer token limits for `S`, `M`, `H`, `D`, and `W`. Use `1000-S` for a single inline CLI limit. Use input JSON for combined limits, such as `1000-S,5000-M,100000-H,500000-D,1000000-W`, because inline model specs use commas as field separators.

### Helm chart LLM function variant

LLM function type is independent of workload packaging. For a Helm-chart backed LLM function, keep the same `functionType` and `models[].llmConfig` fields, then set `helmChart` and `helmChartServiceName`. The service name must match a Kubernetes Service rendered by the chart, and `inferencePort` must be that Service port.

Before creating the function, confirm the exact function name and model name with the user. If either value is missing, stop and ask. Do not invent or submit example values for these fields.

```json
{
  "name": "<confirmed-function-name>",
  "inferenceUrl": "/",
  "inferencePort": 8000,
  "functionType": "LLM",
  "health": {
    "protocol": "HTTP",
    "uri": "/health",
    "port": 8000,
    "timeout": "PT30S",
    "expectedStatusCode": 200
  },
  "helmChart": "https://helm.ngc.nvidia.com/example/team/charts/openai-compatible-0.1.0.tgz",
  "helmChartServiceName": "openai-compatible",
  "models": [
    {
      "name": "<confirmed-model-name>",
      "llmConfig": {
        "uris": ["/v1/chat/completions", "/v1/responses", "/v1/embeddings"],
        "routingMethod": "round_robin",
        "tokenRateLimit": "1000-S"
      }
    }
  ]
}
```

Equivalent CLI flag form:

```sh
nvcf-cli function create \
  --name="<confirmed-function-name>" \
  --inference-url=/ \
  --inference-port=8000 \
  --function-type=LLM \
  --helm-chart=https://helm.ngc.nvidia.com/example/team/charts/openai-compatible-0.1.0.tgz \
  --helm-chart-service=openai-compatible \
  --llm-model='name=<confirmed-model-name>,uris=/v1/chat/completions|/v1/responses|/v1/embeddings,routingMethod=round_robin,tokenRateLimit=1000-S'
```

Deploy is unchanged. Put chart-specific values under `deploymentSpecifications[].configuration` only when the chart needs value overrides:

```json
{
  "functionId": "<fn_id>",
  "versionId": "<ver_id>",
  "deploymentSpecifications": [{
    "gpu": "H100",
    "instanceType": "NCP.GPU.H100_1x",
    "minInstances": 1,
    "maxInstances": 1,
    "maxRequestConcurrency": 10,
    "configuration": {
      "serving": {
        "tensorParallelSize": 1
      }
    }
  }]
}
```

LLM Gateway routes requests by the OpenAI `model` value. Use `<function-id>/<model-name>`: the function ID selects the NVCF function, and the model name is forwarded to the upstream container through `stargate-client`.

Supported LLM paths:

| Path | Notes |
| --- | --- |
| `/v1/chat/completions` | Supports streaming and session stickiness. |
| `/v1/responses` | Native Responses proxy path; relays SSE for streaming clients and returns terminal JSON for non-streaming clients. Supports session stickiness. |
| `/v1/embeddings` | Accepts string or string-array `input`; input must be non-empty and may contain at most 2048 entries. No session stickiness. |

## 2. Function DEPLOY

```json
{
  "functionId": "<fn_id>",
  "versionId": "<ver_id>",
  "deploymentSpecifications": [{
    "gpu": "H100",
    "instanceType": "NCP.GPU.H100_1x",
    "minInstances": 1,
    "maxInstances": 1,
    "maxRequestConcurrency": 10
  }]
}
```

Note: `gpu` is the GPU **family** (e.g. `H100`), `instanceType` is the SKU (e.g. `NCP.GPU.H100_1x`). Mismatch returns ICMS 400 "Invalid GPU".

```sh
nvcf-cli function deploy create --input-file=deploy-fn.json
```

This command **blocks** until the deployment reaches ACTIVE (default 900s timeout). When it returns rc=0, a function pod is running in the `nvcf-backend` namespace on the compute-plane cluster.

## 3. Mint an API key with `invoke_function` scope

`nvcf-cli init`'s admin token does NOT carry the `invoke_function` scope — invoke would 403 with "missing requested authorities". Mint an API key:

```sh
nvcf-cli api-key generate --description="<confirmed-function-name> invoke" --expires-in=1h
# → API Key: nvapi-nvcf-stg-...
```

The key is automatically saved to the CLI state file and used by the next `function invoke` call. **Don't echo the key into chat / logs**.

## 4. Function INVOKE

```json
{
  "functionId": "<fn_id>",
  "versionId": "<ver_id>",
  "requestBody": {"message": "hello"}
}
```

```sh
nvcf-cli function invoke --input-file=invoke-fn.json
# → Function invocation completed!
# → Status: fulfilled
# → Request ID: ...
```

For an LLM function, invoke the OpenAI-compatible route after deployment:

```sh
curl -sS -X POST "https://llm.invocation.<domain>/v1/chat/completions" \
  -H "Authorization: Bearer ${NVCF_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<fn_id>/<confirmed-model-name>",
    "stream": true,
    "messages": [
      {
        "role": "user",
        "content": "Hello"
      }
    ]
  }'
```

The OpenAI `model` value must be `<function-id>/<model-name>` so the gateway can select the function and model.

Responses API example:

```sh
curl -sS -X POST "https://llm.invocation.<domain>/v1/responses" \
  -H "Authorization: Bearer ${NVCF_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<fn_id>/<confirmed-model-name>",
    "input": "Write a one sentence summary of NVCF."
  }'
```

Embeddings example:

```sh
curl -sS -X POST "https://llm.invocation.<domain>/v1/embeddings" \
  -H "Authorization: Bearer ${NVCF_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<fn_id>/<confirmed-model-name>",
    "input": "NVCF embeddings check"
  }'
```

If status is `errored`, query ICMS for the deployment's pod logs (kubectl on the compute-plane cluster) and surface to the user.

## 5. Cleanup

When the user is done with the smoke test:

```sh
nvcf-cli function delete --function-id=<fn_id> --version-id=<ver_id>
```

**Confirm with the user before deleting.** This removes both the deployment and the function record.

## Notes

- Deploy times can vary widely depending on NATS stream-init latency on cold-cluster runs (1-7 minutes observed in dev-VM tests). Be patient; don't kill the `function deploy create` command early.
- For different GPU families, change both `gpu` and `instanceType` together. Common SKUs: `NCP.GPU.H100_1x`, `NCP.GPU.H100_2x`, `NCP.GPU.A100_1x`.
- The function's container image must be pullable from the compute-plane cluster's image-credential-helper config (check `kubectl describe pod -n nvcf-backend` if pods land in `ImagePullBackOff`).
