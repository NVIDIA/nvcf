# OpenAI-compatible sample

A controllable HTTP target for testing NVCF LLM functions with OpenAI client
libraries and load generators. It returns synthetic text and deterministic
embedding vectors. It is not a model server and does not implement the full
OpenAI API. It also serves a synthetic Anthropic Messages route for Anthropic
clients.

## Supported routes

| Route | Behavior |
|-------|----------|
| `POST /v1/chat/completions` | Chat completions with JSON or SSE output |
| `POST /v1/completions` | Legacy text completions with JSON or SSE output |
| `POST /v1/responses` | Responses API JSON or SSE output |
| `POST /v1/embeddings` | Embeddings for one string or an array of strings |
| `POST /v1/messages` | Anthropic Messages JSON or SSE output |
| `GET /v1/models` | Lists the sample model |
| `GET /v1/models/{id}` | Returns metadata for a model ID |
| `GET /health` | Health check returning 200 |

The sample accepts normal OpenAI request envelopes. It reads the standard
fields needed to select streaming and embedding output, but does not inspect
prompt or message content for benchmark tuning. Images, files, tools,
multimodal input, stored responses, retrieval, and token-array embeddings are
not implemented.

## Benchmark controls

Benchmark controls use either HTTP headers or top-level JSON body fields. Body
controls may appear anywhere in the JSON object. The normal OpenAI fields,
including `model`, `stream`, `input`, `messages`, and `prompt`, are still
decoded normally.

If any request header starts with `X-Load-Tester-`, all body controls are
ignored. Header controls and defaults apply instead. The default output chunk
is `xxxx` unless the container sets `LOAD_TESTER_DEFAULT_CHUNK`. Text controls apply to Chat Completions, Responses, Messages, and
legacy Completions. Queue delay, TTFT, status injection, and concurrency limits apply
to all POST routes.

| Control | Header | JSON body field | Default | Behavior |
|---------|--------|-----------------|---------|----------|
| Queue delay | `X-Load-Tester-Queue-Delay-Ms` | `x_load_tester_queue_delay_ms` | `0` | Delay before processing the request. |
| TTFT | `X-Load-Tester-TTFT-Ms` | `x_load_tester_ttft_ms` | `LOAD_TESTER_DEFAULT_TTFT_MS`, else `0` | Delay before the first response byte. |
| TTFT jitter | `X-Load-Tester-TTFT-Jitter-Ms` | `x_load_tester_ttft_jitter_ms` | `0` | Random extra delay from 0 through this value. |
| ITL | `X-Load-Tester-ITL-Ms` | `x_load_tester_itl_ms` | `LOAD_TESTER_DEFAULT_ITL_MS`, else `0` | Delay between streamed output chunks. |
| ITL jitter | `X-Load-Tester-ITL-Jitter-Ms` | `x_load_tester_itl_jitter_ms` | `0` | Random extra delay between chunks. |
| Chunk text | `X-Load-Tester-Chunk` | `x_load_tester_chunk` | `LOAD_TESTER_DEFAULT_CHUNK`, else `xxxx` | Text returned in each output chunk. |
| Chunk bytes | `X-Load-Tester-Chunk-Bytes` | `x_load_tester_chunk_bytes` | `0` | Generate a random chunk of this byte length. |
| Output chunks | `X-Load-Tester-Output-Chunks` | `x_load_tester_output_chunks` | `1` | Number of text chunks to return, capped by the startup limit. |
| Status injection | `X-Load-Tester-Status-Code` | `x_load_tester_status_code` | unset | Return an OpenAI-shaped HTTP error. |
| Stream error | `X-Load-Tester-Stream-Error-After-Chunks` | `x_load_tester_stream_error_after_chunks` | unset | End a stream with an OpenAI-shaped error after this many chunks. |
| Stream truncate | `X-Load-Tester-Stream-Truncate-After-Chunks` | `x_load_tester_stream_truncate_after_chunks` | unset | Close a stream without its completion event after this many chunks. |
| Concurrency limit | `X-Load-Tester-Max-Concurrency` | `x_load_tester_max_concurrency` | `0` | Return 429 when the global in-flight request count exceeds this value. |
| Echo | `X-Load-Tester-Echo` | `x_load_tester_echo` | `false` | Return the raw request body as the output text. |

Timing values are non-negative integer milliseconds and are capped at five
minutes. Output is capped at 1 MiB and the startup chunk limit. The
`LOAD_TESTER_MAX_OUTPUT_CHUNKS` environment variable defaults to 6000 and
accepts values from 1 through 60000. It is read once when the server starts.
`Chunk` and `Chunk-Bytes` cannot be combined. Stream error and truncate controls
are mutually exclusive.
For a deterministic concurrency test, send the same concurrency limit header
on every request in the load test.

## Container defaults

Three environment variables set defaults for one container. A request that
sets the matching control, by header or body field, still overrides them.

| Environment variable | Control | Accepted values |
|----------------------|---------|-----------------|
| `LOAD_TESTER_DEFAULT_TTFT_MS` | TTFT | Integer milliseconds from 0 through 300000 |
| `LOAD_TESTER_DEFAULT_ITL_MS` | ITL | Integer milliseconds from 0 through 300000 |
| `LOAD_TESTER_DEFAULT_CHUNK` | Chunk text | Text of at most 1 MiB |

The server reads them once at startup and exits with an error on an invalid
value. An empty value counts as unset. The `Chunk-Bytes` and echo controls
replace the default chunk text.

Use them when a request cannot choose the container that serves it. For
example, give two versions of one NVCF LLM function different defaults through
each version's container environment. The request router picks the version,
so only a container default can make one version slower. A distinct default
chunk for each version, such as `fast` and `slow`, shows in every response
which version answered.

## Echo control

Set `x_load_tester_echo` to `true`, or send `X-Load-Tester-Echo: true`, to
return the request body exactly as the sample received it. Use it to check that
a gateway forwards request fields without adding, dropping, or rewriting them.

Echo applies to Chat Completions and Responses, streaming and non-streaming.
The output text is the raw body, so parse it as JSON and compare fields. A body
control is part of the echoed body. Echo cannot be combined with `Chunk`,
`Chunk-Bytes`, or `Output-Chunks`, and the 1 MiB output cap applies. Other
routes reject the control with 400.

```bash
curl -sS http://localhost:8000/v1/chat/completions \
  --header 'Content-Type: application/json' \
  --data '{
    "model": "test-model",
    "messages": [{"role": "user", "content": "hello"}],
    "chat_template_kwargs": {"enable_thinking": false},
    "x_load_tester_echo": true
  }' | jq -r '.choices[0].message.content | fromjson'
```

## Anthropic Messages

`POST /v1/messages` returns synthetic text in the
[Anthropic Messages](https://platform.claude.com/docs/en/api/messages) format.
A request needs `model`, a non-negative integer `max_tokens`, and a non-empty
`messages` array. Other fields, such as `system`, `tools`, `thinking`, and
`stop_sequences`, are accepted and ignored. The `anthropic-version` and
`x-api-key` headers are not checked.

- Without `stream`, the response is a `message` object with one `text` content
  block, `stop_reason`, `stop_sequence: null`, and `usage`.
- With `stream: true`, the
  [event stream](https://platform.claude.com/docs/en/build-with-claude/streaming)
  sends `message_start`, `content_block_start`, `ping`, one
  `content_block_delta` (`text_delta`) per output chunk, `content_block_stop`,
  `message_delta`, and `message_stop`.
- Each output chunk counts as one output token. `usage.input_tokens` and the
  cache token fields are always 0.
- If `max_tokens` is lower than the output chunk count, output stops after
  `max_tokens` chunks and `stop_reason` is `max_tokens`. Otherwise it is
  `end_turn`. With `max_tokens: 0` the message has no content blocks. Stream
  error and truncate controls past the capped output have no effect.
- Every response has a `request-id` header. Errors use the Messages error shape,
  `{"type":"error","error":{"type":"...","message":"..."},"request_id":"..."}`.
- The error type follows the HTTP status: 400 `invalid_request_error`, 401
  `authentication_error`, 402 `billing_error`, 403 `permission_error`, 404
  `not_found_error`, 409 `conflict_error`, 413 `request_too_large`, 429
  `rate_limit_error`, 500 `api_error`, 504 `timeout_error`, and 529
  `overloaded_error`. Other 4xx codes use `invalid_request_error` and other 5xx
  codes use `api_error`.
- The stream error control sends an `error` event with type `api_error`. Stream
  error and truncate controls end the stream without `message_stop`.

Tool use, thinking blocks, images, documents, citations, stop sequences,
prompt caching, token counting, and Message Batches are not implemented.
`GET /v1/models` keeps the OpenAI shape.

## Build and run

```bash
cd examples/function-samples/openai-compatible-sample
docker build --platform linux/amd64 -t openai-compatible-sample .
docker run --rm -p 18000:8000 openai-compatible-sample
```

Release images are built with Bazel for linux/amd64 and linux/arm64. A
`feat`, `fix`, or `perf` commit under this directory cuts an
`examples/function-samples/openai-compatible-sample/vX.Y.Z` release, and the
release pipeline publishes that version. Build the same image locally with:

```bash
bazel build //examples/function-samples/openai-compatible-sample/http-server:image_index
```

## Smoke test

```bash
curl --request POST \
  --url http://localhost:18000/v1/chat/completions \
  --header 'Content-Type: application/json' \
  --header 'X-Load-Tester-Chunk: token' \
  --header 'X-Load-Tester-Output-Chunks: 3' \
  --data '{
    "model": "test-model",
    "messages": [{"role": "user", "content": "hello"}]
  }'

curl --request POST \
  --url http://localhost:18000/v1/responses \
  --header 'Content-Type: application/json' \
  --data '{
    "model": "test-model",
    "input": "hello",
    "stream": true,
    "x_load_tester_ttft_ms": 200,
    "x_load_tester_itl_ms": 50,
    "x_load_tester_chunk": "token",
    "x_load_tester_output_chunks": 3
  }'

curl --request POST \
  --url http://localhost:18000/v1/responses \
  --header 'Content-Type: application/json' \
  --header 'X-Load-Tester-TTFT-Ms: 200' \
  --header 'X-Load-Tester-ITL-Ms: 50' \
  --header 'X-Load-Tester-Chunk: token' \
  --header 'X-Load-Tester-Output-Chunks: 3' \
  --data '{
    "model": "test-model",
    "input": "hello",
    "stream": true
  }'

curl --request POST \
  --url http://localhost:18000/v1/embeddings \
  --header 'Content-Type: application/json' \
  --data '{"model": "test-model", "input": ["one", "two"], "encoding_format": "float"}'

curl --request POST \
  --url http://localhost:18000/v1/messages \
  --header 'Content-Type: application/json' \
  --header 'anthropic-version: 2023-06-01' \
  --header 'X-Load-Tester-TTFT-Ms: 200' \
  --header 'X-Load-Tester-ITL-Ms: 50' \
  --header 'X-Load-Tester-Chunk: token' \
  --header 'X-Load-Tester-Output-Chunks: 3' \
  --data '{
    "model": "test-model",
    "max_tokens": 1024,
    "stream": true,
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

## OpenAI Python client

Use a normal OpenAI client with the sample URL as its `base_url`:

```python
from openai import OpenAI

client = OpenAI(
    api_key="not-needed",
    base_url="http://localhost:18000/v1",
    _strict_response_validation=True,
)

response = client.responses.create(
    model="test-model",
    input="hello",
    extra_headers={
        "X-Load-Tester-Chunk": "token",
        "X-Load-Tester-Output-Chunks": "3",
    },
)
assert response.output_text == "tokentokentoken"

chat = client.chat.completions.create(
    model="test-model",
    messages=[{"role": "user", "content": "hello"}],
)
assert chat.choices[0].message.content == "xxxx"

embedding = client.embeddings.create(
    model="test-model",
    input=["one", "two"],
    encoding_format="float",
)
assert len(embedding.data) == 2
```

Run the included client compatibility check after starting the Go server:

```bash
cd examples/function-samples/openai-compatible-sample/http-server
go run .

# In another terminal, with the openai package installed:
python3 openai_client_check.py
```

## Anthropic Python client

Use the sample root URL, without `/v1`, as the Anthropic client `base_url`:

```python
import anthropic

client = anthropic.Anthropic(
    api_key="not-needed",
    base_url="http://localhost:18000",
    _strict_response_validation=True,
)

message = client.messages.create(
    model="test-model",
    max_tokens=1024,
    messages=[{"role": "user", "content": "hello"}],
    extra_headers={
        "X-Load-Tester-Chunk": "token",
        "X-Load-Tester-Output-Chunks": "3",
    },
)
assert message.content[0].text == "tokentokentoken"
assert message.stop_reason == "end_turn"

with client.messages.stream(
    model="test-model",
    max_tokens=2,
    messages=[{"role": "user", "content": "hello"}],
    extra_headers={"X-Load-Tester-Output-Chunks": "3"},
) as stream:
    text = "".join(stream.text_stream)
assert text == "xxxxxxxx"
assert stream.get_final_message().stop_reason == "max_tokens"
```

Run the included Messages compatibility check after starting the Go server:

```bash
cd examples/function-samples/openai-compatible-sample/http-server
go run .

# In another terminal, with the anthropic package installed:
python3 anthropic_client_check.py
```

## 60-second SSE capacity run

Use the matching pinned xk6 binary from `examples/load-tests`. The command
opens two approximately 60-second streams per VU at 5 ms ITL. Start the sample
with `LOAD_TESTER_MAX_OUTPUT_CHUNKS=12000` so it accepts the requested output
shape. Raise the generator file-descriptor limit before using high concurrency.

```bash
cd examples/load-tests
ulimit -n 65536

./k6 run functions/oai_compatible_responses_sse_load_test.js \
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

## NVCF LLM functions

Expose port 8000 and configure the function inference URL as `/`. Declare the
routes required by the workload, such as:

```text
/v1/chat/completions
/v1/completions
/v1/responses
/v1/embeddings
/v1/messages
```

See the [LLM Gateway guide](../../../docs/user/llm-gateway.md) for function
model configuration and invocation flow.
