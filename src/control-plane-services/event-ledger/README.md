<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Event Ledger

NVCF deploys functions through a control plane and one or more GPU clusters.
Each cluster runs an NVIDIA Cluster Agent (NVCA), and several services observe
different parts of the [function lifecycle](https://docs.nvidia.com/nvcf/function-lifecycle).

Event Ledger brings those observations together. It accepts Kubernetes events
and service events, stores the history for each workload context, and tracks the
latest event so callers can follow a deployment across service and cluster
boundaries. It records what happened; it does not schedule workloads or drive
deployment. See the [NVCF architecture overview](https://docs.nvidia.com/nvcf/architecture-overview)
for where it fits.

## V3 API

| Method | Endpoint | Description |
| --- | --- | --- |
| `POST` | `/v3/ledger/cloudevents` | Store one CloudEvent or a batch of CloudEvents. |
| `POST` | `/v3/ledger/k8s-events` | Store OTLP log records encoded as protobuf. |
| `GET` | `/v3/ledger/namespace/{namespace}/events` | List events for a namespace and context. |
| `GET` | `/v3/ledger/namespace/{namespace}/stats` | Return the latest event for each context in a namespace. |

### Send a CloudEvent

The endpoint accepts structured CloudEvents JSON. Use
`application/cloudevents+json` for one event or
`application/cloudevents-batch+json` for an array.

```bash
curl -X POST http://localhost:8080/v3/ledger/cloudevents \
  -H 'Content-Type: application/cloudevents+json' \
  -d '{
    "specversion": "1.0",
    "id": "event-1",
    "source": "example-service",
    "type": "ready",
    "time": "2026-08-11T12:00:00Z",
    "namespace": "example",
    "instanceId": "instance-1",
    "data": {"message": "instance is ready"}
  }'
```

`namespace` is required. The optional context fields are `instanceId`,
`deploymentId`, `gpuSpecificationId`, `clusterId`, and `resourceId`.

### Send OTLP log records

`POST /v3/ledger/k8s-events` accepts an OTLP
`ExportLogsServiceRequest` with `Content-Type: application/x-protobuf`. Gzip
request compression is supported.

Each log record must include these string attributes:

- `event_name`
- `namespace`
- `source`

It may also include `instance_id`, `deployment_id`, `gpu_specification_id`,
`cluster_id`, and `resource_id`.

### Read events

Pass the context fields as query parameters: `instance_id`, `deployment_id`,
`gpu_specification_id`, `cluster_id`, and `resource_id`. Context values may
contain letters, numbers, and dashes. Instance IDs may also contain dots between
non-empty segments.

```bash
curl 'http://localhost:8080/v3/ledger/namespace/example/events?instance_id=instance-1'
```

Events are returned newest first. If no context parameters are supplied, the
endpoint returns events stored without a context.

`resource_id` is a generic context field for events that have no other
distinguishing context field, keyed by a producer-supplied identifier. It is
part of the context key, so a lookup by `resource_id` is a direct read:

```bash
curl 'http://localhost:8080/v3/ledger/namespace/example/events?resource_id=request-123'
```

#### Filter by a details attribute

The optional `attribute_key` and `attribute_value` parameters narrow the result
to events whose `details.attributes[attribute_key]` equals `attribute_value`.
This is a generic correlation filter over any producer-supplied attribute. The
two parameters must be provided together: omitting both disables filtering and
all events are returned, while supplying only one returns `400 Bad Request`.
Filtering runs over the events of the queried context, so it is combined with
the context parameters above.

```bash
curl 'http://localhost:8080/v3/ledger/namespace/example/events?instance_id=instance-1&attribute_key=correlation_id&attribute_value=request-123'
```

### Read stats

```bash
curl 'http://localhost:8080/v3/ledger/namespace/example/stats'
```

Use `eventFilter` for a comma-separated list of event names:

```bash
curl 'http://localhost:8080/v3/ledger/namespace/example/stats?eventFilter=ready,destroyed'
```

The optional `view=filtered` query selects the filtered stats view when it is
enabled in configuration. The endpoint returns `404` when that view is disabled.

## Authentication

Authentication is enabled by default. For the JWT provider, configure
`auth.jwk-set-url`, `auth.issuer`, `auth.audience`, and `auth.tenant-claim`.
Tokens must have an expiration and the configured tenant claim must contain the
account or namespace being accessed. The tenant claim may be a string or a list
of strings. Send a bearer token with the `write` scope for `POST` requests or
the `read` scope for `GET` requests.

JWT authentication is supported on the tenant-aware v3 API. Set
`deprecate-endpoints: true` when using the JWT provider.

For local development, authentication can be disabled with
`--disable-authentication`. Do not disable it in production.

## Configuration

Generate a configuration file:

```bash
go run ./cmd/api generate-config
```

Start the service with that file:

```bash
go run ./cmd/api --config config.yaml
```

Configuration is loaded from files, then environment variables, then command-line
flags. Run `go run ./cmd/api --help` for all options.

Cassandra is the supported database. Its schema must be provisioned before the
service starts. CloudEvents publishing and distributed tracing are optional.

## Build and test

Use the Go version declared in `go.mod`.

```bash
go build -o event-ledger-api ./cmd/api
go test ./...
(cd common && go test ./...)
```

Build the container image with:

```bash
docker build -t event-ledger-api .
```

The older v1 and v2 endpoints remain available unless `deprecate-endpoints` is
enabled.
