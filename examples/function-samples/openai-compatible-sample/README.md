# OpenAI-compatible sample

A controllable HTTP target for testing NVCF LLM functions with OpenAI client
libraries and load generators. It returns synthetic text and deterministic
embedding vectors. It is not a model server and does not implement the full
OpenAI API.

## Supported routes

| Route | Behavior |
|-------|----------|
| `POST /v1/chat/completions` | Chat completions with JSON or SSE output |
| `POST /v1/completions` | Legacy text completions with JSON or SSE output |
| `POST /v1/responses` | Responses API JSON or SSE output |
| `POST /v1/embeddings` | Embeddings for one string or an array of strings |
| `GET /v1/models` | Lists the sample model |
| `GET /v1/models/{id}` | Returns metadata for a model ID |
| `GET /health` | Health check returning 200 |

The sample accepts normal OpenAI request envelopes. It reads the standard
fields needed to select streaming and embedding output, but does not inspect
prompt or message content for benchmark tuning. Images, files, tools,
multimodal input, stored responses, retrieval, and token-array embeddings are
not implemented.

## Benchmark controls

All benchmark controls are HTTP headers. The default output chunk is `xxxx`.
The text headers apply to Chat Completions, Responses, and legacy Completions.
Queue delay, TTFT, status injection, and concurrency limits apply to all POST
routes.

| Control | Header | Default | Behavior |
|---------|--------|---------|----------|
| Queue delay | `X-Load-Tester-Queue-Delay-Ms` | `0` | Delay before processing the request. |
| TTFT | `X-Load-Tester-TTFT-Ms` | `0` | Delay before the first response byte. |
| TTFT jitter | `X-Load-Tester-TTFT-Jitter-Ms` | `0` | Random extra delay from 0 through this value. |
| ITL | `X-Load-Tester-ITL-Ms` | `0` | Delay between streamed output chunks. |
| ITL jitter | `X-Load-Tester-ITL-Jitter-Ms` | `0` | Random extra delay between chunks. |
| Chunk text | `X-Load-Tester-Chunk` | `xxxx` | Text returned in each output chunk. |
| Chunk bytes | `X-Load-Tester-Chunk-Bytes` | `0` | Generate a random chunk of this byte length. |
| Output chunks | `X-Load-Tester-Output-Chunks` | `1` | Number of text chunks to return. |
| Status injection | `X-Load-Tester-Status-Code` | unset | Return an OpenAI-shaped HTTP error. |
| Stream error | `X-Load-Tester-Stream-Error-After-Chunks` | unset | End a stream with an OpenAI-shaped error after this many chunks. |
| Stream truncate | `X-Load-Tester-Stream-Truncate-After-Chunks` | unset | Close a stream without its completion event after this many chunks. |
| Concurrency limit | `X-Load-Tester-Max-Concurrency` | `0` | Return 429 when the global in-flight request count exceeds this value. |

Timing values are non-negative integer milliseconds and are capped at five
minutes. Output is capped at 1 MiB and 4096 chunks. `Chunk` and `Chunk-Bytes`
cannot be combined. Stream error and truncate controls are mutually exclusive.
For a deterministic concurrency test, send the same concurrency limit header
on every request in the load test.

## Build and run

```bash
cd examples/function-samples/openai-compatible-sample
docker build --platform linux/amd64 -t openai-compatible-sample .
docker run --rm -p 18000:8000 openai-compatible-sample
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

## NVCF LLM functions

Expose port 8000 and configure the function inference URL as `/`. Declare the
routes required by the workload, such as:

```text
/v1/chat/completions
/v1/completions
/v1/responses
/v1/embeddings
```

See the [LLM Gateway guide](../../../docs/user/llm-gateway.md) for function
model configuration and invocation flow.
