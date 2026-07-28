# nvcf-observability-stack

Reusable Helmfile stack for self-hosted NVCF observability. A cluster installs
this stack at most once and selects the targets with one value:

```yaml
observability:
  profile: control
```

The stack can own:

- Prometheus Operator CRDs for `ServiceMonitor` and `PodMonitor`
- OpenTelemetry Operator
- One OpenTelemetry Collector with Target Allocator support
- Read-only discovery RBAC
- VictoriaMetrics
- NVCF-owned default monitor resources

## Profiles

| Profile | Control targets | Compute targets | Result |
| --- | --- | --- | --- |
| `disabled` | No | No | Render no observability releases or resources. |
| `control` | Yes | No | Install one stack for control-plane metrics and the autoscaler backend. |
| `compute` | No | Yes | Install one stack for NVCA, DCGM, and worker metrics; resolve NVCA BYOO support on. |
| `all` | Yes | Yes | Install the union once; do not duplicate shared components. |

The packaged reusable stack defaults to `disabled`. Its self-managed
control-plane example overlay defaults to `control`. A standalone compute-plane
consumer should select `compute`. A colocated deployment should install this
artifact once with `all`; it must not configure a second observability-stack
release from the compute-plane consumer.

The public configuration does not expose
`planes.control.enabled` or `planes.compute.enabled`. Those values are derived
internally from the profile.

## Profile Defaults

Every enabled profile installs these shared components by default:

- Prometheus Operator CRDs
- OpenTelemetry Operator
- One OpenTelemetry Collector
- Target Allocator
- Discovery RBAC
- One VictoriaMetrics instance

The profile-specific defaults are:

| Default | `disabled` | `control` | `compute` | `all` |
| --- | --- | --- | --- | --- |
| Control-plane monitors | Off | On | Off | On |
| NVCA `ServiceMonitor` | Off | Off | On | On |
| DCGM `PodMonitor` | Off | Off | On | On |
| Worker `PodMonitor` | Off | Off | On | On |
| BYOO support | Off | Off | On | On |
| Autoscaler integration | Off | On | Off | On |
| NVCA collector | Off | Off | Off | Off |

Selecting a plane defaults its complete monitor set on. No separate
plane-monitor value is required for the normal path, and `all` defaults both
sets on. Explicit `defaultMonitors.controlPlane.enabled` and
`defaultMonitors.computePlane.enabled` values override the profile defaults.

Control-plane monitoring covers State Metric Service, Invocation Service, gRPC
Proxy, and LLM API Gateway. Compute monitoring selects NVCA in `nvca-system`,
DCGM pods that carry NVCA's DCGM metrics label, and pods exposing a `metrics`
port in NVCA-managed workload namespaces.

For `compute` and `all`, the profile defaults BYOO support on.
The NVCA installer should map that value to its existing `BYOObservability`
feature gate. The feature gate enables the per-function BYOO collector path; it
does not create a second shared collector. This stack does not deploy NVCA.

## Fine-Grained Overrides

Profiles provide normal defaults. Advanced installations can override shared
component ownership with `install`, `existing`, or `disabled`.

Monitor groups can also be overridden without changing the selected profile:

```yaml
observability:
  profile: control

defaultMonitors:
  controlPlane:
    enabled: false
  computePlane:
    enabled: true
    worker:
      enabled: false
```

These values control only monitor rendering. They do not change the selected
profile, BYOO support, or autoscaler integration. The `disabled` profile
still omits the entire observability release. Compute monitor overrides live
under `defaultMonitors.computePlane.services` for ServiceMonitors,
`defaultMonitors.computePlane.dcgm`, and
`defaultMonitors.computePlane.worker`.

The fully expanded ownership configuration below is equivalent to setting only
`observability.profile: control`:

```yaml
observability:
  profile: control
  components:
    prometheusOperatorCrds:
      mode: install
    otelOperator:
      mode: install
    collector:
      mode: install
    targetAllocator:
      mode: install
    discoveryRbac:
      mode: install

metricsBackend:
  mode: install
  type: victoriaMetrics
```

For example, a deployment that supplies its own operator and metrics backend
can override only those ownership decisions:

```yaml
observability:
  profile: control
  components:
    otelOperator:
      mode: existing

metricsBackend:
  mode: existing
  type: external
  remoteWriteEndpoint: https://metrics.example.com/write
  promqlEndpoint: https://metrics.example.com
```

Supported component paths are:

- `observability.components.prometheusOperatorCrds.mode`
- `observability.components.otelOperator.mode`
- `observability.components.collector.mode`
- `observability.components.targetAllocator.mode`
- `observability.components.discoveryRbac.mode`
- `metricsBackend.mode`

`install` makes this stack the owner. `existing` skips installation and requires
a separate installer preflight to verify a compatible external component.
`disabled` does not install or use the component. Overrides that contradict
required dependencies fail during Helmfile rendering. Component overrides
cannot install resources under `profile: disabled`.

The bundled backend derives both endpoints from the VictoriaMetrics namespace:

```text
remote write: http://vmsingle.monitoring.svc.cluster.local:8428/api/v1/write
PromQL:       http://vmsingle.monitoring.svc.cluster.local:8428
```

An external backend must provide both endpoints for `control` and `all`.

## Autoscaler Integration

The autoscaler is a control-plane consumer, not a second observability stack.
It reads from the same `metricsBackend.promqlEndpoint` selected by the profile.
For `control` and `all`, the self-managed stack renders the resolved backend
settings directly into the function autoscaler chart's existing
`function-autoscaler-env` ConfigMap:

```yaml
data:
  TIMESERIES_DB__TIMESERIES_DB_URL: http://vmsingle.monitoring.svc.cluster.local:8428
  TIMESERIES_DB__AUTH_MODE: none
  TIMESERIES_DB__IGNORE_ENV: "true"
```

For an external backend, `metricsBackend.authentication.mode` supports `none`,
`token`, and `mtls`. Token mode also requires `authnEndpoint`; mTLS mode
requires paths for the mounted client certificate and private key.

The autoscaler chart owns this environment ConfigMap and its rollout checksum.
Its Vault Agent template remains a separate ConfigMap because it is mounted as
a file and has a separate lifecycle. There is no generic observability profile
or contract ConfigMap.

The self-managed Helmfile deploys the function autoscaler for `control` and
`all`. It also supplies the Cassandra and NVCF API endpoints needed by the
self-managed runtime. The autoscaler does not need another observability enable
flag or backend mode. The `compute` and `disabled` profiles do not deploy it.

## Monitor Contracts

Application charts own stable metrics endpoints, labels, named ports, paths,
and namespaces. This stack owns the default monitor resources that select those
contracts.

Default monitors carry
`nvcf.nvidia.com/observability-target: "true"` so the Target Allocator selects
only NVCF-owned scrape targets.

The default monitor chart uses one generic ServiceMonitor template and one
generic PodMonitor template. Individual targets are data in chart values rather
than target-specific templates.

The default namespace is `monitoring`. If a deployment changes it, it must also
provide NetworkPolicy reachability for the collector.

## Local Rendering

The checked-in local environment is disabled by default:

```sh
make template HELMFILE_ENV=local
```

Change only `observability.profile` in the environment file to render a
complete enabled profile. Replace the example chart repository and image
repository before installing in a cluster.

Run the Helmfile profile assertions and autoscaler chart render checks with:

```sh
make test
```
