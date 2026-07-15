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
Edge/direct path.

Pylon model membership is either an explicit repeatable `--model-name` set or,
when that flag is omitted, a continuously discovered Dynamo `GET /v1/models`
set. Every Pylon must also select exactly one local stats initialization source:
`--do-calibration` for a sole Pylon in its cluster or a per-Pylon
`--initial-input-tps` value for shared-hardware clusters. See
[Pylon onboarding](docs/operations/pylon-onboarding.md) for the complete
lifecycle and calibration contract.

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
