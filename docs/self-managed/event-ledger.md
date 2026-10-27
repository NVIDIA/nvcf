# Enable and Operate Event Ledger

Event Ledger is an optional self-managed control-plane service that records
function deployment events from SIS and registered compute planes. The control
plane service and the NVCA collector are enabled separately.

See [Event Ledger](../overview/event-ledger.md) for the event model, query
workflow, API, and storage semantics.

## Deployment flow

Enabling Event Ledger has three parts:

1. Deploy Event Ledger and its gateway route on the control plane. This also
   enables SIS v3 event publishing.
2. Enable the NVCA OTel collector on each compute plane that should publish
   Kubernetes and ICMSRequest events.
3. Generate a dedicated Event Ledger API key for each operator or automation
   identity that reads events.

A registered compute plane does not publish NVCA events until its collector is
explicitly enabled.

## Prerequisites

- A deployed self-managed NVCF control plane with Cassandra, OpenBao, API Keys,
  SIS, and Gateway API ingress available.
- The Event Ledger chart and image from the selected stack release mirrored to
  the configured chart and image repositories.
- The optional `nvcf-otel-collector` image mirrored before the compute-plane
  collector is enabled.
- A registered compute plane running a compatible NVCA Operator release.
- Network reachability from each compute plane to Event Ledger.

The default self-hosted artifact manifest does not include the optional NVCA
Event Ledger collector image. Obtain the collector image selected by the NVCA
Operator release and mirror it with the other compute-plane images. The
compute-plane stack reads the tag from
`global.nvcaOperator.selfManaged.otelCollector.imageTag` and defaults the
repository to:

```text
<global.image.registry>/<global.image.repository>/nvcf-otel-collector
```

See [Image Mirroring](../overview/image-mirroring.md) for registry workflows.

## Enable the control-plane service

The current base environment enables Event Ledger. Set the value explicitly in
`deploy/stacks/self-managed/environments/<environment-name>.yaml` so the
environment records the intended behavior:

```yaml
addons:
  eventLedger:
    enabled: true
```

This value controls:

- The `event-ledger` Helm release in the `nvcf` namespace.
- The `events.<domain>` HTTPRoute.
- SIS v3 Event Ledger publishing.
- The internal service address used by control-plane producers.

Apply the control-plane environment using the same Helmfile installation path
used for the rest of the stack. A full apply updates Event Ledger, SIS, and the
gateway route together:

```bash
cd deploy/stacks/self-managed
HELMFILE_ENV=<environment-name> helmfile apply
```

### Verify the control plane

Wait for the deployment and inspect the Service:

```bash
kubectl -n nvcf rollout status deployment/event-ledger --timeout=5m
kubectl -n nvcf get deployment/event-ledger service/event-ledger
```

Forward the in-cluster health endpoint in one terminal:

```bash
kubectl -n nvcf port-forward service/event-ledger 18080:8080
```

Then check the endpoint from another terminal:

```bash
curl -fsS http://127.0.0.1:18080/health
```

Verify the gateway route and external health endpoint:

```bash
kubectl get httproute -A --field-selector=metadata.name=event-ledger \
  -o jsonpath='{range .items[*]}{.metadata.namespace}{"\t"}{.spec.hostnames[0]}{"\t"}{.status.parents[*].conditions[?(@.type=="Accepted")].status}{"\n"}{end}'

curl -fsS -H "Host: events.<domain>" "http://<gateway-address>/health"
```

The route hostname is `events.<domain>`, and its `Accepted` condition should be
`True`.

Confirm that SIS event publishing is enabled:

```bash
kubectl -n sis get configmaps -o yaml | \
  grep -E 'ICMS_FNDS_MESSAGES_(ENABLED|V3_ENABLED): "true"'
```

## Enable the compute-plane collector

Configure each compute-plane environment separately in
`deploy/stacks/nvcf-compute-plane/environments/<environment-name>.yaml`:

```yaml
global:
  nvcaOperator:
    selfManaged:
      otelCollector:
        enabled: true
```

The default collector repository follows `global.image`. Set an explicit
repository only when the collector is mirrored somewhere else:

```yaml
global:
  nvcaOperator:
    selfManaged:
      otelCollector:
        enabled: true
        imageRepository: <registry>/<repository>/nvcf-otel-collector
```

Keep `imageTag` at the value selected by the compute-plane stack or its NVCA
Operator chart. Do not choose the tag independently of the operator release.

### Colocated control plane and compute plane

When the control plane and compute plane share a Kubernetes cluster, leave
`eventLedgerServiceURL` empty. NVCA uses this in-cluster endpoint:

```text
http://event-ledger.nvcf.svc.cluster.local:8080
```

### Split control plane and compute plane

When the compute plane runs in another cluster, set a URL it can resolve and
reach. Prefer the Event Ledger gateway hostname so the correct route is selected
without a manual Host header:

```yaml
global:
  nvcaOperator:
    selfManaged:
      eventLedgerServiceURL: https://events.<domain>
      otelCollector:
        enabled: true
```

The URL is a base URL. NVCA appends `/v3/ledger/k8s-events` for the OTLP export.
Configure DNS, TLS trust, firewalls, and proxies so Pods in the compute plane can
reach it.

Apply the compute-plane environment:

```bash
cd deploy/stacks/nvcf-compute-plane
HELMFILE_ENV=<environment-name> helmfile --selector name=nvca-operator apply
```

### Verify the collector configuration

Verify that the registered backend enables the collector:

```bash
kubectl -n nvca-operator get nvcfbackend -o yaml
```

The selected `NVCFBackend` must contain:

```yaml
spec:
  otelCollector:
    enabled: true
```

Wait for NVCA to roll out:

```bash
kubectl -n nvca-system rollout status deployment/nvca --timeout=10m
```

The collector runs as a restartable init container named
`nvca-otel-collector`. Inspect its image and status:

```bash
kubectl -n nvca-system get deployment/nvca \
  -o jsonpath='{range .spec.template.spec.initContainers[?(@.name=="nvca-otel-collector")]}{.image}{"\n"}{end}'

kubectl -n nvca-system get pods \
  -l app.kubernetes.io/name=nvca \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{range .status.initContainerStatuses[?(@.name=="nvca-otel-collector")]}{.ready}{"\t"}{.state}{end}{"\n"}{end}'
```

Verify the endpoint and projected-token authentication settings:

```bash
kubectl -n nvca-system get deployment/nvca \
  -o jsonpath='{range .spec.template.spec.initContainers[?(@.name=="nvca-otel-collector")].env[*]}{.name}={.value}{"\n"}{end}'
```

For a colocated self-managed deployment, the output includes:

```text
NVCA_OTEL_COLLECTOR_FNDS_ENDPOINT=http://event-ledger.nvcf.svc.cluster.local:8080/v3/ledger/k8s-events
NVCA_OTEL_COLLECTOR_BEARER_TOKEN_FILE=/var/run/secrets/tokens/token
NVCA_OTEL_COLLECTOR_AUTHENTICATOR=bearertokenauth
```

The projected service account token (PSAT) is verified through SIS. Event
Ledger binds the verified cluster identity to every NVCA event. It fills a
missing `cluster_id` from that identity and rejects a conflicting cluster ID.

## Verify event reads

Configure the CLI with the external Event Ledger route. When the CLI connects
to a gateway address, set both values:

```yaml
event_ledger_url: http://<gateway-address>
event_ledger_host: events.<domain>
```

When `base_http_url` begins with `api.`, the CLI can derive the sibling
`events.` URL. An explicit `event_ledger_url` is clearer for self-managed
environments and is required when that derivation is not possible.

Generate a dedicated read key:

```bash
nvcf-cli api-key generate --for event-ledger
```

Deploy a function, wait for the deployment to begin, and query its version ID:

```bash
nvcf-cli stats get --function-version-id <function-version-id>
```

SIS events should appear when the control-plane service is enabled. NVCA Pod
and ICMSRequest events require the collector on the compute plane. See
[Event Ledger](../overview/event-ledger.md#read-events-for-a-context) for the
complete query workflow.

## Troubleshooting

### Event Ledger release is absent

Check the resolved environment value and Helm releases:

```bash
cd deploy/stacks/self-managed
HELMFILE_ENV=<environment-name> helmfile build | grep -A3 -B3 event-ledger
helm list -A | grep event-ledger
```

Confirm that `addons.eventLedger.enabled` is `true` in the final environment.

### Event Ledger Pod does not start

Inspect the deployment, Pod, and recent namespace events:

```bash
kubectl -n nvcf describe deployment/event-ledger
kubectl -n nvcf get pods -l app.kubernetes.io/name=event-ledger
kubectl -n nvcf logs deployment/event-ledger --all-containers
kubectl -n nvcf get events --sort-by=.lastTimestamp | tail -50
```

Common causes include an unavailable image, missing pull credentials, failed
OpenBao secret injection, or unavailable Cassandra schema and credentials.

### Gateway returns 404

Check that the request uses `events.<domain>` as its DNS name or Host header.
Then inspect the route:

```bash
kubectl get httproute -A --field-selector=metadata.name=event-ledger -o yaml
```

The route must be accepted and point to `service/event-ledger` in namespace
`nvcf` on port `8080`.

### CLI reports that no Event Ledger key is configured

Generate the dedicated key:

```bash
nvcf-cli api-key generate --for event-ledger
```

Function and task API keys use different audiences and are not used as a
fallback.

### CLI receives 401 or 403

Generate a fresh Event Ledger key and confirm that it has the
`fnds:getEvents` scope. A key minted for the NVCF function or NVCT task service
has the wrong audience.

### Stats are empty

Check these conditions in order:

1. The supplied function version ID is correct.
2. SIS v3 event publishing is enabled.
3. The NVCA collector is enabled for the compute plane.
4. The collector image is present in the configured registry.
5. The collector endpoint is reachable.
6. The collector logs do not contain authentication or export errors.

Inspect collector logs with:

```bash
kubectl -n nvca-system logs deployment/nvca \
  -c nvca-otel-collector --tail=200
```

### Events are empty but stats show a context

The context flags must reproduce the canonical context exactly. Copy every
field from `contexts[].context`. Do not add fields that are absent. Running
`stages events list` with no context flags reads only the empty context.

### Attribute filtering returns no events

First query the exact context with `--json` and inspect
`events[].details.attributes`. Attribute filtering is exact, applies only to
that context, and requires both `--attribute-key` and `--attribute-value`.

## Disable Event Ledger

Disable collectors before removing the control-plane service so NVCA does not
continue exporting to an unavailable endpoint.

For every compute plane, set:

```yaml
global:
  nvcaOperator:
    selfManaged:
      otelCollector:
        enabled: false
```

Apply each compute-plane environment. Then set the control-plane value:

```yaml
addons:
  eventLedger:
    enabled: false
```

Apply the control-plane environment. This removes the Event Ledger release and
route and disables SIS publishing. It does not remove existing records from
Cassandra. Follow your Cassandra backup and retention policy before removing
data.
