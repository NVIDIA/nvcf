# Stargate

Stargate is a control plane and HTTP router for inference servers.

- Pylons register local inference servers with Stargate.
- Stargate keeps local routing state by model and routing key.
- Clients send OpenAI-compatible HTTP requests to Stargate.
- Stargate forwards each request over an established QUIC tunnel to a selected pylon.

Two backend connectivity configurations are first-class:

- **Edge/direct:** Stargate and pylons share a network. Pylon listens on a
  reachable QUIC address and Stargate connects directly. No reverse listener
  or `stargate-k8s-router` is required.
- **Cloud/reverse:** pylons cannot accept connections from Stargate. Each pylon
  connects to a Stargate reverse listener, optionally through
  `stargate-k8s-router` or a load balancer.

Set the same `--backend-connectivity=direct|reverse` topology on Stargate and
pylon. The [local quickstart](docs/getting-started/local-quickstart.md) uses the
Edge/direct path. On Stargate, a reverse listener
(`--reverse-tunnel-listen-addr`) requires `--backend-connectivity=reverse`; the
mismatched combination is rejected at startup.

Pylon model membership is either an explicit repeatable `--model-name` set or,
when that flag is omitted, a continuously discovered Dynamo `GET /v1/models`
set. Every Pylon must also select exactly one local stats initialization source:
`--do-calibration` for a sole Pylon in its cluster or a per-Pylon
`--initial-input-tps` value for shared-hardware clusters. See
[Pylon onboarding](docs/operations/pylon-onboarding.md) for the complete
lifecycle and calibration contract.

Pylon gates startup on an upstream health probe. It tries `/health` and then
`/v1/health/ready`, reuses whichever path answers first, and forwards Stargate's
`/health` RTT probe to that same path, so engines that serve only the
OpenAI-style ready endpoint work without extra configuration. The repeatable
`--upstream-health-path` puts your own paths ahead of those defaults, which stay
in place as a fallback, and `--upstream-health-wait-ms` bounds how long startup
retries the probe (default 60000; `0` probes once and exits).
`--wait-for-upstream` (`PYLON_WAIT_FOR_UPSTREAM`) overrides that bound: startup
keeps probing every 500 ms until the upstream answers, so a Pylon created
before its backend waits instead of restarting. SIGTERM or SIGINT during the
wait exits cleanly.

For engines without a stats endpoint, set `pylon --max-engine-concurrency N`
to provide a positive concurrency fallback for routing and local queue
admission. Engine-reported limits take precedence. See the
[runtime stats interface](docs/runtime-stats-interface.md#concurrency-fallback).

Use [docs/README.md](docs/README.md) as the docs entrypoint.
Use [local quickstart](docs/getting-started/local-quickstart.md) to run the local stack.

For the local Kubernetes stack:

```bash
make cluster-kind
make tilt-up-kind
```

To render or apply the standalone Edge example instead:

```bash
kubectl kustomize kustomize/overlays/edge
kubectl apply -k kustomize/overlays/edge
```

Stopping the Make-managed Tilt process cleans up its Kubernetes resources,
namespaces, and instance-scoped CoreDNS rewrite. Calling `tilt up` directly
bypasses that cleanup wrapper.
For the CI-style integration run, use
`python3 scripts/run_tilt.py ci --context kind-kind --timeout 30m`; it performs
the same teardown after Tilt exits.

In Kubernetes, pod identity and headless DNS provide discovery; they do not
enable peer relay. The built-in backend peer relay is a default-off,
development-only CLI option and must not be used in production. Use
[`stargate-k8s-router`](docs/operations/deployment-shape.md) or a supported
load-balancer topology for production backend traffic.

## Worker authentication

Stargate authenticates every Pylon registration stream and reverse tunnel
handshake, and refuses to start unless exactly one worker authenticator is
selected:

- `--worker-auth-endpoint URL` verifies the bearer token with the gateway
  `AuthLlmWorker` gRPC call, which may scope the worker to a routing key.
- `--worker-auth-file PATH` (`STARGATE_WORKER_AUTH_FILE`) verifies the bearer
  token against a static YAML file and binds it to a cluster id. The two
  options are mutually exclusive.
- `--allow-open-worker-auth` (`STARGATE_ALLOW_OPEN_WORKER_AUTH`) accepts
  unauthenticated workers. It is valid only when neither option is set;
  combining it with one is a startup error. Use it for development only;
  Stargate logs a warning at startup.

The worker auth file binds each cluster id to the SHA-256 hashes of that
cluster's bearer tokens. A cluster lists a second hash while its credential
rotates:

```yaml
clusters:
  spark-berlin-01:
    - sha256:<64 hex>
    - sha256:<64 hex>    # next credential during rotation
```

Compute a hash without a trailing newline, for example
`printf '%s' "$TOKEN" | sha256sum`. The file is rejected when an entry is not
`sha256:` followed by exactly 64 hex characters (hex case is ignored), when a
hash is listed twice (one credential identifies one cluster), when a cluster
id is empty, a cluster lists no hash, or `clusters` is empty, and when it has
other top-level keys or exceeds 1 MiB. Rejection messages never quote entries,
so a token pasted in place of its hash does not reach the logs.

A worker authenticated this way registers without a routing key and must send
the matching `cluster_id`. Stargate compares the token hash with every hash of
every cluster in constant time.

Stargate refuses to start when the file is missing or invalid, and the error
names the path. Afterwards a reload task re-reads the file every 30 seconds
(`WORKER_AUTH_RELOAD_INTERVAL`) and swaps a valid changed set in atomically, so
a rotated Kubernetes Secret takes effect without a restart. A read or parse
failure keeps the last good set active and logs one error per distinct failure;
a change logs the number of clusters and hashes, never the hashes. Rotate in
three steps: add the new hash, rotate the token on the compute side, remove the
old hash.

Metrics:

- `stargate_registration_auth_failures_total{reason}` counts rejected
  registrations: `missing_token`, `unknown_credential`, or `cluster_mismatch`.
  The claimed cluster id is never a label. A verifier failure, such as an
  unreachable auth gateway, is logged but not counted.
- `stargate_worker_auth_reloads_total{outcome}` counts reloads that activated a
  changed set (`success`) and reloads rejected for a read or parse failure
  (`rejected`; a file that keeps failing the same way counts once).

Both are pre-initialized to zero.

## Pylon registration

`pylon_registration_stream_closures_total{router,reason}` counts registration
streams to each router that closed or failed to open. Every reason starts at
zero for each router Pylon targets:

- `unauthenticated`, `invalid_argument`, `permission_denied`, `unavailable`:
  the router's gRPC status. `unauthenticated` is a rejected credential, and
  `invalid_argument` is a rejected registration, such as a `cluster_id` that
  does not match the credential.
- `end_of_stream`: the router closed the stream without an error status.
- `io`: Pylon could not send a registration message on the stream.
- `connect`: Pylon could not open the gRPC connection.
- `other`: any other status or local failure.

Pylon logs a closure at warn with its `router` and `reason` when the reason
differs from the previous closure since the router last acknowledged a
registration, and at debug otherwise. `connect` closures always log at debug.

Pylon waits before reopening a registration stream to a router. The first wait
after a failed open or a closed stream is 1 s, and each further failure doubles
it up to `--registration-reconnect-max-backoff-ms`
(`PYLON_REGISTRATION_RECONNECT_MAX_BACKOFF_MS`, default `30000`; `0` is
rejected). Every wait carries up to 20 percent jitter either way and never
exceeds the cap. The wait returns to 1 s once the router acknowledges a
registration, so a router that keeps rejecting Pylon, for example for a wrong
credential or `cluster_id`, sees about one attempt per cap interval instead of a
tight retry loop. Stargate discovery (`WatchStargates`) retries every second.

When a registration or discovery stream fails to open for any reason other than
a TLS certificate failure, which has its own error log, Pylon logs
`failed to open stargate gRPC stream; retrying` at warn with `router`,
`operation`, `error` and `retry_in_ms`. It logs each distinct error at warn once
until a stream opens, and at debug while the same error repeats.

`--grpc-tls-ca-cert-path` (`STARGATE_GRPC_TLS_CA_CERT_PATH`) requires an
`https://` `--stargate-address`. A custom CA cannot apply to plaintext gRPC, so
when the CA is set and the address is `http://` or has no scheme, Pylon exits
at startup with an error before it contacts Stargate.

## Load balancing

Use `wait-and-widen` with `cache_affinity_wait_ms` to keep requests in their
cache-affinity group before opening global TTFT buckets. The setting defaults
to `0`. A positive value works without a request SLO header. Optional
`cache_affinity_input_tokens_scale` discounts request prefill during affinity
selection. Global buckets include every candidate, including the affinity
group, at full prefill cost. Queued work and the expected queue header sent to
Pylon are not discounted.

See [Load balancer configuration](docs/load-balancer-configuration.md) for
examples, defaults, routing deadlines, and retry behavior.

## Read First

| Need | Read |
| --- | --- |
| Run locally | [Local quickstart](docs/getting-started/local-quickstart.md) |
| Gateway/proxy integration | [API gateway contract](docs/api-gateway-contract.md) |
| Pylon/runtime onboarding | [Pylon onboarding](docs/operations/pylon-onboarding.md) |
| Kubernetes shape | [Deployment shape](docs/operations/deployment-shape.md) |
| CLI flags and config | [CLI reference](docs/reference/cli.md), [Config and environment](docs/reference/config-and-env.md) |
| Metrics and troubleshooting | [Observability](docs/operations/observability.md), [Troubleshooting](docs/operations/troubleshooting.md) |
| Routing and tunnel contracts | [Multi-backend clusters](docs/multi-backend-clusters.md), [Tunnel transports](docs/tunnel-transports.md) |

## Main Crates

- `crates/stargate`: server binary
- `crates/pylon` and `crates/pylon-lib`: sidecar CLI and library
- `crates/stargate-k8s-router`: optional backend-facing gRPC, Raw QUIC, and
  WebTransport router
- `crates/proto`: protobuf API
- `crates/protocol`: tunnel framing
- `crates/mock-dynamo`: local OpenAI-style backend
- `crates/stargate-bench`: benchmark runner

The versioned Stargate runtime image also includes
`/usr/local/bin/stargate-k8s-router`. Kubernetes deployments can run the main
Stargate process and the backend router from the same immutable image tag.

## Benchmarks

```bash
cargo run -p stargate-bench -- list-scenarios
cargo run -p stargate-bench -- run --scenario hotset-8-backends --output-dir .bench-out/hotset
cargo run --release -p stargate-bench -- transport-bench --requests 20000 --concurrency 256 --output-dir .bench-out/transport
```

Read [docs/local-benchmark-runner.md](docs/local-benchmark-runner.md).

## Checks

```bash
cargo fmt --all
cargo test -p stargate
cargo test -p pylon-lib
cargo test -p stargate-bench
scripts/check_rust_lint.sh
scripts/check_pr.sh --host-only
scripts/check_pr.sh
```

Coverage policy: [docs/code-coverage.md](docs/code-coverage.md).
