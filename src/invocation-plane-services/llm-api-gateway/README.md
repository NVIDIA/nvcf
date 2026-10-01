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
- `GET /v1/models` (OpenAI model list; see Model discovery)
- `GET /v1/registry` (per-model registry; see Model discovery)
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
- `Authorization: Bearer ...` is the caller credential. It is checked by the
  configured authenticator (see Authentication) and is never forwarded to
  Stargate.
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
`x-routing-key` (only when the routing key is not empty), `x-model`,
`x-input-tokens`, and `x-token-estimate`. A client-supplied `x-routing-key`
is dropped before forwarding.

For OpenAI-compatible multi-turn stickiness, chat completions and responses
accept `prompt_cache_key` and return the selected session value in
`x-multi-turn-session-id`. Clients can send the same `prompt_cache_key` or
persist the response header and send it on later requests for the same
conversation. The gateway preserves the raw body field for the model backend.
It forwards only a SHA-256-derived value in the internal
`x-cache-affinity-key` header to Stargate.

## Authentication

The gateway fails closed. It starts only when one of these modes is selected:

| Mode | Selected by | `model` format | Routing key |
|---|---|---|---|
| NVCF | `NVCF_GRPC_ADDR` | `<routing_key>/<model>` | first `model` segment |
| Static API keys | `API_KEYS_PATH` | bare, for example `meta/llama-3.1-8b-instruct` | always empty |
| Anonymous | `ALLOW_ANONYMOUS=true` and neither of the above | `<routing_key>/<model>` | first `model` segment |

`NVCF_GRPC_ADDR` and `API_KEYS_PATH` are mutually exclusive. With neither set
and `ALLOW_ANONYMOUS` unset or false, the gateway exits at startup with an
error naming the three options.

NVCF mode authenticates each request through the NVCF LLM gRPC auth service,
derives the per-caller rate-limit key from `authContext["ncaId"]`, optionally
scopes it further by project, and keeps final token consumption accounting in
the gateway after completion or stream close. A `model` without a routing-key
prefix is rejected with `model prefix is required`.

Static API key mode authenticates callers against a mounted key file, with no
NVCF dependency:

- Callers send `Authorization: Bearer <key>`. The gateway hashes the key with
  SHA-256 and compares it in constant time against every entry in the file.
- The whole `model` string is the model id. It is forwarded unchanged in the
  body and in `x-model`; `x-routing-key` is never sent.
- The key id is the rate-limit key. The request context records the caller as
  `api-key:<id>`.
- Only paths listed in `STATIC_ALLOWED_PATHS` are served. Other paths return
  403 after authentication. `/healthz`, `/readyz`, and `/info` need no key.
  `GET /v1/models` and `GET /v1/registry` are always allowed and need not be
  listed; any other method on those paths stays subject to the list.
- Static keys carry no per-model specs, so `MODEL_URI_ALLOWLIST_ENABLED` and
  per-model token rate limits do not apply.

Key file format:

```json
{
  "keys": [
    {"id": "team-a", "sha256": "<lowercase hex SHA-256 of the key>"}
  ]
}
```

Compute a digest with `printf '%s' "$API_KEY" | sha256sum`. Ids are 1 to 128
characters from letters, digits, `.`, `_`, and `-`, and start with a letter
or digit. Ids and digests must be unique. The file is read on the first
request and re-read at most every 60 seconds, so keys rotate without a
restart. A missing, empty, or malformed file rejects every request with 401
until it is valid again; each distinct cause is logged once.

Anonymous mode accepts every request without credentials and logs a warning
at startup. Use it for local development only.

`PUBLIC_READ_ENDPOINTS` decides whether the read-only discovery endpoints,
exactly `GET /v1/models` and `GET /v1/registry`, bypass authentication. It
defaults to `true` in static API key and anonymous mode and to `false` in
NVCF mode, where the router's model list spans every tenant. Any other method
on those paths is authenticated like every other route.

| Mode | `PUBLIC_READ_ENDPOINTS=true` | `PUBLIC_READ_ENDPOINTS=false` |
|---|---|---|
| Static API keys | no key needed | valid key required, 401 without one |
| NVCF | no bearer needed | bearer required, checked by the NVCF auth service with an empty routing key |
| Anonymous | open | open (no authenticator) |

In every mode the caller's `Authorization` header stays at the gateway. Set
`STARGATE_SERVICE_TOKEN` to send `Authorization: Bearer <token>` to Stargate
instead.

Set `TLS_CERT_FILE` and `TLS_KEY_FILE` together to serve the listener over
TLS. Setting only one of them is a startup error, and so is a pair that does
not load. While serving, the gateway checks the modification time and size of
both files every `TLS_RELOAD_INTERVAL` (default `30s`) and loads the pair again
when either changed. New connections get the new certificate; open connections
keep theirs. A pair that fails to load (unreadable, malformed, or a key that
does not match the certificate) is rejected: the gateway keeps serving the last
good pair, logs one warning per distinct error, and retries on every check
until a good pair loads. Every load is logged at info with the leaf's subject,
serial, and `not_after`.

In Kubernetes, mount the certificate Secret (for example one cert-manager
renews) as a volume and point the two variables at its files. The kubelet
updates the mounted files when the Secret changes, and the gateway serves the
renewed pair at most one `TLS_RELOAD_INTERVAL` later, without a restart. The
kubelet's own delay depends on its sync period (about a minute by default).
Secret volumes mounted with `subPath` are never updated; mount the whole
volume.

### Model discovery

Both discovery endpoints call the router's `GET /v1/models` on
`STARGATE_URL` on every request. The call forwards no caller header; it
carries only `STARGATE_SERVICE_TOKEN` when set, and is bounded by
`STARGATE_REQUEST_TIMEOUT`, or 10 seconds when that is unset. The router
lists only routable inference servers, so a model appears only while the
router can route to at least one of its servers. A router error, a non-2xx status, or a
body that is not the expected JSON returns 502; a timeout returns 504.

`GET /v1/models` returns the OpenAI list, one entry per distinct model id in
the router's order:

```json
{"object":"list","data":[{"id":"meta/llama-3.1-8b-instruct","object":"model","created":0,"owned_by":"nvidia"}]}
```

`GET /v1/registry` aggregates the router's per-server entries per model,
sorted by model. `inferenceServers` is the number of routable servers, and
`clusterId` is the installation's cluster id. Servers that disagree on the
cluster id yield their sorted distinct ids joined with `,` and a warning in the
gateway log.

```json
{"object":"list","data":[{"model":"meta/llama-3.1-8b-instruct","clusterId":"spark-berlin","inferenceServers":1}]}
```

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
`http://127.0.0.1:8000`. NVCF gRPC auth is optional for local bootstrapping:
`mise run run` sets `ALLOW_ANONYMOUS=true` unless it is already set, so the
gateway starts without an authenticator and accepts unauthenticated requests.

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
- `NVCF_GATEWAY_INFERENCE_WRITE_TIMEOUT` to cap how long one response write
  may stall on a client that stopped reading (default `60s`, `0s` disables).
  It applies only while a write is in progress, so long streams, long
  generations, and upstream pauses are not cut off.
- `NVCF_GRPC_ADDR` to enable NVCF gRPC auth
- `API_KEYS_PATH` to enable static API key auth from the key file at that path
  (see Authentication)
- `ALLOW_ANONYMOUS=true` to start without any authenticator
- `STATIC_ALLOWED_PATHS` for the comma-separated request paths served in static
  API key mode (default `/v1/chat/completions`)
- `PUBLIC_READ_ENDPOINTS` to serve `GET /v1/models` and `GET /v1/registry`
  without authentication (default `true` in static API key and anonymous
  mode, `false` in NVCF mode; see Authentication)
- `STARGATE_SERVICE_TOKEN` for the bearer the gateway sends to Stargate
- `TLS_CERT_FILE` and `TLS_KEY_FILE` to serve the listener over TLS
- `TLS_RELOAD_INTERVAL` for how often the TLS files are checked for a renewed
  pair (default `30s`, must be positive; see Authentication)
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

## Metrics

Request-facing metrics include a `function_id` label. The value comes from the
request routing key. Requests without a function, such as health checks, use
`function_id="none"`.

The label is present on HTTP request, upstream request, token usage, provider
time, first-token time, and stream duration metrics. Infrastructure metrics for
authentication, pub/sub, rate-limit synchronization, TLS, and Olric remain
function-independent.

With TLS enabled, `llm_api_gateway_tls_certificate_expiry_seconds` is the unix
time at which the served certificate expires. It has no sample on a plaintext
gateway. `llm_api_gateway_tls_reloads_total{outcome="success|rejected"}`
counts reload attempts after startup; a rejected pair is retried, and counted,
on every check until a good pair loads. Example alert on a certificate that is
not being renewed:

```promql
llm_api_gateway_tls_certificate_expiry_seconds - time() < 7 * 24 * 3600
```

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
  -e ALLOW_ANONYMOUS=true \
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
