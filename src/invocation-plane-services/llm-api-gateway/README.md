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

Configured functions control the downstream `model`, service tier, routing
method, and per-function rate limits. Prompt rendering and exact prompt
tokenization are not gateway-owned surfaces.

When a request is forwarded to Stargate, the gateway emits routing headers for
the selected function/model and estimated prompt size, including
`x-routing-key`, `x-model`, `x-input-tokens`, and `x-token-estimate`.

For OpenAI-compatible multi-turn stickiness, chat completions and responses
return `x-multi-turn-session-id`. Clients should persist that value and send it
on later requests for the same conversation. The gateway forwards only a hashed
internal `x-cache-affinity-key` to Stargate.

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
- `STARGATE_CONNECT_TIMEOUT` to control Stargate dial timeout
- `STARGATE_REQUEST_TIMEOUT` to cap end-to-end Stargate request time
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
- `OLRIC_ENABLED=false` to skip starting the embedded Olric node
- `OLRIC_BIND_PORT`, `OLRIC_MEMBERLIST_BIND_PORT`, and `OLRIC_PEERS` for
  multi-instance Olric clustering
- `OTEL_SERVICE_NAME` to override the emitted service name
- `OTEL_TRACES_EXPORTER=otlp|stdout|none` to enable trace export
- `OTEL_METRICS_EXPORTER=otlp|none` to enable metric export

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

The same image also contains `./llm-api-gateway-rate-limit-sync-worker`.

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
