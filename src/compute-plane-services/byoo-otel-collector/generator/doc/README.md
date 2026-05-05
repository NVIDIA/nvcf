# Platform Metrics

## Telemetry Attributes

All traces, logs and metrics have the following attributes added to their metadata:
| Backend | Workload | Required Attributes |
| --- | --- | --- |
| gfn | function | nca_id, instance_id, host.id, function_id, function_version_id, zone_name, cloud_provider, origin |
| gfn | task | nca_id, instance_id, host.id, task_id, zone_name, cloud_provider, origin |
| non-gfn | function | nca_id, cloud_provider, instance_id, host.id, function_id, function_version_id, cloud_region, origin |
| non-gfn | task | nca_id, cloud_provider, instance_id, host.id, task_id, cloud_region, origin |

## Metrics Details

## helm
### nvcf-worker
* nvcf_worker_service_request_total
* nvcf_worker_service_response_total
* nvcf_worker_service_stream_streaming_app_ready
* nvcf_worker_service_stream_streaming_app_ready_timestamp
* nvcf_worker_service_stream_session_duration_seconds_bucket
* nvcf_worker_service_stream_session_duration_seconds_count
* nvcf_worker_service_stream_session_duration_seconds_sum
* nvcf_worker_service_inference_request_time_seconds_total
* nvcf_worker_service_inference_uploads_total
* nvcf_worker_service_inference_bytes_total
* nvcf_worker_service_inference_failure_total
* nvcf_worker_service_worker_thread_count_total
* nvcf_worker_service_worker_thread_busy_seconds_total
* nvcf_worker_service_bytes_total
* nvcf_worker_service_request_latency_seconds_bucket
* nvcf_worker_service_request_latency_seconds_sum
* nvcf_worker_service_request_latency_seconds_count
* nvcf_worker_service_stream_latency_seconds_bucket
* nvcf_worker_service_stream_latency_seconds_sum
* nvcf_worker_service_stream_latency_seconds_count
* nvct_worker_service_result_total
* nvcf_worker_service_stream_active

### nvca
* nvca_instance_type_allocatable
* nvca_instance_type_capacity

### kubernetes-cadvisor
##### CPU
* container_cpu_cfs_throttled_periods_total (Only present if container was throttled)
* container_cpu_cfs_throttled_seconds_total (Only present if container was throttled)
* container_cpu_usage_seconds_total

##### Memory
* container_memory_cache
* container_memory_rss
* container_memory_swap
* container_memory_usage_bytes
* container_memory_working_set_bytes

##### Filesystem
only present if the container is performing IO operations
* container_fs_limit_bytes
* container_fs_usage_bytes
* container_fs_reads_total
* container_fs_writes_total
* container_fs_writes_bytes_total
* container_fs_reads_bytes_total

##### Network
only present if the container is performing network operations
* container_network_receive_bytes_total
* container_network_receive_errors_total
* container_network_receive_packets_dropped_total
* container_network_receive_packets_total
* container_network_transmit_bytes_total
* container_network_transmit_errors_total
* container_network_transmit_packets_dropped_total
* container_network_transmit_packets_total

### kube-state-metrics
##### deployment
Only present if helm-based function has a deployment k8s object
* kube_deployment_status_condition
* kube_deployment_status_replicas
* kube_deployment_status_replicas_available
* kube_deployment_status_replicas_ready
* kube_deployment_status_replicas_unavailable
* kube_deployment_status_replicas_updated
* kube_service_created

##### replicaset
Only present if helm-based function has a replicaset k8s object. Please notice that metrics are only available if replicaset had the status in the target metric.
* kube_replicaset_status_replicas
* kube_replicaset_status_ready_replicas

##### statefulset
Only present if helm-based function has a stateful k8s object
* kube_statefulset_status_replicas
* kube_statefulset_status_replicas_ready

##### job/cronjob
Only present if the helm-based function has a job/cronjob k8s object. For NVCT only
* kube_job_status_active
* kube_job_status_failed
* kube_job_status_succeeded
* kube_cronjob_status_active

##### configmap
Only present if function has a configmap k8s object
* kube_configmap_created

##### secret
Only present if function has a secret k8s object
* kube_secret_created

##### pod_container
Only present if function has a pod k8s object, NVCF and NVCT. Please notice that metrics are only available if container had the status in the target metric.
* kube_pod_container_info
* kube_pod_container_resource_limits
* kube_pod_container_resource_requests (Only present if resources were requested)
* kube_pod_container_status_last_terminated_exitcode (Only present if an error happened)
* kube_pod_container_status_last_terminated_reason (Only present if an error happened)
* kube_pod_container_status_restarts_total
* kube_pod_container_status_running
* kube_pod_container_status_terminated (Only present if terminated)
* kube_pod_container_status_terminated_reason (Only present if terminated)
* kube_pod_container_status_waiting (Only present if pod is waiting)
* kube_pod_container_status_waiting_reason (Only present if pod is waiting)
* kube_pod_container_status_ready
* kube_pod_container_state_started (Unix start timestamp for a container in the workload pod)

##### pod_general
Only present if function/task helm deployments, NVCF and NVCT
* kube_pod_info
* kube_pod_status_reason
* kube_pod_created (When the workload pod was created)
* kube_pod_start_time (When the workload pod started after scheduling)
* kube_pod_status_ready_time (When all containers passed readiness probes, if configured)

##### init_container
Only present if function/task helm defined an init container. Please notice that metrics are only available if init container had the status in the target metric.
* kube_pod_init_container_info
* kube_pod_init_container_status_ready
* kube_pod_init_container_status_restarts_total
* kube_pod_init_container_status_running
* kube_pod_init_container_last_status_terminated_reason
* kube_pod_init_container_status_waiting_reason
* kube_pod_init_container_status_waiting

### nvidia-dcgm-exporter
##### GPU
Always present for container and helm, NVCF and NVCT.
* DCGM_FI_DEV_GPU_UTIL
* DCGM_FI_PROF_PIPE_TENSOR_ACTIVE
* DCGM_FI_PROF_DRAM_ACTIVE
* DCGM_FI_PROF_SM_ACTIVE
* DCGM_FI_PROF_SM_OCCUPANCY
* DCGM_FI_PROF_PCIE_TX_BYTES
* DCGM_FI_PROF_PCIE_RX_BYTES
* DCGM_FI_PROF_NVLINK_TX_BYTES
* DCGM_FI_PROF_NVLINK_RX_BYTES
* DCGM_FI_DEV_POWER_USAGE
* DCGM_FI_DEV_VGPU_MEMORY_USAGE

### opentelemetry-collector
Always present for container and helm, NVCF and NVCT. The final list of metrics depends on telemetries received & exporter by function/task. For instance, if function is not publishing `otlp` logs then there will be no metrics related to logs.
* otelcol_exporter_sent_metric_points_total
* otelcol_exporter_sent_spans_total
* otelcol_exporter_sent_log_records_total
* otelcol_exporter_send_failed_log_records_total
* otelcol_exporter_send_failed_spans_total
* otelcol_exporter_send_failed_metric_points_total
* otelcol_processor_incoming_items_total
* otelcol_processor_outgoing_items_total
* otelcol_receiver_accepted_log_records_total
* otelcol_receiver_accepted_metric_points_total
* otelcol_receiver_accepted_spans_total
* otelcol_receiver_refused_log_records_total
* otelcol_receiver_refused_spans_total
* otelcol_receiver_refused_metric_points_total


## container
### nvcf-worker
* nvcf_worker_service_request_total
* nvcf_worker_service_response_total
* nvcf_worker_service_stream_streaming_app_ready
* nvcf_worker_service_stream_streaming_app_ready_timestamp
* nvcf_worker_service_stream_session_duration_seconds_bucket
* nvcf_worker_service_stream_session_duration_seconds_count
* nvcf_worker_service_stream_session_duration_seconds_sum
* nvcf_worker_service_inference_request_time_seconds_total
* nvcf_worker_service_inference_uploads_total
* nvcf_worker_service_inference_bytes_total
* nvcf_worker_service_inference_failure_total
* nvcf_worker_service_worker_thread_count_total
* nvcf_worker_service_worker_thread_busy_seconds_total
* nvcf_worker_service_bytes_total
* nvcf_worker_service_request_latency_seconds_bucket
* nvcf_worker_service_request_latency_seconds_sum
* nvcf_worker_service_request_latency_seconds_count
* nvcf_worker_service_stream_latency_seconds_bucket
* nvcf_worker_service_stream_latency_seconds_sum
* nvcf_worker_service_stream_latency_seconds_count
* nvct_worker_service_result_total
* nvcf_worker_service_stream_active

### nvca
* nvca_instance_type_allocatable
* nvca_instance_type_capacity

### kubernetes-cadvisor
##### CPU
* container_cpu_cfs_throttled_periods_total
* container_cpu_cfs_throttled_seconds_total
* container_cpu_usage_seconds_total

##### Memory
* container_memory_cache
* container_memory_rss
* container_memory_swap
* container_memory_usage_bytes
* container_memory_working_set_bytes

##### Filesystem
only present if the container is performing IO operations
* container_fs_limit_bytes
* container_fs_usage_bytes
* container_fs_reads_bytes_total
* container_fs_reads_total
* container_fs_writes_bytes_total
* container_fs_writes_total

##### Network
only present if the container is performing network operations
* container_network_receive_bytes_total
* container_network_receive_errors_total
* container_network_receive_packets_dropped_total
* container_network_receive_packets_total
* container_network_transmit_bytes_total
* container_network_transmit_errors_total
* container_network_transmit_packets_dropped_total
* container_network_transmit_packets_total

### kube-state-metrics
##### pod_container
Only present if function has a pod k8s object, NVCF and NVCT. Please notice that metrics are only available if container had the status in the target metric.
* kube_pod_container_info
* kube_pod_container_resource_limits
* kube_pod_container_resource_requests (Only present if resources were requested)
* kube_pod_container_status_last_terminated_exitcode (Only present if an error happened)
* kube_pod_container_status_last_terminated_reason (Only present if an error happened)
* kube_pod_container_status_ready
* kube_pod_container_status_restarts_total
* kube_pod_container_status_running
* kube_pod_container_status_terminated (Only present if terminated)
* kube_pod_container_status_terminated_reason (Only present if terminated)
* kube_pod_container_status_waiting (Only present if pod is waiting)
* kube_pod_container_status_waiting_reason (Only present if pod is waiting)
* kube_pod_container_state_started (Unix start timestamp for a container in the workload pod)

### nvidia-dcgm-exporter
##### GPU
* DCGM_FI_DEV_GPU_UTIL
* DCGM_FI_PROF_PIPE_TENSOR_ACTIVE
* DCGM_FI_PROF_DRAM_ACTIVE
* DCGM_FI_PROF_SM_ACTIVE
* DCGM_FI_PROF_SM_OCCUPANCY
* DCGM_FI_PROF_PCIE_TX_BYTES
* DCGM_FI_PROF_PCIE_RX_BYTES
* DCGM_FI_PROF_NVLINK_TX_BYTES
* DCGM_FI_PROF_NVLINK_RX_BYTES
* DCGM_FI_DEV_POWER_USAGE
* DCGM_FI_DEV_VGPU_MEMORY_USAGE

### opentelemetry-collector
Always present for container and helm, NVCF and NVCT. The final list of metrics depends on telemetries received & exporter by function/task. For instance, if function is not publishing `otlp` logs then there will be no metrics related to logs.
* otelcol_receiver_refused_metric_points_total
* otelcol_receiver_refused_spans_total
* otelcol_receiver_refused_log_records_total
* otelcol_receiver_accepted_metric_points_total
* otelcol_receiver_accepted_spans_total
* otelcol_receiver_accepted_log_records_total
* otelcol_exporter_sent_metric_points_total
* otelcol_exporter_sent_spans_total
* otelcol_exporter_sent_log_records_total
* otelcol_exporter_send_failed_metric_points_total
* otelcol_exporter_send_failed_spans_total
* otelcol_exporter_send_failed_log_records_total
* otelcol_processor_incoming_items_total
* otelcol_processor_outgoing_items_total

