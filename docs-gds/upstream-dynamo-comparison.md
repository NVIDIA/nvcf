# Comparison with upstream Dynamo discovery work

Status: 2026-10-27. Intermediate discussion note. It compares the open upstream work on pool
discovery for the Dynamo Global Router with the GDS design and implementation on
`feat/global-discovery-service`. It records no decisions; recommendations and open questions
are at the end for the user to decide.

Sources read: the PR descriptions, diffs and review threads listed below (GitHub, fetched
2026-10-27), `ai-dynamo/dynamo` `main` at `df083f8e` (2026-10-27) and the `global-router-v2`
integration branch, cloned to `~/projects/dynamo`.

## 1. What exists upstream

### 1.1 On `main`

Nothing from the Global Router discovery stack has merged to `main`. The relevant code on
`main` is the KV DC Relay (`lib/llm/src/kv_dc_relay`), the component the HLD calls PoolRelay:

- `b84f3a7c` feat(llm): stream serving load from the KV DC Relay (#13187, 2026-10-22): a new
  `SubscribeServingLoad` stream and frontend-load aggregation.
- `909ebee0` fix(relay): replay removals in cached recovery snapshots (#16167, 2026-10-20).
- `5dea6897` chore(codeowners): assign KV DC relay to global-router (#15824, 2026-10-15).
- `395fb8ef` feat: KV DC Relay file based source mode (#14807, 2026-09-16).
- `1921956e` feat(llm): add Relay WAN protobuf transport over universal publisher (#14379,
  2026-09-10).
- `eb300f65` feat(kv-dc-relay): add transport-neutral publication source (#14041, 2026-09-01).

The relay on `main` is a passive gRPC server (`dynamo.kvrelay.v1.KvEventRelay`, plaintext
HTTP/2) with five server streams: `WatchKvPoolCatalog`, `SubscribeKvPool`,
`SubscribeServingReadiness`, `SubscribeKvPoolLoad`, `SubscribeServingLoad`. Routers subscribe
to it; it does not dial routers and does not register with anything. Its own discovery
(`kv_dc_relay/discovery.rs`) is the in-cluster Dynamo runtime discovery of workers, not pool
discovery. This matches what the network comparison note recorded for DEP 11225 (pull
direction).

Also on `main` but outside discovery: `0ce693b3` feat(operator): allow EPP replicas in Dynamo
mode (#14758) and `2756fe33` fix(frontend): TCP_NODELAY (#16862), both by atchernych.

### 1.2 On the `global-router-v2` integration branch

Merge base with `main` is `e4cf7978`. Commits not on `main` (2026-10-20 to 2026-10-26):

- Sachal Malick: `3e9358ca` add Global Discovery Service and shared Global Router types,
  `3dae6f86` Global Frontend forwarding engine, `74c2ec44` route round robin over pools from
  discovery (#17273), `652b39a4` and `35cb00a4` AWS CDK example (#16962, #17260).
- Jacob Ioffe (jioffe502): `c7dfcf70` Global Discovery Service client crate and six fix-ups
  (redirects rejected, Clone and Debug, lease returned from `register_pool`, docs).
- Hannah Zhang: `376e405d` first GlobalViewReceiver, `f44c093a` rename runtime component and
  identities (the PoolRelay rename, PR #17215 on `main`), two fixes.

Crates on that branch: `lib/global-discovery-client`, `lib/global-discovery-service`,
`lib/global-ingress`, `lib/global-router`, `lib/global-router-shared`,
`lib/global-view-receiver`. `examples/global-router/cdk` holds the two-cluster AWS example
(Ohio hub, West member).

`global-ingress` is the Global Frontend binary: it serves the OpenAI API and forwards each
request to a pool frontend. It takes either `--discovery-url` (polls `GET /pools` every
`--discovery-interval-secs`, default 5 s) or static `--pool <site>/<namespace>/<DGD>=<model>@<url>`
flags. It routes round robin over pools, then over a pool's frontends (#17273). A pool counts
as routable whenever the discovery service lists it; gating on live relay state streams is
deferred until a relay-to-router stream transport exists. `global-view-receiver` reconciles
per-source snapshots and defines no transport.

### 1.3 Open pull requests

| PR | Author | State | Base | Scope |
|---|---|---|---|---|
| enhancements #103 | atchernych | draft | main | DEP "Pluggable Pool Discovery for the Global Router" |
| enhancements #99 | atchernych | open | main | Router as a standalone service; no discovery content |
| dynamo #17208 | jioffe502 | open, approved | main | `lib/global-discovery-client` (HTTP client) |
| dynamo #17330 | atchernych | open | main | `lib/global-common` shared types |
| dynamo #17331 | atchernych | open | #17330 | discovery contract, memory and MCS stores |
| dynamo #17371 | atchernych | draft | #17331 | `global-discovery-service` HTTP binary, image, manifest |
| dynamo #17332 | atchernych | draft | #17371 | registrations joined with MCS, peer sharing |
| dynamo #17333 | atchernych | open | #17371 | `POST /routers` reserved (501) |
| dynamo #17412 | atchernych | draft | main | operator creates ServiceExports for pools |
| dynamo #16963 | atchernych | open | global-router-v2 | Karmada and MCS in the CDK example |
| dynamo #17355 | sachalmalick | open | main | empty `lib/global-*` crates and code ownership |
| dynamo #17276 | sachalmalick | open | global-router-v2 | MCS pool models from a routing file (alternative to #17332) |
| dynamo #17278 | sachalmalick | open | branch | deploy and validate discovery-backed global ingress |
| dynamo #15295 | sachalmalick | open | main | Global View pool state, `PoolId` derivation |
| dynamo #17215 | hhzhang16 | open | main | rename KV DC Relay to PoolRelay, `dc_id` to `cluster_id` |
| dynamo #16714, #15933, #15949, #17282, #17283 | | closed | | superseded POCs and predecessors |

PR #99 contains nothing about discovery, registration, pools or MCS; it is about the KvRouter
ownership boundary. It is not discussed further.

### 1.4 Changes since the first pass (checked 2026-10-27, evening)

The stack moved during the day. What changed and what it means for this comparison:

- DEP #103 gained a rewritten per-mode components table (commits 8143c2e and aeba94e). It now
  says pool discovery "moved into a Global Discovery Service" serving every mode over HTTP, that
  in Full Kubernetes mode the PoolRelay both gets a ServiceExport from the operator and calls
  `POST /pools` on a timer, and that "frontend addresses come from MCS". So the DEP has converged
  on the same split as our design: MCS for reachability and liveness, a registry for what MCS
  cannot know. The DEP also states that the state connection between clusters is still TBD:
  "a new push RPC from the Relay, or a reverse tunnel that runs subscriptions over a connection
  Relay opens". Registration direction is therefore open upstream too, which matches the
  neutral stance in design section 4.3 and in `network-interaction-comparison.md`.
- #17330 (shared types): the 24 h lease cap moved out of the shared `Lease` type (8e30f3c);
  the service binary in #17371 now enforces it with `--lease-duration-secs` capped at 86400
  (d0dc8fb). `relay_identity` became `relay_identities: Vec<String>` so a dual-stack relay pod
  lists both addresses (8b6c23e). Mergeable state is `dirty` against main at head c7ee161.
- nnshah1 posted a naming review on #17330 (2026-10-27 17:56) and would hold the PR on the
  first item: `PoolKey.site_id` should be the relay's `cluster_id` from #17215, and
  `PoolLocation.cluster` should go or be documented as the physical cluster. Also:
  `ReplicaEndpoint` to `RouterReplica`, `WatchEvent::Added` to `Upserted`, `Capacity.count` to
  `workers`, and a note that `capabilities = ["aggregated"]` is a serving mode. If accepted,
  the terminology map in section 5 simplifies: `cluster_id` maps one to one onto our
  `Location.cluster` and the source-cluster label, and their `Upserted` matches our `UPSERT`.
- #17332: `--pool-service-suffix` and `--frontend-service-suffix` now accept values starting
  with a hyphen (c506f67). The suffix convention (`-pool-relay`, `-frontend`) is therefore
  configurable on their side, which weakens the case for hard-coding it in our controller
  (recommendation 3 in section 7); read the suffixes from flags if we infer roles at all.
- #17331 review threads (dynamo-review-agent, answered by atchernych): the Kubernetes cache
  waits for the first complete list before `list_and_watch` yields, and the service answers
  503 on `/healthz` and `GET /pools` until then. Staleness after a dead watch is computed
  lazily on snapshot, so existing watchers do not learn their snapshot went `Estimated`.
  Our view handles the same case differently: `view_age_seconds` drives readiness and a
  failed refresh does not change what watchers already hold either. Worth noting in
  `docs/consistency.md` as a shared limitation.
- #17208 (client) merged main twice today and is `blocked` on checks, not reviews. #17412
  (operator ServiceExports) has one commit, no review comments, and is `blocked`.

Nothing in the day's changes alters sections 3 to 8. The open questions stand.

## 2. The upstream design in short

The DEP (#103) frames pool membership behind a `PoolDiscovery` abstraction with modes gRPC
Native (relay calls RegisterPool, in-memory), Hybrid (records and Leases in the hub cluster's
Kubernetes API), Full Kubernetes (operator exports the PoolRelay Service through MCS, Karmada
first, imported into one hub cluster, routers read EndpointSlices) and a static file. Its
addendum, the only part updated after the first draft, says pool discovery moved into a
"Global Discovery Service" with three HTTP routes and names the dynamo PRs above as its
implementation. It explicitly bends the HLD "no external store" rule, treats the hub API
server as a single point of failure for joins, and does not address multiple regions. Replica
readiness ("a new router replica must know which pools to wait for") is its central concern.
The DEP has no review comments.

The code implements that addendum:

- Transport: HTTP/JSON (axum, `lib/http-server` copied from #17178), optional TLS and mTLS.
  Routes `POST /pools` (register, and every call is also the heartbeat; reply is a
  `Lease{heartbeat_interval_s, lease_duration_s}` chosen by the server, defaults 5 s and 15 s,
  max one day), `GET /pools` (`{pools[], complete}`), `GET /routers`
  (`{routers[{replica "<cluster>/<pod>", url}]}`), `GET /healthz`, `POST /routers` (501),
  and with #17332 `GET /registrations` for peer sharing. Unversioned paths, no clusterset, no
  watch, no get, patch or delete.
- Identity: `PoolKey{site_id, namespace, dgd_name}` and `PoolId = "pool-v1-" + blake3`
  derived client side. nnshah1's open review asks that `site_id` become the relay's
  `cluster_id` (#17215 already renames `dc_id` to `cluster_id`).
- Pool record: location `{region, availability_zone?, cluster?, datacenter?}`, one `model`
  string, `capabilities[]`, `frontends[]` URLs, `capacity[{sku, count}]`,
  `relay_identities[]` (`ip:<addr>` of the relay pod, for routers to admit the state stream by
  source address). No state enum; a pool is listed or not, and the list carries a
  `complete` flag (`Exact` or `Estimated`).
- Stores: memory (single replica; each instance keeps only the registrations it received) and
  MCS (`--store karmada|mcs`), which reads EndpointSlices that Karmada or another MCS
  implementation already imported into the cluster the service runs in. Pool = Service named
  `<dgd>-pool-relay`, live while a relay endpoint is Ready; frontends from `<dgd>-frontend`;
  routers = Ready pods of the router Service's imported slices. `location.region` is set to the
  cluster name. Creates no MCS objects. #17332 joins MCS readiness with registrations, and
  makes instances copy each other's registrations over plain HTTP through an MCS-exported
  `--peer-service`, twice per heartbeat; a cluster whose instances are unreachable loses its
  pools in every region within one lease. Its own text names "a registration store that every
  region reads, such as the planned one on DynamoDB global tables" as the fix.
- Operator (#17412): `OperatorConfiguration.multiCluster.mcsImplementation = mcs|karmada`;
  for a DGD annotated `nvidia.com/global-router-pool: "true"` it creates ServiceExport
  `multicluster.x-k8s.io/v1alpha1` for `<dgd>-pool-relay` and `<dgd>-frontend`, owned by the
  DGD. No role, model or hardware metadata on the Service or the export. Karmada name limit
  55 here, 57 in #17331.
- Client (#17208): reqwest, two calls (`routers()`, `register_pool()`), plus `pools()` on the
  v2 branch. No watch, retry, failover, TLS options or metrics. Approved by four reviewers.
- Infrastructure (#16963): Karmada 1.19 control plane on the hub, VPC peering, security group
  ingress for all TCP from peer VPC CIDRs, CNI SNAT exclusion, so routers dial relay pod IPs
  directly. This is the same routable-pod-CIDR prerequisite our design states in 1.6 and 15.

## 3. Side by side

| Topic | Upstream (DEP #103 addendum and PRs) | GDS (this branch) |
|---|---|---|
| Role of the service | Reads what Karmada or an MCS controller imported into one hub; MCS is a dependency | Is the MCS implementation: controller exports to GDS, GDS materialises imports in every member cluster and serves the same view over its API |
| Replicas and store | One replica per cluster with a memory store, or several that poll each other over HTTP | Leader-less stateless replicas per region over one DynamoDB Global Table; memory store for dev |
| Multi-region | Not in the DEP; #17332 peer polling, pools lost if a cluster's instances are unreachable | Core requirement: replicated table, regional endpoints, Route53 global name with fallbacks, resumable watches |
| Transport | REST JSON, snake_case, optional TLS and mTLS | gRPC plus grpc-gateway REST (proto3 JSON, lowerCamelCase); TLS supported by clients, no auth in v1 |
| Paths | `/pools`, `/routers`, `/registrations`, `/healthz` | `/v1/clustersets/{cs}/pools[/{id}]`, `:heartbeat`, `:watch`, `/routers`, `/clusters`, `/exports`, `/imports`, `/v1/serverinfo` |
| Pool identity | `PoolKey{site_id, namespace, dgd_name}`; `pool-v1-<blake3>` derived by the client | `(cluster, name)`; server-assigned UUIDv5 `pool_id` unless given; idempotent register |
| Registration and heartbeat | `POST /pools` is both; server dictates interval and duration; no epoch | `RegisterPool` mints a lease epoch; `HeartbeatPool(epoch)`; `UpdatePool` with field mask; `DeregisterPool`; client proposes ttl (10 s to 5 m) |
| Liveness | Memory: lease expiry removes the pool. MCS: Ready relay endpoint. #17332: both must agree | Cluster lease (30 s) and ready endpoints for exported pools; pool lease only for API-only pools; read-time expiry with 2 s skew; UNHEALTHY instead of removal, GONE on deregister |
| Pool fields | one `model`, `capabilities[]`, `frontends[]` URLs, `capacity[{sku,count}]`, `relay_identities[]`, region = cluster name | `models[]{model_id, aliases, attributes}`, `hardware_class`, worker counts, `frontends[]{role OPENAI|KV_RELAY, url or service ref, port_name}`, `services[]`, labels, real region and dc, `endpoints[]`, `ready_endpoint_count` |
| States | None; `complete` flag on lists | REGISTERED, ACTIVE, DRAINING, UNHEALTHY, GONE plus `desired_state`; DRAINING-then-remove for blue/green |
| Watch | Internal traits only; HTTP clients poll (5 s) | Server streams with SNAPSHOT_START/END, UPSERT, DELETE, BOOKMARK and opaque resume |
| Routers | Ready pods of the router Service's imported slices as `http://ip:port`; `POST /routers` reserved | Routers derived from exports labelled `gds.nvidia.com/role=router`, per-replica addresses, ports, dial scheme; no router registration either |
| ServiceExport creator | Dynamo operator from a DGD annotation, `v1alpha1`, name suffixes `-pool-relay` and `-frontend` | gds-mcs-controller from Service label `gds.nvidia.com/export=true` or explicit export, `v1beta1`; also for API-registered pools |
| ServiceImport creator | Karmada or an external MCS controller, hub only | gds-mcs-controller in every member: ServiceImport, derived Service, standard-labelled EndpointSlices |
| Pool metadata source | Only the relay's `POST /pools` | Service labels and annotations, plus the API overlay |
| Relay admission | `relay_identities[]` for source-address matching | Not modelled; `endpoints[]` of the kv-relay export carry the addresses |
| Registration direction | Relay dials out: registers and (per DEP) dials every router replica; relay on `main` is still pull-only | Neutral: pull per DEP 11225 and push per the Pylon pattern both supported |
| Readiness problem | Central concern of the DEP | Any replica lists the full set from the store; snapshots end with SNAPSHOT_END |
| Deployment | One Deployment on the hub, RBAC read EndpointSlices | Server chart per region (NLB, IRSA, PDB, HPA), controller chart per member |
| Clients | Rust REST client, polling | Rust `gds-client` and Go `pkg/client`, watch-based caches, failover, TLS, metrics |
| Tests | Unit tests, not run on a cluster | Unit, envtest, three-phase k3d e2e on real clusters |
| Repo | `ai-dynamo/dynamo` `lib/global-*` | nvcf monorepo, to port to `ai-dynamo/stargate` |

## 4. Compatibility

Their client against our server: not as is. The paths could be aligned with a base URL of
`/v1/clustersets/<cs>`, but the bodies differ (`PoolRegistration` versus
`RegisterPoolRequest{pool, lease_ttl}`, lease reply shape, `{routers[{replica,url}]}` versus
`Router{replicas[]}`), and the semantics differ (heartbeat by re-POST versus `:heartbeat` with
an epoch; no `pool_id`; no generation). Every field they need is derivable from our Pool and
Router records, so a small compatibility surface in gds-server serving their JSON for a
configured default clusterset would let #17208's client, `global-ingress` and a future
PoolRelay talk to GDS unchanged.

Their operator exports into our MCS layer: partly. Our controller forwards any explicit
ServiceExport, so the two exports would become ServiceImports everywhere. Blockers: #17412
writes `v1alpha1` while our controller and the mcs-api v0.5.2 CRDs serve `v1beta1`; and the
Services carry no `gds.nvidia.com/role` or `gds.nvidia.com/pool` labels, so no Pool is
derived. Either our controller infers roles from the `-pool-relay` and `-frontend` suffixes,
or the operator adds three labels.

Their service on our MCS layer: yes in principle. Our EndpointSlices carry the standard
`multicluster.kubernetes.io/*` labels that their `--store mcs` dialect reads, so a hub running
our controller and importing the relay, frontend and router Services would feed their service
without Karmada. This is the cheapest interoperability point today.

Routers: both sides derive replicas from the exported router Service and neither registers
routers. Mapping ours to theirs is lossless.

Identity: their `pool-v1` id is a deterministic hash of (site_id, namespace, dgd_name); ours is
a UUID keyed by (cluster, name). The coordinates map (`site_id` = `cluster`, `dgd_name` =
`name`, namespace from `services[]`), the ids do not.

## 5. Terminology map

| Upstream | GDS |
|---|---|
| Global Discovery Service (`lib/global-discovery-service`, HTTP) | GDS (`gds-server`, gRPC and REST) |
| Hub cluster | None; the regional GDS endpoint; every cluster is a member |
| `site_id` (to become `cluster_id`) | `Location.cluster`, `ClusterKey.cluster`, source-cluster label |
| `dgd_name`, `namespace` | `Pool.name`, `services[].namespace`; label `gds.nvidia.com/pool` |
| `PoolId` `pool-v1-<blake3>` | `pool_id` (UUIDv5) |
| `PoolLocation{region, availability_zone, cluster, datacenter}` | `Location{region, zones[], cluster, dc}` |
| `model` | `models[].model_id` |
| `capabilities[]` | none; closest `ModelServed.attributes` or labels |
| `capacity[{sku,count}]` | `hardware_class` plus `worker_count` and `ready_worker_count` |
| `frontends[]` URLs | `frontends[]{role OPENAI, url}` |
| `relay_identities[]` | none; `endpoints[]` of the kv-relay export |
| `Lease{heartbeat_interval_s, lease_duration_s}` | `Lease{epoch, ttl, renewed_at, expires_at, state}`; heartbeat at ttl/3 is a client convention |
| `POST /pools` | `RegisterPool` plus `HeartbeatPool` |
| `GET /pools` with `complete` | `ListPools` with `revision`; `WatchPools` |
| `GET /routers` `ReplicaEndpoint{replica, url}` | `ListRouters` `Router{replicas[]}`; `router_seed_urls` |
| `WatchEvent::Added/Removed/Resync`, `Completeness` | `UPSERT`, `DELETE`, `SNAPSHOT_START..SNAPSHOT_END`, `BOOKMARK` |
| `PoolDirectory`, `ReplicaDirectory`, `PoolRegistrar` | server `internal/view` and handlers; client `PoolCache`, `RouterCache`, `PoolRegistration` |
| `--store karmada|mcs` on the hub | gds-mcs-controller in every cluster |
| `<dgd>-pool-relay` Service | export with `gds.nvidia.com/role=kv-relay` |
| `<dgd>-frontend` Service | export with `gds.nvidia.com/role=pool-frontend` |
| DGD annotation `nvidia.com/global-router-pool` | Service labels `gds.nvidia.com/export`, `role`, `pool` |
| PoolRelay (KV DC Relay, #17215) | PoolRelay, KV DC Relay (`role=kv-relay`) |
| Global Frontend (`global-ingress`) | Global Router (HLD), Stargate in the LLM stack |
| `ClusterProfile` (optional location source) | not used; Cluster record from node labels or flags |

## 6. Assessment

What upstream has that GDS lacks:

- `relay_identities` and the `ip:<addr>` admission convention, `capabilities[]`,
  `capacity[]` per SKU, a `complete` flag on lists, a deterministic pool id, Karmada slice
  naming, ClusterProfile as a location source, a DGD-level switch in the Dynamo operator, and
  a routability rule that includes "first state received on the relay stream" and the DEP 11225
  readiness gate.
- A Global Frontend binary that already consumes a discovery service, and a relay-side client.

What GDS has that upstream lacks:

- Multi-region replication and failover, multi-replica service without peer polling, watches
  with resume, heartbeat with epochs and generations, pool states including DRAINING for
  blue/green (FR7), cluster records and leases, label-driven exports with metadata, the MCS
  controller itself (no Karmada or external MCS implementation needed, `v1beta1`), derived
  Service and per-replica router addressing, Go and Rust clients with failover and caches, charts
  for both components, and an end-to-end test on real clusters.
- An answer to the DEP's own open problems: hub API server down (there is no hub), replica
  readiness (full snapshot from the store), and the comparison table's empty "best" column.

Where the two contradict:

- Two services named Global Discovery Service with incompatible REST contracts and pool ids.
- Upstream models pool liveness as "listed or not"; GDS keeps records and derives states.
- Upstream leans on dial-out registration with the relay dialing every router; the relay on
  `main` is still pull-only, and GDS stays neutral.
- `v1alpha1` versus `v1beta1` MCS API; hub-centric imports versus importer in every cluster.
- `location.region` means cluster name upstream and a real region in GDS.

## 7. Recommendations

Small, compatible changes on our side:

1. Add optional Pool fields `capabilities[]`, `capacity[]{sku, count}` and
   `relay_identities[]` (or document the annotation keys), and record `namespace` on the Pool.
2. Add a `complete` indicator to list responses (always true for us).
3. Let gds-mcs-controller infer `kv-relay` and `pool-frontend` roles from the `-pool-relay`
   and `-frontend` suffixes when the pool label is absent, so a DGD exported by #17412 appears
   as a Pool.
4. Offer a compatibility HTTP surface (`POST /pools` with their lease reply and
   register-or-heartbeat semantics, `GET /pools`, `GET /routers`, `GET /healthz`) for a default
   clusterset, so `global-ingress`, #17208 and a future PoolRelay work unchanged and DynamoDB
   becomes the shared registration store that #17332 asks for.
5. Document their routability rule as a router-side layer on ACTIVE in design 4.6, and the pool
   Service naming convention next to our labels.

To raise upstream:

- #17412 should target ServiceExport `v1beta1` (or both); `v1alpha1` cannot coexist with
  current mcs-api CRDs or our controller. The Karmada limit is 55 in #17412 and 57 in #17331.
- #17331 sets `location.region` to the cluster name; take the region from the registration or
  a cluster label.
- Our controller is a standard MCS implementation selectable with `--store mcs`, removing the
  Karmada dependency, and DynamoDB-backed GDS is the multi-region registration store #17332
  names, replacing plain-HTTP peer polling.
- Naming threads already open: `site_id` versus `cluster_id`, `ReplicaEndpoint` versus
  `RouterReplica` (our proto already uses `RouterReplica` and `cluster`), `Added` versus
  `Upserted`. Weighing in now avoids a second rename.
- The DEP should list multi-region, hub failure, draining and blue/green as open questions.

## 8. Open questions for the user

1. Should the Dynamo Global Router (`global-ingress`) consume GDS directly, or should GDS only
   supply the MCS layer and store beneath their service? Recommendation 4 is only worth
   building for the first.
2. Which contract wins for the PoolRelay: their HTTP `POST /pools` and `GET /routers`, or our
   gRPC `PoolService`? If ours, #17208's client is replaced by `gds-client` when the code is
   ported to `ai-dynamo/stargate`.
3. Adopt their `PoolKey` and deterministic `pool-v1` id (changes our DynamoDB `PL#` keys and
   idempotency rule), or propose UUIDv5 upstream?
4. Server-dictated lease (their reply) or client-proposed ttl with epochs (ours)? Reviewers
   upstream were firm on the server returning the lease.
5. Should GDS model relay admission (`relay_identities`) or stay strictly membership?
6. Should our design keep registration direction neutral, given the DEP chooses dial-out and
   the relay on `main` is pull-only?
7. Suffix inference in our controller, or labels added to the operator upstream?
8. Who engages on the DEP and the PR threads, and when: now, or after the port to
   `ai-dynamo/stargate` gives a public reference?
