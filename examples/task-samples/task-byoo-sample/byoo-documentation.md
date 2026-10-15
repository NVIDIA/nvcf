# BYO Observability (BYOO) guide

## Environment variables introduced by BYOO

### Container based NVCT deployment

The following environment variables are exposed to the customer for configuring observability-
- `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`
- `OTEL_EXPORTER_OTLP_LOGS_PROTOCOL`
- `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`
- `OTEL_EXPORTER_OTLP_METRICS_PROTOCOL`
- `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`
- `OTEL_EXPORTER_OTLP_TRACES_PROTOCOL`

### Helm charts based NVCT deployment

To configure observability using the customer's Helm charts from the NGC Private Registry, the following variables can be referenced in their deployment spec template -
- `otelExporterOtlpLogsEndpoint`
- `otelExporterOtlpMetricsEndpoint`
- `otelExporterOtlpTracesEndpoint`
- `otelExporterOtlpLogsProtocol`
- `otelExporterOtlpMetricsProtocol`
- `otelExporterOtlpTracesProtocol`

## List of metrics exposed by BYOO otel collector

For iBeta (March 2025), the following list of metrics are made available through the BYOO otel-collector.

### Application metrics

Please refer to sample code as reference on how to instrument your application to send metrics.

### cAdvisor metrics
 - container_cpu_usage_seconds_total
 - container_memory_cache
 - container_memory_rss
 - container_memory_swap
 - container_memory_usage_bytes
 - container_memory_working_set_bytes
 - container_fs_reads_total
 - container_fs_writes_total
 - container_fs_writes_bytes_total
 - container_fs_reads_bytes_total
 - container_network_receive_bytes_total
 - container_network_receive_errors_total
 - container_network_receive_packets_dropped_total
 - container_network_receive_packets_total
 - container_network_transmit_bytes_total
 - container_network_transmit_errors_total
 - container_network_transmit_packets_dropped_total
 - container_network_transmit_packets_total"

### kube-state-metrics
The following list of metrics are supported by the otel-collector. The metrics made available to the customer will depend on the Kubernetes object types utilized in the task:
 - kube_configmap_created
 - kube_cronjob_status_active
 - kube_deployment_status_replicas
 - kube_deployment_status_replicas_ready
 - kube_deployment_status_condition
 - kube_job_status_active
 - kube_job_complete
 - kube_job_status_failed
 - kube_pod_container_status_last_terminated_reason
 - kube_pod_container_status_ready
 - kube_pod_container_status_restarts_total
 - kube_pod_container_status_running
 - kube_pod_init_container_status_last_terminated_reason
 - kube_pod_init_container_status_ready
 - kube_pod_init_container_status_restarts_total
 - kube_pod_init_container_status_running
 - kube_replicaset_status_replicas
 - kube_replicaset_status_replicas_ready
 - kube_secret_created
 - kube_service_created
 - kube_statefulset_status_replicas
 - kube_statefulset_status_replicas_ready

### DCGM metrics
 - DCGM_FI_DEV_GPU_UTIL


### Labels added by BYOO otel-collector

| Label        | Description           | Type  |
| ------------- |-------------|--------------|
| task_id      | NVCT task ID | string |
| instance_id | instance ID of the task deployment      | string |
| nca_id | NCA ID | string |
| cloud_provider | Cloud provider (only available in GFN backend) | string |
