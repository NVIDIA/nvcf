# Stargate

Stargate is a control plane and HTTP router for inference servers.

- Pylons register local inference servers with Stargate.
- Stargate keeps local routing state by model and routing key.
- Clients send OpenAI-compatible HTTP requests to Stargate.
- Stargate forwards each request over an established QUIC tunnel to a selected pylon.

Use [docs/README.md](docs/README.md) as the docs entrypoint.
Use [local quickstart](docs/getting-started/local-quickstart.md) to run the local stack.

## Read First

| Need | Read |
| --- | --- |
| Run locally | [Local quickstart](docs/getting-started/local-quickstart.md) |
| Gateway/proxy integration | [API gateway contract](docs/api-gateway-contract.md) |
| Pylon/runtime onboarding | [Pylon onboarding](docs/operations/pylon-onboarding.md) |
| Kubernetes shape | [Deployment shape](docs/operations/deployment-shape.md) |
| CLI flags and config | [CLI reference](docs/reference/cli.md), [Config and environment](docs/reference/config-and-env.md) |
| Metrics and troubleshooting | [Observability](docs/operations/observability.md), [Troubleshooting](docs/operations/troubleshooting.md) |
| Architecture invariants | [Architecture docs](docs/architecture/README.md) |

## Main Crates

- `crates/stargate`: server binary
- `crates/pylon` and `crates/pylon-lib`: sidecar CLI and library
- `crates/stargate-k8s-router`: optional backend-facing router
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
scripts/check_docs.sh
cargo fmt --all
cargo test -p stargate
cargo test -p pylon-lib
cargo test -p stargate-bench
scripts/check_rust_lint.sh
scripts/check_pr.sh --host-only
scripts/check_pr.sh
```

Coverage policy: [docs/code-coverage.md](docs/code-coverage.md).
