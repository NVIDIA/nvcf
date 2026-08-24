# Function Autoscaler Observability

The function autoscaler emits structured logs, Prometheus metrics that explain dependency health statuses and scaling decisions, and OpenTelemetry spans for outbound calls to its dependencies. The Prometheus exporter serves metrics on the address configured in `server.metrics.exporters`. The local settings file at `crates/server/resources/settings-local.yaml` uses `0.0.0.0:41338`.

Job and namespace labels follow the standard NVCF naming convention for the cluster that runs the function autoscaler.

## Metric reference

| Metric name | Metric type | Description |
|-------------|-------------|-------------|
| `nvcf_autoscaler.autoscaling.status` | Gauge | Scaling status per function, encoded as a reason code. |
| `nvcf_autoscaler.scaling.current_instances` | Gauge | Current instance count per function as read from the timeseries database. |
| `nvcf_autoscaler.scaling.desired_instances` | Gauge | Desired instance count computed by the scaling decision. |
| `nvcf_autoscaler.scaling.utilization` | Gauge | Utilization percentage per function used in the scaling decision. |
| `nvcf_autoscaler.requests.queued_total` | Counter | Scaling requests queued for processing. |
| `nvcf_autoscaler.requests.processed_total` | Counter | Scaling requests processed. |
| `nvcf_autoscaler.requests.rejected_total` | Counter | Scaling requests rejected by the policy or guard rails. |
| `nvcf_autoscaler.requests.rate_limited_total` | Counter | Scaling requests rate-limited downstream. |
| `nvcf_autoscaler.queue.size` | Gauge | Current depth of the scaling work queue. |
| `nvcf_autoscaler.queue.capacity` | Gauge | Configured capacity of the scaling work queue. |
| `nvcf_autoscaler.function_table_state` | Gauge | State of the active function table entry per function. |
| `nvcf_autoscaler.function_discovery_duration_seconds` | Histogram | Duration of each discovery loop run. |
| `nvcf_autoscaler.timeseries_db.requests_total` | Counter | Timeseries database requests, labeled by status. |
| `nvcf_autoscaler.timeseries_db.request_duration_milliseconds` | Histogram | Timeseries database request latency. |
| `nvcf_autoscaler.timeseries_db.auth_failure_total` | Counter | Timeseries database authentication failures. |
| `nvcf_autoscaler.timeseries_db.server_side_failure_total` | Counter | Timeseries database server-side query failures. |
| `nvcf_autoscaler.nvcf_api.request_duration_milliseconds` | Histogram | NVCF API request latency. |
| `nvcf_autoscaler.oauth2_api.request_duration_milliseconds` | Histogram | OAuth2 token endpoint request latency. |
| `nvcf_autoscaler.oauth2_client.token_refresh_failure_total` | Counter | OAuth2 client token refresh failures. |
| `nvcf_autoscaler.cassandra.health_status` | Gauge | Cassandra client health. 1 indicates healthy, 0 indicates unhealthy. |
| `nvcf_autoscaler.health.overall_status` | Gauge | Overall service health status. |
| `nvcf_autoscaler.health.component_status` | Gauge | Per-component health status. |
| `nvcf_autoscaler.distributed_lock` | Gauge | State of the discovery distributed lock for this replica. |
| `nvcf_autoscaler.distributed_lock.acquisition_failures_total` | Counter | Discovery lock acquisition failures. |
| `nvcf_autoscaler.processing.utilization_data_age_milliseconds` | Histogram | Age of the utilization data used in each scaling decision. |

## Tracing

The function autoscaler emits OpenTelemetry spans for outbound calls to the timeseries database and the NVCF API, with the OTLP endpoint and span filter configurable under `server.tracing`.

## Logging

The function autoscaler writes structured logs to stdout. Set log filter directives in the `server.envfilter_directive` configuration field. The format follows the `tracing_subscriber` env filter syntax (Rust ecosystem standard):

```yaml
server:
  envfilter_directive: "server=info,rs_autoscaler=debug,rs_autoscaler::cassandra=warn,info"
```

The same syntax applies to `server.tracing.logging_envfilter_directive` if you separate logging and tracing filters.

Useful target prefixes:

| Target | Covers |
|--------|--------|
| `server` | Binary entry point: startup, server lifecycle. |
| `rs_autoscaler` | Top-level function autoscaler library crate. |
| `rs_autoscaler::work` | Scaling loop, discovery loop, bucket reshuffles. |
| `rs_autoscaler::cassandra` | Cassandra client, LWT lock operations. |
| `rs_autoscaler::nvcf_api` | OAuth2, NVCF API calls. |
| `rs_autoscaler::timeseries_db` | Timeseries database query traces. |

## See also

- [Function Autoscaler Operations](./operations.md) for common symptoms tied to these metrics and log lines.
- [Architecture](./architecture.md) for the components that emit each signal.
- [Configure Autoscaling](../configure-autoscaling.md) for setting per-function scaling bounds and policy via the NVCF API.
