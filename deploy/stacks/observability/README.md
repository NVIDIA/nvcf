# NVCF observability stack

Install this stack at most once per cluster. One profile selects what it
observes:

```yaml
observability:
  profile: control
```

## Profiles

| Profile | Defaults |
| --- | --- |
| `disabled` | Render nothing. |
| `control` | Install shared infrastructure, control-plane monitors, and the function autoscaler backend. |
| `compute` | Install shared infrastructure and NVCA, DCGM, and worker monitors. |
| `all` | Install the union of `control` and `compute`, with shared components only once. |

Enabled profiles install Prometheus Operator CRDs, the OpenTelemetry Operator,
one collector with Target Allocator and discovery RBAC, VictoriaMetrics, and
the selected monitors. The reusable stack defaults to `disabled`; the
self-managed stack defaults to `control`.

Profiles derive the plane behavior internally. There are no
`planes.control.enabled` or `planes.compute.enabled` values.

The compute-plane consumer maps `compute` and `all` to
`selfManaged.otelCollector.enabled: true` and the `BYOObservability` NVCA
feature gate. Explicit collector and feature-gate values still win.

## Overrides

Profiles are defaults, not restrictions. Monitor groups and individual targets
remain configurable:

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

The component modes are `install`, `existing`, and `disabled`:

| Mode | Meaning |
| --- | --- |
| `install` | This stack installs and owns the component. |
| `existing` | Skip installation; the deployment workflow must verify a compatible component. |
| `disabled` | Do not install or use the component. |

This expanded configuration is equivalent to `profile: control`:

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

For customer-managed infrastructure, override only its owners:

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

Invalid dependency combinations fail during Helmfile rendering. A disabled
profile cannot install individual components.

## Metrics backend and autoscaler

With the default `monitoring` namespace, the bundled VictoriaMetrics endpoints
are:

```text
remote write: http://vmsingle.monitoring.svc.cluster.local:8428/api/v1/write
PromQL:       http://vmsingle.monitoring.svc.cluster.local:8428
```

If the VictoriaMetrics namespace is overridden, replace `monitoring` in these
hostnames with the configured namespace.

An external backend always requires `remoteWriteEndpoint`; `control` and `all`
also require `promqlEndpoint`. Authentication modes are `none`, `token`, and
`mtls`. Token mode requires `authnEndpoint`; mTLS requires
`clientCertificatePath` and `clientPrivateKeyPath`.

The function autoscaler is a self-managed control-plane component, not part of
this shared stack. For `control` and `all`, the self-managed Helmfile passes the
resolved PromQL and authentication values into the autoscaler chart's
`function-autoscaler-env` ConfigMap. `compute` and `disabled` do not deploy the
autoscaler.

## Monitor ownership

Application charts own metrics ports, paths, labels, and namespaces. This stack
owns the `ServiceMonitor` and `PodMonitor` resources. One generic template per
kind renders all targets from values.

All default monitors carry
`nvcf.nvidia.com/observability-target: "true"` for Target Allocator discovery.
Control-plane defaults scrape state-metrics, invocation-service, grpc-proxy,
llm-api-gateway, and function-autoscaler in `nvcf`. Compute defaults select
NVCA in `nvca-system`, DCGM pods by their NVCA metrics label, and NVCA-managed
workload pods by `icms-request-id`.

The default observability namespace is `monitoring`. A different namespace
also requires NetworkPolicy reachability to the collector.

## Validate

```sh
make template HELMFILE_ENV=local
make test
git diff --check
```
