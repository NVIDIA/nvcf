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

Add other gates, or turn off a derived gate with a leading `-`, through
`global.nvcaOperator.selfManaged.featureGateValues` in the compute-plane
environment file:

```yaml
observability:
  profile: compute

global:
  nvcaOperator:
    selfManaged:
      featureGateValues:
        - BYOOFluentBit
```

The stack merges these values with its default gates. A value such as
`-BYOObservability` turns off the derived gate.

For a standalone `nvca-operator` Helm release, list the gates in
`selfManaged.featureGateValues`. The `--set` flag replaces the whole list, so
include every gate you need. For more information, refer to
[Managing Feature Flags](../compute-plane/cluster-management/configuration.md#managing-feature-flags).

### Sidecar sizing and collector options

The `nvca-operator` chart exposes BYOO sizing under the `byoo` key. Leave a key
unset to keep the NVCA built-in defaults.

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

The federation jobs select series by the `job`, `namespace`, `pod`, and
`container` labels of the NVIDIA-managed Prometheus, for example
`job="kubelet"` for cAdvisor and `job="nvca"` for NVCA. Series that the target
stores under other job names or labels are not exported.

This setting needs an NVCA operator and a BYOO collector release that include
it. Older releases ignore it and federate from the default target described in
[Federation scrape errors in the collector log](#federation-scrape-errors-in-the-collector-log).

### Collector image access

For self-managed clusters, the `nvca-operator` chart resolves the BYOO
collector image to `nvcr.io/nvidia/nvcf-byoc/byoo-otel-collector`. The chart
sets the tag in `agent.byooOtelCollector.imageTag`. Pulling this image needs
NVIDIA GPU Cloud (NGC) access to that repository.

If your cluster cannot pull from that repository, mirror the image to a
registry that the cluster can reach. Then set
`agent.byooOtelCollector.imageRepository` on the `nvca-operator` release:

```yaml
agent:
  byooOtelCollector:
    imageRepository: <your-registry>/<your-repository>/byoo-otel-collector
```

Keep the mirrored tag equal to `agent.byooOtelCollector.imageTag`, or set that
value to the tag you mirrored. The workload namespace must have pull access to
the registry. For registry credentials, refer to
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

Create the endpoint with `POST /v2/nvcf/telemetries`. The token needs the
`manage_telemetries` scope. The account limit `maxTelemetries` caps the number
of endpoints per account.

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

Other operations on the same path:

- `GET /v2/nvcf/telemetries` lists endpoints.
- `GET /v2/nvcf/telemetries/{telemetryId}` returns one endpoint.
- `DELETE /v2/nvcf/telemetries/{telemetryId}` deletes an endpoint.
- `PUT /v2/nvcf/secrets/telemetries/{telemetryId}` updates the secret.

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

## What is exported

- Logs, metrics, and traces that the workload sends with OTLP to the collector
  on ports 14357 or 14358.
- Container logs, when `BYOOFluentBit` is on. For container workloads, Fluent
  Bit reads the logs of the inference or task container. For Helm workloads, it
  reads the logs of the application pods in the workload namespace that run on
  the same node as the utils pod.
- Platform metrics, such as cAdvisor, kube-state-metrics, NVCA, and worker
  metrics, when platform metrics federation is enabled. For more
  information, refer to [Platform metrics federation](#platform-metrics-federation).

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

### Collector image pull fails

A workload pod shows `ImagePullBackOff` or `ErrImagePull` for the
`byoo-otel-collector` container. The cluster cannot pull
`nvcr.io/nvidia/nvcf-byoc/byoo-otel-collector`. Mirror the image and set
`agent.byooOtelCollector.imageRepository`, as described in
[Collector image access](#collector-image-access).

### Federation scrape errors in the collector log

Releases without the platform metrics federation setting federate from the
`nvcf-byoo-prometheus-kube-prometheus` service in the `monitoring` namespace.
The self-managed stacks do not install that service. The collector log then
shows scrape errors for the `nvcf-worker`, `nvca`, `kubernetes-cadvisor`, and
`kube-state-metrics` jobs. Platform metrics are not exported, except `up`
series with the value 0 for the failing jobs. OTLP telemetry from the workload
and container logs are not affected.

With a configured `target`, the same errors mean that the collector cannot
reach the target. Check the target host and port, or set `enabled: false`.

Read the collector log:

```bash
kubectl logs -n <workload-namespace> <pod-name> -c byoo-otel-collector
```

### Container logs are missing

Traces and OTLP metrics arrive, but container logs do not. Check that
`BYOOFluentBit` is in the NVCA feature gates, and that the logs endpoint
includes `LOGS` in `types`. Without the gate, only logs that the workload sends
with OTLP are exported.

### Function or task stays queued

A function or task that references telemetry endpoints stays queued when the
target cluster does not have `BYOObservability` on. NVCA rejects the workload
with this error:

```text
telemetries is set but required features are disabled: BYOObservability
```

For functions, the message also names `UseFunctionTranslator`, which is on by
default. Turn on the gate with the `compute` or `all` profile, or add
`BYOObservability` to `featureGateValues`. You can also remove the telemetry
references from the workload.

To check the gates on a cluster, refer to
[Verifying Feature Flags](../compute-plane/cluster-management/configuration.md#verifying-feature-flags).
