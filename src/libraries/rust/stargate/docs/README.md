# Stargate Docs

> Type: Index. Start here, then read only the docs that match the task.

Use this page as the durable docs entrypoint. The goal is to keep agents and
humans from ingesting every document when only one topic is relevant.

## Start Here By Task

| Task | Read first | Then read only if needed |
| --- | --- | --- |
| Local setup or first request | [Local quickstart](getting-started/local-quickstart.md) | [API gateway contract](api-gateway-contract.md), [Pylon onboarding](operations/pylon-onboarding.md) |
| Exact public HTTP proxy or gateway behavior | [API gateway contract](api-gateway-contract.md) | [gRPC and protobuf API](reference/grpc-api.md), [Config and environment](reference/config-and-env.md) |
| Routing, proxying, registration, Kubernetes connectivity, pylon behavior, or observability | [Architecture](architecture/README.md) | [Testing](testing/README.md), [Benchmarks](benchmarks/README.md) |
| Pylon or inference runtime onboarding | [Pylon onboarding](operations/pylon-onboarding.md) | [Runtime stats interface](runtime-stats-interface.md), [Tunnel transport selection](tunnel-transports.md) |
| Kubernetes deployment shape | [Deployment shape](operations/deployment-shape.md) | [Operations](operations/README.md), [Testing](testing/README.md) |
| Metrics, tracing, or on-call debugging | [Observability](operations/observability.md) | [Troubleshooting](operations/troubleshooting.md), [Metrics reference](reference/metrics.md) |
| CLI flags, env vars, gRPC, or metrics names | [Reference docs](reference/README.md) | [Architecture](architecture/README.md) |
| Tunnel transport choice or QUIC/HTTP3/WebTransport changes | [Architecture](architecture/README.md) | [Tunnel transport selection](tunnel-transports.md) |
| Test, coverage, CI, mutation, or behavior-suite work | [Testing](testing/README.md) | [Architecture](architecture/README.md) |
| Benchmark runner, benchmark scenarios, or performance evidence | [Benchmarks](benchmarks/README.md) | [Testing](testing/README.md) |
| Release, change-control, or operator runbooks | [Operations](operations/README.md) | [Architecture](architecture/README.md) |
| Repo documentation organization or agent-readable docs | [Docs best practices](docs-best-practices.md) | [Architecture](architecture/README.md), [Testing](testing/README.md), [Operations](operations/README.md) |
| Planning repo documentation improvements | [Stargate docs improvement plan](stargate-docs-improvement-plan.md) | [Docs best practices](docs-best-practices.md) |

## Current Versus Historical

Current contracts live in the topic indexes above and in the docs they link as
current. Historical audits and retired contract notes are retained only when
they still explain important rationale.

## Diagrams

PlantUML source lives in [diagrams](diagrams/). Render it with:

```bash
scripts/watch_puml.sh docs/diagrams
```
