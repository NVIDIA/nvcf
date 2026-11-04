# gRPC Proxy Metrics

The NVCF gRPC proxy emits Prometheus metrics on Service `grpc` (and the headless variant `grpc-headless`) in namespace `nvcf`, port `metrics` (10083), path `/metrics`. The exposition format is Prometheus text (via `prometheus/client_golang`).

The metric families below cover client connection health, the NATS pipe to workers, and gRPC worker session attachment.

| Metric name                                              | Metric type | Source           | Description                                                                                            | Unit (where applicable) | Interesting Labels             | Required Filters (where applicable)                  |
| -------------------------------------------------------- | ----------- | ---------------- | ------------------------------------------------------------------------------------------------------ | ----------------------- | ------------------------------ | ---------------------------------------------------- |
| nvcf_grpc_proxy_service_active_connections_total         | Gauge       | grpc:10083/metrics | Active client TCP connections to the gRPC proxy                                                        |                         |                                | namespace="nvcf"                                     |
| nvcf_grpc_proxy_service_active_http_requests_total       | Gauge       | grpc:10083/metrics | Active client HTTP requests in-flight                                                                  |                         |                                | namespace="nvcf"                                     |
| nvcf_grpc_proxy_service_nats_in_bytes                    | Gauge       | grpc:10083/metrics | Bytes received from the NATS connection                                                                | bytes                   |                                | namespace="nvcf"                                     |
| nvcf_grpc_proxy_service_nats_in_msgs                     | Gauge       | grpc:10083/metrics | Messages received from the NATS connection                                                             |                         |                                | namespace="nvcf"                                     |
| nvcf_grpc_proxy_service_nats_out_bytes                   | Gauge       | grpc:10083/metrics | Bytes sent to the NATS connection                                                                      | bytes                   |                                | namespace="nvcf"                                     |
| nvcf_grpc_proxy_service_nats_out_msgs                    | Gauge       | grpc:10083/metrics | Messages sent to the NATS connection                                                                   |                         |                                | namespace="nvcf"                                     |
| nvcf_grpc_proxy_service_nats_error_total                 | Counter     | grpc:10083/metrics | Errors observed on the NATS connection                                                                 |                         |                                | namespace="nvcf"                                     |
| nvcf_grpc_proxy_service_nats_reconnect_total             | Counter     | grpc:10083/metrics | NATS reconnect attempts                                                                                |                         |                                | namespace="nvcf"                                     |
| nvcf_grpc_proxy_service_nats_reconnects                  | Gauge       | grpc:10083/metrics | Current reconnect attempt count                                                                        |                         |                                | namespace="nvcf"                                     |
| nvcf_grpc_proxy_service_nats_lame_duck_total             | Counter     | grpc:10083/metrics | NATS lame-duck messages observed                                                                       |                         |                                | namespace="nvcf"                                     |
| nvcf_grpc_proxy_service_session_init_seconds_bucket      | Histogram   | grpc:10083/metrics | Time spent initializing a gRPC worker session. Fires only on gRPC worker attach to port 10086.         | seconds                 | is_reconnect, le               | namespace="nvcf"                                     |
| http_server_request_duration_seconds_bucket              | Histogram   | grpc:10083/metrics | RED metric: HTTP server request duration on the gRPC proxy.                                            | seconds                 | http_request_method, http_response_status_code, http_route, le | namespace="nvcf"                                     |
| http_server_request_body_size_bytes_bucket               | Histogram   | grpc:10083/metrics | HTTP server request body size                                                                          | bytes                   | http_request_method, http_route, le | namespace="nvcf"                                     |
| http_server_response_body_size_bytes_bucket              | Histogram   | grpc:10083/metrics | HTTP server response body size                                                                         | bytes                   | http_request_method, http_route, le | namespace="nvcf"                                     |
| rpc_client_duration_milliseconds_bucket                  | Histogram   | grpc:10083/metrics | OpenTelemetry gRPC client RPC duration (per-RPC outcome)                                               | milliseconds            | rpc_service, rpc_method, rpc_grpc_status_code, le | namespace="nvcf"                                     |

## Notes

- `nvcf_grpc_proxy_service_session_init_seconds_bucket` is the SLI for **gRPC** inference function health. HTTP inference functions invoked through the regular HTTP invocation gateway bypass the gRPC proxy entirely and do not register session-init samples here.
- The `nvcf_grpc_proxy_service_nats_*` family is a useful proxy signal for "is the gRPC proxy → NATS pipe healthy" (in/out bytes and message deltas) and "are NATS upstreams stable" (reconnect and error counters).
- Per-RPC outcomes (success vs. error per call) are covered by the OpenTelemetry `rpc_client_*` family; aggregate proxy-side errors are covered by `nvcf_grpc_proxy_service_nats_error_total`.
- Standard Go runtime metrics (`go_*`) and process metrics (`process_*`) are also exposed on the same endpoint and follow upstream conventions.

## Reproducing locally

```bash
kubectl port-forward -n nvcf svc/grpc 10083:10083
curl http://127.0.0.1:10083/metrics | grep -E '^nvcf_grpc_proxy_'
```
