<!--
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

# GDS API

The Global Discovery Service (GDS) API is the protobuf package
`nvidia.gds.v1` in `api/proto/nvidia/gds/v1`. Every RPC is served over gRPC
and mirrored as REST/JSON by grpc-gateway. The OpenAPI description is
`api/gen/openapi/gds.swagger.json`. The design is in
`docs-gds/global-discovery-service-design.md`, section 5.

The API is additive only. A breaking change means a new `v2` package.

## Services

| Service | File | Callers |
| :---- | :---- | :---- |
| `ClusterService` | `cluster.proto` | gds-mcs-controller (writes), anyone (reads) |
| `ServiceExportService` | `mcs.proto` | gds-mcs-controller of the exporting cluster |
| `ServiceImportService` | `mcs.proto` | gds-mcs-controller of every cluster |
| `PoolService` | `pool.proto` | pool operators and PoolRelays (writes), routers and pylons (reads) |
| `ServerInfoService` | `server.proto` | every client, for fallback endpoints |

## REST paths

`{cs}` is the clusterset. Path segments are DNS-1123 labels.

| RPC | Method and path |
| :---- | :---- |
| `RegisterCluster` | `PUT /v1/clustersets/{cs}/clusters/{cluster}` |
| `HeartbeatCluster` | `POST /v1/clustersets/{cs}/clusters/{cluster}:heartbeat` |
| `DeregisterCluster` | `DELETE /v1/clustersets/{cs}/clusters/{cluster}` |
| `GetCluster` | `GET /v1/clustersets/{cs}/clusters/{cluster}` |
| `ListClusters` | `GET /v1/clustersets/{cs}/clusters` |
| `WatchClusters` | `GET /v1/clustersets/{cs}/clusters:watch` |
| `ListClusterSets` | `GET /v1/clustersets` |
| `PutServiceExport` | `PUT /v1/clustersets/{cs}/clusters/{cluster}/exports/{namespace}/{name}` |
| `DeleteServiceExport` | `DELETE /v1/clustersets/{cs}/clusters/{cluster}/exports/{namespace}/{name}` |
| `ListServiceExports` | `GET /v1/clustersets/{cs}/clusters/{cluster}/exports` |
| `ReplaceEndpoints` | `PUT /v1/clustersets/{cs}/clusters/{cluster}/exports/{namespace}/{name}/endpoints` |
| `GetServiceImport` | `GET /v1/clustersets/{cs}/imports/{namespace}/{name}` |
| `ListServiceImports` | `GET /v1/clustersets/{cs}/imports` |
| `WatchServiceImports` | `GET /v1/clustersets/{cs}/imports:watch` |
| `RegisterPool` | `POST /v1/clustersets/{cs}/pools` |
| `UpdatePool` | `PATCH /v1/clustersets/{cs}/pools/{pool_id}` |
| `HeartbeatPool` | `POST /v1/clustersets/{cs}/pools/{pool_id}:heartbeat` |
| `DeregisterPool` | `DELETE /v1/clustersets/{cs}/pools/{pool_id}` |
| `GetPool` | `GET /v1/clustersets/{cs}/pools/{pool_id}` |
| `ListPools` | `GET /v1/clustersets/{cs}/pools` |
| `WatchPools` | `GET /v1/clustersets/{cs}/pools:watch` |
| `ListRouters` | `GET /v1/clustersets/{cs}/routers` |
| `WatchRouters` | `GET /v1/clustersets/{cs}/routers:watch` |
| `GetServerInfo` | `GET /v1/serverinfo` |

Writes with a body take the request message as JSON (`body: "*"`); path
parameters override body fields. GET filters are query parameters named after
the request fields, for example
`GET /v1/clustersets/prod/pools?model=llama-3&states=POOL_STATE_ACTIVE&states=POOL_STATE_DRAINING&includeEndpoints=true`.
`api/gateway_test.go` pins every path to its RPC.

## JSON

JSON follows the proto3 JSON mapping:

- Field names are lowerCamelCase on output (`poolId`, `readyWorkerCount`).
  Input accepts lowerCamelCase and the proto names (`pool_id`).
- Enums are strings with their full names, for example
  `"POOL_STATE_DRAINING"` and `"SERVICE_TYPE_CLUSTER_SET_IP"`.
- `uint64` fields (`generation`, `epoch`) are strings: `"generation": "1761480000000"`.
- Durations are strings with an `s` suffix (`"leaseTtl": "30s"`); timestamps
  are RFC 3339 strings.
- `UpdatePool.updateMask` is a comma-separated string of field paths,
  for example `"updateMask": "desiredState,workerCount"`.

Register a cluster, then heartbeat it with the returned epoch:

```bash
curl -sX PUT http://gds:8080/v1/clustersets/prod/clusters/use1-a \
  -d '{"region":"us-east-1","zones":["us-east-1a"],"leaseTtl":"30s"}'
curl -sX POST http://gds:8080/v1/clustersets/prod/clusters/use1-a:heartbeat \
  -d '{"epoch":"1761480000000"}'
```

Drain a pool through the overlay:

```bash
curl -sX PATCH http://gds:8080/v1/clustersets/prod/pools/7f1c... \
  -d '{"pool":{"desiredState":"POOL_STATE_DRAINING"},"updateMask":"desiredState","generation":"1761480005000"}'
```

## Writers, generations and leases

- Every record has one logical writer: the controller of a cluster or the
  operator of a pool.
- Writes carry a `generation` that must exceed the stored one. Writers use
  `max(previous + 1, unix milliseconds)`, which stays monotonic across
  restarts and region failover. A stale write fails with
  `STALE_GENERATION` and the stored generation.
- `RegisterCluster` and `RegisterPool` mint a new lease epoch,
  `max(previous + 1, unix milliseconds)`. Heartbeats must carry it; any other
  epoch fails with `LEASE_EPOCH_MISMATCH` and the writer registers again.
- Lease TTLs are clamped to [10 s, 5 m]. Defaults: 30 s for clusters, 60 s
  for pools. Writers heartbeat every TTL/3.
- A lease expires at read time once `renewed_at + ttl + skew` (skew 2 s) is
  in the past. Nothing writes expiry.
- `ReplaceEndpoints` replaces the whole endpoint set; there is no
  per-endpoint write.

## Watches

`WatchClusters`, `WatchServiceImports`, `WatchPools` and `WatchRouters` stream
events. Each response has a `type` (`EventType`), the object for `UPSERT` and
`DELETE`, and a `revision`.

1. `EVENT_TYPE_SNAPSHOT_START`: discard local state.
2. One `EVENT_TYPE_UPSERT` per object.
3. `EVENT_TYPE_SNAPSHOT_END` with the revision.
4. Deltas: `EVENT_TYPE_UPSERT` with the full object, `EVENT_TYPE_DELETE` with
   the last known object (a deleted pool carries state `POOL_STATE_GONE`).
5. `EVENT_TYPE_BOOKMARK` with the current revision every 5 s. It keeps the
   NLB 350 s idle timeout and chunked REST streams alive.

The revision is an opaque `view_epoch:seq` token of the serving replica. Pass
the last revision seen as `resume_revision` to resume: the same replica
replays the missed deltas; any other replica, or a revision too old for its
history (10000 events per clusterset), starts with a new snapshot. A resume
never fails and never skips events.

A watcher that falls 1024 events behind is re-snapshotted, not disconnected.
A replica serves at most `--max-watchers` (5000) streams; above that a watch
fails with `TOO_MANY_WATCHERS`. Filters (namespace, selector, region, model,
`include_endpoints`) apply per subscriber.

Over REST a watch is a chunked response of newline-delimited JSON objects,
one per event, each wrapped in `result`:

```text
{"result":{"type":"EVENT_TYPE_SNAPSHOT_START"}}
{"result":{"type":"EVENT_TYPE_UPSERT","pool":{"clusterset":"prod","poolId":"7f1c...","name":"llama-blue","state":"POOL_STATE_ACTIVE"}}}
{"result":{"type":"EVENT_TYPE_SNAPSHOT_END","revision":"1761480000000:42"}}
{"result":{"type":"EVENT_TYPE_BOOKMARK","revision":"1761480000000:42"}}
```

A stream that ends with an error sends one last line `{"error":{...}}` with
the status described below.

```bash
curl -sN 'http://gds:8080/v1/clustersets/prod/pools:watch?model=llama-3'
```

## Errors

Errors are `google.rpc.Status` values. Over REST the body is
`{"code": <gRPC code>, "message": "...", "details": [...]}`. Every GDS error
carries a `google.rpc.ErrorInfo` detail with domain `gds.nvidia.com` and a
reason:

| Reason | gRPC code | HTTP | Meaning and client action |
| :---- | :---- | :---- | :---- |
| `STALE_GENERATION` | `FAILED_PRECONDITION` | 400 | The generation does not exceed the stored one. `metadata.current_generation` and a `PreconditionFailure` detail hold the stored generation; retry with a larger one. |
| `LEASE_EPOCH_MISMATCH` | `ABORTED` | 409 | The heartbeat epoch is not current. Register again and resend state. |
| `CLUSTER_NOT_REGISTERED` | `FAILED_PRECONDITION` or `NOT_FOUND` | 400 or 404 | The cluster has no registration. Register first. |
| `POOL_NAME_TAKEN` | `ALREADY_EXISTS` | 409 | Another pool of the cluster has the name. |
| `TOO_MANY_ENDPOINTS` | `INVALID_ARGUMENT` | 400 | The endpoint set exceeds 1000 endpoints. |
| `TOO_MANY_WATCHERS` | `RESOURCE_EXHAUSTED` | 429 | The replica is at `--max-watchers`. Back off or try another replica. |
| `STORE_UNAVAILABLE` | `UNAVAILABLE` | 503 | The store did not answer. Retry with backoff. |
| `SHUTTING_DOWN` | `UNAVAILABLE` | 503 | The replica is draining. Reconnect; another replica serves. |

Other invalid input is `INVALID_ARGUMENT` with a `google.rpc.BadRequest`
detail naming the fields. The Go constants are in `api/errors.go`.

Example body:

```json
{
  "code": 9,
  "message": "export generation 5 does not exceed stored generation 7",
  "details": [
    {
      "@type": "type.googleapis.com/google.rpc.ErrorInfo",
      "reason": "STALE_GENERATION",
      "domain": "gds.nvidia.com",
      "metadata": {"current_generation": "7"}
    }
  ]
}
```

## Limits

| Item | Limit |
| :---- | :---- |
| Identifiers (clusterset, cluster, namespace, name, pool name) | DNS-1123 label |
| Labels per object | 64 |
| Annotations per object | 32, at most 16 KiB in total |
| Ports per export | 64 |
| Endpoints per export per cluster | 1000 |
| Attributes per endpoint | 16 |
| Models per pool | 256 |
| Message size | 4 MiB |

The constants are in `api/errors.go`.

## Well-known labels and annotations

Services opt in to GDS through labels and annotations with the
`gds.nvidia.com/` prefix: `export`, `role` (`router`, `pool-frontend`,
`kv-relay`), `pool`, and the annotations `pool-models`, `hardware-class`,
`dc`, `dial-scheme`, `grpc-port-name` and `http-port-name`. The constants and
their meaning are in `api/labels.go`; the Rust gds-client crate mirrors them.

Clients dial an import through its derived Service,
`derived-<hash>.<namespace>.svc.cluster.local`, where the name is
`api.DerivedServiceName(namespace, name)`, the same algorithm as
`sigs.k8s.io/mcs-api`.
