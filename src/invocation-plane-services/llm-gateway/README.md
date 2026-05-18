# llm-api-gateway

> Note: this project does not build outside NVIDIA yet. The Bazel
> build path fetches `github.com/nvidia-lpu/{harmony,minijinja,parsec}`
> Go modules from private repositories; the legacy Makefile path uses
> `mise run bootstrap` to populate `nvidia-lpu-vendor/` from the same
> modules. Both paths require an authorized GitHub token. Public release
> of those dependencies is in progress.

## Build with Bazel

Bazel is the canonical build path. Set `NV_GITHUB_TOKEN` (a GitHub PAT
with SSO-authorized access to the `nvidia-lpu` org) in your environment
or as a CI/CD variable; Gazelle's `go_deps` extension uses it to fetch
`github.com/nvidia-lpu/{harmony,minijinja,parsec}`. Without it the
build fails when fetching those modules.

```shell
# Build everything Bazel knows about.
bazel build //...

# Run all tests with auto-retry on timing-sensitive failures.
# Two tokenizer-fixture-dependent tests are tagged `manual` and are
# excluded from `//...`; run them explicitly when the legacy
# `bin/setup-tokenizers.sh` working tree is in place.
bazel test //... --flaky_test_attempts=3

# Build the linux/amd64 OCI image index for each binary. arm64 is
# disabled by default in rules/oci/private/common.bzl because zig's
# lld (under hermetic_cc_toolchain) rejects the
# `-Wl,--allow-multiple-definition` flag that upstream harmony /
# minijinja require to link multiple Rust static libs.
bazel build //:image_index
bazel build //:rate_limit_sync_worker_image_index

# Push to the internal NGC registries (kaze / nv-ngc-devops / ncp-dev).
bazel run //:image_push
bazel run //:image_push_devops
bazel run //:image_push_ncp_dev

# Regenerate per-package BUILD files after Go source changes.
bazel run //:gazelle

# Refresh module graph after go.mod changes.
bazel mod tidy
```

### Bazel scaffold notes

- `patches/` carries small unified diffs applied via
  `go_deps.module_override` against three external modules:
  - `daulet/tokenizers` — strips the upstream Rust BUILD and wires
    libtokenizers.a (fetched as an `http_archive` in `MODULE.bazel`,
    sha256-pinned) as a `cdep`.
  - `nvidia-lpu/harmony` and `nvidia-lpu/minijinja` — supply a
    hand-written BUILD that wraps the per-arch
    libharmony_cabi.a / libminijinja_cabi.a (shipped inside the
    module zip) in `cc_import` + `cc_library` select(), then
    feeds them to `go_library` via `cdeps`. Also renames the
    misdeclared `package harmony_cabi` -> `package uniffi` to
    match the directory name.
- `third_party/BUILD.bazel` exposes a select()-on-arch
  `cc_library` that the daulet/tokenizers patch consumes for
  libtokenizers.

Local Bazel cache setup is documented in the nvcf/nvcf umbrella's
`nvidia-internal/bazel-cheatsheet.md`.

`llm-api-gateway` is a slimmed-down OpenAI-compatible gateway for routing chat
and responses traffic onto NVCF functions. It was bootstrapped from
`lpu-api-gateway`, but keeps only the pieces needed for HTTP serving, OpenAI
request and response types, prompt templating, tokenization, telemetry, and
rate limiting. Rate limit state is held in an embedded Olric node rather than
an external Redis cluster.

The gateway keeps the OpenAI-facing normalization and responses adaptation in
this repo, then forwards normalized chat inference requests to Stargate over
HTTP and SSE. That lets chat and responses share one handler path instead of
forking by router type.

When rate-limit synchronization is enabled, the gateway publishes local token
consumption events and the optional `llm-api-gateway-rate-limit-sync-worker`
binary can consume and apply remote events in a separate process.

This repo intentionally does not include the donor service's org or project
model, admin flows, data or db packages, migrations, or Postgres-backed caches.
Routing is function-centric: each request resolves to a configured function,
then applies that function's model, template, tokenizer, and limits.

## Supported API Surface

The gateway currently serves:

- `GET /healthz`
- `GET /readyz`
- `POST /v1/chat/completions`
- `POST /v1/chat/completions/template`
- `POST /v1/responses`
- `POST /v1/embeddings`
- `POST /v1/reranking`
- `POST /v1/audio/speech`
- `POST /v1/audio/transcriptions`
- `POST /v1/audio/translations`
- `POST /v1/audio/x/sts`
- `GET /v1/files`
- `POST /v1/files`
- `GET /v1/files/:file`
- `GET /v1/files/:file/content`
- `DELETE /v1/files/:file`
- `GET /v1/batches`
- `POST /v1/batches`
- `GET /v1/batches/:id`
- `POST /v1/batches/:id/cancel`
- `GET /v1/models`
- `GET /v1/models/:model`

## Request Routing

Each request is normalized into a function-scoped request context rather than
the donor repo's org-centric API context.

- `X-NVCF-Function-ID` selects the configured function.
- For chat and responses requests, if the header is omitted, the gateway
  expects the OpenAI `model` field to use the public route id
  `<function_id>/<model>`, and derives the function id from that prefix. There
  is no request-time fallback to `LOCAL_FUNCTION_ID`.
- For JSON inference endpoints such as chat, responses, embeddings, reranking,
  and audio speech, the gateway rewrites the request `model` field to the
  configured downstream function model before proxying to Stargate.
- For multipart endpoints such as audio transcription and translation, function
  selection should be explicit via `X-NVCF-Function-ID`; the multipart payload
  is preserved and the configured downstream model is forwarded via headers.
- `Authorization: Bearer ...` is treated as the caller principal for telemetry
  and is forwarded to NVCF gRPC auth when that adapter is configured.
- `X-Request-ID` is accepted if present, otherwise the gateway generates one.
- `X-Groq-Region` is forwarded into the request context as the target region.

Configured functions control the downstream `model`, prompt `template`,
`tokenizer`, service tier, and per-function rate limits.

When a request is forwarded to Stargate, the gateway emits routing headers for
the selected function/model and rough prompt size, including `x-routing-key`,
`x-model`, `x-input-tokens`, and `x-token-estimate`.

When NVCF auth returns `modelSpecs[model].routingMethod`, the gateway forwards
that load-balancer hint to Stargate as `X-Routing-Method` after trimming
surrounding whitespace. Empty values are omitted, which lets Stargate use its
configured/default load-balancer algorithm. Stargate owns routing method
acceptance and algorithm preconfiguration; unknown or unconfigured methods are
rejected by Stargate.

For OpenAI-compatible multi-turn stickiness, chat completions and responses
return `x-multi-turn-session-id`. Clients should persist that value and send it
on later requests for the same conversation. Responses requests prefer native
body fields before the header: `prompt_cache_key`, then `conversation.id`, then
`x-multi-turn-session-id`. Chat completions use `x-multi-turn-session-id` when
present. If no explicit ID is available, the gateway returns an opaque ID
derived from the first input payload (`input` for responses, `messages` for
chat completions). The gateway forwards only a hashed internal
`x-cache-affinity-key` to Stargate; raw body IDs and client session IDs are not
sent to the router. Sending a returned session ID back in the header maps to the
same internal affinity key used by the request that produced it.

When `NVCF_GRPC_ADDR` and `NVCF_GRPC_AUTH_TOKEN` are configured, the gateway
authenticates each chat or responses request through the NVCF LLM gRPC auth
service, derives the per-caller rate-limit key from `authContext["ncaId"]`,
optionally scopes it further by project when `authContext` includes a project
identifier, keys the embedded Olric DMap counters by that subject plus function
id, and keeps final token consumption accounting in the gateway after
completion or stream close.

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

Install Go dependencies and local build prerequisites. `mise run bootstrap` runs `bootstrap:nvidia-lpu-vendor` first (downloads each `github.com/nvidia-lpu/*` module into the cache and copies it into `nvidia-lpu-vendor/` so `go.mod` `replace` paths exist), then `bootstrap:go-deps` (`go mod download` for the main module), then `bootstrap:tokenizers` (`libtokenizers.a` for host CGO).

```bash
mise run bootstrap
```

The Dockerfile runs `bin/setup-tokenizers.sh` in the builder stage so no host-side fetch is needed for the image. Local `go build` uses `bootstrap:tokenizers` for the host OS/arch.

Files under `nvidia-lpu-vendor/` are copied from the Go module cache, which stores extractions as read-only (directories `dr-xr-xr-x`, files `-r--r--r--`). `cp -a` preserves those modes, so a later `rm -rf` on the tree will fail with `Permission denied`. The sync script handles this by running `chmod -R u+w` before removal. If you hit permission errors outside the script, run `chmod -R u+w nvidia-lpu-vendor` and retry.

## Local Development

Run the gateway with live reload:

```bash
mise run run
```

`mise run run` automatically sources `.env` and then `.env.local` from the repo
root if those files exist. Use `.env` for shared local defaults and
`.env.local` for machine-specific or secret overrides.

Rate limit state lives in an embedded Olric node inside the gateway process.
Set `OLRIC_ENABLED=true` and, for multi-instance deployments, `OLRIC_PEERS` to
a comma-separated list of member addresses so the nodes can discover each
other. Single-node local development works out of the box with
`OLRIC_ENABLED=true` alone.

`mise run run` does not start Stargate. You need a reachable Stargate HTTP
endpoint separately; by default the gateway targets `http://127.0.0.1:8000`.
NVCF gRPC auth is optional for local bootstrapping; if `NVCF_GRPC_ADDR` is not
set, the auth middleware is disabled.

If `RATE_LIMIT_SYNC_TRANSPORT` is set to `pubsub` or `nats`, run the sync
consumer as a separate process:

```bash
go run ./cmd/llm-api-gateway-rate-limit-sync-worker
```

The gateway process remains the publisher; the worker owns subscription and
remote quota application.

The default local runtime uses:

- `PORT=8080`
- `OLRIC_ENABLED=true`
- `OLRIC_ENV=local`
- `STARGATE_URL=http://127.0.0.1:8000`
- `NVCF_REGION=local`
- `LOCAL_FUNCTION_ID=default`
- `NVCF_DEFAULT_MODEL=bootstrap-echo`

`LOCAL_FUNCTION_ID` is only used by the env-driven local bootstrap config to
name the single registered function. For chat and responses requests without
`X-NVCF-Function-ID`, send the composite model id `<function_id>/<model>` in
the request `model` field. With the default local config, that is
`default/bootstrap-echo`.

Useful overrides:

- `NVCF_GATEWAY_ADDR` to bind a specific listen address
- `NVCF_DEFAULT_TEMPLATE` to enable prompt rendering for the default function
- `NVCF_DEFAULT_TOKENIZER` to override tokenizer selection
- `STARGATE_CONNECT_TIMEOUT` to control Stargate dial timeout
- `STARGATE_REQUEST_TIMEOUT` to cap end-to-end Stargate request time
- `NVCF_GRPC_ADDR` to enable NVCF gRPC auth
- `NVCF_GRPC_AUTH_TOKEN` for the gateway-to-NVCF service bearer token
- `NVCF_GRPC_INSECURE=true` to disable TLS for local gRPC testing
- `NVCF_GRPC_TIMEOUT` to cap each gRPC auth or policy call
- `RATE_LIMIT_ENABLED=false` to disable rate limiting locally
- `RATE_LIMIT_FAIL_OPEN=false` to make Olric or limiter failures fatal
- `OLRIC_ENABLED=false` to skip starting the embedded Olric node (rate limiter
  falls back to allow-all or reject-all depending on `RATE_LIMIT_FAIL_OPEN`)
- `OLRIC_BIND_PORT`, `OLRIC_MEMBERLIST_BIND_PORT`, and `OLRIC_PEERS` to
  configure cluster formation across multiple gateway replicas
- `OTEL_SERVICE_NAME` to override the emitted service name
- `OTEL_TRACES_EXPORTER=otlp|stdout|none` to enable trace export
- `OTEL_METRICS_EXPORTER=otlp|none` to enable metric export
- Standard OTLP exporter env vars such as `OTEL_EXPORTER_OTLP_ENDPOINT` are
  honored by the Go OTel exporters when traces or metrics are enabled

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

`mise run test` executes the gateway smoke suite. `mise run test:all` runs the
broader repo tests and expects tokenizer assets under `lib/tokenizers/vendor`.

## Vendoring `nvidia-lpu-vendor`

Trees under `nvidia-lpu-vendor/` mirror the published `github.com/nvidia-lpu/*`
Go modules (same bytes as the module zip / your module cache). Do not edit
anything under `nvidia-lpu-vendor/` to work around Git behavior; keep those
trees identical to upstream.

Some upstream packages ship a `.gitignore` that ignores native libraries
(`*.a`, `*.dylib`, and similar) even though those files are **committed in the
upstream repo** and included in the module. After you copy or refresh a vendor
subtree, normal `git add` will skip those paths. Stage the vendor tree exactly
as it appears on disk with a forced add:

```bash
git add -f nvidia-lpu-vendor/
```

Use the same command when you add a new vendored module under
`nvidia-lpu-vendor/` so every file from the module is tracked without changing
upstream files. Large binaries under `nvidia-lpu-vendor/` are stored with Git
LFS per `.gitattributes`.

## Container

Build the container image with `mise run build:docker` or `docker build`. The Dockerfile runs `bin/setup-tokenizers.sh` in the builder stage (needs network for the release download).

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

The same image also contains `./llm-api-gateway-rate-limit-sync-worker` for a
dedicated rate-limit sync worker container.

## Kubernetes

Render the local overlay:

```bash
kustomize build kustomize/overlays/local
```

Apply it:

```bash
kubectl apply -k kustomize/overlays/local
```

The local overlay deploys the gateway; rate-limit state is kept in an embedded
Olric node inside the gateway pods, so there is no separate Redis workload. It
expects a Stargate HTTP service to already be reachable at
`http://stargate:8000`; this repo does not currently deploy Stargate itself.

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

## Harmony Templates

Harmony-backed GPT-OSS templates are excluded from the default build. If you
need them, build with the `harmony` tag and ensure the required Harmony assets
are available to the runtime.
