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
| `PoolService` | `pool.proto` | pool operators, Pylons and PoolRelays (writes), routers and pylons (reads) |
| `ServerInfoService` | `server.proto` | every client, for fallback endpoints |

## REST paths

`{cs}` is the clusterset. Path segments are DNS-1123 labels, except
`{pool_id}` and `{role}`, the enum name of a pool agent role
(`FRONTEND_ROLE_OPENAI`, `FRONTEND_ROLE_KV_RELAY`, `FRONTEND_ROLE_PYLON`).

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
| `RegisterPoolAgent` | `POST /v1/clustersets/{cs}/pool-agents` |
| `HeartbeatPoolAgent` | `POST /v1/clustersets/{cs}/pools/{pool_id}/agents/{role}:heartbeat` |
| `DeregisterPoolAgent` | `DELETE /v1/clustersets/{cs}/pools/{pool_id}/agents/{role}?agentId={agent_id}` |
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
- Responses omit fields with default values (empty strings, zero counts,
  empty lists); read a missing field as its default. Unknown input fields
  are ignored.
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

- Every record has one logical writer: the controller of a cluster, the
  operator of a pool, or the process instance of a pool agent registration.
- Writes carry a `generation` that must exceed the stored one. Writers use
  `max(previous + 1, unix milliseconds)`, which stays monotonic across
  restarts and region failover. A stale write fails with
  `STALE_GENERATION` and the stored generation.
- `RegisterCluster`, `RegisterPool` and `RegisterPoolAgent` mint a new
  lease epoch, `max(previous + 1, unix milliseconds)`. Heartbeats must carry
  it; any other epoch fails with `LEASE_EPOCH_MISMATCH` and the writer
  registers again.
- Lease TTLs are clamped to [10 s, 5 m]. Defaults: 30 s for clusters, 60 s
  for pools and pool agents. Writers heartbeat every TTL/3.
- A lease expires at read time once `renewed_at + ttl + skew` (skew 2 s) is
  in the past. Nothing writes expiry.
- `ReplaceEndpoints` replaces the whole endpoint set; there is no
  per-endpoint write. An empty list clears it.
- A write with `generation` 0 (or none) asks the server to assign
  `max(stored + 1, unix milliseconds)`. That suits a single writer such as a
  curl session; controllers and clients send their own.
- `PutServiceExport` and `ReplaceEndpoints` need a registered cluster
  (`FAILED_PRECONDITION` `CLUSTER_NOT_REGISTERED`). A heartbeat for an
  unknown cluster is `NOT_FOUND` `CLUSTER_NOT_REGISTERED`.
- Defaults: port protocol TCP, session affinity None, and an endpoint
  address type inferred from its first address (IPv4, IPv6, else FQDN).
  `spec.type` is required. Without `export_creation_timestamp` the server
  keeps the stored one, or uses the time of the first write; controllers
  send the ServiceExport's creationTimestamp.

## ServiceImports

The server merges the exports of one `(namespace, name)` from every cluster
with the rules of Cilium's MCS implementation:

- Only exports of clusters with an active lease take part, oldest
  `export_creation_timestamp` first, ties broken by cluster name. The first
  one wins.
- Ports are the union by (port, protocol); a name or app protocol
  disagreement resolves to the oldest export, and a port whose name is
  already used by another (port, protocol) is skipped.
- IP families are intersected; an export without a common family is
  skipped. An export that lists no family does not constrain the result.
- Type, session affinity and its config, annotations and labels come from
  the winner.

`conflict` is set when any participating export disagrees with the winner
(on ports, IP families, type, session affinity, its client IP timeout,
annotations, labels, internal traffic policy or traffic distribution), with
the mcs-api reason (`PortConflict`, `TypeConflict`, ...). Each
`clusters[]` entry carries:

- `participating`: false when the cluster's lease is not active.
- `conflict`: set when this cluster's own export disagrees with the winner.
- `conditions`: `Valid` (`True/Valid`), `Ready` (`True/Ready`, or
  `False/ClusterLeaseExpired`) and `Conflict` (`True` with the import's
  conflict reason on every participating cluster, as KEP-1645 asks,
  `False/NoConflicts`, or `Unknown/ClusterLeaseExpired`). Controllers copy
  them onto their ServiceExports.

`endpoints[]` has one entry per participating cluster, with counts always
set and the endpoint list only with `include_endpoints`. An import whose
exporting clusters all expired keeps its shape with no participants and no
endpoints until compaction removes the records.

## Pools

A pool is identified by `(cluster, name)` and addressed by `pool_id`. It
exists while a labelled export names it, the API registered it
(`RegisterPool`), or one of its agents registered (`RegisterPoolAgent`), in
any combination; all of them merge into one pool.

### Roles and pool agents

A pool is served by up to three kinds of process, its agents, each with its
own Service and role:

| Role | Label value | Process | Dial mode | Ingress |
| :---- | :---- | :---- | :---- | :---- |
| `FRONTEND_ROLE_OPENAI` | `pool-frontend` | OpenAI-compatible frontend | `DIAL_MODE_INBOUND`: routers dial it | yes |
| `FRONTEND_ROLE_KV_RELAY` | `kv-relay` | Dynamo PoolRelay | `DIAL_MODE_OUTBOUND`: it dials routers over gRPC | no |
| `FRONTEND_ROLE_PYLON` | `pylon` | Pylon transport | `DIAL_MODE_OUTBOUND`: it dials routers and carries requests over its reverse tunnel | yes |

- Label-derived pools: an export whose labels have `gds.nvidia.com/role` set
  to a pool role and `gds.nvidia.com/pool=<name>` creates or joins the pool
  `<name>` of the exporting cluster. A process that fills several roles (a
  frontend that embeds Pylon) is one Service with the annotation
  `gds.nvidia.com/roles: pool-frontend,pylon`; when that annotation is
  present and non-empty it replaces the role label, and the Service yields
  one agent per role, backed by the same endpoints. The exports'
  annotations, the OPENAI export's first, give `models`
  (`gds.nvidia.com/pool-models`), `hardware_class` and `location.dc`; the
  cluster registration gives the region and zones; the exported labels
  (without the role label) become the pool labels.
- `pool_id` is the writer's id or, by default, a name-based UUID (version 5)
  of `clusterset/cluster/name`, the same on every replica, for the
  label-derived pool, its API registration and its agent registrations,
  and across re-registrations.
- API overlay: an API record of the same `(cluster, name)` overrides
  non-empty `location`, `hardware_class` and `models`, adds its labels,
  frontends and services, and supplies `worker_count`,
  `ready_worker_count`, `desired_state`, `lease`, `generation` and
  `owner_id`. Its `pool_id` wins.
- `services[]` and `frontends[]` hold one entry per (exported Service,
  role), OPENAI first: port name from `gds.nvidia.com/http-port-name`
  (default `http`) for OPENAI, `gds.nvidia.com/grpc-port-name` (default
  `grpc`) for KV_RELAY, none for PYLON; plus the API's entries. Every
  `services[]` entry is a Service in the pool's cluster with a `role`
  (required); the MCS controller of that cluster exports it while the pool
  is registered through the API.
- `endpoints[]` has one entry per Service in `services[]` of the pool's
  cluster, also when the Service fills several roles, with zero counts and
  no endpoints while the cluster lease is not active.
  `ready_endpoint_count` counts the ready endpoints of the OPENAI Services;
  `ready_ingress_count` those of the OPENAI and PYLON Services, each Service
  counted once. Routers that dispatch inbound use the first, routers that
  work through Pylon tunnels the second.

`agents[]` has one entry per role present, sorted by role:

- `source`: `AGENT_SOURCE_LABEL` (the pool has an exported Service in the
  role), `AGENT_SOURCE_API` (agent registrations exist) or
  `AGENT_SOURCE_BOTH`.
- From the label source: `service` (the first Service in the role),
  `port_name` (INBOUND agents only), `endpoint_count` and
  `ready_endpoint_count` (ready endpoints while the cluster lease is
  active).
- From the API source: `registrations[]`, one per process instance sorted
  by `agent_id`, each with its own `lease`, `attributes`, `generation`,
  `registered_at` and `updated_at`; `active_registration_count`; and, for
  convenience, the fields of the registration whose lease expires last
  mirrored on the agent (`lease`, `agent_id`, `attributes`, `generation`,
  `registered_at`, `updated_at`).
- `dial_mode` from the role, and `live`: a ready endpoint while the
  cluster lease is active, or any active registration lease.

`kv_relay_live` is true while the KV_RELAY agent is live.

### State

- `GONE` when the labelled exports, the API record and every agent
  registration are gone, for example after `DeregisterPool` of a pool
  without labelled exports. Watchers get a `DELETE` with state `GONE` and
  `GetPool` returns it for one bookmark interval.
- A pool is live while its cluster lease is active and an ingress Service
  (OPENAI or PYLON) has a ready endpoint, or while an ingress agent
  registration has an active lease. An API-only pool (no labelled export,
  no `services[]`, no agent registration) is live while its own lease is
  active.
- `REGISTERED` until the pool has been live once (for an API-only pool:
  until its first heartbeat after registering), while a lease it depends on
  is active (the cluster lease, an agent lease or the pool lease; the pool
  lease alone for an API-only pool), else `UNHEALTHY`. A pool whose only
  live agent is a KV_RELAY therefore stays `REGISTERED`: routers can learn
  its cache facts but have nowhere to send requests.
- Once started: `desired_state` (`ACTIVE` or `DRAINING`) while it is live,
  `UNHEALTHY` otherwise.

### Calls

- `RegisterPool` is idempotent on `(cluster, name)`: it reuses the pool's
  id, replaces its writer-owned fields, mints a new lease epoch and returns
  the merged pool without endpoint lists. A different `pool_id` for a taken
  name, or a `pool_id` that another cluster's pool uses, is
  `ALREADY_EXISTS` `POOL_NAME_TAKEN`. Lease TTL default 60 s.
- `UpdatePool` changes the paths in `update_mask` (`name`, `location`,
  `location.region`, `location.dc`, `location.zones`, `hardware_class`,
  `models`, `worker_count`, `ready_worker_count`, `frontends`, `services`,
  `labels`, `desired_state`, `owner_id`); an empty mask replaces them all.
  `location.cluster` cannot change. On a pool without an API record it
  creates the overlay record (without a lease), which is how a labelled or
  agent-registered pool is drained.
- `HeartbeatPool` renews the lease of an API registration and applies the
  optional worker counts and desired state. A pool without an API
  registration has no lease: `NOT_FOUND`.
- `DeregisterPool` removes the API registration and every agent
  registration of the pool. A pool without labelled exports turns `GONE`;
  a labelled pool stays, without its overlay and agent registrations.
- `RegisterPoolAgent` registers one process instance of an agent:
  `cluster`, `pool_name`, `agent.role`, `agent.agent_id` (the process's id,
  for example a Pylon `inference_server_id`; 1 to 253 bytes) and
  `agent.attributes` (at most 16). It is idempotent on `(cluster,
  pool_name, role, agent_id)` and mints a new lease epoch for that
  registration only, so the N pods of one role never fence each other. It
  creates the pool when none exists, under the default `pool_id`; an
  explicit `pool_id` must be the id of the pool holding the name, and a
  `pool_id` used by another pool is `POOL_NAME_TAKEN`. `generation` must
  exceed the stored one of that registration (`STALE_GENERATION`). The
  response holds `pool_id`, the merged `agent`, this process's
  `registration` with its lease, and `pool_state`.
- `HeartbeatPoolAgent` renews one registration, addressed by `pool_id`,
  `role` and `agent_id`, with its `epoch`; non-empty `attributes` replace
  the registration's. An unknown registration is `NOT_FOUND`, another epoch
  `LEASE_EPOCH_MISMATCH`.
- `DeregisterPoolAgent` removes one registration (`agent_id` is a query
  parameter over REST). The agent stays while other registrations or its
  export remain. Deregistering an unknown registration succeeds.
- `ListPools` and `WatchPools` exclude `GONE` pools unless `states` names
  it.

Register a Pylon pod with a pool, heartbeat it, then deregister it:

```bash
curl -sX POST http://gds:8080/v1/clustersets/prod/pool-agents \
  -d '{"cluster":"use1-a","poolName":"llama-blue","agent":{"role":"FRONTEND_ROLE_PYLON","agentId":"pylon-7f9c","attributes":{"models":"llama-3"}},"leaseTtl":"30s"}'
curl -sX POST http://gds:8080/v1/clustersets/prod/pools/7f1c.../agents/FRONTEND_ROLE_PYLON:heartbeat \
  -d '{"agentId":"pylon-7f9c","epoch":"1761480000000"}'
curl -sX DELETE 'http://gds:8080/v1/clustersets/prod/pools/7f1c.../agents/FRONTEND_ROLE_PYLON?agentId=pylon-7f9c'
```

## Routers

Every export labelled `gds.nvidia.com/role=router` yields one Router per
`(cluster, namespace, name)`: the export's type, labels and annotations,
`grpc_port`, `http_port` and `quic_port` from the ports named by
`gds.nvidia.com/grpc-port-name` (default `grpc`),
`gds.nvidia.com/http-port-name` (default `http`) and
`gds.nvidia.com/quic-port-name` (default `quic`; 0 when absent; the UDP
port of the reverse tunnel, informational for Pylon), `ports` with every
port of the exported Service (name, protocol, app protocol, number) for
consumers whose protocol GDS does not name, `dial_scheme` from
`gds.nvidia.com/dial-scheme` (`http` when absent or invalid), the cluster's
region and lease state, and one replica per endpoint (first address, ready
flag, hostname, zone) while the cluster lease is active.

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
`include_endpoints`) apply per subscriber: an object that stops matching
arrives as `DELETE` with its new state. A bookmark's revision covers changes
the filter skipped. A draining replica ends watches with `SHUTTING_DOWN`;
reconnect, usually to another replica, which sends a new snapshot. See
`docs/consistency.md` for revisions and resume.

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
| `POOL_NAME_TAKEN` | `ALREADY_EXISTS` | 409 | Another pool of the cluster has the name, or the `pool_id` belongs to another pool. |
| `TOO_MANY_ENDPOINTS` | `INVALID_ARGUMENT` | 400 | The endpoint set exceeds 1000 endpoints. |
| `TOO_MANY_WATCHERS` | `RESOURCE_EXHAUSTED` | 429 | The replica is at `--max-watchers`. Back off or try another replica. |
| `STORE_UNAVAILABLE` | `UNAVAILABLE` | 503 | The store did not answer. Retry with backoff. |
| `SHUTTING_DOWN` | `UNAVAILABLE` | 503 | The replica is draining. Reconnect; another replica serves. |
| `INVALID_ARGUMENT` | `INVALID_ARGUMENT` | 400 | Invalid input. A `google.rpc.BadRequest` detail names every invalid field. |
| `NOT_FOUND` | `NOT_FOUND` | 404 | No such ServiceImport, pool or pool agent registration. |
| `INTERNAL` | `INTERNAL` | 500 | An unexpected server error, logged by the server. |

The Go constants are in `api/errors.go`.

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
| Attributes per pool agent registration | 16 |
| Pool agent id | 1 to 253 bytes |
| Message size | 4 MiB |

The constants are in `api/errors.go`.

## Well-known labels and annotations

Services opt in to GDS through labels and annotations with the
`gds.nvidia.com/` prefix: `export`, `role` (`router`, `pool-frontend`,
`kv-relay`, `pylon`), `pool`, and the annotations `roles` (several pool
roles of one Service, comma-separated), `pool-models`, `hardware-class`,
`dc`, `dial-scheme`, `grpc-port-name`, `http-port-name` and
`quic-port-name`. The constants and
their meaning are in `api/labels.go`; the Rust gds-client crate mirrors them.

Clients dial an import through its derived Service,
`derived-<hash>.<namespace>.svc.cluster.local`, where the name is
`api.DerivedServiceName(namespace, name)`, the same algorithm as
`sigs.k8s.io/mcs-api`.
Router seeds are the exception: exports of one namespace and name from
several clusters share one import, so pylons dial the ready replicas of each
Router record, `<dial_scheme>://<address>:<grpc_port>`, instead of its
derived Service (`pkg/client/README.md`, Seed URLs).
