# Monitoring & Observability

The NVIDIA Cluster Agent and Operator provide built-in monitoring through Prometheus metrics,
structured logging, and OpenTelemetry tracing.

## Monitoring Data

### Metrics

**Prerequisites**

To use the PodMonitor and ServiceMonitor examples below, you must first install the Prometheus Operator. Follow the [Prometheus Operator installation guide](https://prometheus-operator.dev/docs/getting-started/installation/) to set this up in your cluster.

The cluster agent and operator emit Prometheus-style metrics. The following metric labels are available by default. The full list of available metrics are updated regularly and therefore not listed.

| Metric Label       | Metric Label Description         |
| ------------------ | -------------------------------- |
| nvca_event_name    | The name of the event            |
| nvca_nca_id        | The NCA ID of this NVCA instance |
| nvca_cluster_name  | The NVCA cluster name            |
| nvca_cluster_group | The NVCA cluster group           |
| nvca_version       | The NVCA version                 |

Cluster maintainers can scrape the available metrics. See a full example of how to do this with an OpenTelemetry Collector in the cluster [cluster monitoring example](https://github.com/NVIDIA/nvcf/tree/main/examples/cluster-monitoring-sample).

Use the following examples of a PodMonitor for NVCA Operator and ServiceMonitor for NVCA for reference:

**Sample NVCA Operator PodMonitor**

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
    labels:
        app.kubernetes.io/component: metrics
        app.kubernetes.io/instance: prometheus-agent
        app.kubernetes.io/name: metrics-nvca-operator
        jobLabel: metrics-nvca-operator
        release: prometheus-agent
        prometheus.agent/podmonitor-discover: "true"
    name: metrics-nvca-operator
    namespace: monitoring
spec:
    podMetricsEndpoints:
    - port: http
      scheme: http
      path: /metrics
    jobLabel: jobLabel
    selector:
        matchLabels:
            app.kubernetes.io/name: nvca-operator
    namespaceSelector:
        matchNames:
        - nvca-operator
```

**Sample NVCA ServiceMonitor**

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
    labels:
        app.kubernetes.io/component: metrics
        app.kubernetes.io/instance: prometheus-agent
        app.kubernetes.io/name: metrics-nvca
        jobLabel: metrics-nvca
        release: prometheus-agent
        prometheus.agent/servicemonitor-discover: "true"
    name: prometheus-agent-nvca
    namespace: monitoring
spec:
    endpoints:
    - port: nvca
    jobLabel: jobLabel
    selector:
        matchLabels:
            app.kubernetes.io/name: nvca
    namespaceSelector:
        matchNames:
        - nvca-system
```

### Logs

Both the Cluster Agent and Cluster Agent Operator emit logs locally by default.

Local logs for the NVIDIA Cluster Agent Operator can be obtained via `kubectl`:

```bash
kubectl logs -l app.kubernetes.io/instance=nvca-operator -n nvca-operator --tail 20
```

Similarly, NVIDIA Cluster Agent logs can be obtained with the following command via kubectl:

```bash
kubectl logs -l  app.kubernetes.io/instance=nvca -n nvca-system --tail 20
```

<Warning>
Current function-level inference container logs are **not supported** for functions deployed on non-NVIDIA-managed clusters. Customers are encouraged to emit logs directly from their inference containers running on their own clusters to any third-party tool, there are no public egress limitations for containers.

</Warning>

### Tracing

The NVIDIA Cluster Agent exports OpenTelemetry traces to Lightstep. The
`lightstep` exporter requires an access token and an OTLP gRPC endpoint.
`otelConfig.endpoint` is required when `otelConfig.accessToken` is set. A
successful Kubernetes patch only confirms that the API accepted the
configuration; the Operator must reconcile it before tracing is enabled.

#### Enable tracing with Lightstep

1. Get a [Lightstep access token](https://docs.lightstep.com/docs/create-and-manage-access-tokens)
   and export it as `LS_ACCESS_TOKEN` in your shell.
2. Select the endpoint for your Lightstep project. For direct ingestion, use
   `ingest.lightstep.com:443` for the US region or
   `ingest.eu.lightstep.com:443` for the EU region. For a managed collector,
   obtain its endpoint from your administrator. Use `host:port` without an
   `https://` prefix. See the [Lightstep tracing quickstart](https://docs.lightstep.com/docs/quick-start-instrumentation).

   ```bash
   export LS_OTEL_ENDPOINT="ingest.lightstep.com:443"
   ```

3. List the backends and select the one to configure:

   ```bash
   kubectl get nvcfbackends -n nvca-operator
   nvcf_cluster_name="<backend-name>"
   ```

4. Apply the configuration. This command requires Python 3 and sends the token
   through standard input instead of including it in the `kubectl` arguments.

   ```bash
   python3 - <<'PYCODE' | kubectl patch nvcfbackends.nvcf.nvidia.io \
     -n nvca-operator "$nvcf_cluster_name" --type=merge --patch-file=/dev/stdin
   import json
   import os

   print(json.dumps({"spec": {"overrides": {"featureGate": {"otelConfig": {
       "exporter": "lightstep",
       "endpoint": os.environ["LS_OTEL_ENDPOINT"],
       "serviceName": "nvcf-nvca",
       "accessToken": os.environ["LS_ACCESS_TOKEN"],
   }}}}}))
   PYCODE
   ```

5. Check the Operator logs and the agent rollout:

   ```bash
   kubectl logs -l app.kubernetes.io/instance=nvca-operator \
     -n nvca-operator --tail=100
   kubectl rollout status deployment/nvca -n nvca-system --timeout=5m
   ```

   Wait for the Operator to reconcile the change and replace the agent Pod.
   Then deploy or invoke a function and confirm that new spans appear in your
   Lightstep project. An existing ready agent or a successful patch alone does
   not confirm trace delivery.

If the Operator logs `otelConfig.endpoint is required when
otelConfig.accessToken is set`, reapply the configuration with the endpoint.
A merge patch preserves existing fields, so an old endpoint can mask an
incomplete configuration example. Always specify the endpoint and token
together when enabling tracing.

For a Helm-managed backend, persist the same endpoint in `otel.endpoint` along
with `otel.enabled`, `otel.lightstep.serviceName`, and
`otel.lightstep.accessToken` in your [Helm values](./reference.md#opentelemetry).
