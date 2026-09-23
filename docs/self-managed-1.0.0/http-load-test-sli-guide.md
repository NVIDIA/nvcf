(http-load-test-sli-guide)=

# HTTP Load Test SLI Guide

This document describes which metrics to watch when load testing a self-hosted
NVCF deployment using direct HTTP invocations, what each metric indicates, and
how to interpret the failure sequence. Values are hardware-dependent -- what is
transferable is the order in which signals appear and what they mean.

For run commands and cluster setup, see {ref}`self-managed-http-load-test`.

---

## How Self-Hosted NVCF Handles HTTP Load

- **Envoy Gateway** routes HTTP requests based on the `Host` header and is in
  the return path for responses. It does not queue -- requests pass through
  immediately or are rejected. Envoy enforces TCP connection timeouts: if a
  connection is held open beyond the LB timeout, Envoy closes the socket
  directly, producing an `EOF` error at the client with no HTTP status code.
- The **Invocation Service (IS)** receives direct HTTP invocation requests,
  dispatches to the worker via NATS, and holds the connection open until the
  worker responds or the configured hold-open duration expires.
- The **worker pod** serves inference over HTTP. Its concurrency limit
  (`maxRequestConcurrency`) and inference time per request set the maximum
  sustainable req/s.
- **NATS** dispatches work between the IS and worker.
- **NVCA** manages worker scaling. It only acts when scale-out is configured
  (`minInstances < maxInstances`).

**Primary data source**: the Invocation Service (Prometheus `job=invocation`).

---

## SLIs to Monitor

### Group 1: Leading Indicators

These rise *before* errors appear. Use them to predict saturation.

#### `axum_http_requests_total` (rate)

**What it is**: Request rate at the IS per method and path.

**What to look for**:

- `rate(axum_http_requests_total{job="invocation", method="POST"}[1m])`
  gives req/s. Should rise with load during healthy operation.
- **Plateau while VUs keep rising = worker throughput ceiling reached.** The
  IS continues dispatching to NATS as fast as the worker can consume;
  additional requests accumulate as in-flight HTTP connections rather than
  increasing throughput. The IS is not the bottleneck.

```none
rate(axum_http_requests_total{job="invocation", method="POST"}[1m])
```

#### `axum_http_requests_pending`

**What it is**: In-flight HTTP requests currently held at the IS, waiting for
the worker to respond.

**What to look for**:

- Non-zero at idle = IS under pressure before load even peaks.
- Rising without a matching rise in `axum_http_requests_total` rate = requests
  are stacking at the IS faster than workers can drain them:

```none
axum_http_requests_pending{job="invocation", method="POST"}
```

---

### Group 2: Throughput and Capacity

#### `nvca_instance_type_allocatable`

**What it is**: Number of worker slots available in the cluster fleet.

**What to look for**:

- Drops as workers are allocated to deployments.
- If allocatable reaches 0 on a fixed cluster: new worker deployments will fail with a no-capacity error

```none
nvca_instance_type_allocatable{instance_type="<your-instance-type>"}
```

---

### Group 3: Lagging Indicators

These confirm saturation after it has occurred. k6 is the primary source.

#### `http_req_duration` p95 (k6)

**What it is**: End-to-end request latency measured by k6.

**What to look for**:

- Rises steeply after the throughput plateau.
- Use p95 > 500ms as a warning, p95 > 2s as at/past saturation.

**k6 metric**: `http_req_duration` (watch p90, p95 in k6 Cloud)

#### `http_req_failed` (k6)

**What it is**: k6 metric tracking the rate of failed http requests.

**What to look for**:

- Stays at 0% through moderate overload.
- **Error mode is `EOF`** -- TCP connection closed by Envoy, not the IS.
  The IS holds each direct HTTP invocation connection open while waiting for
  the worker. When the Envoy connection timeout fires first, the socket is
  closed directly. The IS never sends an HTTP error response. The client sees
  `EOF` with no status code.
- **Non-zero `http_req_failed` is a breaking-point signal**, not an early
  warning. The system is well past the capacity wall by the time EOF errors
  appear.

**k6 metric**: `http_req_failed` (rate or count in k6 Cloud)

#### `function_request_latency` p95 (worker-side)

**What it is**: Per-request latency as measured by the worker itself -- the time
spent inside the function from the moment the worker picks up the request.

**What to look for**:

- Complements `http_req_duration` (client-side). If k6 p95 is high but
  worker p95 is low, the bottleneck is queuing at the IS or Envoy, not
  inference time.
- Rising worker latency under load indicates the worker itself is the
  throughput ceiling.

```none
histogram_quantile(0.95, rate(function_request_latency_bucket[1m]))
```

---

### Group 4: Stability Signals

These should be zero during a clean load test. Any non-zero value warrants
investigation.

| Metric | Threshold | What it means |
| --- | --- | --- |
| `nats_event_disconnected` | > 0 | IS lost NATS connection. in-flight requests will stall |
| `total_nats_errors` | > 0 | Cumulative NATS errors from the IS |
| `nvca_event_error_total{nvca_event_name="TICK_ACKNOWLEDGE_REQUEST"}` | > 0 | NVCA failing to acknowledge worker heartbeats |
| `nvca_container_crash_total` | > 0 | Worker pod OOM or crash |
| `nvca_controller_runtime_reconcile_errors_total` | > 0 | k8s controller errors in NVCA |
| `kube_pod_container_status_restarts_total{namespace="envoy-gateway-system"}` | > 0 during a run | Envoy gateway crash |
| `envoy_listener_downstream_cx_overflow{envoy_listener_address="0.0.0.0_10080"}` | > 0 | Envoy HTTP listener TCP connection ceiling hit |

#### NATS JetStream

NATS is the message bus between the IS and the worker.

**Early-warning signal**: `axum_http_requests_pending` is still the earliest
IS-side queuing indicator. NATS connection counts and stream lag now provide
independent cross-checks.

#### Envoy Gateway

Envoy Gateway sits in both the request and return path for all HTTP invocations.
It enforces TCP connection timeouts and is the direct cause of EOF failures at
overload.

Useful envoy signals during an HTTP test:

```none
# Active downstream connections on the HTTP listener
envoy_listener_downstream_cx_active{envoy_listener_address="0.0.0.0_10080"}

# Overflow -- TCP connection ceiling hit (should stay 0 unless saturated)
envoy_listener_downstream_cx_overflow{envoy_listener_address="0.0.0.0_10080"}

# Envoy pod restart count (the April 2026 SIGSEGV signal)
sum(increase(kube_pod_container_status_restarts_total{namespace="envoy-gateway-system"}[$__range]))
```

---

## The Saturation Sequence

Regardless of hardware, HTTP saturation follows this order:

```none
1. axum_http_requests_total rate rises with load
         ↓
2. rate growth decouples from VU count                 ← LEADING SIGNAL
         ↓
3. axum_http_requests_pending climbs                   ← QUEUING AT IS
         ↓
4. Throughput (req/s) plateaus despite more VUs        ← CAPACITY WALL
         ↓
5. k6 p95 latency rises steeply (>500ms, then >2s)    ← LAGGING SIGNAL
         ↓
6. EOF errors appear (Envoy closes held connections)   ← FAILURE VISIBLE TO CLIENTS
```

Steps 1-4 are observable before errors reach clients. Steps 5-6 confirm
saturation is underway.

---

## Recommended Thresholds

These are starting-point thresholds to calibrate against your baseline -- not
absolute values. Hardware, workload, and deployment configuration all affect
where these numbers land.

| Signal | Threshold | Action |
| --- | --- | --- |
| `axum_http_requests_pending` | > 0 at steady-state between tests | IS already under pressure |
| IS throughput plateau | req/s flat while VUs still increasing | Capacity wall reached |
| `http_req_duration` p95 (k6) | > 500ms | Approaching saturation |
| `http_req_duration` p95 (k6) | > 2s | At or past saturation |
| `http_req_failed` (k6) | > 0% | Breaking point -- Envoy closing connections |
