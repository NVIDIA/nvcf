# LLM Request Router Metrics

The LLM Request Router serves Prometheus metrics from
`llm-request-router:9090/metrics` when
`llmRequestRouter.metrics.enabled` is `true`.

The self-managed stack maps
`addons.llm.requestRouter.metrics.enabled` to this chart value. It defaults to
`true`. The request-router chart passes `--metrics-port` and uses Stargate's
default `stargate_` metric prefix and `stargate` trace service name.

## Label Boundaries

Use bounded labels only. Keep `routing_key`, `model`, `inference_server_id`,
`algorithm`, `selection`, `status`, `result`, and `reason` to bounded service
dimensions. Do not add request IDs, session IDs, function IDs, organization
IDs, project IDs, raw URLs, raw prompts, authorization values, or other
unbounded request fields as metric labels.

## Metrics

| Metric name | Type | Source endpoint | Labels | Notes |
| --- | --- | --- | --- | --- |
| `stargate_requests_total` | Counter | `llm-request-router:9090/metrics` | `routing_key`, `model`, `inference_server_id`, `status` | Total proxied requests by selected backend and status. |
| `stargate_proxy_attempts_total` | Counter | `llm-request-router:9090/metrics` | `routing_key`, `model`, `inference_server_id`, `result` | Upstream proxy attempts by selected backend and result. |
| `stargate_proxy_retries_total` | Counter | `llm-request-router:9090/metrics` | `routing_key`, `model`, `reason` | Total proxy retries by retry reason. |
| `stargate_routing_selections_total` | Counter | `llm-request-router:9090/metrics` | `routing_key`, `model`, `algorithm`, `selection` | Primary and ranked fallback cluster choices used for upstream attempts. |
| `stargate_routing_kv_free_token_fallback_selections_total` | Counter | `llm-request-router:9090/metrics` | `routing_key`, `model`, `algorithm` | Routes selected after a higher-ranked candidate failed the KV free-token check. |
| `stargate_proxy_retry_exhausted_total` | Counter | `llm-request-router:9090/metrics` | `routing_key`, `model`, `reason` | Total requests that exhausted retry options. |
| `stargate_admission_rejections_total` | Counter | `llm-request-router:9090/metrics` | `routing_key`, `model`, `reason` | Requests rejected by local input-work admission control. |
| `stargate_quic_connection_evictions_total` | Counter | `llm-request-router:9090/metrics` | `inference_server_id`, `reason` | Total QUIC pool evictions by backend and reason. |
| `stargate_quic_hot_path_reconnect_total` | Counter | `llm-request-router:9090/metrics` | `inference_server_id`, `result` | Direct QUIC reconnect attempts from the proxy hot path. |
| `stargate_proxy_replay_buffer_bytes` | Histogram | `llm-request-router:9090/metrics` | `model` | Proxied request replay buffer size in bytes. |
| `stargate_proxy_duration_seconds` | Histogram | `llm-request-router:9090/metrics` | `routing_key`, `model`, `inference_server_id` | Time to first byte from upstream in seconds. |
| `stargate_routing_duration_seconds` | Histogram | `llm-request-router:9090/metrics` | `routing_key`, `model` | Load-balancer decision time in seconds. |
| `stargate_active_inference_servers` | Gauge | `llm-request-router:9090/metrics` | `routing_key`, `model` | Currently routable inference servers for a routing target. |
