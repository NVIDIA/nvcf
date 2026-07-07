# NVCA Metrics Documentation

This document describes all Prometheus metrics exposed by NVCA (NVIDIA Cloud Functions Agent).

## Metrics Overview

NVCA exposes metrics to monitor queue operations, instance capacity, container health, event processing, and Kubernetes API interactions.

All metrics include the following default labels:

- `nvca_nca_id` - NVCA instance identifier
- `nvca_cluster_name` - Kubernetes cluster name
- `nvca_cluster_group` - Cluster group identifier
- `nvca_version` - NVCA version

## Queue Metrics

### `nvca_queue_message_processed_total`

**Type:** Counter

**Description:** Total number of messages processed by this NVCA instance.

**Labels:**

- `message_action` - Type of message action: `FunctionCreation`, `TaskCreation`, or `Termination`

**Usage:**

```promql
# Rate of messages processed per second
rate(nvca_queue_message_processed_total[5m])

# Total messages processed by action type
sum by (message_action) (nvca_queue_message_processed_total)
```

---

### `nvca_queue_message_dequeued_total`

**Type:** Counter

**Description:** Total number of messages dequeued from SQS queues. Only increments when messages are actually received (does not count empty polls). Tracks the dequeue rate per queue type and GPU.

**Labels:**

- `queue_type` - Type of queue: `createQueue`, `clusterCreateQueue`, `taskClusterCreateQueue`, or `termQueue`
- `gpu_name` - GPU name (e.g., `A100`, `L40`, `H100`) or `none` for non-GPU-specific queues

**Usage:**

```promql
# Dequeue rate per second for all queues
rate(nvca_queue_message_dequeued_total[5m])

# Dequeue rate for A100 creation queue
rate(nvca_queue_message_dequeued_total{queue_type="createQueue", gpu_name="A100"}[5m])

# Total messages dequeued by queue type
sum by (queue_type) (nvca_queue_message_dequeued_total)

# Compare dequeue rates across GPU types
sum by (gpu_name) (rate(nvca_queue_message_dequeued_total[5m]))
```

---

### `nvca_queue_dequeue_batch_size`

**Type:** Histogram

**Description:** Distribution of batch sizes (number of messages) pulled per dequeue operation. Records for **every dequeue attempt**, including empty polls (0 messages). Helps understand queue depth, batch utilization, and how often queues are empty.

**Labels:**

- `queue_type` - Type of queue: `createQueue`, `clusterCreateQueue`, `taskClusterCreateQueue`, or `termQueue`
- `gpu_name` - GPU name (e.g., `A100`, `L40`, `H100`) or `none` for non-GPU-specific queues

**Buckets:** `[0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10]`

**Usage:**

```promql
# Average batch size per dequeue operation (including empty polls)
rate(nvca_queue_dequeue_batch_size_sum[5m]) / rate(nvca_queue_dequeue_batch_size_count[5m])

# 95th percentile batch size for A100 creation queue
histogram_quantile(0.95, rate(nvca_queue_dequeue_batch_size_bucket{queue_type="createQueue", gpu_name="A100"}[5m]))

# Distribution of batch sizes across all queues
sum by (le) (rate(nvca_queue_dequeue_batch_size_bucket[5m]))

# How often do we get empty pulls (0 messages)?
rate(nvca_queue_dequeue_batch_size_bucket{le="0"}[5m])

# Percentage of pulls that are empty
rate(nvca_queue_dequeue_batch_size_bucket{le="0"}[5m]) / rate(nvca_queue_dequeue_batch_size_count[5m]) * 100

# How often do we get exactly 1 message?
rate(nvca_queue_dequeue_batch_size_bucket{le="1"}[5m]) - rate(nvca_queue_dequeue_batch_size_bucket{le="0"}[5m])

# How often do we get full batch pulls (10 messages)?
rate(nvca_queue_dequeue_batch_size_bucket{le="10"}[5m]) - rate(nvca_queue_dequeue_batch_size_bucket{le="9"}[5m])

# Average batch size excluding empty pulls
rate(nvca_queue_message_dequeued_total[5m]) / (rate(nvca_queue_dequeue_batch_size_count[5m]) - rate(nvca_queue_dequeue_batch_size_bucket{le="0"}[5m]))
```

---

## Instance Type Metrics

### `nvca_instance_type_capacity`

**Type:** Gauge

**Description:** Count of instances that could be deployed on schedulable node resources by instance type.

**Labels:**

- `instance_type` - Instance type identifier

**Usage:**

```promql
# Current capacity by instance type
nvca_instance_type_capacity

# Capacity trend over time
nvca_instance_type_capacity[1h]
```

---

### `nvca_instance_type_allocatable`

**Type:** Gauge

**Description:** Count of instances that can be deployed on available schedulable node resources by instance type. This represents available capacity after accounting for current allocations.

**Labels:**

- `instance_type` - Instance type identifier

**Usage:**

```promql
# Available capacity by instance type
nvca_instance_type_allocatable

# Capacity utilization rate
(nvca_instance_type_capacity - nvca_instance_type_allocatable) / nvca_instance_type_capacity
```

---

### `nvca_instance_type_unschedulable`

**Type:** Gauge

**Description:** Count of instances that could be deployed on unschedulable node resources by instance type. Nodes marked as unschedulable by NVCA.

**Labels:**

- `instance_type` - Instance type identifier

**Usage:**

```promql
# Unschedulable capacity by instance type
nvca_instance_type_unschedulable

# Total unavailable capacity
sum(nvca_instance_type_unschedulable)
```

---

### `nvca_gpu_node_unclassified_count`

**Type:** Gauge

**Description:** Count of nodes that have GPU resources present in `status.allocatable` but no recognized `nvca.nvcf.nvidia.io/instance-type` label (missing entirely, or set to a value NVCA does not know about). These nodes' GPUs are excluded from `nvca_instance_type_capacity`, `nvca_instance_type_allocatable`, and `nvca_instance_type_unschedulable`, since those metrics are bucketed per instance type. A nonzero value indicates a GPU discovery/labeling gap independent of the per-instance-type numbers.

**Labels:**

- `gpu_family` - Value of the `nvidia.com/gpu.family` node label (e.g. `blackwell`)
- `gpu_machine` - Value of the `nvidia.com/gpu.machine` node label (e.g. `GB200-NVL`)

---

### `nvca_gpu_node_total_count`

**Type:** Gauge

**Description:** Total count of GPU nodes that are part of the cluster, both classified (attributed to a known instance type) and unclassified (`nvca_gpu_node_unclassified_count`). Use alongside the unclassified count to gauge the proportion of GPU capacity NVCA is failing to classify.

**Labels:**

- `gpu_family` - Value of the `nvidia.com/gpu.family` node label (e.g. `blackwell`)
- `gpu_machine` - Value of the `nvidia.com/gpu.machine` node label (e.g. `GB200-NVL`)

---

## Event Metrics

### `nvca_event_error_total`

**Type:** Counter

**Description:** Total error count of NVCA event processing by event kind.

**Labels:**

- `nvca_event_name` - Name of the event that encountered an error

**Usage:**

```promql
# Error rate per second by event type
rate(nvca_event_error_total[5m])

# Total errors by event type
sum by (nvca_event_name) (nvca_event_error_total)

# Alert on high error rate
rate(nvca_event_error_total[5m]) > 0.1
```

---

### `nvca_event_queue_length`

**Type:** Gauge

**Description:** Current length of NVCA event queues. Indicates backlog of events to process.

**Labels:**

- `nvca_event_name` - Name of the event queue

**Usage:**

```promql
# Current queue lengths
nvca_event_queue_length

# Alert on queue backlog
nvca_event_queue_length > 100
```

---

### `nvca_event_process_latency`

**Type:** Summary

**Description:** Latency of NVCA event processing. Provides percentile distribution of event processing times.

**Labels:**

- `nvca_event_name` - Name of the event being processed

**Quantiles:** 50th, 90th, 99th percentiles

**Usage:**

```promql
# Median processing latency by event type
nvca_event_process_latency{quantile="0.5"}

# 99th percentile processing latency
nvca_event_process_latency{quantile="0.99"}

# Alert on high processing latency
nvca_event_process_latency{quantile="0.99"} > 30
```

---

## Container Health Metrics

### `nvca_container_crash_total`

**Type:** Counter

**Description:** Total number of container crashes in NVCA workload pods.

**Labels:**

- `container` - Container name

**Usage:**

```promql
# Crash rate per second
rate(nvca_container_crash_total[5m])

# Total crashes by container
sum by (container) (nvca_container_crash_total)

# Alert on frequent crashes
rate(nvca_container_crash_total[5m]) > 0.01
```

---

### `nvca_container_restart_total`

**Type:** Counter

**Description:** Total number of container restarts in NVCA workload pods.

**Labels:**

- `container` - Container name

**Usage:**

```promql
# Restart rate per second
rate(nvca_container_restart_total[5m])

# Total restarts by container
sum by (container) (nvca_container_restart_total)

# Containers with restarts in last hour
count(increase(nvca_container_restart_total[1h]) > 0)
```

---

### `nvca_image_pull_issue_total`

**Type:** Counter

**Description:** Total number of container image pull errors per registry host. Errors are counted once per NVCF instance.

**Labels:**

- `image_registry` - Registry host experiencing pull errors

**Usage:**

```promql
# Image pull error rate
rate(nvca_image_pull_issue_total[5m])

# Registries with pull issues
sum by (image_registry) (nvca_image_pull_issue_total)
```

---

## Workload Result Metrics

### `nvca_workload_result_total`

**Type:** Counter

**Description:** Total workload results by type, kind, status, and failure category. Tracks terminal states of pods and miniservices. Incremented once per workload when it reaches a terminal state (success or failure), gated by the heartbeat deduplication mechanism to prevent double-counting.

**Labels:**

- `workload_type` - Kubernetes workload type: `container` or `helm`
- `workload_kind` - NVCF request kind, derived from `req.Spec.Action`: `function` or `task`
- `workload_status` - Terminal status: `success` or `failure`
- `failure_category` - Failure root cause (empty string for success). See tables below.

**Failure Categories:**

Each `failure_category` value is derived from the workload's terminal `ICMSInstanceState`
by `ICMSInstanceStateToFailureCategory` in `metrics.go`. The state itself is set by the
pod/miniservice reconcilers in `pkg/nvca/`. The mapping table below is the quick reference;
the detailed table that follows tells a cluster manager what each value means, what
condition in NVCA raises it, and where to look first.

| Category | Description | Mapped from ICMSInstanceState |
|----------|-------------|-------------------------------|
| `image_pull` | Container image pull failures | `ICMSInstanceFailedImagePullIssues` |
| `init_stuck` | Init container stuck | `ICMSInstanceFailedInitContainerStuck` |
| `init_restart_loop` | Init container restart loop | `ICMSInstanceFailedInitContainerRestartLoop` |
| `container_restart_loop` | Application container restart loop | `ICMSInstanceFailedContainerRestartLoop` |
| `create_container_error` | Kubelet cannot create the container | `ICMSInstanceFailedCreateContainerError` |
| `no_capacity` | No GPU/node capacity available | `ICMSInstanceKilledNoCapacity` |
| `admission_error` | Pod admission rejected | `ICMSInstanceKilledAdmissionError` |
| `shared_storage` | Shared storage failure | `ICMSInstanceSharedStorageFailure` |
| `persistent_storage` | Persistent storage failure | `ICMSInstanceInternalPersistentStorageFailure` |
| `degraded_worker` | Worker node degraded | `ICMSInstanceDegradedWorker` |
| `not_found` | Workload not found (pod/miniservice deleted) | `ICMSInstanceFailedNotFound` |
| `terminal_error` | Unrecoverable terminal error | `ICMSInstanceTerminatedTerminalError` |
| `sync_action` | Terminated due to sync action | `ICMSInstanceTerminatedDuetoSyncAction` |
| `service_maintenance` | Terminated for service maintenance | `ICMSInstanceTerminatedServiceMaintenance` |
| `precondition_failure` | Precondition check failed | `ICMSInstanceTerminatedPreconditionFailure` |
| `unknown` | Generic failure (fallback) | `ICMSInstanceFailed` |

Triage guide. Use this table to deduce the cause of a workload-deployment
failure from the `failure_category` label. "When it fires" describes the
condition NVCA detects; "Owner" is the party who usually has to act; "Where to
look / recommended action" is the first debugging step for a cluster manager.

| Category | When it fires | Owner | Where to look / recommended action |
|----------|---------------|-------|-------------------------------------|
| `image_pull` | Pod scheduled but the kubelet cannot pull the container image past `MaxImagePullErrorThreshold` (ImagePull/ErrImagePull/ImagePullBackOff). | Function owner / registry | Check image tag exists and the pull secret is valid. `nvca_image_pull_issue_total{image_registry=...}` shows the registry. Verify registry reachability and rate limits from the node. |
| `init_stuck` | An init container has not completed within `PodLaunchThresholdMinutesOnInitFailure` (or `IsPodStuckInitializing` trips). | Function owner | Inspect init container logs and events. Common causes: model/asset download hanging, a dependency service unreachable, or an init command that never exits. |
| `init_restart_loop` | An init container keeps restarting (crash on start, repeated non-zero exit). | Function owner | Read init container logs from the last few restarts. Usually a bad entrypoint, missing config, or a failing readiness precondition inside the init step. |
| `container_restart_loop` | The application container keeps restarting after start (CrashLoopBackOff). | Function owner | Read application logs across restarts. Look for OOMKilled (raise memory request/limit), missing env/secret, or an application-level panic on startup. |
| `create_container_error` | Kubelet reports CreateContainerError / CreateContainerConfigError; the container spec cannot be realized. Not zero-initialized, so it only appears once observed. | Cluster manager | Check pod events for the exact message. Typical causes: a referenced ConfigMap/Secret or volume mount is missing, or an invalid device/runtime request on the node. |
| `no_capacity` | Pod stays Pending and unscheduled past `PodScheduledThreshold` (no node with the requested GPU/resources). | Cluster manager | Confirm GPU nodes of the requested type are Ready and have free capacity. Check taints, node selectors, and the scheduler (`SchedulerWorkloadCount`). Scale the node pool or free workers. |
| `admission_error` | The API server rejects the pod at admission (webhook/quota/policy denial). | Cluster manager | Read the admission rejection in pod events. Check admission webhooks, ResourceQuota, LimitRange, and PodSecurity/network policies in the target namespace. |
| `shared_storage` | The miniservice/helm workload fails provisioning or mounting shared (SMB/read) storage. | Cluster manager | Verify the SMB CSI driver is healthy and the share/credentials are valid. Check PVC and pod events for mount errors. |
| `persistent_storage` | Internal persistent-storage (PVC) provisioning or attach fails. | Cluster manager | Check the storage class, provisioner health, and PVC binding state. Look for attach/detach errors in pod and PV events. |
| `degraded_worker` | A Running pod is detected unhealthy/degraded by `IsPodDegraded` (for example a GPU/node fault) after start. | Cluster manager | Inspect node health and GPU state (XID errors, NVLink). Cordon and remediate the node. If `autoPurgeDegradedWorkers` is off, the workload is left for manual handling. |
| `not_found` | The expected pod/miniservice for an active request no longer exists (deleted out from under NVCA). | Cluster manager | Usually a manual delete or an external controller/GC removed the workload. Correlate with `nvca_orphaned_resource_cleanup_total` and cluster audit logs. |
| `terminal_error` | NVCA classifies the failure as unrecoverable and terminates the request with a terminal error. | Function owner / cluster manager | Read `SystemFailure` on the termination update for the specific reason. This is a hard stop; the request will not be retried as-is. |
| `sync_action` | The workload is terminated by a sync/reconcile action (a newer desired state supersedes it). | Expected / control plane | Normally benign: the control plane replaced or scaled down the request. Only investigate if it correlates with unexpected churn. |
| `service_maintenance` | The workload is terminated for planned service maintenance. | Expected / cluster manager | Expected during maintenance windows. Confirm the maintenance was intended; workloads should reschedule afterward. |
| `precondition_failure` | A precondition check fails before or during deployment, so the request is terminated. | Cluster manager | Read `SystemFailure` for the failed precondition. Verify the cluster meets the workload's requirements (features, capabilities, config). |
| `unknown` | Generic `ICMSInstanceFailed` with no more specific mapping (fallback bucket). | Cluster manager | A rising `unknown` rate means a failure mode is not yet categorized. Inspect pod/miniservice events and file an issue so the state can be mapped to a specific category. |

**Cardinality:** 64 zero-initialized series (2 workload types × 2 workload kinds × 16
status/category combinations: 15 zero-initialized failure categories plus the empty
success category). `create_container_error` is not zero-initialized, so it appears as an
additional series only after it is first observed.

**Zero-initialized:** Yes. The 64 combinations listed above are pre-initialized to 0 on
startup. `create_container_error` is intentionally excluded from `AllFailureCategories`
and shows up only when the condition occurs.

**Usage:**

```promql
# Overall workload success rate
sum(rate(nvca_workload_result_total{workload_status="success"}[5m])) /
sum(rate(nvca_workload_result_total[5m]))

# Failure rate by category
sum by (failure_category) (rate(nvca_workload_result_total{workload_status="failure"}[5m]))

# Success rate by workload kind (function vs task)
sum by (workload_kind) (rate(nvca_workload_result_total{workload_status="success"}[5m])) /
sum by (workload_kind) (rate(nvca_workload_result_total[5m]))

# Top failure categories for container workloads
topk(5, sum by (failure_category) (increase(nvca_workload_result_total{workload_type="container", workload_status="failure"}[1h])))

# Container vs Helm workload failure rate
sum by (workload_type) (rate(nvca_workload_result_total{workload_status="failure"}[5m]))

# No-capacity failures specifically
rate(nvca_workload_result_total{failure_category="no_capacity"}[5m])

# Alert on high failure rate
sum(rate(nvca_workload_result_total{workload_status="failure"}[5m])) /
sum(rate(nvca_workload_result_total[5m])) > 0.1
```

---

## Upstream (ICMS) Request Metrics

### `nvca_upstream_request_total`

**Type:** Counter

**Description:** Total number of upstream (ICMS) requests by operation, status, and HTTP status code. Tracks all outbound calls from NVCA to the ICMS control plane. Incremented once per request on completion.

**Labels:**

- `operation` - The upstream operation type. One of:
  - `heartbeat` — periodic health status update sent to ICMS (`PutHealthStatus`)
  - `register` — initial cluster registration with ICMS (`Register`)
  - `credentials` — queue credential fetch from ICMS (`GetCreds`)
  - `jwks-push` — projected-SA-token JWKS rotation push to ICMS (`PUT /v1/nvca/clusters/{id}/jwks`); self-hosted PSAT-mode only
- `status` - Request outcome: `success` or `failure`
- `http_status` - HTTP status code as a string: `"200"` on success, the numeric code (e.g. `"401"`, `"503"`) for HTTP errors, or `"0"` for non-HTTP errors (network failures, context cancellations, etc.)

**Zero-initialized:** Yes — all 8 combinations (4 operations × success/200 and failure/0) are pre-initialized to 0 on startup.

**Usage:**

```promql
# Overall upstream request success rate
sum(rate(nvca_upstream_request_total{status="success"}[5m])) /
sum(rate(nvca_upstream_request_total[5m]))

# Request rate by operation
sum by (operation) (rate(nvca_upstream_request_total[5m]))

# Failure rate by operation
sum by (operation) (rate(nvca_upstream_request_total{status="failure"}[5m]))

# Heartbeat failures with HTTP status breakdown
sum by (http_status) (rate(nvca_upstream_request_total{operation="heartbeat", status="failure"}[5m]))

# Alert on heartbeat failures
rate(nvca_upstream_request_total{operation="heartbeat", status="failure"}[5m]) > 0.1

# Detect credential fetch failures (may cause queue inactivity)
rate(nvca_upstream_request_total{operation="credentials", status="failure"}[5m]) > 0

# Alert on ICMS rejecting JWKS rotation pushes (PSAT-mode self-hosted)
rate(nvca_upstream_request_total{operation="jwks-push", status="failure"}[10m]) > 0
```

---

## Kubernetes API Metrics

### `nvca_k8s_api_success_total`

**Type:** Counter

**Description:** Total number of successful Kubernetes API server Get operations.

**Labels:**

- `resource` - Kubernetes resource type being accessed

**Usage:**

```promql
# Successful API calls per second
rate(nvca_k8s_api_success_total[5m])

# Success rate by resource type
sum by (resource) (rate(nvca_k8s_api_success_total[5m]))
```

---

### `nvca_k8s_api_failure_total`

**Type:** Counter

**Description:** Total number of failed Kubernetes API server Get operations (excluding NotFound errors).

**Labels:**

- `resource` - Kubernetes resource type being accessed

**Usage:**

```promql
# Failed API calls per second
rate(nvca_k8s_api_failure_total[5m])

# Failure rate by resource type
sum by (resource) (rate(nvca_k8s_api_failure_total[5m]))

# API error rate (failures / total calls)
sum(rate(nvca_k8s_api_failure_total[5m])) /
(sum(rate(nvca_k8s_api_success_total[5m])) + sum(rate(nvca_k8s_api_failure_total[5m])))

# Alert on high API failure rate
rate(nvca_k8s_api_failure_total[5m]) > 0.1
```

---

## Storage Controller Metrics

### `nvca_model_cache_result_total`

**Type:** Counter

**Description:** Total number of model cache operations by result. Tracks success and failure of model cache setup with detailed failure reasons. This metric provides visibility into cache failures that were previously silent fallbacks to non-cached workers.

**Labels:**

- `nvca_cluster_name` - Kubernetes cluster name
- `nvca_cluster_group` - Cluster group identifier
- `nvca_version` - NVCA version
- `result` - Operation result: `success` or `failure`
- `failure_reason` - Specific failure reason (empty string for success)

**Note:** This metric uses 3 labels (excluding `nvca_nca_id`) for backwards compatibility with storage metrics.

**Failure Reasons:**
| Reason | Description |
|--------|-------------|
| `cache_spec_invalid` | Spec validation failures (missing fields, decode errors) |
| `pvc_setup_failed` | Primary PV/PVC setup failures |
| `pvc_bind_failed` | RO PVC bind failures |
| `rw_pvc_bind_failed` | RW PVC bind failures |
| `job_not_found` | Init cache job not found |
| `job_backoff_exceeded` | Job exceeded backoff limit |
| `job_timeout` | Job timed out waiting for completion |
| `image_pull` | Container image pull issues |
| `init_stuck` | Pod stuck initializing |
| `scheduling_timeout` | Pod scheduling timeout |
| `admission_rejected` | Pod admission rejected |
| `init_job_failed` | Generic init job failure (fallback) |

**Usage:**

```promql
# Cache success rate
sum(rate(nvca_model_cache_result_total{result="success"}[5m])) /
sum(rate(nvca_model_cache_result_total[5m]))

# Failures by reason
sum by (failure_reason) (increase(nvca_model_cache_result_total{result="failure"}[1h]))

# Total cache operations per cluster
sum by (nvca_cluster_name) (rate(nvca_model_cache_result_total[5m]))

# Alert on high failure rate
sum(rate(nvca_model_cache_result_total{result="failure"}[5m])) /
sum(rate(nvca_model_cache_result_total[5m])) > 0.1
```

---

### `nvca_storage_controller_request_duration`

**Type:** Histogram

**Description:** Duration of NVCA Storage Controller request to terminal state in seconds. Tracks how long it takes for storage requests to reach completion or failure. Storage provisioning is a long-running operation with a 4-minute (240s) SLO, so the buckets are coarse and minutes-scale rather than the default sub-second Prometheus buckets, following OpenTelemetry explicit-bucket guidance for long-running operations.

**Labels:**

- `nvca_cluster_name` - Kubernetes cluster name
- `nvca_cluster_group` - Cluster group identifier
- `nvca_version` - NVCA version
- `storage_request_phase` - Terminal phase of the request (e.g., `StorageReady`, `StorageFailed`)

**Note:** This metric uses 3 labels (excluding `nvca_nca_id`) for backwards compatibility.

**Buckets (seconds):** `[10, 30, 60, 120, 180, 240, 300, 600, 1200, 1800]`. The 240s boundary is the "Storage Provisioner Latency" panel SLO threshold.

**Usage:**

```promql
# Fraction of storage requests within the 4-minute SLO (per phase)
sum by (storage_request_phase) (rate(nvca_storage_controller_request_duration_bucket{le="240"}[5m]))
  / sum by (storage_request_phase) (rate(nvca_storage_controller_request_duration_count[5m]))

# 99th percentile storage request duration by phase
histogram_quantile(0.99, sum by (storage_request_phase, le) (rate(nvca_storage_controller_request_duration_bucket[5m])))

# Median (p50) storage request duration
histogram_quantile(0.5, sum by (le) (rate(nvca_storage_controller_request_duration_bucket[5m])))

# Average storage request duration by phase
rate(nvca_storage_controller_request_duration_sum[5m]) / rate(nvca_storage_controller_request_duration_count[5m])

# Alert on slow storage requests (p99 over the 4-minute SLO)
histogram_quantile(0.99, sum by (le) (rate(nvca_storage_controller_request_duration_bucket[5m]))) > 240
```

---

## MiniService Controller Metrics

### `nvca_miniservice_controller_reconcile_phase_total`

**Type:** Counter

**Description:** Total number of reconciliations per MiniService phase. Tracks controller reconciliation activity.

**Labels:**

- `nvca_cluster_name` - Kubernetes cluster name
- `nvca_cluster_group` - Cluster group identifier
- `nvca_version` - NVCA version
- `nvca_nca_id` - NVCA instance identifier (set per MiniService)
- `miniservice_phase` - MiniService phase (e.g., `Pending`, `Installing`, `Running`, `Completed`, `Failed`)

**Note:** This metric uses a custom label order with NCAID passed per-call for backwards compatibility.

**Usage:**

```promql
# Reconciliation rate per second by phase
rate(nvca_miniservice_controller_reconcile_phase_total[5m])

# Total reconciliations by phase
sum by (miniservice_phase) (nvca_miniservice_controller_reconcile_phase_total)

# Reconciliations by NCAID
sum by (nvca_nca_id) (rate(nvca_miniservice_controller_reconcile_phase_total[5m]))
```

---

### `nvca_miniservice_controller_phase_transitions_total`

**Type:** Counter

**Description:** Total number of MiniService phase transitions. Use this to track state changes over time.

**Labels:**

- `nvca_cluster_name` - Kubernetes cluster name
- `nvca_cluster_group` - Cluster group identifier
- `nvca_version` - NVCA version
- `nvca_nca_id` - NVCA instance identifier (set per MiniService)
- `from_phase` - Previous MiniService phase
- `to_phase` - New MiniService phase

**Note:** This metric uses a custom label order with NCAID passed per-call for backwards compatibility.

**Usage:**

```promql
# Phase transition rate
rate(nvca_miniservice_controller_phase_transitions_total[5m])

# Transitions into Failed/InstallFailed
sum by (to_phase) (rate(nvca_miniservice_controller_phase_transitions_total{to_phase=~"Failed|InstallFailed"}[5m]))
```

---

### `nvca_miniservice_controller_failures_total`

**Type:** Counter

**Description:** Total number of MiniService failures by reason. Use this for alerting on failure rates.

**Labels:**

- `nvca_cluster_name` - Kubernetes cluster name
- `nvca_cluster_group` - Cluster group identifier
- `nvca_version` - NVCA version
- `nvca_nca_id` - NVCA instance identifier (set per MiniService)
- `failure_reason` - Failure reason (finite set; see MiniService status reasons)

**Note:** This metric uses a custom label order with NCAID passed per-call for backwards compatibility.

**Usage:**

```promql
# Failure rate by reason
sum by (failure_reason) (rate(nvca_miniservice_controller_failures_total[5m]))

# Alert on failures
rate(nvca_miniservice_controller_failures_total[5m]) > 0
```

---

### `nvca_miniservice_controller_miniservice_ready_status`

**Type:** Gauge

**Description:** Success or failure of a MiniService function or task. Values: 2=Running/Completed, 1=Installed, 0=Unknown, -1=InstallFailed, -2=Failed.

**Deprecated:** This metric uses high-cardinality labels and can leave stale series. Use `nvca_miniservice_controller_phase_transitions_total` and `nvca_miniservice_controller_failures_total` instead.

**Labels:**

- `nvca_cluster_name` - Kubernetes cluster name
- `nvca_cluster_group` - Cluster group identifier
- `nvca_version` - NVCA version
- `nvca_nca_id` - NVCA instance identifier (set per MiniService)
- `function_id` - Function ID (set for function instances)
- `function_version_id` - Function version ID (set for function instances)
- `task_id` - Task ID (set for task instances)

**Note:** This metric uses a custom label order with NCAID passed per-call for backwards compatibility.

**Usage:**

```promql
# Current status of all MiniServices
nvca_miniservice_controller_miniservice_ready_status

# Count of failed MiniServices
count(nvca_miniservice_controller_miniservice_ready_status < 0)

# Alert on MiniService failures
nvca_miniservice_controller_miniservice_ready_status < 0

# Status by NCAID
sum by (nvca_nca_id) (nvca_miniservice_controller_miniservice_ready_status)
```

---

### `nvca_miniservice_controller_reval_request_total`

**Type:** Counter

**Description:** Total number of ReVal service requests per HTTP status code. Tracks interactions with the Helm ReVal service.

**Labels:**

- `nvca_cluster_name` - Kubernetes cluster name
- `nvca_cluster_group` - Cluster group identifier
- `nvca_version` - NVCA version
- `nvca_nca_id` - NVCA instance identifier (set per MiniService)
- `endpoint` - ReVal endpoint path (e.g., `/v1/render`)
- `http_code` - HTTP status code returned

**Note:** This metric uses a custom label order with NCAID passed per-call for backwards compatibility.

**Usage:**

```promql
# ReVal request rate by status code
sum by (http_code) (rate(nvca_miniservice_controller_reval_request_total[5m]))

# ReVal error rate (non-200 responses)
sum(rate(nvca_miniservice_controller_reval_request_total{http_code!="200"}[5m])) / sum(rate(nvca_miniservice_controller_reval_request_total[5m]))

# Alert on ReVal failures
rate(nvca_miniservice_controller_reval_request_total{http_code!="200"}[5m]) > 0.1

# ReVal requests by NCAID
sum by (nvca_nca_id) (rate(nvca_miniservice_controller_reval_request_total[5m]))
```

---

### `nvca_miniservice_controller_event_error_total`

**Type:** Counter

**Description:** Total error count of miniservice controller events (e.g., translation errors).

**Labels:**

- `event_kind` - Type of event that encountered an error (e.g., `EVENT_TRANSLATE_FUNCTION_ERROR`, `EVENT_TRANSLATE_TASK_ERROR`)

**Usage:**

```promql
# Error rate per second by event type
rate(nvca_miniservice_controller_event_error_total[5m])

# Total errors by event type
sum by (event_kind) (nvca_miniservice_controller_event_error_total)

# Alert on translation errors
rate(nvca_miniservice_controller_event_error_total[5m]) > 0.05
```

---

## Garbage Collection (GC) Metrics

### `nvca_gc_orphaned_resource_cleanup_total`

**Type:** Counter

**Description:** Total number of orphaned resources cleaned up by GC cleaners.

**Labels:**

- `nvca_cluster_name` - Kubernetes cluster name
- `nvca_cluster_group` - Cluster group identifier
- `nvca_version` - NVCA version
- `resource_type` - Type of resource being cleaned up (e.g., `Pod`, `PVC`, `ConfigMap`)
- `status` - Status of cleanup operation (e.g., `success`, `failure`)

**Note:** This metric uses 3 labels (excluding `nvca_nca_id`) for backwards compatibility.

**Usage:**

```promql
# Cleanup rate per second by resource type
rate(nvca_gc_orphaned_resource_cleanup_total[5m])

# Total cleanups by resource type
sum by (resource_type) (nvca_gc_orphaned_resource_cleanup_total)

# Failed cleanups
sum(nvca_gc_orphaned_resource_cleanup_total{status="failure"})

# Success rate
sum(rate(nvca_gc_orphaned_resource_cleanup_total{status="success"}[5m])) /
sum(rate(nvca_gc_orphaned_resource_cleanup_total[5m]))
```

---

### `nvca_gc_cleaner_run_total`

**Type:** Counter

**Description:** Total number of GC cleaner runs per cleaner and status. Tracks how often each cleaner executes.

**Labels:**

- `nvca_cluster_name` - Kubernetes cluster name
- `nvca_cluster_group` - Cluster group identifier
- `nvca_version` - NVCA version
- `cleaner_name` - Name of the GC cleaner (e.g., `pod_cleaner`, `pvc_cleaner`)
- `status` - Status of the run (e.g., `success`, `failure`)

**Note:** This metric uses 3 labels (excluding `nvca_nca_id`) for backwards compatibility.

**Usage:**

```promql
# Cleaner run rate per second
rate(nvca_gc_cleaner_run_total[5m])

# Total runs by cleaner
sum by (cleaner_name) (nvca_gc_cleaner_run_total)

# Failed runs
sum(nvca_gc_cleaner_run_total{status="failure"})

# Success rate by cleaner
sum by (cleaner_name) (rate(nvca_gc_cleaner_run_total{status="success"}[5m])) /
sum by (cleaner_name) (rate(nvca_gc_cleaner_run_total[5m]))

# Alert on cleaner failures
rate(nvca_gc_cleaner_run_total{status="failure"}[5m]) > 0.01
```

---

## Cluster Attribute Metrics

### `nvca_kata_runtime_isolation_enabled`

**Type:** Gauge

**Description:** Whether Kata runtime isolation is enabled on this cluster. Value is 1 when enabled, 0 when disabled. Set at agent startup based on the `KataRuntimeIsolation` cluster attribute.

**Labels:**

- `nvca_nca_id` - NVCA instance identifier
- `nvca_cluster_name` - Kubernetes cluster name
- `nvca_cluster_group` - Cluster group identifier
- `nvca_version` - NVCA version

**Usage:**

```promql
# Find clusters with Kata enabled
nvca_kata_runtime_isolation_enabled == 1

# Find clusters with Kata disabled
nvca_kata_runtime_isolation_enabled == 0

# Count of Kata-enabled clusters
sum(nvca_kata_runtime_isolation_enabled)

# Aggregate by cluster
sum by (nvca_cluster_name) (nvca_kata_runtime_isolation_enabled)

# Detect Kata state changes
changes(nvca_kata_runtime_isolation_enabled[1h]) > 0
```

---

### `nvca_maintenance_mode_state`

**Type:** Gauge

**Description:** Whether NVCA is in a maintenance mode on this cluster. The series whose `mode` label matches the active maintenance mode is 1, and every other `mode` series is 0 (one-hot encoding). Set at agent startup from the configured maintenance mode (config or feature flags). Backs the informational "NVCA Desired State Signal" panel and lets workload-deployment failures be correlated with cluster maintenance windows.

**Labels:**

- `mode` - Maintenance mode. One of:
  - `None` - normal operation; workloads deploy as usual
  - `CordonOnly` - creation of new functions/tasks is paused (cordoned); existing workloads keep running
  - `CordonAndDrain` - creation is cordoned and existing workloads are drained
- `nvca_nca_id` - NVCA instance identifier
- `nvca_cluster_name` - Kubernetes cluster name
- `nvca_cluster_group` - Cluster group identifier
- `nvca_version` - NVCA version

**Cardinality:** 3 series per NVCA instance (one per mode).

**Zero-initialized:** Yes. All three `mode` series are pre-initialized to 0 on startup, then the active mode is set to 1.

**Usage:**

```promql
# Active maintenance mode per cluster (returns the mode label of the active series)
nvca_maintenance_mode_state == 1

# Is this cluster draining?
nvca_maintenance_mode_state{mode="CordonAndDrain"} == 1

# Clusters not in normal operation
nvca_maintenance_mode_state{mode="None"} == 0

# Correlate deployment failures with maintenance windows
sum by (failure_category) (rate(nvca_workload_result_total{workload_status="failure"}[5m]))
  and on(nvca_cluster_name) (nvca_maintenance_mode_state{mode="None"} == 0)
```

---

## Scheduler Workload Metrics

### `nvca_scheduler_workload_count`

**Type:** Gauge

**Description:** Number of active functions and tasks grouped by Kubernetes scheduler and workload kind. Recomputed from live ICMSRequest state on each heartbeat, so the metric is correct even after NVCA restarts. The scheduler is determined from the actual schedulerName on each workload's pod. For requests whose pods have not been created yet, falls back to the `KAIScheduler` feature flag.

**Labels:**

- `nvca_nca_id` - NVCA instance identifier
- `nvca_cluster_name` - Kubernetes cluster name
- `nvca_cluster_group` - Cluster group identifier
- `nvca_version` - NVCA version
- `scheduler_name` - Kubernetes scheduler: `default-scheduler` or `kai-scheduler`
- `workload_kind` - NVCF workload kind: `function` or `task`

**Zero-initialized:** Yes — all 4 combinations (2 schedulers × 2 workload kinds) are pre-initialized to 0 on startup.

**Cardinality:** 4 series per cluster (fixed).

**Usage:**

```promql
# Total active workloads per scheduler
sum by (scheduler_name) (nvca_scheduler_workload_count)

# Functions vs tasks per scheduler
nvca_scheduler_workload_count

# Total active functions across all schedulers
sum(nvca_scheduler_workload_count{workload_kind="function"})

# Total active tasks across all schedulers
sum(nvca_scheduler_workload_count{workload_kind="task"})

# Alert if KAI scheduler has no workloads when expected
nvca_scheduler_workload_count{scheduler_name="kai-scheduler"} == 0

# Compare workload distribution across clusters
sum by (nvca_cluster_name, scheduler_name) (nvca_scheduler_workload_count)
```

---

## Useful Dashboards and Queries

### Queue Health Dashboard

```promql
# Queue dequeue rate by GPU
sum by (gpu_name) (rate(nvca_queue_message_dequeued_total[5m]))

# Average messages per batch (including empty polls)
rate(nvca_queue_dequeue_batch_size_sum[5m]) / rate(nvca_queue_dequeue_batch_size_count[5m])

# Average messages per batch (excluding empty polls)
rate(nvca_queue_message_dequeued_total[5m]) / (rate(nvca_queue_dequeue_batch_size_count[5m]) - rate(nvca_queue_dequeue_batch_size_bucket{le="0"}[5m]))

# Percentage of pulls that are empty (queue idle rate)
rate(nvca_queue_dequeue_batch_size_bucket{le="0"}[5m]) / rate(nvca_queue_dequeue_batch_size_count[5m]) * 100

# Compare empty vs non-empty pulls by queue type
sum by (queue_type) (rate(nvca_queue_dequeue_batch_size_bucket{le="0"}[5m]))
# vs
sum by (queue_type) (rate(nvca_queue_dequeue_batch_size_count[5m]) - rate(nvca_queue_dequeue_batch_size_bucket{le="0"}[5m]))

# Queue processing efficiency (processed vs dequeued)
sum(rate(nvca_queue_message_processed_total[5m])) / sum(rate(nvca_queue_message_dequeued_total[5m]))
```

### Capacity Dashboard

```promql
# Total cluster capacity
sum(nvca_instance_type_capacity)

# Available capacity
sum(nvca_instance_type_allocatable)

# Utilization percentage
(sum(nvca_instance_type_capacity) - sum(nvca_instance_type_allocatable)) / sum(nvca_instance_type_capacity) * 100
```

### Health Dashboard

```promql
# Container crash rate
sum(rate(nvca_container_crash_total[5m]))

# Event processing errors
sum by (nvca_event_name) (rate(nvca_event_error_total[5m]))

# K8s API health
sum(rate(nvca_k8s_api_failure_total[5m])) / (sum(rate(nvca_k8s_api_success_total[5m])) + sum(rate(nvca_k8s_api_failure_total[5m])))
```

### Storage and MiniService Dashboard

```promql
# Model cache success rate
sum(rate(nvca_model_cache_result_total{result="success"}[5m])) /
sum(rate(nvca_model_cache_result_total[5m])) * 100

# Model cache failures by reason
sum by (failure_reason) (increase(nvca_model_cache_result_total{result="failure"}[1h]))

# Average storage request duration by phase
rate(nvca_storage_controller_request_duration_sum[5m]) / rate(nvca_storage_controller_request_duration_count[5m])

# MiniService reconciliation rate
sum(rate(nvca_miniservice_controller_reconcile_phase_total[5m]))

# Failed MiniServices count
count(nvca_miniservice_controller_miniservice_ready_status < 0)

# ReVal service error rate
sum(rate(nvca_miniservice_controller_reval_request_total{http_code!="200"}[5m])) / sum(rate(nvca_miniservice_controller_reval_request_total[5m]))

# Translation error rate
sum(rate(nvca_miniservice_controller_event_error_total[5m]))
```

### Workload Result Dashboard

```promql
# Overall workload success rate
sum(rate(nvca_workload_result_total{workload_status="success"}[5m])) /
sum(rate(nvca_workload_result_total[5m])) * 100

# Failure breakdown by category
sum by (failure_category) (increase(nvca_workload_result_total{workload_status="failure"}[1h]))

# Success rate by workload kind
sum by (workload_kind) (rate(nvca_workload_result_total{workload_status="success"}[5m])) /
sum by (workload_kind) (rate(nvca_workload_result_total[5m])) * 100

# Container vs Helm failure rate
sum by (workload_type) (rate(nvca_workload_result_total{workload_status="failure"}[5m]))

# Top failure categories
topk(5, sum by (failure_category) (increase(nvca_workload_result_total{workload_status="failure"}[1h])))
```

### Scheduler Workload Dashboard

```promql
# Active workloads per scheduler
sum by (scheduler_name) (nvca_scheduler_workload_count)

# Functions vs tasks per scheduler
nvca_scheduler_workload_count

# Total active workloads (all schedulers)
sum(nvca_scheduler_workload_count)

# Workload distribution across clusters
sum by (nvca_cluster_name, scheduler_name, workload_kind) (nvca_scheduler_workload_count)
```

### GC Dashboard

```promql
# Cleanup rate by resource type
sum by (resource_type) (rate(nvca_gc_orphaned_resource_cleanup_total[5m]))

# Total orphaned resources cleaned up
sum(nvca_gc_orphaned_resource_cleanup_total)

# GC cleaner run rate
sum by (cleaner_name) (rate(nvca_gc_cleaner_run_total[5m]))

# GC cleanup success rate
sum(rate(nvca_gc_orphaned_resource_cleanup_total{status="success"}[5m])) /
sum(rate(nvca_gc_orphaned_resource_cleanup_total[5m])) * 100

# Failed cleanups by resource type
sum by (resource_type) (nvca_gc_orphaned_resource_cleanup_total{status="failure"})
```

### Upstream (ICMS) Dashboard

```promql
# Upstream success rate across all operations
sum(rate(nvca_upstream_request_total{status="success"}[5m])) /
sum(rate(nvca_upstream_request_total[5m])) * 100

# Request rate by operation
sum by (operation) (rate(nvca_upstream_request_total[5m]))

# Failure rate by operation
sum by (operation) (rate(nvca_upstream_request_total{status="failure"}[5m]))

# Heartbeat failure breakdown by HTTP status
sum by (http_status) (rate(nvca_upstream_request_total{operation="heartbeat", status="failure"}[5m]))
```

---

## Alerting Examples

### Critical Alerts

```yaml
# High queue message dequeue failures
- alert: HighQueueDequeueFailureRate
  expr: |
    rate(nvca_event_error_total{nvca_event_name=~".*queue.*"}[5m]) > 0.1
  for: 5m
  annotations:
    summary: High rate of queue dequeue failures

# Capacity exhaustion
- alert: CapacityExhausted
  expr: |
    sum by (nvca_cluster_name) (nvca_instance_type_allocatable) == 0
  for: 10m
  annotations:
    summary: No available capacity in cluster

# High container crash rate
- alert: HighContainerCrashRate
  expr: |
    rate(nvca_container_crash_total[5m]) > 0.05
  for: 5m
  annotations:
    summary: High rate of container crashes

# MiniService failures
- alert: MiniServiceFailed
  expr: |
    nvca_miniservice_controller_miniservice_ready_status < 0
  for: 5m
  annotations:
    summary: MiniService instance has failed

# High translation error rate
- alert: HighTranslationErrorRate
  expr: |
    rate(nvca_miniservice_controller_event_error_total[5m]) > 0.1
  for: 5m
  annotations:
    summary: High rate of workload translation errors
```

# High workload failure rate
- alert: HighWorkloadFailureRate
  expr: |
    sum(rate(nvca_workload_result_total{workload_status="failure"}[5m])) /
    sum(rate(nvca_workload_result_total[5m])) > 0.1
  for: 10m
  annotations:
    summary: More than 10% of workloads are failing

# Spike in no-capacity failures
- alert: NoCapacityFailureSpike
  expr: |
    rate(nvca_workload_result_total{failure_category="no_capacity"}[5m]) > 0.05
  for: 10m
  annotations:
    summary: High rate of workload failures due to no capacity
```

### Warning Alerts

```yaml
# Queue backlog building up
- alert: QueueBacklogIncreasing
  expr: |
    nvca_event_queue_length > 100
  for: 15m
  annotations:
    summary: Event queue backlog is growing

# Low batch utilization
- alert: LowBatchUtilization
  expr: |
    (rate(nvca_queue_dequeue_batch_size_sum[5m]) / rate(nvca_queue_dequeue_batch_size_count[5m])) < 2
    and rate(nvca_queue_dequeue_batch_size_count[5m]) > 0
  for: 30m
  annotations:
    summary: Queue batches are consistently small, may indicate low queue depth

# Queues consistently empty (may indicate upstream issues)
- alert: QueuesConsistentlyEmpty
  expr: |
    (rate(nvca_queue_dequeue_batch_size_bucket{le="0"}[5m]) / rate(nvca_queue_dequeue_batch_size_count[5m])) > 0.95
    and rate(nvca_queue_dequeue_batch_size_count[5m]) > 0
  for: 30m
  annotations:
    summary: Queue is consistently empty (>95% empty polls), may indicate upstream issues or idle cluster

# Slow storage requests
- alert: SlowStorageRequests
  expr: |
    histogram_quantile(0.99, sum by (le) (rate(nvca_storage_controller_request_duration_bucket[5m]))) > 240
  for: 15m
  annotations:
    summary: Storage requests are taking longer than the 4-minute SLO (p99 > 240s)

# High model cache failure rate
- alert: HighModelCacheFailureRate
  expr: |
    sum(rate(nvca_model_cache_result_total{result="failure"}[5m])) /
    sum(rate(nvca_model_cache_result_total[5m])) > 0.1
  for: 10m
  annotations:
    summary: Model cache operations have high failure rate (>10%)

# ReVal service errors
- alert: ReValServiceErrors
  expr: |
    rate(nvca_miniservice_controller_reval_request_total{http_code!="200"}[5m]) > 0.05
  for: 10m
  annotations:
    summary: ReVal service is returning errors

# Kata runtime isolation unexpectedly disabled
- alert: KataRuntimeIsolationDisabled
  expr: |
    nvca_kata_runtime_isolation_enabled == 0
  for: 5m
  annotations:
    summary: Kata runtime isolation is not enabled on this cluster

# GC cleaner failures
- alert: GCCleanerFailures
  expr: |
    rate(nvca_gc_cleaner_run_total{status="failure"}[5m]) > 0.05
  for: 10m
  annotations:
    summary: GC cleaners are experiencing failures

# High orphaned resource cleanup failures
- alert: HighOrphanedResourceCleanupFailures
  expr: |
    rate(nvca_gc_orphaned_resource_cleanup_total{status="failure"}[5m]) > 0.1
  for: 15m
  annotations:
    summary: High rate of failures cleaning up orphaned resources
```

---

## Cluster-Validator Metrics

The cluster-validator runs as a short-lived process (init container + CronJob) so it cannot serve a `/metrics` endpoint directly. Instead, it writes a structured summary to a well-known ConfigMap at the end of every run, and the NVCA agent's long-lived `/metrics` endpoint republishes the values as gauges. The fixed-cardinality gauges are updated in place on each new ConfigMap update, so a run does not mint a fresh series set (no TSDB churn). Config-driven series (per-endpoint and per-netpol-pair) can come and go as the customer changes their network checks; the agent prunes any such series that the latest run no longer reports. The run time itself is exposed as the value of `nvca_cluster_validator_last_run_timestamp_seconds`, not as a label.

### `nvca_cluster_validator_ready`

Overall verdict for the latest cluster-validator run. **This is the load-bearing SLI metric** for cluster-readiness alerting.

- **Type**: Gauge
- **Value**: 1 if the run passed all critical checks (NVCF-Ready), 0 otherwise (NVCF-Not-Ready)
- **Labels**: default labels only (initialized to 0 until the first run completes)

### `nvca_cluster_validator_check_status`

Per-check status from the latest run. The check set is fixed (~10 entries; see `CheckKey*` constants in `internal/clustervalidator/summary.go`).

- **Type**: Gauge
- **Value**: 1 = passed, 0 = failed (or not-run; the `check` label is omitted entirely when a check was skipped)
- **Labels**: default labels + `check`

> **Alerting caveat — absent vs. zero for optional checks.** Three checks are
> *conditional*: `endpoint_reachability`, `configurable_netpol`, and
> `netpol_enforcement` only run when a network-checks ConfigMap is configured.
> They are pre-initialized to `0` in the init-to-zero baseline so they
> appear on the first scrape, but on the **first real run of a cluster that has
> no network-checks config** they are pruned and **not re-emitted** — they go
> *absent*, not `0` (the validator omits a check it didn't run so "not run" is
> distinguishable from "ran and failed"). Write alerts on these three with an
> `absent()` guard, not a bare `== 0`, e.g.
> `absent(nvca_cluster_validator_check_status{check="endpoint_reachability"}) or nvca_cluster_validator_check_status{check="endpoint_reachability"} == 0`.
> The seven always-run checks (control_plane, worker_nodes_all_ready, webhooks,
> network_policies_supported, smb_csi, gpu_resources, gpu_operator) are always
> present and safe to alert on with `== 0`.

### `nvca_cluster_validator_endpoint_reachable`

Per-endpoint reachability for the user-configured `reachability.endpoints` list. Variable cardinality, but pruned on each new run.

- **Type**: Gauge
- **Value**: 1 if reachable, 0 otherwise
- **Labels**: default labels + `endpoint` (user-supplied name) + `critical` (`"true"`/`"false"`)

### `nvca_cluster_validator_netpol_pair_passed`

Directional NetworkPolicy coverage for the user-configured `networkPolicies.pairs` list. Each pair emits up to four series — one per `direction` × `policy_side` — so an operator can see exactly which side is blocked rather than just "the pair failed".

- **Type**: Gauge
- **Value**: 1 if that side allows the traffic, 0 if blocked
- **Labels**: default labels + `pair` (user-supplied name) + `direction` (`a_to_b`/`b_to_a`) + `policy_side` (`egress` = the source namespace's egress, `ingress` = the destination namespace's ingress) + `critical`
- **Overall pair coverage**: `min by (pair) (nvca_cluster_validator_netpol_pair_passed)` (1 only when all four sides allow)

> A `0` always means a real policy block. When a direction cannot be evaluated before reaching policy rules (a namespace in the pair does not exist, or an API error occurs), that direction's series are omitted rather than emitted as `0`, so they never misreport a policy block. The pair still fails (`nvca_cluster_validator_check_status{check="configurable_netpol"}` is `0`) and the validator's recommendation names the real cause.

```promql
# Which side of a pair is blocked?
nvca_cluster_validator_netpol_pair_passed == 0

# Overall: did each pair pass end-to-end on the latest run?
min by (pair) (nvca_cluster_validator_netpol_pair_passed)
```

### `nvca_cluster_validator_last_run_timestamp_seconds`

Unix timestamp (seconds) of the latest cluster-validator run. The canonical staleness signal — alert on `time() - <metric> > <threshold>` to catch a validator pod that has stopped running entirely.

- **Type**: Gauge
- **Labels**: default labels only

### `nvca_cluster_validator_last_run_duration_seconds`

Wall-clock duration of the latest run.

- **Type**: Gauge
- **Labels**: default labels only

### Example PromQL

```promql
# Current SLI
nvca_cluster_validator_ready

# Which checks regressed between the last two runs?
( nvca_cluster_validator_check_status offset 3h ) - nvca_cluster_validator_check_status

# Alert: any critical endpoint unreachable on the latest run
nvca_cluster_validator_endpoint_reachable{critical="true"} == 0

# Alert: validator hasn't run in 6h (CronJob or pod stuck).
# The `> 0` guard excludes the "never ran" baseline (the metric is 0 until the
# first run); without it the alert fires immediately on a fresh deployment,
# since time() - 0 is always far greater than the threshold.
(time() - nvca_cluster_validator_last_run_timestamp_seconds > 21600)
  and nvca_cluster_validator_last_run_timestamp_seconds > 0
```

### Edge cases

| Scenario | Effect on metrics |
|---|---|
| Agent boots before any validator run | Fixed-cardinality gauges at 0. No config-driven (endpoint/netpol) series until first run. |
| Agent restart after a successful run | Reconciler's initial List delivers an Add event; metrics populated immediately. |
| Validator pod panics mid-run | ConfigMap not updated; last-good metrics retained. Operator detects via `_last_run_timestamp_seconds` staleness. |
| Summary ConfigMap deleted | Last-good metrics **preserved** — an accidental delete (kubectl, GC sweep, reinstall) must not wipe the SLI. Genuine staleness is caught by the `_last_run_timestamp_seconds` alert. |
| Explicit metrics reset requested | Create a ConfigMap named `cluster-validator-metrics-reset` in the summary namespace. The agent resets all fixed gauges to the zero baseline (and prunes config-driven series) and then deletes that ConfigMap (consumes the one-shot signal). |
| Malformed JSON / unknown schemaVersion | Last-good metrics preserved (no transient blip surfaces as SLI failure). |
| Endpoint removed from customer config | Its `_endpoint_reachable` series pruned on next run; new endpoints appear. |

---

## Metric Cardinality

The following metrics have dynamic cardinality based on cluster configuration:

- **Queue metrics** (`nvca_queue_message_dequeued_total`, `nvca_queue_dequeue_batch_size`): Cardinality = number of GPU types × number of queue types (typically 4-6 GPU types × 4 queue types = 16-24 series per cluster)
- **Instance type metrics**: Cardinality = number of distinct instance types in cluster
- **Event metrics**: Cardinality = number of event types (relatively static)
- **Container metrics**: Cardinality = number of monitored containers (relatively static)
- **Workload result metric** (`nvca_workload_result_total`): 64 series (2 workload types × 2 workload kinds × 16 status/category combinations, fixed)
- **K8s API metrics**: Cardinality = number of resource types accessed (relatively static)
- **Storage controller metrics**:
  - Request duration: number of storage request phases (low, typically 2-3 series)
  - Model cache result: result values × failure reasons (low, typically 2-14 series)
- **MiniService controller metrics**:
  - Reconcile phase: number of miniservice phases (low, typically 5-6 series)
  - Ready status: number of active functions/tasks (dynamic, scales with workload)
  - ReVal requests: number of endpoints × HTTP codes (low, typically 2-4 series)
  - Event errors: number of event types (low, typically 2-3 series)
- **GC metrics**:
  - Orphaned resource cleanup: number of resource types × status values (low, typically 2-4 series)
  - Cleaner runs: number of cleaner names × status values (low, typically 2-4 series)
- **Cluster attribute metrics**: 1 series per cluster (Kata runtime isolation enabled/disabled)
- **Cluster-validator metrics**: fixed-cardinality vectors (`nvca_cluster_validator_ready`, `_last_run_timestamp_seconds`, `_last_run_duration_seconds`) yield 1 series each and are updated in place on every run (no per-run churn). `_check_status` yields ~10 series (one per built-in check). `_endpoint_reachable` is bounded by the customer's `networkChecks` config (typically <20 entries); `_netpol_pair_passed` is bounded by 4 × the number of configured pairs (direction × policy_side). Config-driven series are pruned when the latest run no longer reports them.
- **Upstream request metric** (`nvca_upstream_request_total`): 6 series fixed (3 operations × 2 statuses, pre-initialized)
- **Scheduler workload count** (`nvca_scheduler_workload_count`): 4 series fixed (2 schedulers × 2 workload kinds, pre-initialized)

Total expected cardinality per cluster: **159-254 time series** depending on configuration and active workload count.
