# llm-api-gateway

`llm-api-gateway` is an OpenAI-compatible gateway for routing chat,
responses, and embeddings traffic to NVCF functions through Stargate.

Requests reach this gateway as OpenAI-compatible payloads. The gateway does
not render Hugging Face or Jinja chat templates, does not tokenize prompts, and
does not require LPU vendored modules. Token accounting uses gateway estimates
for admission and routing hints until backend usage is returned.

## Build with Bazel

Bazel is the canonical build path.

```shell
bazel build //...
bazel test //... --flaky_test_attempts=3

bazel build //:image_index
bazel build //:rate_limit_sync_worker_image_index

bazel run //:gazelle
bazel mod tidy
```

Internal push targets are defined under `nvidia-internal`.

## Supported API Surface

The gateway currently serves:

- `GET /healthz`
- `GET /readyz`
- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/embeddings`

## Request Routing

Each request is normalized into a function-scoped request context.

- `X-NVCF-Function-ID` selects the configured function.
- For chat and responses requests, if the header is omitted, the gateway
  expects `model` to use `<function_id>/<model>` and derives the function id
  from that prefix.
- For JSON inference endpoints, the gateway rewrites `model` to the configured
  downstream model before forwarding to Stargate.
- For multipart endpoints, function selection should be explicit through
  `X-NVCF-Function-ID`; the multipart payload is preserved and the configured
  downstream model is forwarded through headers.
- `Authorization: Bearer ...` is treated as the caller principal for telemetry
  and is forwarded to NVCF gRPC auth when that adapter is configured.
- `X-Request-ID` is accepted if present, otherwise the gateway generates one.
- `X-NVCF-Target-Region` is forwarded into the request context.
- `X-Priority` is reserved for the gateway and derived from the caller's
  resolved priority; a client-supplied `X-Priority` on the LLM endpoints is
  rejected with 400.

Configured functions control the downstream `model`, service tier, routing
method, and per-function rate limits. Prompt rendering and exact prompt
tokenization are not gateway-owned surfaces.

When a request is forwarded to Stargate, the gateway emits routing headers for
the selected function/model and estimated prompt size, including
`x-routing-key`, `x-model`, `x-input-tokens`, and `x-token-estimate`.

For OpenAI-compatible multi-turn stickiness, chat completions and responses
accept `prompt_cache_key` and return the selected session value in
`x-multi-turn-session-id`. Clients can send the same `prompt_cache_key` or
persist the response header and send it on later requests for the same
conversation. The gateway preserves the raw body field for the model backend.
It forwards only a SHA-256-derived value in the internal
`x-cache-affinity-key` header to Stargate.

### Last-cluster hints

With `STARGATE_LAST_CLUSTER_ENABLED=true`, the gateway remembers which
Stargate cluster served each session's last successful request and sends it
back on the session's next request as `x-stargate-last-cluster-id`. Stargate
uses the hint only for models whose routing algorithm sets
`last_cluster_affinity`.

- A session is the tuple of routing key, model, and cache affinity key.
- Sessions from `prompt_cache_key`, `conversation_id`, or the
  `x-multi-turn-session-id` or `x-claude-code-session-id` header are eligible.
  Payload-derived sessions are also eligible. No extra setting is needed.
- A payload-derived affinity key hashes the full message list, which changes
  every turn. For these sessions the gateway keys the store by a prefix chain
  instead:
  - It writes the cluster under a hash of the full conversation.
  - It looks up under a hash of the conversation before the last contiguous
    run of model-output items. For chat and Messages these are assistant
    messages. For Responses they are assistant `message`, `reasoning`,
    `function_call`, `custom_tool_call`, `web_search_call`,
    `code_interpreter_call`, `mcp_call`, `mcp_list_tools`, and
    `mcp_approval_request` items.
  - Messages hashes `system` first. Responses hashes `instructions` and
    `prompt` first.
  - Values come from the raw request JSON. Object key order and whitespace
    outside strings do not change them. They are scoped by endpoint, org and
    project, routing key, and model.
  - Clients that append to their history and resend it verbatim, including tool
    loops, find the cluster that served the previous turn. Edited, truncated,
    or summarized history misses and routes as a new session when the change
    falls in the part hashed for lookup. Assistant prefill continuation is
    outside this guarantee and usually misses. Responses requests with
    `previous_response_id` or an `item_reference` item are skipped.
  - A first turn has no lookup value. It skips the store read, counts as
    `miss`, and is written on 2xx.
  - Delivery is best-effort. A turn that arrives before the previous write
    lands, reaches a replica with a different local store, or arrives after
    the TTL routes as a new session.
  - `CacheAffinityKey`, the `x-cache-affinity-key` header, and hash-ring and
    Pulsar placement do not change.
- The gateway writes the `x-stargate-cluster-id` of a 2xx Stargate response
  when the response headers arrive, before a stream ends. Non-2xx responses and
  transport errors never change the stored value. Writes run in the background
  and never delay or fail the client response.
- The lookup is bounded by `STARGATE_LAST_CLUSTER_LOOKUP_TIMEOUT`. On a miss,
  error, or timeout the request proceeds without the hint.
- At most 1024 lookups and 1024 writes may wait on the store at once, counting
  calls that outlived their timeout. When a cap is reached, a lookup returns at
  once without the hint and counts as `timeout`, and a write is dropped and
  counts as `error`. Requests never wait for a slot.
- On shutdown the gateway stops serving, waits for in-flight requests, drains
  pending writes, and then stops the Olric node. The drain and the Olric
  shutdown are each bounded by `OLRIC_SHUTDOWN_TIMEOUT`.
- Store keys are `lc:v1:` plus the hex SHA-256 of the length-prefixed routing
  key, model, and cache affinity key or payload prefix-chain value. Raw routing
  keys never appear in store keys. Affinity keys, session IDs, and message
  content never appear in store keys or logs. Cluster IDs over 256 bytes are
  not stored.
- With `OLRIC_ENABLED=true`, entries live in the `stargate-last-cluster` DMap
  of the embedded Olric cluster, so every replica sees them. With Olric off,
  each replica keeps its own LRU capped at
  `STARGATE_LAST_CLUSTER_LOCAL_MAX_ENTRIES`. Every write resets the entry TTL.
- Header-keyed sessions overwrite one entry each. Payload chains add roughly
  one entry per successful payload request per TTL window, so 100 requests per
  second with the default `10m` TTL hold about 60,000 entries. Olric has no
  entry cap. The local store keeps its
  `STARGATE_LAST_CLUSTER_LOCAL_MAX_ENTRIES` cap.
- The gateway always strips a client-supplied `x-stargate-last-cluster-id`,
  whether or not the feature is enabled, and keeps stripping `x-stargate-*`
  response headers from client responses.

When `NVCF_GRPC_ADDR` is configured, the gateway authenticates each request
through the NVCF LLM gRPC auth service, derives the per-caller rate-limit key
from `authContext["ncaId"]`, optionally scopes it further by project, and keeps
final token consumption accounting in the gateway after completion or stream
close.

## Prerequisites

- [mise](https://mise.jdx.dev) for pinned tools and task execution

Install the pinned tool versions:

```bash
mise install
```

We use `mise` for both tool installation and task running:

- Tool versions are pinned in `.mise/config.toml`.
- Local tasks live under `.mise/tasks`.
- List tasks with `mise tasks`.
- Run arbitrary commands in the toolchain with `mise x -- <command>`.

## Bootstrap

Install Go dependencies:

```bash
mise run bootstrap
```

## Local Development

Run the gateway with live reload:

```bash
mise run run
```

`mise run run` sources `.env` and then `.env.local` from the repo root when
those files exist.

`mise run run` does not start Stargate. By default the gateway targets
`http://127.0.0.1:8000`. NVCF gRPC auth is optional for local bootstrapping; if
`NVCF_GRPC_ADDR` is not set, the auth middleware is disabled.

If `RATE_LIMIT_SYNC_TRANSPORT` is set to `pubsub` or `nats`, run the sync
consumer as a separate process:

```bash
go run ./cmd/llm-api-gateway-rate-limit-sync-worker
```

The default local runtime uses:

- `PORT=8080`
- `OLRIC_ENABLED=true`
- `OLRIC_ENV=local`
- `STARGATE_URL=http://127.0.0.1:8000`
- `NVCF_REGION=local`
- `LOCAL_FUNCTION_ID=default`
- `NVCF_DEFAULT_MODEL=bootstrap-echo`

For chat and responses requests without `X-NVCF-Function-ID`, send the
composite model id `<function_id>/<model>` in the request `model` field. With
the default local config, that is `default/bootstrap-echo`.

Useful overrides:

- `NVCF_GATEWAY_ADDR` to bind a specific listen address
- `NVCF_GATEWAY_MAX_REQUEST_BODY_BYTES` to reject larger request bodies with
  413 (default `0`, no limit)
- `STARGATE_CONNECT_TIMEOUT` to control Stargate dial timeout
- `STARGATE_REQUEST_TIMEOUT` to cap end-to-end Stargate request time
- `STARGATE_LAST_CLUSTER_ENABLED=true` to send session last-cluster hints to
  Stargate (default `false`)
- `STARGATE_LAST_CLUSTER_TTL` for the last-cluster entry lifetime, reset on
  every write (default `10m`)
- `STARGATE_LAST_CLUSTER_LOOKUP_TIMEOUT` for the lookup deadline on the
  request path (default `20ms`)
- `STARGATE_LAST_CLUSTER_LOCAL_MAX_ENTRIES` for the per-replica store size
  when Olric is off (default `100000`)
- `NVCF_GATEWAY_INFERENCE_WRITE_TIMEOUT` to cap how long one response write
  may stall on a client that stopped reading (default `60s`, `0s` disables).
  It applies only while a write is in progress, so long streams, long
  generations, and upstream pauses are not cut off.
- `NVCF_GRPC_ADDR` to enable NVCF gRPC auth
- `SECRETS_PATH` for the gateway-to-NVCF secrets file. Use `nvcfApiToken` for
  fixed bearer-token auth, or `id` and `secret` with `OAUTH2_PROVIDER_HOST` for
  OAuth2 client-credentials auth.
- `OAUTH2_PROVIDER_HOST` to enable OAuth2 client-credentials auth when
  `nvcfApiToken` is not present in `SECRETS_PATH`
- `NVCF_GRPC_INSECURE=true` to disable TLS for local gRPC testing
- `NVCF_GRPC_TIMEOUT` to cap each gRPC auth or policy call
- `RATE_LIMIT_ENABLED=false` to disable rate limiting locally
- `RATE_LIMIT_FAIL_OPEN=false` to make Olric or limiter failures fatal
- `OLRIC_ENABLED=false` to skip starting the embedded Olric node. When it is
  `true`, the node starts even if rate limiting is off, and it also holds
  last-cluster hints
- `OLRIC_BIND_PORT`, `OLRIC_MEMBERLIST_BIND_PORT`, and `OLRIC_PEERS` for
  multi-instance Olric clustering
- `OTEL_SERVICE_NAME` to override the emitted service name
- `OTEL_TRACES_EXPORTER=otlp|stdout|none` to enable trace export
- `OTEL_METRICS_EXPORTER=otlp|none` to enable metric export

## Metrics

Request-facing metrics include a `function_id` label. The value comes from the
request routing key. Requests without a function, such as health checks, use
`function_id="none"`.

The label is present on HTTP request, upstream request, token usage, provider
time, first-token time, and stream duration metrics. Infrastructure metrics for
authentication, pub/sub, rate-limit synchronization, and Olric remain
function-independent.

Last-cluster store metrics use only a `result` label:

- `llm_api_gateway_last_cluster_lookups_total`: `hit`, `miss`, `error`,
  `timeout`, or `skipped` (enabled, but the session is not eligible)
- `llm_api_gateway_last_cluster_writes_total`: `ok` or `error`
- `llm_api_gateway_last_cluster_lookup_duration_seconds`: lookup latency, with
  buckets from 1 ms to 50 ms

Lookups and writes also create the `llm-api-gateway.last_cluster_lookup` and
`llm-api-gateway.last_cluster_write` spans.

Example request-rate query:

```promql
sum by (function_id) (
  rate(llm_api_gateway_http_requests_total[5m])
)
```

Example p95 request-latency query:

```promql
histogram_quantile(
  0.95,
  sum by (le, function_id) (
    rate(llm_api_gateway_http_request_duration_seconds_bucket[5m])
  )
)
```

## Tooling and Tasks

Common tasks:

```bash
mise run build
mise run test
mise run test:all
mise run lint
mise run fmt
mise run kustomize:build:local
```

## Container

Build the container image with `mise run build:docker` or `docker build`.

```bash
docker build -t llm-api-gateway:dev .
```

Run it with the embedded Olric rate limiter enabled:

```bash
docker run --rm -p 8080:8080 \
  -e OLRIC_ENABLED=true \
  -e STARGATE_URL=http://host.docker.internal:8000 \
  llm-api-gateway:dev
```

The same image also contains `/usr/bin/llm-api-gateway-rate-limit-sync-worker`.

## Kubernetes

Render the local overlay:

```bash
kustomize build kustomize/overlays/local
```

Apply it:

```bash
kubectl apply -k kustomize/overlays/local
```

The local overlay deploys the gateway; rate-limit state is kept in embedded
Olric nodes inside the gateway pods. It expects a Stargate HTTP service to be
reachable at `http://stargate:8000`.

To deploy a separate rate-limit sync consumer, add
`kustomize/bases/rate-limit-sync-worker` to your overlay alongside the server
base.

## Before Pushing

Run the standard local checks:

```bash
mise run fmt
mise run lint
mise run test
```
