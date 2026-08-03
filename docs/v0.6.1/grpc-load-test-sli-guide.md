(grpc-load-test-sli-guide)=

# gRPC Load Test SLI Guide

This document describes which metrics to watch when load testing a self-hosted
NVCF gRPC deployment, what each metric indicates, and how to interpret the
saturation sequence. Values are hardware-dependent -- what is transferable is
the order in which signals appear and what they mean.

For run commands and cluster setup, see {ref}`self-managed-grpc-load-test`.

---

## How Self-Hosted NVCF Handles Load

Understanding the request path helps interpret the metrics:

- The **gRPC proxy** holds in-flight requests. It does not reject requests until
  `maxRequestConcurrency` is exhausted -- it queues them.
- The **worker sidecar** is the throughput ceiling. Its concurrency limit
  (`maxRequestConcurrency`) and inference time per request set the maximum
  sustainable req/s.
- **NATS** dispatches work between components. It is downstream of the proxy's
  internal queue and will not show pressure until the proxy itself is saturated.
- **NVCA** only acts on scaling when scale-out is configured
  (`minInstances < maxInstances`).

---

## SLIs to Monitor

### Group 1: Leading Indicators

These rise *before* errors appear. Use them to predict saturation.

#### `nvcf_grpc_proxy_service_active_connections_total`

**What it is**: Number of active worker connections held by the gRPC proxy.

**What to look for**:

- Rises with load during healthy operation.
- **Decouples from throughput at the saturation point** -- connections keep
  rising while req/s flattens. This is the earliest saturation signal.

```none
nvcf_grpc_proxy_service_active_connections_total
```

#### `nvcf_grpc_proxy_service_session_init_seconds_total` (p95)

**What it is**: Time for the proxy to establish a worker session (first contact
for a new connection).

**What to look for**:

- Low at idle, rises when the proxy is busy competing for worker slots.
- A rising p95 means new requests are waiting longer to get a worker session
- Check bucket distribution: are requests piling up in the higher latency
  buckets (>100ms, >250ms)?

```none
histogram_quantile(0.95,
  rate(nvcf_grpc_proxy_service_session_init_seconds_total_bucket{is_reconnect="false"}[1m]))
```

---

### Group 2: Throughput and Capacity

#### `function_request_total`

**What it is**: Cumulative completed requests for a specific function, scraped
from the gRPC Proxy (`job=grpc`). Filter by `function_id` to isolate a
single function's throughput. Labels: `function_id`, `function_version_id`,
`nca_id`.

**What to look for**:

- `rate(function_request_total[1m])` gives req/s. Plot alongside VU count.
- **Throughput plateau = capacity wall.** If req/s stops growing while VUs keep
  increasing, the system is saturated.

```none
rate(function_request_total{job="grpc", function_id="<your-function-id>"}[1m])
```

#### `nvca_instance_type_allocatable`

**What it is**: Available worker slots in the cluster fleet.

**What to look for**:

- Drops as workers are allocated to new deployments
- If allocatable reaches 0 on a fixed cluster: new worker deployments will fail with a no-capacity error

```none
nvca_instance_type_allocatable{instance_type="<your-instance-type>"}
```

---

### Group 3: Lagging Indicators

These confirm saturation after it has occurred. Not useful for early warning,
but confirm the failure mode. k6 is the primary source for these signals.

#### `grpc_req_duration` p95 (k6)

**What it is**: End-to-end gRPC request latency measured by k6.

**What to look for**:

- Rises steeply after the throughput plateau.
- Use p95 > 5s as a lagging SLO threshold. By the time it rises, the capacity
  wall has already been hit.

**k6 metric**: `grpc_req_duration` (watch p90, p95 in k6 Cloud)

#### `grpc_req_failed` (k6)

**What it is**: k6 metric tracking the rate of failed gRPC requests.

**What to look for**:

- Stays near zero through moderate overload. The proxy holds connections and
  queues requests rather than rejecting them -- failures only appear once
  requests have been held long enough to hit the k6 client timeout.
- **Non-zero `grpc_req_failed` is a breaking-point signal**, not an early
  warning. By the time it rises, the system is well past the capacity wall.
- Error type matters:

  - `context deadline exceeded` -- overload, expected at extreme VU counts.
  - `UNAVAILABLE` or connection errors -- proxy or network issue unrelated
    to capacity.

**k6 metric**: `grpc_req_failed` (rate or count in k6 Cloud)

#### `function_request_latency` p95 (worker-side)

**What it is**: Per-request latency as measured by the worker itself. The time
spent inside the function from the moment the worker picks up the request.

**What to look for**:

- Complements `grpc_req_duration` (client-side). If k6 p95 is high but
  worker p95 is low, the bottleneck is queuing at the proxy, not inference time.
- Rising worker latency under load indicates the worker itself is the
  throughput ceiling.

```none
histogram_quantile(0.95, rate(function_request_latency_bucket[1m]))
```

---

### Group 4: Stability Signals

These should remain at zero during a clean load test. Any non-zero value
warrants investigation.

| Metric | Threshold | What it means |
| --- | --- | --- |
| `nvcf_grpc_proxy_service_nats_error_total` | > 0 | Proxy lost connectivity to NATS |
| `nvcf_grpc_proxy_service_nats_reconnect_total` | > 0 | NATS connection instability |
| `nvca_event_error_total{nvca_event_name="TICK_ACKNOWLEDGE_REQUEST"}` | > 0 | NVCA failing to acknowledge worker heartbeats |
| `nvca_container_crash_total` | > 0 | Worker pod OOM or crash |
| `nvca_controller_runtime_reconcile_errors_total` | > 0 | k8s controller errors in NVCA |
| `nvca_event_queue_length` | sustained > 0 | NVCA falling behind processing heartbeat/scaling events |

#### NATS JetStream

NATS is the message bus between the gRPC proxy and the worker.

**Early-warning signal**: `nvcf_grpc_proxy_service_active_connections_total`
decoupling from throughput is still the earliest proxy-side saturation indicator.

#### Envoy Gateway

Useful envoy signals during a gRPC test:

```none
# Active downstream connections on the gRPC listener
envoy_listener_downstream_cx_active{envoy_listener_address="0.0.0.0_10081"}

# Overflow -- TCP connection ceiling hit (should stay 0 unless saturated)
envoy_listener_downstream_cx_overflow{envoy_listener_address="0.0.0.0_10081"}

# Envoy pod restart count
sum(increase(kube_pod_container_status_restarts_total{namespace="envoy-gateway-system"}[$__range]))
```

---

## The Saturation Sequence

Regardless of hardware, saturation follows this order:

```none
1. active_connections_total rises with load
         ↓
2. active_connections_total growth decouples from throughput    ← LEADING SIGNAL
         ↓
3. Throughput (req/s) plateaus despite more VUs                 ← CAPACITY WALL
         ↓
4. grpc_req_duration p95 rises steeply                          ← LAGGING SIGNAL
         ↓
5. Client timeouts (context deadline exceeded)                  ← FAILURE VISIBLE TO CLIENTS
```

Steps 1-3 are observable before errors reach clients. Steps 4-5 confirm
saturation is underway.

---

## Recommended Thresholds

These are relative thresholds to calibrate against your baseline -- not
absolute values. Hardware, workload, and deployment configuration all affect
where these numbers land.

| Signal | Threshold | Action |
| --- | --- | --- |
| `nvcf_grpc_proxy_service_active_connections_total` | > 50% of `maxRequestConcurrency` sustained 2 min | Warning: approaching saturation |
| `nvcf_grpc_proxy_service_active_connections_total` | > 80% of `maxRequestConcurrency` sustained 1 min | Critical: at capacity |
| Throughput plateau | req/s flat while VUs still increasing | Capacity wall reached |
| `session_init_seconds` p95 | > 100ms | Proxy contention -- investigate |
| `nats_error_total` | > 0 | Immediate investigation |
