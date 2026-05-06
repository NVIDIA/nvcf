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

Cluster maintainers can scrape the available metrics. See a full example of how to do this with an OpenTelemetry Collector in the cluster [here](https://github.com/NVIDIA/nv-cloud-function-helpers/tree/main/examples/cluster_monitoring_sample).

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

The NVIDIA Cluster Agent provides OpenTelemetry integration for exporting traces and events to compatible collectors. As of agent version 2.0, the only supported collector receiver is Lightstep.

**Enable Tracing with Lightstep**

1. Get your [Lightstep access token](https://docs.lightstep.com/docs/create-and-manage-access-tokens) from the [Lightstep UI](https://app.lightstep.com) and set to `LS_ACCESS_TOKEN` environment variable.
2. Get the NVCF cluster name:

```bash
nvcf_cluster_name="$(kubectl get nvcfbackends -n nvca-operator -o name | cut -d'/' -f2)"
```

3. Apply the tracing configuration:

```bash
kubectl patch nvcfbackends.nvcf.nvidia.io -n nvca-operator "$nvcf_cluster_name"  --type=merge --patch="{\"spec\":{\"overrides\":{\"featureGate\":{\"otelConfig\":{\"exporter\":\"lightstep\",\"serviceName\":\"nvcf-nvca\",\"accessToken\":\"${LS_ACCESS_TOKEN}\"}}}}}"
```
