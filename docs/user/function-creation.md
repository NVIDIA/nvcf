# Function Creation

This page describes how to create functions within Cloud Functions.

Functions can be created in one of two ways:

1. Custom Container
   - Enables any container-based workload as long as the container exposes an inference endpoint and a health check.
   - Option to leverage any server, ex. [PyTriton](https://triton-inference-server.github.io/pytriton/), [FastAPI](https://fastapi.tiangolo.com/), [Triton](https://developer.nvidia.com/triton-inference-server).
   - See [Container-Based Function Creation](./container-functions.md).

2. Helm Chart
   - Enables orchestration across multiple containers. For complex use cases where a single container isn't flexible enough.
   - Requires one "mini-service" container defined as the inference entry point for the function.
   - Does not support partial response reporting, gRPC or HTTP streaming-based invocation.
   - See [Helm-Based Function Creation](./helm-functions.md).

Additionally, Cloud Functions supports [Low Latency Streaming (LLS) functions](./streaming-functions.md) for video, audio, and data streaming via WebRTC.

## LLM Functions

Use an LLM function when requests should enter through the LLM invocation
gateway and NVCF should route them by function and model. LLM functions use
`functionType: "LLM"`, accept OpenAI-compatible request bodies, and define model
routing metadata under `models[].llmConfig`.

For the full request path, supported endpoints, native proxy behavior, and session stickiness details, see [LLM Gateway](./llm-gateway.md).

![LLM invocation path](images/nvcf-llm-invocation-path.svg)

LLM requests must already be OpenAI-compatible when they reach the gateway. NVCF does not render Hugging Face chat templates or tokenize prompts for the function.

```json
{
  "name": "sample-llm-function",
  "containerImage": "nvcr.io/example/openai-compatible:latest",
  "inferenceUrl": "/",
  "inferencePort": 8000,
  "functionType": "LLM",
  "models": [
    {
      "name": "dummy-model",
      "llmConfig": {
        "uris": ["/v1/chat/completions", "/v1/responses", "/v1/embeddings"],
        "routingMethod": "round_robin",
        "tokenRateLimit": "1000-S"
      }
    }
  ]
}
```

The same configuration can be provided with CLI flags:

```bash
./nvcf-cli function create \
  --name "sample-llm-function" \
  --image "nvcr.io/example/openai-compatible:latest" \
  --inference-url "/" \
  --inference-port 8000 \
  --function-type LLM \
  --llm-model "name=dummy-model,uris=/v1/chat/completions|/v1/responses|/v1/embeddings,routingMethod=round_robin,tokenRateLimit=1000-S"
```

`llmConfig.uris` lists the OpenAI-compatible paths handled by the model. Supported LLM paths are `/v1/chat/completions`, `/v1/responses`, and `/v1/embeddings`. `routingMethod` accepts `round_robin`, `power_of_two`, `groq_multiregion`, `pulsar`, or `random`. `tokenRateLimit` applies a per-model token limit in `<value>-<unit>` format. Supported units are `S` (seconds), `M` (minutes), `H` (hours), `D` (days), and `W` (weeks). Use `1000-S` for a single limit, or `1000-S,5000-M,100000-H,500000-D,1000000-W` for a combined limit with distinct units. Use JSON input for combined limits because inline CLI model specs use commas as field separators.

After creation, update per-model `routingMethod` or `tokenRateLimit` with `nvcf-cli function update --llm-model-update "name=<model>,routingMethod=<method>,tokenRateLimit=<limit>"` or JSON `modelUpdates`.

Note: LLM functions do not use the normal function invocation hostname or path.
Send requests to the LLM invocation route, such as `https://llm.invocation.<domain>/v1/chat/completions`, and set `model` to `<function-id>/<model-name>`.
The CLI `function invoke` command detects LLM functions automatically. Pass `--inference-url` with the OpenAI-compatible path and `--model-name <model-name>` so the CLI can set the OpenAI `model` value to `<function-id>/<model-name>`.

After deployment, invoke chat completions through the LLM invocation route:

```bash
curl -sS -X POST "https://llm.invocation.<domain>/v1/chat/completions" \
  -H "Authorization: Bearer ${NVCF_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<function-id>/dummy-model",
    "stream": true,
    "messages": [
      {
        "role": "user",
        "content": "Write a one sentence summary of NVCF."
      }
    ]
  }'
```

The OpenAI `model` value uses the format `<function-id>/<model-name>` so the gateway can select the target function and model.

Embeddings use the same model format:

```bash
curl -sS -X POST "https://llm.invocation.<domain>/v1/embeddings" \
  -H "Authorization: Bearer ${NVCF_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<function-id>/dummy-model",
    "input": "NVCF embeddings check"
  }'
```

## Best Practices

### Container Versioning

- Ensure that any resources that you tag for deployment into production environments are not simply using "latest" and are following a standard version control convention.
  - During autoscaling, a function scaling any additional instances will pull the same specificed container image and version. If version is set to "latest", and the "latest" container image is updated between instance scaling, this can lead to undefined behavior.

- Function versions created are immutable, this means that the container image and version cannot be updated for a function without creating a new version of the function.

### Security

- Do not run containers as root user: Running containers as root is not supported in Cloud Functions. Always specify a non-root user in your Dockerfile using the `USER` instruction.
- Use Kubernetes Secrets: For sensitive information like API keys, credentials, or tokens, use Kubernetes Secrets instead of environment variables. This provides better security and follows Kubernetes best practices for secret management.

#### Available Container Variables

The following is a reference of available variables via the headers of the invocation message (auto-populated by Cloud Functions), accessible within the container.

For examples of how to extract and use some of these variables, see [NVCF Container Helper Functions](https://github.com/NVIDIA/nv-cloud-function-helpers/tree/main).

| Name                         | Description                                             |
| ---------------------------- | ------------------------------------------------------- |
| NVCF-REQID                   | Request ID for this request.                            |
| NVCF-SUB                     | Message subject.                                        |
| NVCF-NCAID                   | Function's organization's NCA ID.                       |
| NVCF-FUNCTION-NAME           | Function name.                                          |
| NVCF-FUNCTION-ID             | Function ID.                                            |
| NVCF-FUNCTION-VERSION-ID     | Function version ID.                                    |
| NVCF-LARGE-OUTPUT-DIR        | Large output directory path.                            |
| NVCF-MAX-RESPONSE-SIZE-BYTES | Max response size in bytes for the function.            |
| NVCF-NSPECTID                | NVIDIA reserved variable.                               |
| NVCF-BACKEND                 | Backend or "Cluster Group" the function is deployed on. |
| NVCF-INSTANCETYPE            | Instance type the function is deployed on.              |
| NVCF-REGION                  | Region or zone the function is deployed in.             |
| NVCF-ENV                     | Spot environment if deployed on spot instances.         |

#### Environment Variables

The following environment variables are automatically injected into your function containers when they are deployed and can be accessed using standard environment variable access methods in your application code:

| Name                     | Description                                             |
| ------------------------ | ------------------------------------------------------- |
| NVCF_BACKEND             | Backend or "Cluster Group" the function is deployed on. |
| NVCF_ENV                 | Spot environment if deployed on spot instances.         |
| NVCF_FUNCTION_ID         | Function ID.                                            |
| NVCF_FUNCTION_NAME       | Function name.                                          |
| NVCF_FUNCTION_VERSION_ID | Function version ID.                                    |
| NVCF_INSTANCETYPE        | Instance type the function is deployed on.              |
| NVCF_NCA_ID              | Function's organization's NCA ID.                       |
| NVCF_REGION              | Region or zone the function is deployed in.             |

<Note>
All environment variables with the `NVCF_*` prefix are reserved and should not be overridden in your application code or function configuration.

</Note>
