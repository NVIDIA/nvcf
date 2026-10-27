# Event Ledger

Event Ledger records function deployment events from the NVCF control plane
and its registered compute planes. Use it to find the latest deployment state
for each workload context and inspect the events that led to that state.

Event Ledger is an observation service. It does not schedule workloads, change
deployment state, or replace application logs, metrics, or traces.

## How it works

Event Ledger combines events from two sources:

- The Spot Instance Service (SIS) publishes function instance lifecycle events
  from the control plane as CloudEvents.
- The NVIDIA Cluster Agent (NVCA) OpenTelemetry Collector watches Kubernetes
  Pod and ICMSRequest events on each enabled compute plane and publishes them as
  OpenTelemetry Protocol (OTLP) log records.

The Event Ledger stores both sources under a namespace. For function events,
the namespace is the function version ID. Each event also has a context that
identifies the workload or resource that produced it.

Event delivery is asynchronous. A newly produced event might not be visible
immediately.

## Event model

An Event Ledger record contains these fields:

| Field | Description |
| --- | --- |
| `namespace` | Function version ID for function deployment events. |
| `context` | Canonical, comma-separated set of fields that identifies one workload or resource. |
| `event_name` | Producer-defined event name. |
| `source` | Producer of the event, such as `nvidia-spot` or `nvca`. |
| `timestamp` | Time reported by the producer. |
| `details.body` | Producer payload, such as a Kubernetes event message or object. |
| `details.attributes` | Additional producer attributes available for inspection and filtering. |
| `created_at` | Time the event name was first stored for the context. |
| `updated_at` | Time the stored event was last updated. |

### Context fields

A context can contain any combination of these fields. The canonical context
uses the order shown below:

| Context field | Meaning |
| --- | --- |
| `cluster_id` | Registered compute-plane cluster ID. |
| `deployment_id` | NVCF deployment ID. |
| `gpu_specification_id` | GPU specification selected for the deployment. |
| `instance_id` | Function instance or Kubernetes Pod name. |
| `resource_id` | Producer-supplied resource identifier, such as an ICMSRequest ID. |

For example:

```text
cluster_id=cluster-1,deployment_id=5a3ef171-f6df-48cd-81db-c8096a6298d6,gpu_specification_id=gpu-a100,instance_id=function-abc-0
```

Context matching is exact. Supply every field shown in the context when you
read its events. If no context fields are supplied, Event Ledger returns only
events that were stored without a context. It does not return events from every
context.

### Event names

SIS publishes these function instance lifecycle event names:

| Event name | Meaning |
| --- | --- |
| `InstanceReady` | The function instance became ready. |
| `InstanceDestroyed` | The function instance was destroyed. |
| `InstanceRequestingTermination` | Termination of the function instance was requested. |

The NVCA collector creates names from Kubernetes event reasons:

| Event name form | Meaning |
| --- | --- |
| `Pod.<reason>` | A Pod-level Kubernetes event. |
| `Pod.<container>.<reason>` | A Kubernetes event for a specific container in a Pod. |
| `ICMSRequest.InstanceCreation[.<instance_state>]` | An instance creation request event. |
| `ICMSRequest.InstanceStatusUpdate[.<instance_state>]` | An instance state change. |
| `ICMSRequest.InstanceTermination[.<instance_state>]` | An instance termination request event. |

For example, a running instance can produce
`ICMSRequest.InstanceStatusUpdate.running`. Kubernetes reason values are
producer-defined, so Pod event names vary by workload and cluster.

NVCA events can include attributes such as `function_id`, `nca_id`,
`icms_request_id`, `instance_type_name`, `failure_category`,
`termination_cause`, `k8s.event.reason`, and Kubernetes object metadata. Use
JSON output to inspect all attributes on an event.

## Storage semantics

`stats get` returns one row per context. Each row contains the latest event for
that context, and the summary counts contexts by their latest event name.

Event Ledger stores one row for each unique combination of namespace, context,
and event name. If the same event name occurs again for the same context, the
new occurrence replaces the stored payload and timestamp. `created_at` remains
the time that event name was first stored, while `updated_at` records the most
recent update.

As a result, `stages events list` returns the latest stored occurrence of each
event name for a context, ordered by event timestamp. It is not an immutable log
of every repeated occurrence.

The v3 read endpoints do not paginate results. A read returns all stored event
names for the selected context. The v3 API does not expose a retention
duration. Plan Cassandra capacity and backups according to the event rate of
the registered compute planes.

## Prerequisites

Before reading events:

1. Enable and verify Event Ledger on the control plane.
2. Enable the NVCA collector on every compute plane whose Kubernetes events you
   want to record.
3. Configure `nvcf-cli` with the Event Ledger endpoint and gateway host.
4. Generate a dedicated Event Ledger API key.

See [Enable and Operate Event Ledger](../self-managed/event-ledger.md) for the
self-managed setup.

## Generate an API key

Event Ledger uses a dedicated audience and does not fall back to the function or
task API key. Generate a read key with the default `fnds:getEvents` scope:

```bash
nvcf-cli api-key generate --for event-ledger
```

The CLI saves the key as its dedicated Event Ledger credential. It also reads
`EVENT_LEDGER_API_KEY` from the environment. Running `nvcf-cli api-key generate`
without `--for` generates function, task, and Event Ledger keys together.

## Inspect deployment state

Start with `stats get`. The command accepts a function version ID or uses the
version ID saved by a previous CLI function command:

```bash
nvcf-cli stats get --function-version-id <function-version-id>
```

Example output:

```text
Function version: 38ff2c62-c76f-4f89-b944-f3aaf68bf62a
Contexts: 2
  ICMSRequest.InstanceStatusUpdate.running: 1
  InstanceReady: 1

CONTEXT                                              EVENT                                         TIMESTAMP                 FIRST SEEN
instance_id=function-abc-0                           InstanceReady                                 2026-10-26T20:10:20Z      2026-10-26T20:09:01Z
cluster_id=cluster-1,resource_id=request-123          ICMSRequest.InstanceStatusUpdate.running      2026-10-26T20:10:18Z      2026-10-26T20:08:55Z
```

Use `--event-filter` to keep contexts whose latest event has one of the exact
event names:

```bash
nvcf-cli stats get \
  --function-version-id <function-version-id> \
  --event-filter InstanceReady,ICMSRequest.InstanceStatusUpdate.running
```

Use `--json` when scripting or when you need to inspect contexts without the
text table:

```bash
nvcf-cli stats get \
  --function-version-id <function-version-id> \
  --json | jq '.contexts[]'
```

## Read events for a context

Copy the fields from one `stats get` context into `stages events list`. Include
only the fields present in that context.

For a Pod or SIS instance context:

```bash
nvcf-cli stages events list \
  --function-version-id <function-version-id> \
  --cluster-id <cluster-id> \
  --deployment-id <deployment-id> \
  --gpu-specification-id <gpu-specification-id> \
  --instance-id <instance-id>
```

For an ICMSRequest resource context:

```bash
nvcf-cli stages events list \
  --function-version-id <function-version-id> \
  --cluster-id <cluster-id> \
  --resource-id <icms-request-id>
```

Text output includes the timestamp, event name, severity, and message. JSON
output also includes the source, attributes, body, `created_at`, and
`updated_at` fields:

```bash
nvcf-cli stages events list \
  --function-version-id <function-version-id> \
  --cluster-id <cluster-id> \
  --resource-id <icms-request-id> \
  --json | jq '.events[]'
```

Events are returned most recent first.

### Filter by an event attribute

Use `--attribute-key` and `--attribute-value` together to keep events whose
`details.attributes` entry has the requested value:

```bash
nvcf-cli stages events list \
  --function-version-id <function-version-id> \
  --cluster-id <cluster-id> \
  --deployment-id <deployment-id> \
  --gpu-specification-id <gpu-specification-id> \
  --instance-id <instance-id> \
  --attribute-key icms_request_id \
  --attribute-value <icms-request-id>
```

Attribute filtering applies after the context is selected. It does not search
other contexts in the namespace. Both flags are required, and comparison uses
the string representation of the stored attribute value.

## HTTP API

The CLI uses the Event Ledger v3 read API:

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `GET` | `/v3/ledger/namespace/{namespace}/stats` | Return the latest event for every context. |
| `GET` | `/v3/ledger/namespace/{namespace}/events` | Return stored event names for one exact context. |

The stats endpoint accepts `eventFilter`, a comma-separated list of exact event
names. The events endpoint accepts `instance_id`, `deployment_id`,
`gpu_specification_id`, `cluster_id`, `resource_id`, `attribute_key`, and
`attribute_value`.

Use an Event Ledger API key as a bearer credential:

```bash
curl -sS "${EVENT_LEDGER_URL}/v3/ledger/namespace/${FUNCTION_VERSION_ID}/stats" \
  -H "Authorization: Bearer ${EVENT_LEDGER_API_KEY}" \
  -H "Accept: application/json"
```

When sending requests to a gateway address instead of the Event Ledger DNS
name, also set `Host: events.<domain>`.

Event producers use these v3 endpoints:

| Method | Endpoint | Content type | Producer authentication |
| --- | --- | --- | --- |
| `POST` | `/v3/ledger/cloudevents` | `application/cloudevents+json` or `application/cloudevents-batch+json` | Service bearer token or an authorized NVCA projected token. |
| `POST` | `/v3/ledger/k8s-events` | `application/x-protobuf` OTLP logs, optionally gzip-compressed | Service bearer token or an authorized NVCA projected token. |

The CLI does not wrap the producer endpoints. The NVCA collector and SIS
configure them as part of the self-managed deployment.

The API returns errors as `application/problem+json`. Common status codes are:

| Status | Meaning |
| --- | --- |
| `400` | Invalid namespace, context, filter, or event payload. |
| `401` | Missing or invalid bearer credential. |
| `403` | The credential lacks the required scope or tenant access. |
| `404` | The route or optional filtered stats view was not found. |
| `413` | A decompressed OTLP request exceeded 10 MiB. |
| `415` | The producer used an unsupported content type. |
| `500` | Event Ledger could not read or write its database. |

Self-managed deployments expose the v3 API. The legacy v1 and v2 APIs are
disabled and are not part of the supported self-managed interface.

## Task events

The NVCA collector can store task-related Kubernetes events under a task ID
namespace. The current `stats` and `stages` CLI commands accept only a function
version UUID, so they do not query task namespaces.

Use `nvcf-cli task events <task-id>` for the NVCT task lifecycle API. That
command and the Event Ledger read commands use different APIs and credentials.

## Limitations

- Event Ledger does not store every repeated occurrence of an event name.
- Context reads require an exact context match.
- Attribute filters do not search across contexts.
- The v3 read API does not paginate results.
- The Event Ledger CLI commands currently query function version namespaces
  only.
