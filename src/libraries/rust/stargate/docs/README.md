# Stargate Documentation

Use this page to choose the smallest feature-level context for a change. Exact
external behavior belongs in the linked contract/reference pages, source, and
tests. This page is navigation, not a second architecture specification.

## Features

| Need | Start here | Important code and tests | Detailed contracts |
| --- | --- | --- | --- |
| Run the local stack or make a first request | [Local quickstart](getting-started/local-quickstart.md) | `kustomize/`, `crates/mock-dynamo`, `crates/pylon` | [Pylon onboarding](operations/pylon-onboarding.md) |
| Integrate a gateway or use the HTTP proxy | [API gateway contract](api-gateway-contract.md) | `crates/stargate/src/http_proxy.rs`, `crates/stargate/tests/suite/proxy_contract.rs` | [gRPC API](reference/grpc-api.md), [config and environment](reference/config-and-env.md) |
| Change registration, routing, retries, or clusters | [Multi-backend cluster routing](multi-backend-clusters.md) | `crates/stargate/src/routing_state/`, `crates/stargate/src/load_balancer/`, lifecycle and proxy tests | [Feature and behavior tests](feature-behavior-test-matrix.md) |
| Add or onboard a pylon model | [Pylon onboarding](operations/pylon-onboarding.md) | `crates/pylon-lib/src/`, `crates/pylon/src/` | [Runtime stats interface](runtime-stats-interface.md), [CLI reference](reference/cli.md) |
| Change QUIC, HTTP/3, WebTransport, or backend connectivity | [Tunnel transport selection](tunnel-transports.md) | `crates/protocol/`, `crates/pylon-lib/src/quic_http_tunnel/`, `crates/stargate-k8s-router/` | [Deployment shape](operations/deployment-shape.md) |
| Deploy or operate Stargate and pylon in Kubernetes | [Deployment shape](operations/deployment-shape.md) | `kustomize/`, `scripts/run_k8s_integ.py`, `scripts/run_tilt.py` | [Pylon onboarding](operations/pylon-onboarding.md), [troubleshooting](operations/troubleshooting.md) |
| Debug metrics, traces, or request routing | [Observability](operations/observability.md) | `crates/stargate/src/metrics.rs`, `crates/pylon-lib/src/metrics.rs` | [Metrics reference](reference/metrics.md), [API gateway contract](api-gateway-contract.md) |
| Change tests, coverage, CI, or behavior validation | [Feature and behavior tests](feature-behavior-test-matrix.md) | `scripts/`, focused Rust tests, `scripts/check_pr.sh` | [Coverage and test quality](code-coverage.md), [scripts guide](../scripts/README.md) |
| Run benchmarks or interpret performance evidence | [Local benchmark runner](local-benchmark-runner.md) | `crates/stargate-bench/`, `scripts/profile_stargate_bench.sh` | [Tunnel transport selection](tunnel-transports.md) |
| Find exact flags, environment variables, APIs, or metric names | [CLI reference](reference/cli.md) | `crates/stargate/src/main.rs`, `crates/pylon/src/main.rs`, `crates/proto/proto/` | [Config and environment](reference/config-and-env.md), [gRPC API](reference/grpc-api.md), [metrics](reference/metrics.md) |
| Handle releases or an operational incident | [Release Please layout](release-please.md) | `.release-please-manifest.json`, `.buildkite/`, `scripts/` | [Troubleshooting](operations/troubleshooting.md), [observability](operations/observability.md) |

## Contract Pages

Keep these pages detailed and source-backed because other systems, operators,
or automated checks consume their exact behavior:

- [API gateway contract](api-gateway-contract.md)
- [Runtime stats interface](runtime-stats-interface.md)
- [Tunnel transport selection](tunnel-transports.md)
- [Multi-backend cluster routing](multi-backend-clusters.md)
- [CLI reference](reference/cli.md)
- [Config and environment](reference/config-and-env.md)
- [gRPC and protobuf API](reference/grpc-api.md)
- [Metrics reference](reference/metrics.md)
- [Feature and behavior tests](feature-behavior-test-matrix.md)

## Diagrams

PlantUML source lives in [diagrams](diagrams/). Render it with:

```bash
scripts/watch_puml.sh docs/diagrams
```
