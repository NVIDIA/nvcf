# LLM Request Router Metrics

The LLM Request Router serves Prometheus metrics from
`llm-request-router:9090/metrics` when
`llmRequestRouter.metrics.enabled` is `true`. The standalone request-router
chart defaults this value to `false`.

The self-managed stack maps
`addons.llm.requestRouter.metrics.enabled` to this chart value. It defaults to
`true` when the LLM add-on is enabled. The request-router chart passes
`--metrics-port` and uses Stargate's default `stargate_` metric prefix and
`stargate` trace service name.

## Label Boundaries

Use bounded labels only. Keep `routing_key`, `model`, `inference_server_id`,
`algorithm`, `selection`, `session_state`, `status`, `result`, `reason`, and
`cache` to bounded service dimensions. Routing expression content never becomes
a label value. Do not add request IDs, session IDs, function IDs, organization
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
| `stargate_routing_session_selections_total` | Counter | `llm-request-router:9090/metrics` | `routing_key`, `model`, `algorithm`, `session_state`, `selection` | Cluster choices for requests classified by `last_cluster_affinity`. `session_state` is `new`, `returning`, or `stale`. For `returning`, `primary` means the request went to its last cluster; for `new` and `stale`, it means the cluster came from the affinity group. Every state and selection pair starts at zero once a routing key, model, and algorithm records its first classified request. |
| `stargate_routing_expressions_total` | Counter | `llm-request-router:9090/metrics` | `algorithm`, `cache` | Accepted `x-routing-method` values with parameters. `cache` is `hit`, `build`, or `rebuild`. Values without parameters and rejected values are not counted. Rejected `x-routing-method` values write the warn log `invalid routing algorithm header` with their `rejection_reason`. |
| `stargate_routing_expression_cache_entries` | Gauge | `llm-request-router:9090/metrics` | none | Approximate number of routing targets that hold a routing expression configuration, with a capacity of 16384 per process. The router runs cache maintenance once a minute to remove entries idle for 15 minutes. Scrapes read the count without maintenance, so it can lag behind pending insertions or removals until the next maintenance run. |
| `stargate_proxy_retry_exhausted_total` | Counter | `llm-request-router:9090/metrics` | `routing_key`, `model`, `reason` | Total requests that exhausted retry options. |
| `stargate_admission_rejections_total` | Counter | `llm-request-router:9090/metrics` | `routing_key`, `model`, `reason` | Requests rejected before an upstream attempt: `input_work_limit_exceeded` and `input_work_capacity_unavailable` from input-work admission, `routing_capacity_unavailable` when every eligible backend lacks capacity. These return `503` with `overloaded_error`. |
| `stargate_quic_connection_evictions_total` | Counter | `llm-request-router:9090/metrics` | `inference_server_id`, `reason` | Total QUIC pool evictions by backend and reason. |
| `stargate_quic_hot_path_reconnect_total` | Counter | `llm-request-router:9090/metrics` | `inference_server_id`, `result` | Direct QUIC reconnect attempts from the proxy hot path. |
| `stargate_tls_reloads_total` | Counter | `llm-request-router:9090/metrics` | `material_type`, `result` | Mounted TLS server identity reload attempts by result. |
| `stargate_tls_certificate_expiry_seconds` | Gauge | `llm-request-router:9090/metrics` | `material_type` | Unix timestamp when the active server identity expires. |
| `stargate_proxy_replay_buffer_bytes` | Histogram | `llm-request-router:9090/metrics` | `model` | Proxied request replay buffer size in bytes. |
| `stargate_proxy_duration_seconds` | Histogram | `llm-request-router:9090/metrics` | `routing_key`, `model`, `inference_server_id` | Time to first byte from upstream in seconds. |
| `stargate_routing_duration_seconds` | Histogram | `llm-request-router:9090/metrics` | `routing_key`, `model` | Load-balancer decision time in seconds. |
| `stargate_active_inference_servers` | Gauge | `llm-request-router:9090/metrics` | `routing_key`, `model` | Currently routable inference servers for a routing target. |
