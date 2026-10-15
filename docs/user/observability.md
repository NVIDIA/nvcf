# Observability Configuration

This page provides guidance on configuring observability for self-hosted NVCF control-plane, including metrics, logging, and tracing.

## Find the answer to your question

Common operator questions and where to look on this page or in linked references.

| Question | Where to look |
|----------|---------------|
| How do I see application-level NVCF stats (number of functions, queue depth, request latency)? | [State Metrics Service metrics](./metrics/state-metrics/metrics.md). The page documents per-function instance count, queue depth, and request latency, plus other function-level signals. |
| How do I debug a single request end-to-end? | Combine the per-hop signals: enable tracing per [Tracing Configuration](#tracing-configuration), correlate with the [Metrics Overview](./metrics/metrics-index.md) for each service in the request path, and tail the matching service logs. A consolidated hop-by-hop walkthrough is in development. |
| Where are per-service metrics? | [Metrics Overview](./metrics/metrics-index.md). |
| Where are gRPC proxy metrics? | [gRPC Proxy metrics](./metrics/grpc-proxy/metrics.md). The page documents client connection counts, NATS pipe health, gRPC worker session-attach latency, and HTTP RED metrics. |
| How do I add custom spans or metrics in a Kit application? | Use the OpenTelemetry API directly, the OmniTrace helper, the Carbonite static metrics API, or the `omni::observability::IMeter` interface. Refer to the Omniverse Kit and Carbonite documentation for details. |
| Where are reference dashboards? | [Example dashboards](./example-dashboards.md) and the [Dashboards](#dashboards) section below. |

## Overview

Self-hosted NVCF control-plane observability enables users to monitor the health and performance of their NVCF deployment. The observability solution is designed to be:

- **Cloud-agnostic**: Works in any Kubernetes environment (cloud provider, on-premises, or air-gapped)
- **Offline-capable**: Fully functional in isolated networks without external dependencies
- **Bring-Your-Own (BYO)**: Integrates with your existing observability platforms
- **No vendor lock-in**: Uses open standards (Prometheus, OpenTelemetry, OTLP)

The observability solution currently provides:

- [Metrics Collection]: Prometheus-compatible metrics from all control-plane services
- [Logging]: Logs emitted to stdout/stderr for easy collection
- [Tracing]: Distributed tracing via OTLP to your collector
- [Dashboards]: Reference Grafana dashboards for key metrics

<Note>
**Looking for a quick start?** If you want to quickly deploy example observability components
to explore metrics, logs, and dashboards, see [self-hosted-example-dashboards](./example-dashboards.md).

The example deployments are designed for development and testing only, and are not suitable
for production use. For production deployments, follow the guidance on this page to integrate
with your own observability infrastructure.

</Note>

## Early Access Phase

NVCF self-hosted observability is currently in Early Access (EA). During EA, NVCF provides interfaces and documentation for you to integrate with your own observability backend:

**What's Provided:**

- Documented metrics for critical control-plane services
- Example scrape targets for prometheus-operator ServiceMonitor configuration
- Metrics exposed via Prometheus-compatible endpoints
- Logs emitted to stdout/stderr for easy collection
- Configuration and deployment documentation
- Example dashboards for key metrics

**Your Responsibility:**

- Deploy and manage your own observability backend (Prometheus, Grafana, Loki, Elasticsearch, etc.)
- Configure metrics scraping from control-plane services
- Deploy log collectors (e.g., Fluentd, Promtail, OTel Collector) to aggregate logs
- Set up your preferred visualization and alerting tools

## Control-Plane Services

The following control-plane services expose metrics and logs for monitoring:

**Core NVCF Services:**

- **NVCF API**: Main API for function management and invocation
- **Invocation Service**: Handles function invocation requests
- **SPOT Instance Service (SIS)**: Manages worker pod and cluster state
- **State Metrics Service**: Aggregates and exports NVCF-specific metrics

**Supporting Services:**

- **Cassandra (C\*)**: Primary database for control-plane state
- **OpenBao/Vault**: Secret management and S2S authentication
- **Encrypted Secrets Service (ESS)**: Function and account secrets
- **NATS Core**: Pub/sub messaging
- **NATS JetStream**: Persistent messaging

**Worker Pod Components:**

- **Utils Container**: Proxy to NATS from user applications
- **Init Container**: Setup and resource loading
- **Inference Container**: Inference workload

## Architecture

### Metrics Collection

All control-plane services expose Prometheus-compatible metrics endpoints. You can scrape these metrics using:

- **Prometheus Operator**: Create ServiceMonitor resources based on the provided scrape targets
- **Prometheus**: Configure scrape targets manually
- **OpenTelemetry Collector**: Use the Prometheus receiver

**Metrics Documentation:**

Detailed metrics documentation is available for each service, including metric names,
types, labels, and descriptions. See the per-service metrics reference under the
`Metrics` section.

### Logging

**Log Format:**

- All services emit logs to stdout/stderr (standard for Kubernetes)
- Sensitive data redaction must be configured by the log collector

**Log Collection:**

You can collect logs using any Kubernetes-compatible log aggregator:

- Fluentd or Fluent Bit
- Promtail (for Loki)
- Filebeat (for Elasticsearch)
- OpenTelemetry Collector (filelog receiver)

**System Logs:**

System logs are available at standard UNIX locations and from the systemd journal.

### Tracing (Available in GA)

Distributed tracing support via OpenTelemetry Protocol (OTLP) is planned for a future release:

- Key flows will be instrumented with OpenTelemetry SDK
- Traces will be exportable via OTLP (HTTP or gRPC)
- Configurable sampling strategies
- Support for any OTLP-compatible backend (Jaeger, Tempo, Zipkin, etc.)
- Tracing is configurable via Helm values under `global.observability.tracing`

## Configuration

You configure observability by integrating with your own backend:

### Metrics Scraping

Metrics export is opt-in and disabled by default. Enable it in your Helmfile
environment before configuring scrape targets:

```yaml
global:
  observability:
    metrics:
      enabled: true
```

Use Prometheus Operator with the provided ServiceMonitor examples:

```yaml
# Example ServiceMonitor for NVCF API
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: nvcf-api
  namespace: nvcf
spec:
  selector:
    matchLabels:
      app: nvcf-api
  endpoints:
  # Endpoint created based on the scrape target in the
  # per-service metrics documentation
  - port: metrics
    interval: 30s
    path: /metrics
```

Or configure Prometheus scrape targets manually in your prometheus.yml.

#### Application-level NVCF stats

The State Metrics Service exposes per-function signals you can query in
Prometheus. The following PromQL examples cover the three most common
operator questions. Metric names and labels are sourced from
[State Metrics Service metrics](./metrics/state-metrics/metrics.md).

Number of registered functions:

```promql
# nvcf_function_info is emitted per function with descriptive labels.
# Dedupe by function_id so multiple label series do not inflate the count.
count(count by (function_id) (nvcf_function_info))
```

Queue depth per function:

```promql
# nvcf_function_queue_depth is a gauge keyed by function_id.
sum by (function_id, name) (nvcf_function_queue_depth)
```

Function request latency (p50 and p95) over a 5 minute window:

```promql
# p50
histogram_quantile(
  0.50,
  sum by (le, function_id) (rate(function_request_latency_bucket[5m]))
)

# p95
histogram_quantile(
  0.95,
  sum by (le, function_id) (rate(function_request_latency_bucket[5m]))
)
```

### Log Collection

Deploy a log collector as a DaemonSet to ship logs to your backend:

```bash
# Example: Deploy Promtail for Loki
kubectl apply -f promtail-daemonset.yaml

# Example: Deploy Fluentd or Fluent Bit
kubectl apply -f fluentd-daemonset.yaml
```

Configure your log collector to:

- Tail logs from all namespaces
- Add metadata labels (pod name, namespace, service)
- Forward to your log aggregation backend (Loki, Elasticsearch, etc.)

### Tracing Configuration

Enable distributed tracing by setting Helm values under
`global.observability.tracing`. The control-plane exports traces via OTLP
to your own OTLP-compatible collector. Set `collectorEndpoint`,
`collectorPort`, and `collectorProtocol` to match your collector's address.
`collectorProtocol` is the endpoint URI scheme expected by the stack, not the
OTLP transport.

Helm overrides example:

```yaml
global:
  observability:
    tracing:
      enabled: true
      collectorEndpoint: "otel-collector-gateway-collector.observability.svc.cluster.local"
      collectorPort: 4317
      collectorProtocol: http
```

Configuration fields:

- `enabled`: Set to `true` to enable OTLP trace export from control-plane
  services.
- `collectorEndpoint`: DNS name or address of your OTLP collector (e.g.,
  OpenTelemetry Collector, Jaeger collector). Use a Kubernetes service DNS name
  such as `<service>.<namespace>.svc.cluster.local` when the collector runs
  in-cluster.
- `collectorPort`: Port on which the collector accepts OTLP traffic (e.g.,
  4317 for gRPC, 4318 for HTTP depending on your collector setup).
- `collectorProtocol`: URI scheme used to build the collector endpoint
  (`http` or `https`). This value does not select the OTLP transport.

Ensure your collector is deployed and reachable from the NVCF control-plane
namespace, and that it forwards traces to your backend (Jaeger, Tempo, Zipkin,
or another OTLP-compatible system).

## Dashboards

Reference Grafana dashboards are provided for control-plane services showing critical metrics for key services:

- ESS (Encrypted Secrets Service)

- Cassandra

- Vault

- Invocation Service

- NVCF API

- SIS (SPOT Instance Service)

- Worker Pods (Utils Container, Init Container, Inference Container)

  - Note: Worker Pods are deployed in the backend cluster, not the control-plane
    cluster, but their configuration is globally controlled as part of the control-plane

- State Metrics Service

**Dashboard Location:**

Dashboards are provided in native Grafana JSON format for [file-provisioning](https://grafana.com/docs/grafana/latest/administration/provisioning/#dashboards).

Load dashboards into Grafana by placing them in `/etc/grafana/provisioning/dashboards/` on startup.

Published dashboards will be available in the
[NVCF examples](https://github.com/NVIDIA/nvcf/tree/main/examples) public GitHub repository.

## Troubleshooting

For troubleshooting common observability issues:

**Metrics not appearing:**

1. Verify the service is exposing metrics:

   ```bash
   # Port-forward to the service metrics port
   kubectl port-forward -n nvcf svc/nvcf-api 8080:8080

   # In another terminal, curl the metrics endpoint
   curl http://localhost:8080/metrics
   ```

2. Check ServiceMonitor or scrape configuration:

   ```bash
   # Verify ServiceMonitor exists
   kubectl get servicemonitor -n nvcf

   # Check ServiceMonitor details
   kubectl describe servicemonitor nvcf-api -n nvcf
   ```

3. Verify network policies allow scraping:

   ```bash
   # List network policies that might block traffic
   kubectl get networkpolicy -n nvcf

   # Test connectivity from Prometheus namespace
   kubectl run -n <prometheus-namespace> --rm -it debug \
     --image=curlimages/curl --restart=Never -- \
     curl http://nvcf-api.nvcf.svc.cluster.local:8080/metrics
   ```

4. Check service logs for errors:

   ```bash
   # Check for metrics-related errors
   kubectl logs -n nvcf deployment/nvcf-api | grep -i metric
   ```

**Logs not being collected:**

1. Verify log collector DaemonSet is running:

   ```bash
   # Check DaemonSet status (e.g., for Fluentd/Fluent Bit)
   # Note: Namespaces may be different depending on the log collector deployment
   kubectl get daemonset -n logging
   kubectl get pods -n logging -l app=fluent-bit
   ```

2. Check collector can access pod logs:

   ```bash
   # Verify log collector has proper volume mounts
   kubectl describe daemonset fluent-bit -n logging | grep -A5 Mounts

   # Check collector logs for errors
   kubectl logs -n logging -l app=fluent-bit --tail=50
   ```

3. Verify log backend is reachable:

   ```bash
   # Test connectivity to log backend (e.g. Loki)
   kubectl run -n logging --rm -it debug \
     --image=curlimages/curl --restart=Never -- \
     curl -v http://loki.logging.svc.cluster.local:3100/ready
   ```

4. Check for log redaction or filtering rules:

   ```bash
   # Review collector configuration
   kubectl get configmap fluent-bit-config -n logging -o yaml

   # Check if logs are being dropped
   kubectl logs -n logging -l app=fluent-bit | grep -i "drop\|filter"
   ```

## Security

**Metrics Endpoints:**

- Metrics endpoints should be accessed over HTTP in-cluster only

  - Any external access should be SSL/TLS or mTLS secured with a reverse proxy or other ingress controller, or
  - Aggregated locally and exposed via a secured otel-collector

- All sensitive log data should be redacted by the log collector (currently, this is the responsibility of the log collector, not the service)

  - Example implementation by OTEL Collector: [Log Redaction](https://opentelemetry.io/docs/languages/dotnet/logs/redaction/)

- User-provided observability backend should be properly secured with RBAC, TLS/SSL, and other security best practices.

## Related Documentation

- [OpenTelemetry documentation](https://opentelemetry.io/docs/)
- [Prometheus documentation](https://prometheus.io/docs/)

## Version Compatibility

NVCF self-hosted control-plane observability is compatible with:

- Supported versions are the latest Kubernetes minor release and the two prior minor releases (N-2). See official Kubernetes docs for current supported [versions](https://kubernetes.io/releases/version-skew-policy/#supported-versions). 
- Any Prometheus-compatible metrics collection system
- Any log aggregation system that can collect from Kubernetes stdout/stderr or read
  from the filesystem (depending on K8s cluster configuration)

For the latest compatibility information, see the release notes.
