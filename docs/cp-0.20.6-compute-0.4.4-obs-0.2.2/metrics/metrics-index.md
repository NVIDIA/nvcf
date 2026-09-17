# Metrics Overview

Per-service metrics reference for the NVCF self-hosted control plane. Each linked page lists metric names, types, sources, descriptions, and the labels and filters that make the metric useful in queries and dashboards.

## Control plane services

- [NVCF API](./nvcf-api/metrics.md): request rates, response status codes, and log event counts for the NVCF API service.
- [Invocation Service](./invocation-service/metrics.md): HTTP request counts, durations, and invocation error metrics for the invocation path.
- [ESS](./ess/metrics.md): template rendering counters and HTTP client metrics for the Encrypted Secrets Service.
- [gRPC Proxy](./grpc-proxy/metrics.md): client connection counts, NATS pipe health, gRPC worker session-attach latency, and HTTP RED metrics for the gRPC proxy.
- [State Metrics Service](./state-metrics/metrics.md): per-function instance count, stage durations, request latency, and function metadata.
- [SIS/Spot](./sis-spot/metrics.md): HTTP client metrics for the Spot Instance Service.
- [Function Autoscaler](../autoscaling/observability.md): OpenTelemetry metrics emitted by the function autoscaler service.

## LLM services

- [LLM API Gateway](./llm-api-gateway/metrics.md): request and routing metrics for the LLM API gateway.
- [LLM Function Invocation Metrics Report](./llm-function-invocation-path.md): end-to-end LLM invocation path report.
- [LLM Request Router](./llm-request-router/metrics.md): request router metrics for LLM traffic.

## Per-function containers

- [Init Container](./init-container/metrics.md): restart counts and termination reasons for function init containers.
- [Utils Container](./utils-container/metrics.md): restart counts, termination reasons, and worker service response metrics for function utils containers.

## Datastores

- [Cassandra](./cassandra/metrics.md): client request latency, timeouts, authentication failures, and endpoint connection metrics.
- [Vault/OpenBao](./vault-openbao/metrics.md): pointer to upstream OpenBao telemetry documentation.

## See also

- [Observability](../observability.md) for logging, tracing, and overall observability configuration.
- [Example Dashboards](../example-dashboards.md) for reference Grafana dashboards.
