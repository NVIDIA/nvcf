# LLM Function Invocation Metrics Report

This report covers the metrics available on the LLM function invocation path:
the LLM API Gateway, the LLM Request Router, and the Stargate client sidecar in
LLM function pods.

## Scrape points

| Component | Endpoint | Service name | Metric prefix |
| --- | --- | --- | --- |
| LLM API Gateway | `llm-api-gateway:9464/metrics` | `llm-api-gateway` | `llm_api_gateway_` |
| Rate limit sync worker | `:9464/metrics` when deployed with `METRICS_PORT=9464` | `llm-api-gateway-rate-limit-sync-worker` | `llm_api_gateway_` |
| LLM Request Router | `llm-request-router:9090/metrics` | `llm-request-router` | `llm_request_router_` |
| Stargate client sidecar | `:9089/metrics` by default | `stargate-client` | `stargate_client_` |

The request-router chart passes `--metrics-prefix=llm_request_router_`.
Upstream Stargate still defaults to `stargate_` when run outside the NVCF chart.

## LLM API Gateway

| Metric | Labels |
| --- | --- |
| `llm_api_gateway_http_requests_total` | `method`, `route`, `status` |
| `llm_api_gateway_http_request_duration_seconds` | `method`, `route`, `status` |
| `llm_api_gateway_http_active_requests` | `method`, `route` |
| `llm_api_gateway_upstream_requests_total` | `upstream`, `result`, `status` |
| `llm_api_gateway_upstream_request_duration_seconds` | `upstream`, `result`, `status` |
| `llm_api_gateway_llm_tokens_total` | `endpoint`, `token_type`, `stream` |
| `llm_api_gateway_provider_time_seconds` | `endpoint`, `phase`, `stream` |
| `llm_api_gateway_stream_first_token_seconds` | `endpoint` |
| `llm_api_gateway_stream_duration_seconds` | `endpoint`, `status` |
| `llm_api_gateway_pubsub_publish_failures_total` | None |
| `llm_api_gateway_pubsub_consume_failures_total` | None |
| `llm_api_gateway_pubsub_consume_duration_seconds` | None |
| `llm_api_gateway_rate_limit_event_replication_lag_seconds` | None |
| `llm_api_gateway_rate_limit_events_received_total` | None |
| `llm_api_gateway_rate_limit_events_dropped_total` | `reason` |
| `llm_api_gateway_rate_limit_events_applied_total` | None |
| `llm_api_gateway_rate_limit_events_failed_apply_total` | None |
| `llm_api_gateway_rate_limit_events_dry_run_would_apply_total` | None |
| `llm_api_gateway_rate_limit_synchronizer_publish_duration_seconds` | None |
| `llm_api_gateway_rate_limit_synchronizer_queue_wait_seconds` | None |
| `llm_api_gateway_rate_limit_synchronizer_queue_length` | None |
| `llm_api_gateway_rate_limit_synchronizer_events_dropped_total` | `reason` |

The sync worker reuses the same telemetry package and emits the rate limit
synchronizer and Pub/Sub metrics under the worker service name.

## LLM Request Router

| Metric | Labels |
| --- | --- |
| `llm_request_router_requests_total` | `routing_key`, `model`, `inference_server_id`, `status` |
| `llm_request_router_proxy_attempts_total` | `routing_key`, `model`, `inference_server_id`, `result` |
| `llm_request_router_proxy_retries_total` | `routing_key`, `model`, `reason` |
| `llm_request_router_proxy_retry_exhausted_total` | `routing_key`, `model`, `reason` |
| `llm_request_router_quic_connection_evictions_total` | `inference_server_id`, `reason` |
| `llm_request_router_quic_hot_path_reconnect_total` | `inference_server_id`, `result` |
| `llm_request_router_proxy_replay_buffer_bytes` | `model` |
| `llm_request_router_proxy_duration_seconds` | `routing_key`, `model`, `inference_server_id` |
| `llm_request_router_routing_duration_seconds` | `routing_key`, `model` |
| `llm_request_router_active_inference_servers` | `routing_key`, `model` |

## Stargate Client Sidecar

| Metric | Labels |
| --- | --- |
| `target_info` | `service_version`, `service_name`, `commit` |
| `stargate_client_requests_inflight` | `model` |
| `stargate_client_requests_state` | `model`, `state` |
| `stargate_client_requests_state_input_tokens` | `model`, `state` |
| `stargate_client_requests_total` | `model`, `routing_key`, `status` |
| `stargate_client_request_time_to_response_headers_seconds` | `model`, `routing_key` |
| `stargate_client_request_time_to_first_output_seconds` | `model`, `routing_key` |
| `stargate_client_request_time_to_first_token_seconds` | `model`, `routing_key` |
| `stargate_client_request_duration_seconds` | `model`, `routing_key`, `status` |
| `stargate_client_request_input_tokens_total` | `model`, `routing_key`, `status` |
| `stargate_client_request_output_tokens_total` | `model`, `routing_key`, `status` |
| `stargate_client_request_input_tokens` | `model`, `routing_key`, `status` |
| `stargate_client_request_output_tokens` | `model`, `routing_key`, `status` |
| `stargate_client_registration_stream_connected` | `router` |
| `stargate_client_reverse_tunnel_connected` | `router` |
| `stargate_client_model_input_tps` | `model` |
| `stargate_client_model_output_tps` | `model` |
| `stargate_client_model_max_input_tps` | `model` |
| `stargate_client_model_max_output_tps` | `model` |
| `stargate_client_model_queue_size` | `model` |
| `stargate_client_model_queued_input_tokens` | `model` |
| `stargate_client_model_kv_cache_capacity_tokens` | `model` |
| `stargate_client_model_kv_cache_used_tokens` | `model` |
| `stargate_client_model_kv_cache_free_tokens` | `model` |
| `stargate_client_model_advertised_status` | `router`, `model`, `status` |
| `stargate_client_retryable_responses_total` | `inference_server_id`, `reason`, `status` |
| `stargate_client_nonretryable_failures_total` | `inference_server_id`, `reason` |

Keep request IDs, session IDs, function IDs, organization IDs, project IDs,
authorization values, raw prompts, and raw URLs out of metric labels.
