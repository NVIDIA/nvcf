# Bring Your Own Observability

Bring Your Own Observability (BYOO) exports logs, metrics, and traces from a
tenant's functions and tasks to an observability backend that the tenant owns.
Each workload pod gets an OpenTelemetry (OTel) Collector sidecar. The sidecar
sends telemetry to the endpoints that the tenant registers with the NVCF API.

This guide is for two audiences:

- Operators enable BYOO on self-managed GPU clusters.
- Tenants register telemetry endpoints and attach them to functions and tasks.

## When to use BYOO

Use BYOO when a tenant needs workload telemetry in its own backend, such as
Grafana Cloud, Datadog, Splunk, Azure Monitor, a Prometheus remote-write
receiver, or any OpenTelemetry Protocol (OTLP) HTTP receiver.

BYOO does not replace the operator's observability stack. Control-plane and
cluster-level monitoring is installed by the observability stack. For details,
refer to [Observability](../observability/observability.md). BYOO exports only the
telemetry of the workloads that reference a telemetry endpoint.

## Enable BYOO on a cluster

### Feature gates

NVCA (NVIDIA Cloud Functions Agent) controls BYOO with two feature gates:

| Gate | Default | Effect |
|---|---|---|
| `BYOObservability` | Off | Adds the BYOO OTel Collector sidecar to workloads that reference telemetry endpoints. |
| `BYOOFluentBit` | Off | Adds a Fluent Bit sidecar that reads container logs and forwards them to the collector. |

The compute-plane stack turns on `BYOObservability` when
`observability.profile` is `compute` or `all`. The `compute` profile is the
default for the compute-plane stack. The `disabled` and `control` profiles do
not turn on the gate.

To turn on `BYOOFluentBit`, set `global.nvcaOperator.byooFluentBit.enabled` in
the compute-plane environment file:

```yaml
observability:
  profile: compute

global:
  nvcaOperator:
    byooFluentBit:
      enabled: true
```

`BYOOFluentBit` needs `BYOObservability`. The stack fails to render when
`byooFluentBit.enabled` is true and `BYOObservability` is off.

Add other gates, or turn off a derived gate with a leading `-`, through
`global.nvcaOperator.selfManaged.featureGateValues`. The stack merges these
values with its default gates. A value such as `-BYOObservability` turns off
the derived gate.

For a standalone `nvca-operator` Helm release, list the gates in
`selfManaged.featureGateValues`. The `--set` flag replaces the whole list, so
include every gate you need. For more information, refer to
[Managing Feature Flags](../compute-plane/cluster-management/configuration.md#managing-feature-flags).

### Sidecar sizing and collector options

The `nvca-operator` chart exposes BYOO sizing under the `byoo` key. Leave a key
unset to keep the NVCA built-in defaults. In the compute-plane stack, set the
same keys under `global.nvcaOperator.byoo`. The stack passes them to the chart
unchanged.

| Value | Purpose |
|---|---|
| `byoo.resources` | Requests and limits for the BYOO OTel Collector container. |
| `byoo.fluentbit.resources` | Requests and limits for the Fluent Bit container. |
| `byoo.logChunking` | Log chunking overrides: `maxPayloadBytes` and `dryRun`. |
| `byoo.otelCollector` | Structured collector rendering overrides, including [platform metrics federation](#platform-metrics-federation). |
| `byoo.additionalResourceOverhead` | Cluster capacity reserved for BYOO and related DaemonSet overhead. |

Example values for the `nvca-operator` release:

```yaml
byoo:
  resources:
    requests:
      cpu: 100m
      memory: 256Mi
    limits:
      cpu: 500m
      memory: 512Mi
  fluentbit:
    resources:
      requests:
        cpu: 50m
        memory: 64Mi
      limits:
        cpu: 200m
        memory: 128Mi
  logChunking:
    maxPayloadBytes: 262144
```

You can also tune the collector through `agentConfig.mergeConfig`. For
examples, refer to
[Agent Config Merging](../compute-plane/cluster-management/configuration.md#agent-config-merging).

### Platform metrics federation

The collector can read platform metrics for its workload from a
Prometheus-compatible `/federate` endpoint: worker, NVCA, cAdvisor,
kube-state-metrics, and DCGM series. On self-managed clusters, federation is
off unless you set `byoo.otelCollector.platformMetricsFederation`. The
collector then exports only the telemetry that the workload sends, plus
container logs when `BYOOFluentBit` is on.

To turn it on, set `target` to the `host:port` of a server that serves
`/federate` over HTTP. For the VictoriaMetrics backend of the observability
stack, use `vmsingle.<observability-namespace>.svc.cluster.local:8428`.

```yaml
byoo:
  otelCollector:
    platformMetricsFederation:
      target: vmsingle.monitoring.svc.cluster.local:8428
```

In the compute-plane stack, set the same block under
`global.nvcaOperator.byoo`. Set `enabled: false` to keep federation off
explicitly.

The federation jobs select series by their `job`, `namespace`, `pod`, and
`container` labels. Series that the target stores under other job names or
labels are not exported. With the `compute` or `all` profile, the default
monitors of the observability stack store these series under the job names
that the collector selects:

| Series | Job | Observability stack source |
| --- | --- | --- |
| Worker | `nvcf-worker` | `nvcf-default-monitors-worker` PodMonitor |
| NVCA | `nvca` | `nvcf-default-monitors-nvca` ServiceMonitor |
| DCGM | `nvidia-dcgm-exporter` | `nvcf-default-monitors-dcgm` PodMonitor, for pods that NVCA labels with `nvca.nvcf.nvidia.io/dcgm-metrics-present` |
| cAdvisor | `kubelet` | Not scraped |
| kube-state-metrics | `kube-state-metrics` | Not scraped |

Keep `defaultMonitors.computePlane.worker.jobName` and
`defaultMonitors.computePlane.dcgm.jobName` at these defaults when you use
federation. To export cAdvisor or kube-state-metrics series, or DCGM series
from another exporter, scrape them into the target under the job names in the
table and keep the workload `namespace`, `pod`, and `container` labels. The
DCGM job reads these labels with an `exported_` prefix, which a scrape adds
when the exporter labels conflict with the target labels.

The compute-plane stack pins NVCA and BYOO collector releases that support this
setting. Older releases ignore it and federate from the default target described
in [Federation scrape errors in the collector log](#federation-scrape-errors-in-the-collector-log).

### Sidecar images

The compute-plane stack sets both sidecar images and passes them to the
`nvca-operator` chart:

| Sidecar | Stack value | Default image |
|---|---|---|
| BYOO OTel Collector | `global.nvcaOperator.byooOtelCollector` | `${global.image.registry}/${global.image.repository}/byoo-otel-collector`, with the tag in `imageTag` |
| Fluent Bit | `global.nvcaOperator.byooFluentBit.image` | `docker.io/fluent/fluent-bit`, with the tag in `tag` |

The collector follows `global.image`, so mirror it with the other stack images.
Fluent Bit does not follow `global.image`. To pull it from a private registry or
mirror, copy the image with the tag that the stack pins. Then set its repository
in the compute-plane environment file. Set `byooOtelCollector.imageRepository`
only when the collector is published outside the `global.image` path:

```yaml
global:
  nvcaOperator:
    byooOtelCollector:
      imageRepository: <your-registry>/<your-repository>/byoo-otel-collector
    byooFluentBit:
      image:
        registry: <your-registry>
        repository: <your-repository>/fluent-bit
```

If you mirror other tags, also set `byooOtelCollector.imageTag` and
`byooFluentBit.image.tag`.

For a standalone `nvca-operator` release, set
`agent.byooOtelCollector.imageRepository` and `agent.byooOtelCollector.imageTag`.
For Fluent Bit, set `agent.byooFluentBit.imageRepository`, including the
registry, and `agent.byooFluentBit.imageTag`. Without
`agent.byooFluentBit.imageRepository`, NVCA uses the upstream Fluent Bit image.

The workload namespace must have pull access to each registry. For registry
credentials, refer to
[Working with Third-Party Registries](./third-party-registries.md).

### Network access

In self-hosted mode, NVCA removes NetworkPolicies from workload namespaces, so
NVCA does not restrict collector egress. Your cluster network must still allow
the workload pods to reach each telemetry endpoint. The collector listens
inside the pod on these ports:

| Port | Purpose |
|---|---|
| 14357 | OTLP gRPC receiver |
| 14358 | OTLP HTTP receiver, also used by Fluent Bit |
| 13133 | Health check |
| 18888 | Collector internal metrics |

## Supported providers

A telemetry endpoint has a provider, an endpoint, a set of signal types, and a
secret. The collector supports these providers:

| Provider | Logs | Metrics | Traces | Exporter |
|---|---|---|---|---|
| `OTEL_COLLECTOR` | Yes | Yes | Yes | OTLP HTTP |
| `GRAFANA_CLOUD` | Yes | Yes | Yes | OTLP HTTP with basic auth |
| `DATADOG` | Yes | Yes | Yes | Datadog |
| `AZURE_MONITOR` | Yes | Yes | Yes | Azure Monitor |
| `SPLUNK` | Yes | No | No | Splunk HTTP Event Collector (HEC) |
| `PROMETHEUS` | No | Yes | No | Prometheus remote write |
| `SERVICENOW` | No | No | Yes | OTLP gRPC |

The API accepts other provider values. The BYOO collector does not render an
exporter for them, so do not use them on self-managed clusters.

### Secret shapes

The `secret.value` field is a string or a JSON object. The collector writes a
string to one credential file. It writes each key of a JSON object to its own
file. The `secret.name` is the telemetry name.

| Provider | `endpoint` | `secret.value` |
|---|---|---|
| `OTEL_COLLECTOR` | OTLP HTTP base URL, for example `https://otel.example.com:4318` | String. Sent as `Authorization: Bearer <value>`. |
| `GRAFANA_CLOUD` | Grafana Cloud OTLP endpoint | Object with `instanceId` and `apiKey`. |
| `DATADOG` | Datadog site, for example `datadoghq.com` | String. The Datadog API key. |
| `AZURE_MONITOR` | Azure Monitor ingestion endpoint | Object with `instrumentationKey`, `applicationId`, and `liveEndpoint`. |
| `SPLUNK` | Splunk HEC URL | String. The HEC token. |
| `PROMETHEUS` | Remote-write URL | Object with `clientCert` and `clientKey`, `caFile`, or all three. All values are PEM text. |
| `SERVICENOW` | OTLP gRPC endpoint | String. Sent in the `lightstep-access-token` header. |

Use `OTEL_COLLECTOR` to send to any OTLP HTTP receiver, including your own
OpenTelemetry Collector. The collector appends the standard OTLP signal paths,
such as `/v1/logs`, to the base URL.

For `PROMETHEUS`, a client certificate needs a client key. An empty value
counts as not set.

## Register a telemetry endpoint

Register endpoints with `nvcf-cli telemetry`. For CLI setup, refer to
[Self-hosted CLI](../overview/cli.md). The commands need a credential with the
`manage_telemetries` scope, which the default function API key includes. The
account limit `maxTelemetries` caps the number of endpoints per account.

Write the secret value to a file, in the shape that
[Supported providers](#supported-providers) lists for the provider. A value
that starts with `{` is sent as a JSON object. Then create the endpoint:

```bash
nvcf-cli telemetry create \
  --name my-otel-endpoint \
  --endpoint https://otel.example.com:4318 \
  --provider OTEL_COLLECTOR \
  --type LOGS --type METRICS --type TRACES \
  --secret-file otel-token.txt
```

The command prints the telemetry ID. `--name` is also the secret name and
follows the `secret` rules in the field table below. `--protocol` defaults to
`HTTP`. `--secret-file -` reads the value from standard input. The CLI rejects
a provider, type, or secret shape that the collector cannot render.

Manage endpoints with these commands:

- `nvcf-cli telemetry list` lists endpoints, and
  `nvcf-cli telemetry get <telemetry-id>` shows one. Neither returns the secret.
- `nvcf-cli telemetry delete <telemetry-id>` deletes an endpoint and its
  secret. The API rejects the request while a function references the endpoint.
- `nvcf-cli telemetry update-secret <telemetry-id> --secret-file <file>`
  replaces the secret. It also needs the `update_secrets` scope, which the
  default function API key does not include.

To generate a function API key that can update secrets, list the default scopes
too. `--scopes` replaces them, and the new key replaces the saved function key:

```bash
nvcf-cli api-key generate --for function \
  --scopes invoke_function,list_functions,queue_details,list_functions_details,manage_telemetries,update_secrets
```

### Use the API

To call the API directly, create the endpoint with `POST /v2/nvcf/telemetries`.
The token needs the `manage_telemetries` scope.

Set `NVCF_API_URL` to the NVCF API URL of your Gateway. With DNS and HTTPS
configured as described in
[Production: DNS and HTTPS](./gateway-routing.md#production-dns-and-https),
use the API hostname:

```bash
export NVCF_API_URL="https://api.<your-domain>"

curl -s -X POST "${NVCF_API_URL}/v2/nvcf/telemetries" \
  -H "Authorization: Bearer ${NVCF_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "endpoint": "https://otel.example.com:4318",
    "protocol": "HTTP",
    "provider": "OTEL_COLLECTOR",
    "types": ["LOGS", "METRICS", "TRACES"],
    "secret": {
      "name": "my-otel-endpoint",
      "value": "<bearer-token>"
    }
  }' | jq .
```

The Gateway from [Gateway quickstart](./gateway-routing.md#gateway-quickstart)
has no TLS listener. It serves the API on plain HTTP port 80 and routes by the
`Host` header. On a test cluster with that Gateway, set
`NVCF_API_URL="http://${GATEWAY_ADDR}"` and add
`-H "Host: api.${GATEWAY_ADDR}"` to the command. Plain HTTP sends the API token
and `secret.value` unencrypted. Use it only on a trusted network.

| Field | Values |
|---|---|
| `endpoint` | Provider endpoint, as listed in [Secret shapes](#secret-shapes). |
| `protocol` | `HTTP` or `GRPC`. |
| `provider` | A provider from [Supported providers](#supported-providers). |
| `types` | One or more of `LOGS`, `METRICS`, and `TRACES`. |
| `secret` | `name` and `value`. The name is 1 to 48 characters. It starts with a letter or digit, followed by letters, digits, `_`, `.`, or `-`. It must be unique in the account. |

The response includes a `telemetryId`. Use it to attach the endpoint to
workloads.

Other operations:

- `GET /v2/nvcf/telemetries` lists endpoints.
- `GET /v2/nvcf/telemetries/{telemetryId}` returns one endpoint.
- `DELETE /v2/nvcf/telemetries/{telemetryId}` deletes an endpoint.
- `PUT /v2/nvcf/secrets/telemetries/{telemetryId}` updates the secret. It needs
  the `update_secrets` scope instead of `manage_telemetries`.

## Attach endpoints to functions and tasks

Each workload can reference one endpoint per signal. The endpoint must include
that signal in its `types`.

For functions, pass the IDs to `nvcf-cli function create`:

```bash
nvcf-cli function create --input-file function.json \
  --logs-telemetry-id "${TELEMETRY_ID}" \
  --metrics-telemetry-id "${TELEMETRY_ID}" \
  --traces-telemetry-id "${TELEMETRY_ID}"
```

For tasks, `nvcf-cli task create` accepts the same flags. In a task JSON file
or API request, set the `telemetries` object:

```json
{
  "telemetries": {
    "logsTelemetryId": "<telemetry-id>",
    "metricsTelemetryId": "<telemetry-id>",
    "tracesTelemetryId": "<telemetry-id>"
  }
}
```

The function creation API uses the same `telemetries` object.

`nvcf-cli function get` and `nvcf-cli task get` show the attached IDs under
`Telemetries`.

## What is exported

The collector sends each signal only to the endpoint attached for that signal.

- Logs, metrics, and traces that the workload sends with OTLP to the collector
  on ports 14357 or 14358.
- Container logs, when `BYOOFluentBit` is on. For container workloads, Fluent
  Bit reads the logs of the inference or task container. For Helm workloads, it
  reads the logs of the application pods in the workload namespace that run on
  the same node as the utils pod.
- The collector's own `otelcol_*` metrics, with the workload metrics.
- Worker, NVCA, DCGM, cAdvisor, and kube-state-metrics series for the workload,
  only when platform metrics federation is on. Federation is off by default on
  self-managed clusters. For more information, refer to
  [Platform metrics federation](#platform-metrics-federation).

## Security

- The NVCF API stores the secret in the secrets service. The API does not
  return the secret value.
- At workload start, the secret is delivered to the collector sidecar and
  written to files under `/etc/byoo-otel-collector/secrets`. The collector
  configuration references these files instead of embedding the values.
- Use HTTPS or TLS endpoints so credentials and telemetry are encrypted in
  transit.

## Telemetry compatibility

BYOO preserves valid upstream telemetry from the OTel Collector and cAdvisor.
Upgrades of those components can change metric names, types, labels, and
histogram buckets. For the policy and examples, refer to
[BYOO Telemetry Compatibility](../compute-plane/cluster-management/byoo-telemetry-compatibility.md).

## Troubleshooting

### Sidecar image pull fails

A workload pod shows `ImagePullBackOff` or `ErrImagePull` for the
`byoo-otel-collector` or `fluentbit-logs` container. The cluster cannot pull the
sidecar image from its default registry. Mirror the image and set its
repository, as described in [Sidecar images](#sidecar-images).

### Federation scrape errors in the collector log

The collector log shows scrape errors for its federation jobs, such as
`nvcf-worker` and `nvca`. Platform metrics are not exported, except `up` series
with the value 0 for the failing jobs. OTLP telemetry from the workload and
container logs are not affected.

These errors mean that the collector cannot reach its federation target. Read
the collector log:

```bash
kubectl logs -n <workload-namespace> <pod-name> -c byoo-otel-collector
```

NVCA checks a configured `target` at startup and every 5 minutes while
`BYOObservability` is on. When the check fails, NVCA logs a warning, reports a
`byoo-platform-metrics-federation` component with the error in its `/healthz`
response, and sets
`nvca_byoo_platform_metrics_federation_state{state="unreachable"}` to 1.
Workloads still deploy. Check the target host and port, or set
`enabled: false`. Read the NVCA log for the check result:

```bash
kubectl logs -n nvca-system deploy/nvca -c agent | grep byoo-platform-metrics-federation
```

NVCA does not check the default target. The collector federates from it when
federation is on without a `target`, for example with only `enabled: true`.
NVCA and collector releases without the federation setting also use it. The
default target is the `nvcf-byoo-prometheus-kube-prometheus` service in the
`monitoring` namespace, which the self-managed stacks do not install. Set
`target`, or set `enabled: false`.

### Container logs are missing

Traces and OTLP metrics arrive, but container logs do not. Check that
`BYOOFluentBit` is on, as described in [Feature gates](#feature-gates), and
that the logs endpoint includes `LOGS` in `types`. Without the gate, only logs
that the workload sends with OTLP are exported.

### Function or task fails with a BYOO error

A function deployment or task that references telemetry endpoints fails when
the target cluster does not have `BYOObservability` on. NVCA rejects the
workload and reports it as failed. The function deployment moves to `ERROR`,
and the task moves to `ERRORED`. The `error` field of the deployment or task
`healthInfo` holds the reason:

```text
Bring Your Own Observability (BYOO) is disabled on this cluster: the task requests telemetries, but these NVCA feature flags are not enabled: BYOObservability. Enable them on the cluster, or deploy without telemetries.
```

For a function, the message lists each required flag that is off:
`BYOObservability`, and `UseFunctionTranslator`, which is on by default.

NVCA does not retry a rejected workload. Turn on the gate with the `compute` or
`all` profile, or add `BYOObservability` to `featureGateValues`. Then deploy
the function or create the task again. You can also remove the telemetry
references from the workload.

To check the gates on a cluster, refer to
[Verifying Feature Flags](../compute-plane/cluster-management/configuration.md#verifying-feature-flags).
