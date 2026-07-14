# nvcf-observability-stack

Reusable Helmfile stack scaffold for self-hosted NVCF observability. It is
intended to be consumed by self-managed control-plane deployments and optional
standalone compute-plane deployments once the distribution wiring lands.

The stack can own:

- Prometheus Operator CRDs required for `ServiceMonitor` and `PodMonitor`
- OpenTelemetry Operator
- Control-plane OpenTelemetry Collector with Target Allocator support
- Read-only RBAC for monitor discovery and Kubernetes service discovery
- VictoriaMetrics for the bundled local TSDB
- Default NVCF monitor resources, starting with DCGM

BYOO and NVCA collectors are separate optional features and default off.

## Modes

Set `observability.mode` per consuming stack.

| Mode | Behavior |
| --- | --- |
| `install` | Install enabled shared observability infrastructure in this cluster. |
| `existing` | Assume shared infrastructure already exists, but allow default monitors to be rendered against it. |
| `disabled` | Render no observability runtime resources. Valid for compute-plane deployments that do not need NVCF-managed scrape coverage. |

For a single-cluster self-managed deployment, the consuming control-plane stack
should use `install` and the consuming compute-plane stack should use
`existing`. For standalone compute-plane clusters, the compute-plane stack
should use `install` only when NVCF-managed NVCA, DCGM, or worker metric
scraping is required.

## Component Gates

Base defaults keep runtime resources disabled until a consuming stack wires the
chart repository, mirrored images, release artifact path, and environment
overlay.

A single-cluster self-managed overlay should enable the control-plane path:

```yaml
observability:
  mode: install
  namespace: monitoring

prometheusOperatorCrds:
  enabled: true

opentelemetryOperator:
  enabled: true

controlPlaneCollector:
  enabled: true

victoriaMetrics:
  enabled: true

defaultMonitors:
  enabled: true
  dcgm:
    enabled: true
```

`environments/local.yaml` disables runtime resources so the scaffold can render
offline without a cluster or registry configuration.

When `controlPlaneCollector.enabled` is true in `install` mode, the stack uses
the OpenTelemetry Operator and Prometheus Operator CRDs configured by values.
Enable both unless the deployment uses a customer-managed operator or CRD
contract, because the collector is an `OpenTelemetryCollector` CR and the Target
Allocator monitor path requires the monitor CRDs.

The default namespace is `monitoring` so collector traffic matches NVCA's
current DCGM metrics NetworkPolicy. If a consuming stack changes the namespace,
it must also update the NVCA NetworkPolicy contract or provide an equivalent
reachability rule.

For EKS/ADOT-style environments where another operator owns the
OpenTelemetry CRDs, set:

```yaml
opentelemetryOperator:
  enabled: false

prometheusOperatorCrds:
  enabled: false
```

The control-plane collector can still be configured to remote-write to a
customer-owned TSDB, and the autoscaler chart should read from that same TSDB.
When the bundled VictoriaMetrics release is enabled, the collector remote-write
endpoint must match the VictoriaMetrics namespace.

## Default Monitors

Application charts own stable metrics contracts: service names or labels, pod
labels, port names, paths, and namespaces. This stack owns the default monitor
resources that select those contracts.

The initial scaffold includes one concrete default monitor:

- DCGM exporter pods selected by `nvca.nvcf.nvidia.io/dcgm-metrics-present: "true"` on the `dcgm-metrics` named port

Keep the DCGM monitor disabled until the consuming stack owns the NVCA chart
contract that guarantees the injected DCGM metrics port keeps the
`dcgm-metrics` name.

Default monitors carry `nvcf.nvidia.com/observability-target: "true"` so the Target
Allocator can select only NVCF-owned scrape targets by default.

Add NVCA, State Metric Service, worker, or control-plane service monitors as
concrete templates only after their chart-owned scrape contracts are stable.

The control-plane collector cannot discover compute-plane-local scrape targets
in a different Kubernetes cluster. Decoupled compute clusters need their own
observability-stack instance, customer-owned observability, or no NVCF-managed
DCGM/NVCA/worker scrape coverage.

## Local Rendering

```sh
make template HELMFILE_ENV=local
```

`environments/local.yaml` is an opt-in example. Replace the chart repository,
image repository, and exporter endpoint with values for the target cluster
before enabling runtime components.
