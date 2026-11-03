# LLM API Gateway Metrics

The LLM API Gateway serves Prometheus metrics from
`llm-api-gateway:9464/metrics` when
`llmApiGateway.metrics.enabled` is `true`.

The self-managed stack maps `global.observability.metrics.enabled` to this chart
value. The gateway metrics use the `llm_api_gateway_` service prefix and must not
emit legacy service-prefixed metric names.

## Label Boundaries

Use bounded labels only. Do not add request IDs, session IDs, function IDs,
organization IDs, project IDs, raw URLs, raw prompts, authorization values, or
other unbounded request fields as metric labels.

## Metrics

| Metric name | Type | Source endpoint | Labels | Notes |
| --- | --- | --- | --- | --- |
| `llm_api_gateway_http_requests_total` | Counter | `llm-api-gateway:9464/metrics` | `method`, `route`, `status` | Total inbound HTTP requests. `route` is the templated route, not the raw URL path. |
| `llm_api_gateway_http_request_duration_seconds` | Histogram | `llm-api-gateway:9464/metrics` | `method`, `route`, `status` | Inbound HTTP request duration in seconds. |
| `llm_api_gateway_http_active_requests` | Gauge | `llm-api-gateway:9464/metrics` | `method`, `route` | Current in-flight inbound HTTP requests. |
| `llm_api_gateway_upstream_requests_total` | Counter | `llm-api-gateway:9464/metrics` | `upstream`, `result`, `status` | Total outbound upstream requests. `upstream` is a bounded service name such as `llm-request-router`. |
| `llm_api_gateway_upstream_request_duration_seconds` | Histogram | `llm-api-gateway:9464/metrics` | `upstream`, `result`, `status` | Outbound upstream request duration in seconds. |
| `llm_api_gateway_llm_tokens_total` | Counter | `llm-api-gateway:9464/metrics` | `endpoint`, `token_type`, `stream` | LLM token counts reported by upstream providers. `token_type` is a bounded enum such as `prompt`, `completion`, or `total`. |
| `llm_api_gateway_provider_time_seconds` | Histogram | `llm-api-gateway:9464/metrics` | `endpoint`, `phase`, `stream` | Provider-reported timing phases in seconds. |
| `llm_api_gateway_stream_first_token_seconds` | Histogram | `llm-api-gateway:9464/metrics` | `endpoint` | Time from stream request start to first token in seconds. |
| `llm_api_gateway_stream_duration_seconds` | Histogram | `llm-api-gateway:9464/metrics` | `endpoint`, `status` | Total stream duration in seconds. |
| `llm_api_gateway_pubsub_publish_failures_total` | Counter | `llm-api-gateway:9464/metrics` | None | Number of messages that failed to publish. |
| `llm_api_gateway_pubsub_consume_failures_total` | Counter | `llm-api-gateway:9464/metrics` | None | Number of messages that failed to consume. |
| `llm_api_gateway_pubsub_consume_duration_seconds` | Histogram | `llm-api-gateway:9464/metrics` | None | Time to consume a message in seconds. |
| `llm_api_gateway_rate_limit_event_replication_lag_seconds` | Histogram | `llm-api-gateway:9464/metrics` | None | Lag between rate limit event creation and processing in seconds. |
| `llm_api_gateway_rate_limit_events_received_total` | Counter | `llm-api-gateway:9464/metrics` | None | Number of rate limit events received from the sync transport. |
| `llm_api_gateway_rate_limit_events_dropped_total` | Counter | `llm-api-gateway:9464/metrics` | `reason` | Number of received rate limit events dropped. `reason` is a bounded enum such as `same_cluster`, `old_message`, or `remote_apply_disabled`. |
| `llm_api_gateway_rate_limit_events_applied_total` | Counter | `llm-api-gateway:9464/metrics` | None | Number of rate limit events applied to the local limiter. |
| `llm_api_gateway_rate_limit_events_failed_apply_total` | Counter | `llm-api-gateway:9464/metrics` | None | Number of rate limit events that failed to apply locally. |
| `llm_api_gateway_rate_limit_events_dry_run_would_apply_total` | Counter | `llm-api-gateway:9464/metrics` | None | Number of rate limit events that would apply when remote application is disabled. |
| `llm_api_gateway_rate_limit_synchronizer_publish_duration_seconds` | Histogram | `llm-api-gateway:9464/metrics` | None | Time to publish a rate limit event in seconds. |
| `llm_api_gateway_rate_limit_synchronizer_queue_wait_seconds` | Histogram | `llm-api-gateway:9464/metrics` | None | Time spent queueing a rate limit event in seconds. |
| `llm_api_gateway_rate_limit_synchronizer_queue_length` | Gauge | `llm-api-gateway:9464/metrics` | None | Current rate limit synchronizer queue length. |
| `llm_api_gateway_rate_limit_synchronizer_events_dropped_total` | Counter | `llm-api-gateway:9464/metrics` | `reason` | Number of rate limit events dropped before publishing. `reason` is a bounded enum such as `old_message`. |
