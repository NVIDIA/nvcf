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

Use an LLM function when the deployed workload exposes OpenAI-compatible model routes through the LLM invocation gateway. LLM functions use `functionType: "LLM"` and define model routing metadata under `models[].llmConfig`.

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
        "uris": ["/v1/chat/completions", "/v1/responses"],
        "routingMethod": "round_robin",
        "tokenRateLimit": "1000-M"
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
  --llm-model "name=dummy-model,uris=/v1/chat/completions|/v1/responses,routingMethod=round_robin,tokenRateLimit=1000-M"
```

`llmConfig.uris` lists the OpenAI-compatible paths handled by the model. `routingMethod` accepts `round_robin`, `power_of_two`, or `random`. `tokenRateLimit` uses the same rate limit format as function-level rate limits.

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

### Session Stickiness

The LLM invocation gateway supports sticky routing for multi-turn OpenAI-compatible requests on `/v1/chat/completions` and `/v1/responses`. Sticky routing is not supported on `/v1/embeddings`.

To keep later requests routed to the same backend, send the `x-multi-turn-session-id` response header value back as the `x-multi-turn-session-id` request header on the next request. The gateway returns `x-multi-turn-session-id` on successful supported responses. If the request does not include a valid non-empty header, the gateway derives an opaque session ID from the request input and returns it. Clients should treat a blank `x-multi-turn-session-id` request header as absent.

The gateway chooses the sticky routing key in this order:

| Endpoint | Precedence |
| --- | --- |
| `/v1/responses` | `prompt_cache_key`, `conversation.id`, `x-multi-turn-session-id`, input hash fallback |
| `/v1/chat/completions` | `x-multi-turn-session-id`, messages hash fallback |

For Responses API follow-up calls, `previous_response_id` does not override the
sticky routing key. Continue sending `prompt_cache_key`, `conversation.id`, or
the returned `x-multi-turn-session-id` header when the next request needs the
same backend affinity.

The session ID only affects backend selection when the deployment's LLM request router uses a cache-affinity-aware routing method for the target model. In self-managed deployments, operators configure this with `groq-multiregion` plus `cache_affinity_backend_selection_count` greater than `0`, or `pulsar` with backend KV metrics. `power-of-two`, `round-robin`, and `random` do not provide session stickiness.

Clients should only use `x-multi-turn-session-id`. The gateway derives and forwards the internal `x-cache-affinity-key` to the router; clients should not send that header.

## Best Practices

### Container Versioning

- Ensure that any resources that you tag for deployment into production environments are not simply using "latest" and are following a standard version control convention.
  - During autoscaling, a function scaling any additional instances will pull the same specificed container image and version. If version is set to "latest", and the "latest" container image is updated between instance scaling, this can lead to undefined behavior.

- Function versions created are immutable, this means that the container image and version cannot be updated for a function without creating a new version of the function.

### Security

- **Do not run containers as root user**: Running containers as root is not supported in Cloud Functions. Always specify a non-root user in your Dockerfile using the `USER` instruction.
- **Use Kubernetes Secrets**: For sensitive information like API keys, credentials, or tokens, use Kubernetes Secrets instead of environment variables. This provides better security and follows Kubernetes best practices for secret management.

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
