# Event Ledger Enablement

The Event Ledger is an optional addon of the self-managed control plane. It stores
the events that SIS and the NVCA agents report for function deployments. For
what the events contain and how to read them, see
[Event Ledger](../overview/event-ledger.md).

## Prerequisites

The Event Ledger runs in the `nvcf` namespace and depends on three services that
the control-plane stack already installs:

- Cassandra with the `event_ledger` keyspace, created by the Cassandra migrations
  image.
- OpenBao with the Event Ledger setup migration applied. It creates the JWT
  auth role and the secret paths that the service reads.
- The API Keys service, which evaluates read keys.

Use the OpenBao and Cassandra versions pinned by the stack release. If you mirror
images, include the `event-ledger` image. See [Image Mirroring](../overview/image-mirroring.md).

## Enable the addon

The base environment of the Helmfile stack enables the addon. To turn it off, set
the value to `false` in your environment file, as the
[Quickstart](../overview/quickstart.md#step-4-run-the-install) does.

```yaml
addons:
  eventLedger:
    enabled: true
```

When the addon is enabled the stack:

- Installs the `event-ledger` release in the `nvcf` namespace. The Service is
  `event-ledger.nvcf.svc.cluster.local` on port `8080`, and metrics are on
  port `8081`.
- Creates a gateway route for `events.<domain>` that sends traffic to the Service.
  See [Gateway Routing](./gateway-routing.md#route-configuration).
- Turns on event publishing in SIS and gives the gRPC proxy the Event Ledger address.

The stack sets the image registry and repository from `global.image`. To change
the chart settings, use the `eventLedger` block of your environment values. The
common overrides are:

| Value | Default | Purpose |
| --- | --- | --- |
| `eventLedger.replicaCount` | `1` | Number of replicas |
| `eventLedger.resources` | Conservative | Requests and limits |
| `eventLedger.config.database.cassandra.hosts` | `cassandra.cassandra-system.svc.cluster.local` | Cassandra address |
| `eventLedger.config.auth.policy.policy-evaluator-addr` | API Keys Service address | Where read keys are evaluated |

### Verify the install

Check that the release is deployed and that SIS has event publishing on.

```bash
helm list --namespace nvcf | grep event-ledger
kubectl --namespace sis get configmaps -o yaml | grep ICMS_FNDS_MESSAGES
```

Both `ICMS_FNDS_MESSAGES_ENABLED` and `ICMS_FNDS_MESSAGES_V3_ENABLED` must be
`"true"`. Then check the endpoint through the gateway.

```bash
curl -H "Host: events.$GATEWAY_ADDR" http://$GATEWAY_ADDR/health
```

A healthy service returns `200`.

## Configure the CLI

The CLI derives the Event Ledger URL from `base_http_url` by replacing the leading
`api.` with `events.`. Set `event_ledger_url` and `event_ledger_host` in the CLI
config when your gateway uses another address. See [CLI](../overview/cli.md).

Generate a read key and check that the CLI can read:

```bash
nvcf-cli api-key generate --for event-ledger
nvcf-cli stats get --function-version-id <function-version-id>
```

## Send events from a compute plane

SIS events arrive without extra setup. Events from a GPU cluster need the NVCA
OpenTelemetry collector, which registration does not enable. Set this value in the
environment file of each compute plane that should report events:

```yaml
global:
  nvcaOperator:
    selfManaged:
      otelCollector:
        enabled: true
```

When the compute plane is in a different cluster than the control plane, also
set `global.nvcaOperator.selfManaged.eventLedgerServiceURL` to an address that
can reach the Event Ledger. When they share a cluster, leave it empty. The
collector then sends events to the in-cluster Service. See
[Install the NVCA operator](../compute-plane/cluster-management/self-managed.md#installing-the-nvca-operator).

The collector authenticates with the projected service account token of the NVCA
pod. That token can write events but cannot read them. The cluster ID on each
event must match the cluster that the token identifies. If an event omits it, the
Event Ledger fills it in.

After the compute plane is installed, the `nvca` pod in `nvca-system` has an
`nvca-otel-collector` container.

```bash
kubectl --namespace nvca-system get pods \
  --selector app.kubernetes.io/name=nvca -o jsonpath='{..name}'
```

## Operations

- Metrics: Prometheus metrics are at `event-ledger.nvcf.svc.cluster.local:8081/metrics`.
  Point your Prometheus scrape at that address.
- High availability: the Event Ledger keeps its data in Cassandra. See
  [High Availability](./high-availability.md).
- Keys: read keys expire. The CLI creates 24 hour keys by default. Rotate them
  with `nvcf-cli api-key generate --for event-ledger`.

## Troubleshooting

| Symptom | Likely cause | What to do |
| --- | --- | --- |
| CLI reports `no Event Ledger API key configured` | No saved Event Ledger key | Run `nvcf-cli api-key generate --for event-ledger` |
| CLI reports `--function-version-id is required` | No flag and no saved function version | Pass `--function-version-id` |
| CLI reports that the ID must be a UUID | The function version ID is malformed | Use the version ID from `nvcf-cli function list-versions <function-id>` |
| API returns `401` | No bearer token, or a JWT that does not validate | Send a valid key |
| API returns `403` | Unknown or expired key, or a key without the read scope | Generate a new key |
| API returns `400` on events | A context value has a character outside letters, numbers, and dashes, or only one of `attribute_key` and `attribute_value` is set | Fix the query |
| `stats get` shows `Contexts: 0` for a deployed function | Wrong function version ID, SIS publishing off, or the collector is not enabled | Check the ID, the SIS setting above, and the collector value |
| `stages events list` shows `No events found.` | The context flags do not match an existing context | Run `stats get` and copy the context keys exactly |
| The Event Ledger pod is not ready | Cassandra keyspace or the OpenBao role is missing | Check the pod logs and the migrations of the stack |

For general stack problems, see [Troubleshooting](./troubleshooting.md).
