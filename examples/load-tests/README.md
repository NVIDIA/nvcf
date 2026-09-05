# Load Tests

[k6](https://grafana.com/docs/k6/latest/) load testing scripts for self-hosted NVCF function and NVCT task endpoints.

Many of the function tests target the [Load Tester Supreme](../function-samples/load-tester-supreme/) sample container. The task tests target the [task samples](../task-samples/) ported to the same cluster.

## Project Structure

```
functions/                  NVCF function load tests
  definitions/              Protocol buffer definitions (gRPC)
  test-configs/             k6 configuration files
llm-gateway/                LLM API gateway load tests
  lib/                      Shared helpers and custom metrics
  test-configs/             k6 configuration files
tasks/                      NVCT task load tests
  test-configs/             k6 configuration files
  *.sh                      Cleanup and counting helpers
```

## NVCF Function Tests

| Script | Description |
|--------|-------------|
| `supreme_http_test.js` | Basic HTTP request/response against the supreme sample. |
| `supreme_http_streaming_test.js` | HTTP streaming responses. |
| `supreme_http_test_multi_endpoint.js` | Random selection across multiple HTTP endpoints. |
| `supreme_http_sse_test.js` | Server-sent events via the [xk6-sse](https://github.com/phymbert/xk6-sse) extension. |
| `supreme_grpc_test.js` | Basic gRPC request/response. |
| `supreme_grpc_streaming_test.js` | gRPC streaming responses. |
| `supreme_large_response_test.js` | Large payload responses. |
| `oai_compatible_llm_stream_load_test.js` | Streaming OpenAI-compatible LLM completions. |
| `oai_compatible_llm_load_test.js` | Non-streaming OpenAI-compatible LLM completions. |
| `oai_compatible_responses_sse_load_test.js` | Streaming OpenAI Responses API benchmark with TTFT, ITL, and throughput metrics. |
| `oai_list_models_load_test.js` | OpenAI-compatible model listing endpoint. |
| `sdxl_load_test.js` | Stable Diffusion XL image generation. |
| `nvcf_health_load_test.js` | NVCF health endpoint. |
| `nvcf_list_functions_load_test.js` | NVCF function listing endpoint. |

### Function Test Configs

| Config | Description |
|--------|-------------|
| `k6_hammer_test_config.json` | High-intensity ramping arrival rate (up to 100k RPS). |
| `k6_long_scaling_test_config.json` | Multi-stage ramping VUs for extended scaling tests. |
| `k6_soak_test_config.json` | Extended duration testing. |
| `k6_large_response_soak_test_config.json` | Sustained load with large payloads. |
| `k6_large_request_test_config.json` | Large request payload testing. |
| `k6_regression_test_config.json` | Regression testing scenarios. |
| `k6_sse_streaming_test_config.json` | Server-sent events streaming configuration. |
| `k6_scratch_test_config.json` | Development/scratch config. |

## LLM API Gateway Tests

`llm-gateway/` holds tests that exercise the OpenAI-compatible gateway the way a customer
does, so traffic flows through the request router and the router client sidecar on the
worker. They target a configurable gateway URL and label every metric with the target, so
runs against different deployments stay comparable. See `llm-gateway/README.md`.

## NVCT Task Tests

Task tests hit the NVCT API at `${BASE_URL}/v2/orgs/${ORG_ID}/nvct/tasks`. The create test submits a single-GPU task image and varies the requested runtime per iteration; the list tests paginate tasks, task events, and task results.

| Script | Description |
|--------|-------------|
| `nvct_health_load_test.js` | NVCT health endpoint. |
| `nvct_create_task_load_test.js` | Creates tasks with randomised runtime between 2 and 10 minutes. |
| `nvct_list_task_load_test.js` | Paginates the task list endpoint. |
| `nvct_list_event_task_load_test.js` | Paginates task events for a given task ID. |
| `nvct_list_result_task_load_test.js` | Paginates task results for a given task ID. |

### Task Test Configs

| Config | Description |
|--------|-------------|
| `k6_100_iter_5_vu_config.json` | Warm-up: 100 iterations across 5 VUs. |
| `k6_1000_iter_25_vu_config.json` | Medium load: 1,000 iterations across 25 VUs. |
| `k6_10k_iter_100_vu_config.json` | Stress: 10,000 iterations across 100 VUs. |
| `k6_100k_iter_250_vu_config.json` | Peak: 100,000 iterations across 250 VUs. |

### Task Helper Scripts

| Script | Description |
|--------|-------------|
| `count_tasks.sh` | Count tasks in an org. Usage: `count_tasks.sh <org-id>`. Requires the `ngc` CLI. |
| `clear_all_tasks.sh` | Delete all tasks in an org in parallel. Usage: `clear_all_tasks.sh <org-id> [threads]`. Destructive; prompts before deleting. Requires the `ngc` CLI. |

These helpers use the `ngc` CLI today and target cloud NVCF. Porting them to self-hosted NVCT via REST is tracked as a follow-up.

## Environment Variables

### Supreme / Function Tests

| Variable | Description |
|----------|-------------|
| `TOKEN` | Authentication token. |
| `HTTP_SUPREME_NVCF_URL` | HTTP endpoint URL. |
| `NVCF_GRPC_URL` | gRPC endpoint URL. |
| `GRPC_SUPREME_FUNCTION_ID` | Function ID for gRPC calls. |
| `GRPC_SUPREME_FUNCTION_VERSION_ID` | Function version ID for gRPC calls |
| `SENT_MESSAGE_SIZE` | Payload size in bytes. |
| `RESPONSE_COUNT` | Number of response repeats. |
| `RESPONSE_DELAY_TIME` | Delay between responses in seconds (optional). |

### OpenAI-Compatible Tests

| Variable | Description |
|----------|-------------|
| `OAI_COMPAT_URL` | OpenAI-compatible API endpoint. |
| `TOKEN` | Optional Bearer token. Non-loopback endpoints must use HTTPS. |
| `LLM_MODEL_NAME` | Model identifier. |
| `OPENAI_RESPONSES_PROFILE` | `calibration` (default) or `load` for the Responses SSE benchmark. |
| `OPENAI_RESPONSES_VUS` | Virtual users for the Responses SSE benchmark. Defaults to 1 for calibration and 10 for load. |
| `OPENAI_RESPONSES_ITERATIONS` | Per-VU iterations for calibration. Defaults to 10. |
| `OPENAI_RESPONSES_MAX_DURATION` | Maximum duration for calibration. Defaults to `10m`. |
| `OPENAI_RESPONSES_DURATION` | Test duration for load. Defaults to `30s`. |
| `OPENAI_RESPONSES_TOKENS_PER_CHUNK` | Declared synthetic tokens in each output chunk. Defaults to 1. |
| `OPENAI_RESPONSES_EXPECTED_DELTAS` | Required text-delta count. Defaults to the configured output chunks for calibration and is disabled for load. |
| `OPENAI_RESPONSES_CALIBRATION_TOLERANCE_MS` | Allowed early-observation tolerance for calibration timing checks. Defaults to 10 ms. |
| `OPENAI_RESPONSES_INPUT` | Responses API input string. Defaults to `benchmark`. |
| `LOAD_TESTER_QUEUE_DELAY_MS` | Maps to `X-Load-Tester-Queue-Delay-Ms`. Sent by default only in calibration. |
| `LOAD_TESTER_TTFT_MS` | Maps to `X-Load-Tester-TTFT-Ms`. Sent by default only in calibration. |
| `LOAD_TESTER_TTFT_JITTER_MS` | Maps to `X-Load-Tester-TTFT-Jitter-Ms`. Sent by default only in calibration. |
| `LOAD_TESTER_ITL_MS` | Maps to `X-Load-Tester-ITL-Ms`. Sent by default only in calibration. |
| `LOAD_TESTER_ITL_JITTER_MS` | Maps to `X-Load-Tester-ITL-Jitter-Ms`. Sent by default only in calibration. |
| `LOAD_TESTER_OUTPUT_CHUNKS` | Maps to `X-Load-Tester-Output-Chunks`. Defaults to 8 and must not exceed the sample's startup chunk limit. |
| `LOAD_TESTER_CHUNK` | Maps to `X-Load-Tester-Chunk`. Defaults to `xxxx`. |
| `LOAD_TESTER_STREAM_ERROR_AFTER_CHUNKS` | Maps to `X-Load-Tester-Stream-Error-After-Chunks` for failure-path validation. |
| `LOAD_TESTER_STREAM_TRUNCATE_AFTER_CHUNKS` | Maps to `X-Load-Tester-Stream-Truncate-After-Chunks` for truncated-stream validation. |

### Multi-Endpoint Tests

| Variable | Description |
|----------|-------------|
| `ENDPOINTS` | Comma-separated list of endpoint URLs. |

### Task Tests

| Variable | Description |
|----------|-------------|
| `BASE_URL` | Cluster gateway base URL (no trailing slash). |
| `ORG_ID` | Org or NCA ID that owns the tasks. |
| `TOKEN` | NVCF API key (see `nvcf-cli api-key generate`). |
| `TASK_ID` | Task ID to target for list-event and list-result tests. |
| `CONTAINER_IMAGE` | Container image used by the create-task load test. |

## Running Tests

### Local Execution

```bash
k6 run functions/<script.js> \
  -e TOKEN=$TOKEN \
  -e HTTP_SUPREME_NVCF_URL=$HTTP_SUPREME_NVCF_URL \
  -e SENT_MESSAGE_SIZE=128 \
  -e RESPONSE_COUNT=10 \
  --vus 10 --duration 60s
```

### With a Configuration File

```bash
k6 run functions/<script.js> --config functions/test-configs/<config.json>
```

### Cloud Execution

```bash
k6 cloud functions/<script.js> --config functions/test-configs/<config.json> \
  -e TOKEN=$TOKEN -e HTTP_SUPREME_NVCF_URL=$HTTP_SUPREME_NVCF_URL
```

### SSE Testing Setup

SSE requires a custom k6 binary built with the [xk6-sse](https://github.com/phymbert/xk6-sse) extension.

Build it locally on Linux:

```bash
docker run --rm -it -u "$(id -u):$(id -g)" -v "${PWD}:/xk6" \
  grafana/xk6 build v0.55.2 --with github.com/phymbert/xk6-sse@v0.1.7
```

Then run with the local binary:

```bash
./k6 run functions/supreme_http_sse_test.js \
  --config functions/test-configs/k6_sse_streaming_test_config.json \
  -e TOKEN=$TOKEN -e HTTP_SUPREME_NVCF_URL=$HTTP_SUPREME_NVCF_URL
```

### OpenAI Responses SSE Benchmark

`oai_compatible_responses_sse_load_test.js` measures two start latencies: the
first SSE event and the first `response.output_text.delta`. It records one ITL
sample between each pair of text-delta events, then reports output chunks per
second and declared tokens per second. The output chunk and declared token
counters also provide aggregate rates for streams whose delta timestamps share
the same millisecond. A stream succeeds only after HTTP 200,
`response.completed`, no transport or protocol error, and an optional expected
delta count. For this script, `OAI_COMPAT_URL` must be the full
`/v1/responses` endpoint URL.

Metrics:

- `openai_responses_first_sse_event_ms`, `openai_responses_ttft_ms`, and `openai_responses_itl_ms`
- `openai_responses_output_chunks_per_second` and `openai_responses_declared_tokens_per_second`
- `openai_responses_stream_duration_ms`, `openai_responses_stream_success`, and stream outcome counters

The default calibration profile sends eight `xxxx` chunks with 200 ms TTFT and
50 ms ITL. It validates that those delays are not observed materially early:

```bash
./k6 run functions/oai_compatible_responses_sse_load_test.js \
  -e OAI_COMPAT_URL=http://127.0.0.1:8000/v1/responses
```

The load profile leaves queue, TTFT, and ITL delays unset unless their
`LOAD_TESTER_*` variables are supplied:

```bash
./k6 run functions/oai_compatible_responses_sse_load_test.js \
  -e OAI_COMPAT_URL=$OAI_COMPAT_URL \
  -e OPENAI_RESPONSES_PROFILE=load \
  -e OPENAI_RESPONSES_VUS=10 \
  -e OPENAI_RESPONSES_DURATION=30s
```

For a 60-second per-connection capacity run at 5 ms ITL, start the sample with
`LOAD_TESTER_MAX_OUTPUT_CHUNKS=12000`, then use calibration mode with 12000
chunks and two iterations per VU. Raise the generator file-descriptor limit
before a high-concurrency run. Set `OAI_COMPAT_URL` to the full
`/v1/responses` endpoint.

```bash
ulimit -n 65536

./k6 run functions/oai_compatible_responses_sse_load_test.js \
  --summary-export responses-sse-summary.json \
  -e OAI_COMPAT_URL=$OAI_COMPAT_URL \
  -e OPENAI_RESPONSES_PROFILE=calibration \
  -e OPENAI_RESPONSES_VUS=1024 \
  -e OPENAI_RESPONSES_ITERATIONS=2 \
  -e OPENAI_RESPONSES_MAX_DURATION=5m \
  -e OPENAI_RESPONSES_EXPECTED_DELTAS=12000 \
  -e OPENAI_RESPONSES_CALIBRATION_TOLERANCE_MS=1 \
  -e LOAD_TESTER_QUEUE_DELAY_MS=0 \
  -e LOAD_TESTER_TTFT_MS=1 \
  -e LOAD_TESTER_TTFT_JITTER_MS=0 \
  -e LOAD_TESTER_ITL_MS=5 \
  -e LOAD_TESTER_ITL_JITTER_MS=0 \
  -e LOAD_TESTER_CHUNK=xxxx \
  -e LOAD_TESTER_OUTPUT_CHUNKS=12000
```

The high-concurrency profile opens a new connection for each iteration. Check
generator file descriptors, sockets, CPU, and network saturation before
interpreting a failure as target capacity.

`openai_responses_declared_tokens_per_second` is synthetic. It multiplies the
observed chunk rate by `OPENAI_RESPONSES_TOKENS_PER_CHUNK`; `xxxx` is not a
tokenizer-derived token. Use `openai_responses_output_chunks_per_second` when
the chunk-to-token mapping is unknown.

## Resources

- [k6 Documentation](https://grafana.com/docs/k6/latest/)
- [xk6-sse Extension](https://github.com/phymbert/xk6-sse)
