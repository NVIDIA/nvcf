# Load Tests

[k6](https://grafana.com/docs/k6/latest/) load testing scripts for self-hosted NVCF function endpoints.

Many of the function tests target the [Load Tester Supreme](../function-samples/load-tester-supreme/) sample container.

## Project Structure

```
functions/                  NVCF function load tests
  definitions/              Protocol buffer definitions (gRPC)
  test-configs/             k6 configuration files
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
| `LLM_MODEL_NAME` | Model identifier. |

### Multi-Endpoint Tests

| Variable | Description |
|----------|-------------|
| `ENDPOINTS` | Comma-separated list of endpoint URLs. |

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

## Resources

- [k6 Documentation](https://grafana.com/docs/k6/latest/)
- [xk6-sse Extension](https://github.com/phymbert/xk6-sse)
