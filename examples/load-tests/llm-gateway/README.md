# LLM API Gateway Load Tests

[k6](https://grafana.com/docs/k6/latest/) load tests that hit the LLM API gateway the way a
customer does. Traffic follows the full invocation path: gateway, request router, and the
router client sidecar on the worker.

These differ from the tests in `../functions/`, which target NVCF function endpoints
directly. Everything here goes through the OpenAI-compatible gateway surface.

## Scripts

One script per gateway endpoint, so a failure points at a single handler.

| Script | Endpoint | Notes |
|--------|----------|-------|
| `chat_completions_test.js` | `POST /v1/chat/completions` | Non-streaming completion. |
| `chat_completions_stream_test.js` | `POST /v1/chat/completions` | Streaming completion, `stream: true`. |
| `responses_test.js` | `POST /v1/responses` | Responses API. |
| `embeddings_test.js` | `POST /v1/embeddings` | Embeddings. |
| `gateway_health_test.js` | `GET /healthz`, `/readyz`, `/info` | Gateway process health. Never reaches the router or a worker. |

These are the only routes the gateway registers. Handlers for audio transcription,
translation, and speech to speech exist in the source but are not wired into a route, so
they are not covered here.

## Test configs

| Config | Description |
|--------|-------------|
| `k6_llm_smoke_config.json` | 2 requests per second for 30 seconds. Run this first to confirm wiring. |
| `k6_llm_breakpoint_config.json` | Ramping arrival rate, 5 to 50 requests per second over 9 minutes. |

Both use an open workload model (arrival rate, not virtual users), so queueing shows up as
latency rather than the load generator throttling itself.

Start with the smoke config. Confirm wiring and a clean baseline before ramping.

## Environment variables

| Variable | Required | Description |
|----------|----------|-------------|
| `TOKEN` | Yes | Bearer token with permission to invoke the function. |
| `LLM_GATEWAY_URL` | Yes | Base URL of the gateway to test. No trailing slash needed. |
| `LLM_FUNCTION_ID` | Yes | Function ID. The gateway requires it as a model prefix. |
| `LLM_MODEL_NAME` | Yes | Model name as declared on the function. |
| `LLM_REGION` | No | Free-form label applied to every metric. Defaults to `default`. |
| `LLM_INSECURE_SKIP_TLS_VERIFY` | No | Set to `true` when addressing an ingress directly. |
| `LLM_REQUEST_TIMEOUT_MS` | No | Request timeout in milliseconds. Defaults to 300000. |

The model sent to the gateway is built as `${LLM_FUNCTION_ID}/${LLM_MODEL_NAME}`. A bare
model name is rejected with a 400.

The timeout default is deliberately well above the k6 default of 60 seconds. A queued
request that outlives the timeout is recorded as a failed request, which reports saturation
as breakage.

## Running

Smoke test first:

```bash
k6 run llm-gateway/chat_completions_test.js \
  --config llm-gateway/test-configs/k6_llm_smoke_config.json \
  -e TOKEN=$TOKEN \
  -e LLM_GATEWAY_URL=$LLM_GATEWAY_URL \
  -e LLM_FUNCTION_ID=$LLM_FUNCTION_ID \
  -e LLM_MODEL_NAME=$LLM_MODEL_NAME
```

Then a breakpoint run:

```bash
k6 run llm-gateway/chat_completions_test.js \
  --config llm-gateway/test-configs/k6_llm_breakpoint_config.json \
  -e TOKEN=$TOKEN \
  -e LLM_GATEWAY_URL=$LLM_GATEWAY_URL \
  -e LLM_FUNCTION_ID=$LLM_FUNCTION_ID \
  -e LLM_MODEL_NAME=$LLM_MODEL_NAME \
  -e LLM_REGION=primary
```

Scripts import from `lib/common.js` using a relative path, so run them from the
`examples/load-tests` directory as shown.

## Choosing a target

Two independent things get called region, and mixing them up produces confusing results.

Client location is where the load generator runs. Locally that is your machine. On k6 Cloud
it is the configured load zone.

Serving location is the gateway and router that handle the request. `LLM_GATEWAY_URL`
selects this.

The two interact whenever the published gateway name is backed by anycast routing, because
then the serving location is chosen by the client location. Running from several client
locations and getting healthy results does not mean the matching serving locations were
exercised. Point `LLM_GATEWAY_URL` at a specific ingress when the question is about one
deployment.

Addressing an ingress directly usually means the certificate will not match the address you
dialed, so set `LLM_INSECURE_SKIP_TLS_VERIFY=true` for those runs. Prefer a hostname over a
literal IP so DNS can spread load across all availability zone nodes.

Set `LLM_REGION` to label each run. Every metric carries it, so results from separate runs
stay attributable.

To drive traffic from other client locations, add a `cloud` block with load zones to a copy
of a config and run `k6 cloud` instead of `k6 run`. The configs under
`../functions/test-configs/` show the block format.

## Metrics

Beyond the k6 defaults, every non-200 response increments `http_errors`, tagged with
`status`, `endpoint`, and `region`.

The breakdown by status code is the useful part, so read it rather than a single total. A
429 is the rate limiter and is not a capacity finding. A 503 is the gateway declining to
serve. A 404 usually means the routing target was not found. Which of these a deployment
produces changes over time, so the counter deliberately records the code instead of
special-casing a particular failure.

```
k6 run ... --summary-export summary.json   # http_errors carries the status tag
```

Thresholds gate on the overall failure rate and the 95th percentile latency. Individual
status codes do not abort a run, so hitting the rate limiter does not end a test early.

## Before reading any numbers

Check the deployment first. A function deployed with `minInstances: 1`, `maxInstances: 1`,
and `maxRequestConcurrency: 1` admits one request at a time, so a load test measures a
one-slot queue rather than the stack. Raise the instance count and request concurrency
before running a breakpoint test.

Three more limits worth knowing:

- Deployments may enforce per-user and per-account rate limiting. Expect 429s under load
  unless the account is exempted. They are counted separately for this reason.
- If the gateway does not cache invocation auth, every request pays the full auth path, and
  that can saturate before anything downstream does. Read a latency knee with that in mind
  rather than attributing it to the router or the worker.
- The streaming test reports time to last token. k6 buffers the whole event stream, so time
  to first token needs a k6 binary built with
  [xk6-sse](https://github.com/phymbert/xk6-sse). The build command is in `../README.md`.

A worker deployed in a different location than the gateway handling the request adds a
network hop, which shows up as higher latency. That is topology, not a gateway regression.

## Further reading

- [NVCF HTTP load testing guide](https://docs.nvidia.com/nvcf/http-load-testing): k6 install
  steps and the local run workflow.
- [k6 documentation](https://grafana.com/docs/k6/latest/).
