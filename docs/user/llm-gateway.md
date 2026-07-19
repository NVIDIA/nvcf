# LLM Gateway

The LLM Gateway handles OpenAI-compatible invocation for NVCF LLM functions in
self-managed deployments. Use LLM functions when requests should enter through
the LLM invocation route and NVCF should route them by function and model.

Self-managed deployments usually expose the LLM invocation route through the
gateway load balancer and use the `Host` header for routing:

```bash
export GATEWAY_ADDR=<gateway-address>
```

Requests use the OpenAI `model` field in this format:

```text
<function-id>/<model-name>
```

The gateway uses `<function-id>` as the routing key for NVCF authorization and backend selection. It forwards `<model-name>` to the upstream model server.

Requests must already be OpenAI-compatible when they reach the LLM invocation route. Any model-specific prompt formatting or tokenizer behavior belongs in the upstream model server.

## Request Flow

The LLM Gateway path has these runtime components:

![LLM invocation path](images/nvcf-llm-invocation-path.svg)

1. Client sends an OpenAI-compatible request to the LLM invocation route.
2. LLM API Gateway extracts the routing key from the `model` field, validates authorization, applies request and token rate limits, and validates endpoint-specific request fields.
3. LLM API Gateway forwards the request to LLM request router with routing metadata such as request ID, routing key, model name, routing method, token estimate, and cache affinity key when present.
4. LLM request router selects a healthy backend for the requested function and model.
5. The `pylon` sidecar on the selected workload forwards the request to the user container through the configured inference port.
6. The user container handles the OpenAI-compatible route and returns the response through the same path.

The function container must expose the declared OpenAI-compatible paths on its inference port. Use `inferenceUrl: "/"` for LLM functions unless the container needs a different base path.

### Multi-Cluster View

In a global deployment, DNS or a custom front door selects a regional
`llm.invocation.<domain>` endpoint. The LLM Gateway can use NATS for
cross-cluster usage-state chatter. LLM worker request streams still use the
request router and worker gateway path, not NATS worker streams. The
worker-gateway arrows show that routers can target local or remote worker
gateways when those workers are registered into the router mesh.

![LLM multi-cluster invocation path](images/nvcf-llm-multicluster-invocation.svg)

## Function Configuration

Set `functionType` to `LLM` and define model routing metadata under `models[].llmConfig`.

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

`llmConfig.uris` declares the OpenAI-compatible paths the model supports. Supported LLM paths are:

| Path | Behavior |
| --- | --- |
| `/v1/chat/completions` | Supports streaming and non-streaming chat completion requests. |
| `/v1/responses` | Supports native Responses API requests. Streaming clients receive server-sent events (SSE). Non-streaming clients receive the terminal Responses JSON object. |
| `/v1/embeddings` | Supports embeddings requests with string or string array input. |

`llmConfig.routingMethod` accepts `round_robin`, `power_of_two`, `groq_multiregion`, `pulsar`, or `random`.

`llmConfig.tokenRateLimit` applies a per-model token limit. Use one or more comma-separated limits in `<value>-<unit>` format, where `<value>` is a positive integer and `<unit>` is one of `S` (seconds), `M` (minutes), `H` (hours), `D` (days), or `W` (weeks). A single limit is one token budget over one time window, such as `1000-S`. A combined limit is multiple token budgets over distinct time windows, such as `1000-S,5000-M,100000-H,500000-D,1000000-W`; do not repeat a unit in the same value.

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

These per-model routing fields are mutable. Use `nvcf-cli function update --llm-model-update "name=<model>,routingMethod=<method>,tokenRateLimit=<limit>"` or JSON `modelUpdates` to change them without recreating the function version.

For request admission and rate limiting, the gateway uses request estimates until the upstream service returns usage data. Do not depend on gateway-side exact token counts.

## Endpoint Behavior

### Chat Completions

Use `/v1/chat/completions` for OpenAI-compatible chat requests:

```bash
curl -sS -X POST "http://${GATEWAY_ADDR}/v1/chat/completions" \
  -H "Host: llm.invocation.${GATEWAY_ADDR}" \
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

When `stream` is `true`, the gateway relays server-sent events. When `stream` is false or omitted, the gateway returns the final JSON response from the upstream service.

### Responses

Use `/v1/responses` for native Responses API requests:

```bash
curl -sS -X POST "http://${GATEWAY_ADDR}/v1/responses" \
  -H "Host: llm.invocation.${GATEWAY_ADDR}" \
  -H "Authorization: Bearer ${NVCF_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<function-id>/dummy-model",
    "input": "Write a one sentence summary of NVCF.",
    "stream": false
  }'
```

The gateway requires the routed model to declare `/v1/responses` in `llmConfig.uris` when model specs are available. The gateway proxies native Responses requests upstream instead of converting them to chat completions.

For upstream compatibility, the gateway sends native Responses requests to the router as streaming requests. If the client requested streaming, the gateway relays SSE. If the client requested a non-streaming response, the gateway consumes the upstream SSE stream and returns the terminal Responses JSON object.

### Embeddings

Use `/v1/embeddings` for OpenAI-compatible embeddings requests:

```bash
curl -sS -X POST "http://${GATEWAY_ADDR}/v1/embeddings" \
  -H "Host: llm.invocation.${GATEWAY_ADDR}" \
  -H "Authorization: Bearer ${NVCF_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<function-id>/dummy-model",
    "input": "NVCF embeddings check"
  }'
```

The gateway accepts `input` as a string or an array of strings. Empty input is rejected. The request can include up to 2048 input entries.

Embeddings requests do not use session stickiness.

## Model Routing And Upstream Paths

The client-facing `model` value must include a routing key and model name:

```text
<function-id>/<model-name>
```

The gateway uses `<function-id>` to authorize the request and select the NVCF function deployment. It rewrites the upstream model to `<model-name>` before forwarding the request.

The configured `llmConfig.uris` must match the paths served by the container. For example, a model that declares `/v1/responses` must have a container backend that can answer `POST /v1/responses`.

## Session Stickiness

The LLM Gateway supports sticky routing for multi-turn OpenAI-compatible requests on `/v1/chat/completions` and `/v1/responses`.

Sticky routing is not supported on `/v1/embeddings`.

To keep later requests routed to the same backend, send the `x-multi-turn-session-id` response header value back as the `x-multi-turn-session-id` request header on the next request.

The gateway chooses the sticky routing key in this order:

| Endpoint | Precedence |
| --- | --- |
| `/v1/responses` | `prompt_cache_key`, `conversation.id`, `x-multi-turn-session-id`, input hash fallback |
| `/v1/chat/completions` | `x-multi-turn-session-id`, messages hash fallback |

For Responses API follow-up calls, `previous_response_id` does not override the sticky routing key. Continue sending `prompt_cache_key`, `conversation.id`, or the returned `x-multi-turn-session-id` header when the next request needs the same backend affinity.

Sticky routing only affects backend selection when the LLM request router is configured with a cache-affinity-aware routing method for the target model. Clients should only use `x-multi-turn-session-id`. The gateway derives and forwards the internal `x-cache-affinity-key`; clients should not send that header.

## Metrics

LLM API Gateway request metrics include a `function_id` label. The value is the
function ID extracted from the request routing key. Requests without a routing
key, such as health checks, use `function_id="none"`.

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `llm_api_gateway_http_requests_total` | Counter | `method`, `route`, `status`, `function_id` | Inbound HTTP requests. |
| `llm_api_gateway_http_request_duration_seconds` | Histogram | `method`, `route`, `status`, `function_id` | Inbound HTTP request latency. |
| `llm_api_gateway_http_active_requests` | Up-down counter | `method`, `route`, `function_id` | In-flight inbound HTTP requests. |
| `llm_api_gateway_upstream_requests_total` | Counter | `upstream`, `result`, `status`, `function_id` | Requests sent to an upstream provider. |
| `llm_api_gateway_upstream_request_duration_seconds` | Histogram | `upstream`, `result`, `status`, `function_id` | Upstream provider request latency. |
| `llm_api_gateway_llm_tokens_total` | Counter | `endpoint`, `token_type`, `stream`, `function_id` | Token counts reported by upstream providers. |
| `llm_api_gateway_provider_time_seconds` | Histogram | `endpoint`, `phase`, `stream`, `function_id` | Provider-reported timing phases. |
| `llm_api_gateway_stream_first_token_seconds` | Histogram | `endpoint`, `function_id` | Time from stream request start to the first token. |
| `llm_api_gateway_stream_duration_seconds` | Histogram | `endpoint`, `status`, `function_id` | Total stream duration. |

Infrastructure metrics for authentication, rate limit synchronization, pub/sub,
and the distributed cache do not include `function_id` because they are not
associated with a single routed function.

Use the label to calculate request rate by function:

```promql
sum by (function_id) (
  rate(llm_api_gateway_http_requests_total[5m])
)
```

Use the histogram label to calculate p95 request latency by function:

```promql
histogram_quantile(
  0.95,
  sum by (le, function_id) (
    rate(llm_api_gateway_http_request_duration_seconds_bucket[5m])
  )
)
```

## Operational Notes

- The LLM invocation route is served by the `llm.invocation.<domain>` hostname.
- The LLM API Gateway chart and LLM request router chart are installed as self-managed stack components.
- The Gateway Routes chart creates the external HTTPRoute for LLM invocation.
- Token rate limits are evaluated per model when `llmConfig.tokenRateLimit` is set.
- The LLM request router and `pylon` worker sidecar images must be from a stack release that includes the endpoint support documented here.
