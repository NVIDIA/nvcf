# Event Ledger

The Event Ledger records what happens to a function deployment. Services on the
control plane and agents on each GPU cluster send it events. It stores the
history of every workload and keeps the latest event, so you can follow a
deployment across services and clusters. It records events only. It does not
schedule workloads or drive deployments.

The Event Ledger is an optional addon of the self-managed control plane. To
install it, see [Event Ledger Enablement](../self-managed/event-ledger.md).

## How events are organized

Events are grouped by function version and then by context.

- Function version: the function version ID is the namespace. All events for one
  function version share it.
- Context: the part of a deployment an event belongs to. A context is built from
  these optional fields.

| Field | Meaning |
| --- | --- |
| `instance_id` | One instance (pod) of the function |
| `deployment_id` | The deployment |
| `gpu_specification_id` | The GPU specification of the deployment |
| `cluster_id` | The cluster that ran the workload |
| `resource_id` | A producer-defined ID, for example an ICMS request |

Values can contain letters, numbers, and dashes. Instance IDs can also contain
dots between non-empty segments.

Events written without any context field are stored under the empty context.
A read must name the context it wants. Reading with no context returns only the
events stored without one.

Each event has a name, a source, a timestamp, and details. The details carry a
severity, a body, and free-form attributes.

## Who sends events

- SIS records instance lifecycle events, such as `InstanceReady` and
  `InstanceDestroyed`.
- The NVCA OpenTelemetry collector forwards Kubernetes events from the GPU
  cluster. Event names look like `ICMSRequest.InstanceStatusUpdate.running` for
  request status and `Pod.Scheduled` for pod events. The ledger takes
  `cluster_id` from the collector's identity, so every NVCA context carries it.
- The gRPC proxy publishes events to the ledger.

The collector is off by default and must be enabled for each compute plane. See
[Install the NVCA operator](../compute-plane/cluster-management/self-managed.md#installing-the-nvca-operator).

Stats keep only the latest event of each context, so the event names you see
depend on how far a deployment has progressed.

## Read events with the CLI

Reading needs an Event Ledger API key. The key is saved in the CLI state.

```bash
nvcf-cli api-key generate --for event-ledger --expires-in 24h
```

The default scope is `fnds:getEvents`. Keys expire after 24 hours unless you set
`--expires-in`.

1. Show the latest event of every context for a function version.

   ```bash
   nvcf-cli stats get --function-version-id <function-version-id>
   ```

   ```text
   Function version: <function-version-id>
   Contexts: 2
     ICMSRequest.InstanceStatusUpdate.running: 1
     Pod.Scheduled: 1

   CONTEXT                                          EVENT                                     TIMESTAMP  FIRST SEEN
   cluster_id=c1,instance_id=i1,resource_id=r1      ICMSRequest.InstanceStatusUpdate.running  ...        ...
   cluster_id=c1,deployment_id=d1,instance_id=i1    Pod.Scheduled                             ...        ...
   ```

   Narrow the result with `--event-filter` and a comma-separated list of event
   names. Omit `--function-version-id` to use the version the CLI has selected.

2. Pass the keys of one context to `stages events list` to read its history,
   newest first.

   ```bash
   nvcf-cli stages events list \
     --function-version-id <function-version-id> \
     --cluster-id c1 --instance-id i1 --resource-id r1
   ```

   Use `--attribute-key` with `--attribute-value` to keep only events whose
   attribute has that value. Add `--json` to either command for JSON output.

An unknown function version is not an error. `stats get` reports `Contexts: 0`
and `stages events list` prints `No events found.`

See [Event Ledger Commands](./cli.md#event-ledger-commands) for all flags.

## Read events with the API

The Event Ledger is served at `events.<domain>` on your gateway. Send the key as a
bearer token.

```bash
curl -H "Authorization: Bearer $EVENT_LEDGER_API_KEY" \
  "http://events.<domain>/v3/ledger/namespace/<function-version-id>/stats"
```

| Method | Endpoint | Description |
| --- | --- | --- |
| `GET` | `/v3/ledger/namespace/{namespace}/stats` | Latest event of each context |
| `GET` | `/v3/ledger/namespace/{namespace}/events` | Event history of one context |
| `POST` | `/v3/ledger/cloudevents` | Store one CloudEvent or a batch |
| `POST` | `/v3/ledger/k8s-events` | Store OTLP log records encoded as protobuf |
| `GET` | `/health` | Health check |

### Stats

`eventFilter` takes a comma-separated list of event names. The optional
`view=filtered` selects the filtered view when it is enabled in configuration.
An unknown view returns `400`, and a disabled view returns `404`.

```json
{
  "namespace": "<function-version-id>",
  "summary": {
    "total_contexts": 2,
    "by_event": {"ICMSRequest.InstanceStatusUpdate.running": 1, "Pod.Scheduled": 1}
  },
  "contexts": [
    {
      "context": "cluster_id=c1,instance_id=i1,resource_id=r1",
      "event_name": "ICMSRequest.InstanceStatusUpdate.running",
      "timestamp": "...",
      "first_seen": "..."
    }
  ]
}
```

### Events

Pass the context fields as query parameters: `instance_id`, `deployment_id`,
`gpu_specification_id`, `cluster_id`, and `resource_id`.

```bash
curl -H "Authorization: Bearer $EVENT_LEDGER_API_KEY" \
  "http://events.<domain>/v3/ledger/namespace/<function-version-id>/events?cluster_id=c1&instance_id=i1&resource_id=r1"
```

`attribute_key` and `attribute_value` filter on `details.attributes`. Send them
together. Sending only one returns `400`. A malformed context value also returns
`400`.

```json
{
  "namespace": "<function-version-id>",
  "context": "cluster_id=c1,instance_id=i1,resource_id=r1",
  "events": [
    {
      "event_name": "ICMSRequest.InstanceStatusUpdate.running",
      "source": "nvca",
      "timestamp": "...",
      "details": {
        "severity": "...",
        "body": "...",
        "attributes": {}
      }
    }
  ]
}
```

The v3 read endpoints take no pagination parameters. They return every matching
event, newest first.

### Write events

`POST /v3/ledger/cloudevents` accepts structured CloudEvents JSON with
`application/cloudevents+json`, or an array with
`application/cloudevents-batch+json`. `namespace` is required. A producer keyed
by function version can send `functionVersionId` instead.
`POST /v3/ledger/k8s-events` accepts an OTLP `ExportLogsServiceRequest` as
`application/x-protobuf`, optionally gzip-compressed. Each log record needs the
string attributes `event_name`, `namespace`, and `source`.

The v1 and v2 endpoints are disabled in self-managed deployments and return
`404`.

## Authentication and scopes

Every request needs a bearer credential. A request without one gets `401`. A
well-formed JWT that does not validate also gets `401`. An opaque key that the
API Keys service does not recognize gets `403`.

| Scope | Allows |
| --- | --- |
| `fnds:getEvents` | Read events and stats |
| `fnds:createEvent` | Write events |

A read key cannot write, and a write key cannot read. The request is rejected.
Services write with their own credentials, so you normally only need a read
key.

Create a read key with `nvcf-cli api-key generate --for event-ledger`. To create
one directly with the API Keys service, see
[Direct API key creation](./api.md#direct-api-key-creation).

## Related pages

- [Event Ledger Enablement](../self-managed/event-ledger.md)
- [CLI](./cli.md)
- [API](./api.md)
- [Container-Based Task Creation](./container-tasks.md#monitoring-a-task) for task
  events, which come from the task API, not the Event Ledger
