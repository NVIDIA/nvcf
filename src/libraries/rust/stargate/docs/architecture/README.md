# Architecture Docs

> Type: Index. Current architecture contracts and deeper explanations.

Read only the documents that match the behavior you are changing.

## Current Contracts

- [Agent architecture reference](../agent-architecture.md): canonical agent
  context for routing identity, proxy flow, load balancing, discovery,
  Kubernetes connectivity, observability, terminology, and important files.
- [API gateway integration contract](../api-gateway-contract.md): public
  frontend responsibilities, `ListModels`, proxy headers, supported endpoints,
  retry guidance, and security boundaries.
- [Tunnel transport selection](../tunnel-transports.md): when to use `custom`,
  `http3`, or `webtransport`; direct and reverse tunnel requirements; L4/L7
  load-balancer constraints.
- [Runtime stats interface](../runtime-stats-interface.md): engine-to-pylon
  NDJSON stats stream, OpenAI fallback, KV polling, aggregation, labels, and
  metrics.
- [Coordinated calibration state machine](../coordinated-calibration-state-machine.md):
  local Stargate calibration ownership and pylon assignment behavior.
- [Multi-backend cluster routing design](../multi-backend-clusters.md):
  current cluster-level routing, stats aggregation, retry semantics, and
  observability.
- [Reference docs](../reference/README.md): exact CLI, config, gRPC, and metric
  names.

## Related Context

- [Feature and behavior test matrix](../feature-behavior-test-matrix.md):
  current behavior mapped to concrete test coverage.
