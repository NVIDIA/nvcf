# LLM Request Router Metrics

The LLM Request Router serves Prometheus metrics from
`llm-request-router:9090/metrics` when
`llmRequestRouter.metrics.enabled` is `true`.

The self-managed stack maps `global.observability.metrics.enabled` to this chart
value. The request-router chart runs Stargate with
`--metrics-prefix=llm_request_router_`, so deployed metric names use the
`llm_request_router_` prefix instead of the upstream default `stargate_` prefix.
The chart also sets the trace service name with
`--otel-service-name=llm-request-router`.

## Label Boundaries

Use bounded labels only. Keep `routing_key`, `model`, `inference_server_id`,
`status`, `result`, and `reason` to bounded service dimensions. Do not add
request IDs, session IDs, function IDs, organization IDs, project IDs, raw URLs,
raw prompts, authorization values, or other unbounded request fields as metric
labels.

## Metrics

| Metric name | Type | Source endpoint | Labels | Notes |
| --- | --- | --- | --- | --- |
| `llm_request_router_requests_total` | Counter | `llm-request-router:9090/metrics` | `routing_key`, `model`, `inference_server_id`, `status` | Total proxied requests by selected backend and status. |
| `llm_request_router_proxy_attempts_total` | Counter | `llm-request-router:9090/metrics` | `routing_key`, `model`, `inference_server_id`, `result` | Upstream proxy attempts by selected backend and result. |
| `llm_request_router_proxy_retries_total` | Counter | `llm-request-router:9090/metrics` | `routing_key`, `model`, `reason` | Total proxy retries by retry reason. |
| `llm_request_router_proxy_retry_exhausted_total` | Counter | `llm-request-router:9090/metrics` | `routing_key`, `model`, `reason` | Total requests that exhausted retry options. |
| `llm_request_router_quic_connection_evictions_total` | Counter | `llm-request-router:9090/metrics` | `inference_server_id`, `reason` | Total QUIC pool evictions by backend and reason. |
| `llm_request_router_quic_hot_path_reconnect_total` | Counter | `llm-request-router:9090/metrics` | `inference_server_id`, `result` | Direct QUIC reconnect attempts from the proxy hot path. |
| `llm_request_router_proxy_replay_buffer_bytes` | Histogram | `llm-request-router:9090/metrics` | `model` | Proxied request replay buffer size in bytes. |
| `llm_request_router_proxy_duration_seconds` | Histogram | `llm-request-router:9090/metrics` | `routing_key`, `model`, `inference_server_id` | Time to first byte from upstream in seconds. |
| `llm_request_router_routing_duration_seconds` | Histogram | `llm-request-router:9090/metrics` | `routing_key`, `model` | Load-balancer decision time in seconds. |
| `llm_request_router_active_inference_servers` | Gauge | `llm-request-router:9090/metrics` | `routing_key`, `model` | Currently routable inference servers for a routing target. |
