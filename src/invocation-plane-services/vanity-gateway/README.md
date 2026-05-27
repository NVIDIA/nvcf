# Vanity Gateway Service

The Vanity Gateway fronts invocation APIs with route mappings from a YAML config
file. It supports two request shapes:

- OpenAI-compatible `/v1/*` endpoints that map public model names to functions.
- Vanity URL routes that forward caller-defined paths to function executions and polling.

The gateway is meant to run against an upstream invocation service with pexec
support. For local testing, populate `.env` with an API token and point
`NVCF_API_ENDPOINT` at the target API.

## Configuration

Configuration is passed through environment variables.

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `MAPPING_PATH` | Yes | None | Path to the rendered mapping config file. |
| `NVCF_API_ENDPOINT` | No | Service default | Upstream invocation service endpoint. In cluster deployments, this usually points to the in-cluster invocation service. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | No | Empty | OTLP endpoint for tracing. Empty disables OTLP export. |
| `TRACING_ACCESS_TOKEN` | No | Empty | Access token for OTLP tracing. Also configurable in the secrets file under `$.tracingAccessToken`. |
| `SECRETS_PATH` | No | `vault/secrets.json` | File used to read `tracingAccessToken` when `TRACING_ACCESS_TOKEN` is not set. |
| `PRIVATE_MODEL_NAME_REGEX_PATTERN` | No | See below | Regex used to hide private model names from `/v1/models`. |
| `POD_IP` | No | Empty | Added to trace attributes as `host.ip`. |
| `AWS_REGION` | No | Empty | Added to trace attributes as `host.dc`. |
| `SHADOW_MAX_CONCURRENT` | No | `256` | Maximum concurrent shadow requests. Non-positive values use the default. |

`PRIVATE_MODEL_NAME_REGEX_PATTERN` defaults to the value compiled into the
service. Override it when your deployment uses different private model naming
conventions.

## Configuration Sources

The gateway runtime reads one YAML file from `MAPPING_PATH`. That file contains
the `v2config` mapping for both OpenAI compatible routes and vanity routes.
Valid file updates are reloaded and the router is swapped without restarting the
process. Invalid updates are logged and skipped.

The gateway does not depend on how this file is produced. Self-managed
deployments can provide it directly with a Kubernetes ConfigMap:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: nvcf-ai-api-gateway-config
data:
  config.yaml: |
    v2config:
      vanity:
        ai_api:
          host: ai.api.example.com
          paths:
            sample_endpoint:
              path: /v1/example/infer
              functionID: 00000000-0000-0000-0000-000000000001
              functionVersionID: 00000000-0000-0000-0000-000000000002
              outgoingPathOverride: /inference
              usePexec: false
              eol: "2099-12-31T23:59:59Z"
              offlineMessage: ""
```

Mount the ConfigMap as a directory and point `MAPPING_PATH` at the file:

```yaml
containers:
  - name: nvcf-ai-api-gateway-service
    env:
      - name: MAPPING_PATH
        value: /etc/nvcf-ai-api-gateway/config/config.yaml
    volumeMounts:
      - name: gateway-config
        mountPath: /etc/nvcf-ai-api-gateway/config
        readOnly: true
volumes:
  - name: gateway-config
    configMap:
      name: nvcf-ai-api-gateway-config
```

Do not mount the ConfigMap file with `subPath`. Kubernetes does not propagate
ConfigMap updates into `subPath` mounts, so the gateway would not observe
mapping changes.

## Mapping Config

Map keys must not contain periods. Runtime routing uses `modelName`
for OpenAI-compatible endpoints and `path` for Vanity URL routes.

When `usePexec` is false or omitted, the gateway forwards to `NVCF_API_ENDPOINT`
with `function-id` and `function-version-id` headers. When `usePexec` is true,
the gateway rewrites the upstream path to the pexec endpoint instead.

```yaml
v2config:
  openai:
    host: api.example.com
    chatCompletions:
      meta_llama-3_1-8b-instruct:
        modelName: meta/llama-3.1-8b-instruct
        functionID: 00000000-0000-0000-0000-000000000001
        functionVersionID: 11111111-1111-1111-1111-111111111111
        usePexec: true
        tooManyRequestsMessage: "Try again later or use a partner endpoint."
        shadowModelNames:
          - private/meta/llama-3.1-8b-shadow
        shadowPercentage: 10
      private_meta_llama-3_1-8b-shadow:
        modelName: private/meta/llama-3.1-8b-shadow
        functionID: 00000000-0000-0000-0000-000000000002
        functionVersionID: 11111111-1111-1111-1111-111111111112
    completions: {}
    embeddings: {}
    responses: {}
    imageGenerations: {}
    imageEdits: {}
    imageVariations: {}
  vanity:
    example_ai:
      host: ai.example.com
      paths:
        sample_infer:
          path: /v1/example/infer
          functionID: 00000000-0000-0000-0000-000000000003
          functionVersionID: 11111111-1111-1111-1111-111111111113
        sample_pexec:
          path: /v1/example/pexec
          functionID: 00000000-0000-0000-0000-000000000004
          functionVersionID: 11111111-1111-1111-1111-111111111114
          usePexec: true
```

### OpenAI Mapping Fields

| Field | Required | Description |
| --- | --- | --- |
| map key | Yes | YAML map key for this entry. It cannot contain periods and does not have to match `modelName`. |
| `modelName` | Yes | Model name expected in the OpenAI-compatible request body. Public names appear in `/v1/models` unless hidden by the private model regex or expired by `eol`. |
| `functionID` | Yes | NVCF function ID to invoke. |
| `functionVersionID` | No | NVCF function version ID. When empty, the gateway targets the function without pinning a version. |
| `outgoingPathOverride` | No | Upstream path to use instead of the incoming OpenAI-compatible path. Ignored when `usePexec` is true. |
| `usePexec` | No | When `true`, route to `/v2/nvcf/pexec/functions/{functionID}` and append `/versions/{functionVersionID}` when set. |
| `eol` | No | RFC3339 timestamp. Future dates add a `Deprecation` header; past dates return `410 Gone` and hide the model from `/v1/models`. |
| `offlineMessage` | No | Non-empty value returns `503 Service Unavailable` with this message. |
| `tooManyRequestsMessage` | No | Message appended to upstream `429 Too Many Requests` responses for this model. |
| `shadowModelName` | No | Legacy single shadow target. Prefer `shadowModelNames` for new config. |
| `shadowModelNames` | No | Additional model names in the same OpenAI section that receive shadow traffic. Not supported for multipart image edit or variation endpoints. |
| `shadowPercentage` | No | Percentage of primary requests to shadow, from `1` to `100`. Defaults to `100` when shadow targets exist. |
| `shadowCancelOnClientDisconnect` | No | When `true`, cancels shadow work if the primary request context is canceled. Requires at least one shadow target. |

### Shadow Traffic Support

Shadow traffic is supported for OpenAI-compatible mapping sections with JSON
request bodies: `chatCompletions`, `completions`, `embeddings`, `responses`, and
`imageGenerations`. Shadow targets must be model names in the same OpenAI
section as the primary model. The gateway rewrites the request `model` field to
the shadow target and marks the replay with `NVCF-Shadow: true` so shadow
requests do not recursively shadow.

Shadow traffic is not supported for multipart image endpoints: `imageEdits` and
`imageVariations`. Config validation rejects shadow fields in those sections.

Admitted shadow requests are bounded by `SHADOW_MAX_CONCURRENT` and the gateway
shadow timeout. Normal primary response completion does not cancel shadow work.
When `shadowCancelOnClientDisconnect` is `true`, the gateway cancels shadow work
only if the client disconnects or cancels the primary request before the primary
response completes.

### Vanity Mapping Fields

| Field | Required | Description |
| --- | --- | --- |
| vanity map key | Yes | YAML map key for a host entry. It cannot contain periods. |
| `host` | Yes | Host header routed by this vanity entry. Each host gets its own route table. |
| path map key | Yes | YAML map key for a path entry. It cannot contain periods. |
| `path` | Yes | Incoming vanity URL path registered on `host`. |
| `functionID` | Yes | NVCF function ID to invoke. |
| `functionVersionID` | No | NVCF function version ID. When empty, the gateway targets the function without pinning a version. |
| `outgoingPathOverride` | No | Upstream path to use instead of the incoming vanity path. Ignored when `usePexec` is true. |
| `usePexec` | No | When `true`, route to `/v2/nvcf/pexec/functions/{functionID}` and append `/versions/{functionVersionID}` when set. |
| `eol` | No | RFC3339 timestamp. Future dates add a `Deprecation` header; past dates return `410 Gone`. |
| `offlineMessage` | No | Non-empty value returns `503 Service Unavailable` with this message. |
| shadow fields | No | Unsupported for Vanity URL routes. `shadowFunctionID`, `shadowFunctionVersionID`, and `shadowPercentage` are rejected during config validation. |

## Invoking OpenAI-compatible Endpoints

### Chat Completions

```shell
source .env
curl -v http://localhost:10081/v1/chat/completions \
-H "Host: api.example.com" \
-H "Content-Type: application/json" \
-H "Authorization: Bearer ${API_TOKEN}" \
-d '{
  "model": "meta/llama-3.1-8b-instruct",
  "messages": [
    {
      "role": "user",
      "content": "What is the history of the internet?"
    }
  ],
  "temperature": 0.2,
  "top_p": 0.7,
  "max_tokens": 1024,
  "stream": true
}'
```

```shell
source .env
curl -v http://localhost:10081/v1/chat/completions \
-H "Host: api.example.com" \
-H "Content-Type: application/json" \
-H "Authorization: Bearer ${API_TOKEN}" \
-d '{
"model": "facebook/opt-125m",
"messages": [
{"role": "system", "content": "You are a helpful assistant."},
{"role": "user", "content": "Who won the world series in 2020?"}
],
"stream": true
}'
```

### Completions

```shell
curl http://localhost:10081/v1/completions \
-H "Host: api.example.com" \
-H "Content-Type: application/json" \
-H "Authorization: Bearer ${API_TOKEN}" \
-d '{
"model": "microsoft/phi-2",
"prompt": "Hello there"
}'
```

### Embeddings

```shell
curl http://localhost:10081/v1/embeddings \
-H "Host: api.example.com" \
-H "Content-Type: application/json" \
-H "Authorization: Bearer ${API_TOKEN}" \
-d '{
"model": "thenlper/gte-base",
"input": "Your text string goes here"
}'
```

### Models

```shell
curl -si -H "Host: api.example.com" http://localhost:10081/v1/models
```

```shell
curl -si -H "Host: api.example.com" http://localhost:10081/v1/models/facebook/opt-125m
```

## Invoking Vanity URL Routes

```shell
source .env
curl -v http://localhost:10081/v1/example/infer \
-H "Host: ai.example.com" \
-H "Content-Type: application/json" \
-H "Authorization: Bearer ${API_TOKEN}" \
-H 'NVCF-POLL-SECONDS: 1' \
-d '{

  <Your data>

}'
```

To test polling for a vanity URL, first invoke a long-running function that returns a
`202` response and `requestId`. Then poll the status endpoint.

```shell
source .env
curl -v http://localhost:10081/v1/status/{requestId} \
-H "Host: ai.example.com" \
-H "Content-Type: application/json" \
-H "Authorization: Bearer ${API_TOKEN}"
```

## Running the Vanity Gateway with Docker

```shell
docker build . -t apigw
docker run --rm -it \
-v "$PWD/config.yaml:/config/config.yaml:ro" \
-e OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:8360 \
-e MAPPING_PATH=/config/config.yaml \
-e NVCF_API_ENDPOINT=https://api.example.com \
-p 10081:10081 \
-p 10083:10083 \
apigw
```

## Health and Metrics

```shell
curl -v localhost:10081/health
curl -v -H 'Host: api.example.com' localhost:10081/health
curl -v localhost:10083/metrics
```

## Running a Local OpenAI-compatible Model

Use this only when validating OpenAI-compatible request bodies against a local model server.
Gateway invocations still require mappings and an NVCF upstream.

### `/v1/chat/completions`

```shell
docker run --gpus all \
  -v ~/.cache/huggingface:/root/.cache/huggingface \
  -p 8000:8000 --ipc=host \
  vllm/vllm-openai:latest --model facebook/opt-125m
```

```shell
ssh -L 8000:localhost:8000 192.168.0.80
```

```shell
curl http://localhost:8000/v1/chat/completions \
-H "Host: api.example.com" \
-H "Content-Type: application/json" \
-d '{
"model": "facebook/opt-125m",
"messages": [
{"role": "system", "content": "You are a helpful assistant."},
{"role": "user", "content": "Who won the world series in 2020?"}
],
"stream": true
}'
```

### `/v1/embeddings`

```shell
docker run --gpus all -p 8080:80 -v $PWD:/data --pull always ghcr.io/huggingface/text-embeddings-inference:1.2 --model-id thenlper/gte-base
```

## Known Limitations

- HTTP polling invocations that last longer than 20 minutes return a `504 Timeout` error when using OpenAI-compatible endpoints. This does not apply to Vanity URL routes.
- Streaming invocations that last longer than 5 minutes return a `502 Timeout` error when using OpenAI-compatible endpoints. This does not apply to Vanity URL routes.
