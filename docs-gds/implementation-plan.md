# Global Discovery Service (GDS): design and implementation plan

## Context

The Dynamo Global Router HLD (Kiro edition, section 2.4 "Discovery") requires that every Global
Router replica in every region knows about every Pool globally, and that pools join and leave
without redeploying routers (FR7, blue/green pools). The HLD rejects MCS + AWS Cloud Map on fault
tolerance grounds (Cloud Map lives in one region) and lists "Custom MCS controller with DynamoDB
Global Tables" as the alternative. This plan builds that alternative as the Global Discovery
Service: a standalone, multi-region Go service in front of a pluggable store (DynamoDB Global
Tables first, in-memory for dev/test) that is itself the discovery backend. Consumers reach it
from any cluster or VPC: an in-cluster MCS controller materialises the Kubernetes Multi-Cluster
Services API (ServiceExport/ServiceImport/EndpointSlices) from it, and typed Rust and Go client
libraries give Stargate (LLM request router = Global Router), Pylon, Pylon Operator and the Dynamo
PoolRelay direct access to the pool and router registries. No Cloud Map or managed provider.

Sources: Global Router HLD 2.4 + "LLD: The Global View" (Register/Update/Get Pools with filter);
Kiro task breakdown "Global Discovery Service" workstreams (Karmada item ignored); KEP-1645;
sigs.k8s.io/mcs-api v0.5.2; AWS Cloud Map MCS controller and Cilium mcsapi as references;
nvcf LLM stack code on feat/inference-in-a-box; Dynamo main (kv_dc_relay).

## Decisions (confirmed with the user)

| Topic | Decision |
|---|---|
| Location | nvcf monorepo. New worktree `~/projects/nvidia/nvcf/nvcf-2-gds`, branch `feat/global-discovery-service` from `feat/inference-in-a-box`. Self-contained so it ports as a unit to `github.com/ai-dynamo/stargate` later. |
| Layout | Go module `github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery` (server, controller, Go client, proto + generated code). Rust crate `src/libraries/rust/stargate/crates/gds-client`. Charts `deploy/helm/global-discovery` and `deploy/helm/global-discovery-mcs-controller`. E2e `tests/e2e/global-discovery-k3d`. |
| Server | Go. gRPC + REST (grpc-gateway v2). buf lint/breaking. Generated code checked in. |
| Clients | Rust crate and Go module in v1. Python later. |
| Store | `Store` interface; DynamoDB Global Tables (single table) and in-memory. |
| Bootstrap | Global Route53 latency/health-checked private name is primary; optional ordered fallback URLs; clients re-resolve DNS on every reconnect, fall back only after the primary fails. `GetServerInfo` also advertises per-region fallback URLs. |
| Auth | None in v1. No-op `Authenticator` interceptor seam only. |
| Export creation | Label-driven primary: a Service labelled `gds.nvidia.com/export=true` is auto-exported by the controller (it creates and owns the ServiceExport). Explicit ServiceExport objects keep working. Role and metadata ride as Service labels/annotations that the controller forwards. |
| Routers | Derived from the exported Stargate backend-router Service (label `gds.nvidia.com/role=router`). No Stargate write path. Pylon seeds are per cluster: for each Router record (cluster, namespace, name) the clients return `<scheme>://<replica address>:<grpc_port>` for every ready replica (the k8s-router or Stargate pod IPs of that cluster, IPv6 bracketed, scheme from `dial_scheme`, default https), skipping clusters whose lease expired; `WatchStargates` on a seed yields per-pod authorities and the k8s-router of that cluster forwards by authority/SNI. Not the derived VIP: every cluster exports the same router Service name, the exports merge into one import, and its VIP spans the k8s-routers of all clusters (revised in ws4b, design 4.5). The derived VIP stays the path for pool frontends. |
| Pools | Derived from labelled exports (`gds.nvidia.com/role=pool-frontend|kv-relay`, `gds.nvidia.com/pool=<name>`) with the Pool API as an overlay keyed by (cluster, pool name) for dynamic state (worker counts, DRAINING) and for non-Kubernetes pools. Both paths converge on the same ServiceExport. |
| Pool HA | Operator's choice via Service type: ClusterIP exports as ClusterSetIP (one VIP per importing cluster), headless exports as Headless (per-replica endpoints). GDS returns both; consumers pick. |
| DNS | CoreDNS multicluster plugin optional. Clients compute the deterministic derived Service name locally; `clusterset.local` names are documented as an add-on. |
| Namespace sameness | Importer waits for a missing namespace (pending, metric); `--create-namespaces` opts into creation. |
| Watch resume | Opaque revision `view_epoch:seq`; unknown or stale token yields a fresh snapshot, never an error. |
| Scope of this pass | All four: core server; MCS controller + Go client; Rust crate; Stargate/Pylon/Pylon Operator integration. |

## Architecture

```
 region A (GDS VPC)                              region B (GDS VPC)
 NLB -> gds-server x3 -> DynamoDB replica  <==replication==>  DynamoDB replica <- gds-server x3 <- NLB
        ^   ^                                                          ^
 Route53 gds.<env>.internal (latency + health check), gds.<region>.<env>.internal
        |   |                                                          |
 cluster 1 (EKS, VPC peered)                 cluster 2                cluster 3
  gds-mcs-controller --export--> GDS         gds-mcs-controller       gds-mcs-controller
  ServiceExport+EndpointSlices               <--watch imports--       ...
  ServiceImport + derived Service + mirrored EndpointSlices (multicluster.kubernetes.io/*)
  Stargate (Rust, gds-client: routers -> remote watch URLs)
  Pylon (Rust, gds-client: routers -> seeds)   Pylon Operator (Go client, --router-source=gds)
  PoolRelay (Rust, gds-client PoolRegistration; integration lands in Dynamo later)
```

Topology facts the design relies on (verified in the chart and code):
- Router: one headless Service for all replicas (`llm-request-router-headless`,
  `publishNotReadyAddresses`), one ClusterIP Service (http, grpc), and for multi-replica
  Deployments a `backend-router` Service (`sessionAffinity: ClientIP`) in front of
  `stargate-k8s-router` pods that forward registration gRPC by HTTP/2 authority and QUIC by SNI to
  the stargate pod. `WatchStargates` publishes per-pod hostnames from
  `--advertised-hostname-template` plus `grpc_pylon_dial_addr`; Pylon dials the dial address and
  sets the per-pod hostname as authority/SNI, never resolving it. So Pylon needs one dialable
  address per router cluster; per-replica identities come from `WatchStargates`.
- Pylon: transport Deployment with N pods, no Service; each pod registers its own
  `inference_server_id` and opens stream + QUIC tunnel to every router replica. HA = N
  independent registrations. A Service in front of Pylon only matters in direct mode (unused).

Data flows:

```
EXPORT (cluster A)                              GDS (nearest region)                 IMPORT (cluster B)
Service + EndpointSlices                        gds-server                           gds-mcs-controller(B)
  label gds.nvidia.com/export=true                |                                    |
  v                                               |                                    |
gds-mcs-controller(A)                             |                                    |
  ServiceExport (auto or explicit) --PutServiceExport--> store write + ApplyLocal       |
  EndpointSlices -----------ReplaceEndpoints(gen)--> DynamoDB(A) ==replicate==> DynamoDB(B)
  HeartbeatCluster every ttl/3 ------------------>  lease                               |
                                                  view refresh 2 s, merge (oldest wins), lease eval
                                                  hub ---WatchServiceImports stream---> importer
                                                                                        v
                                                     ServiceImport + derived-<hash> Service + EndpointSlices
                                                                                        v
                                                     kube-proxy VIP -> remote pod IPs over VPC peering
ROUTER PATH: A's backend-router Service (role=router) exported -> GDS Routers view
             -> pylon in B (RouterCache): seeds = https://<replica address>:50071 per ready replica of A
             -> WatchStargates on each seed -> per-pod authorities -> N streams + N QUIC tunnels
             -> k8s-router(A) forwards to the stargate pod
POOL PATH:   pool frontend / relay Service (role=pool-frontend|kv-relay, gds.nvidia.com/pool=<name>)
             -> GDS Pools view (static metadata from labels; liveness = cluster lease + ready endpoints)
             -> optional UpdatePool/HeartbeatPool overlay (worker counts, DRAINING)
             -> Global Router (PoolCache watch) -> dials frontend via derived VIP or per-replica endpoints
```

Data model has two layers, both keyed by `clusterset`:
- MCS layer: Cluster (lease), ServiceExport record per (cluster, namespace, name) with a Cilium
  shaped spec, EndpointSet per export (full replace with writer generation), merged ServiceImport
  view computed server side (oldest export wins).
- Pool layer: Pool (location, hardware, models, frontends, `services[]` refs to exported
  Services for the endpoint join, desired/derived state, optional lease). A Pool record exists
  when either a labelled export names it (`gds.nvidia.com/pool`) or the API registered it; the
  API overlay is merged by (cluster, pool name). Routers derived from imports labelled
  `gds.nvidia.com/role=router`, one Router per (cluster, namespace, name) with its replicas.

Well-known labels and annotations (`api/labels.go`, mirrored in the Rust crate):
`gds.nvidia.com/export` ("true"), `gds.nvidia.com/role` (router | pool-frontend | kv-relay),
`gds.nvidia.com/pool` (pool name), annotations `gds.nvidia.com/pool-models` (comma list),
`gds.nvidia.com/hardware-class`, `gds.nvidia.com/dc`, `gds.nvidia.com/dial-scheme` (http | https),
`gds.nvidia.com/grpc-port-name` (default `grpc`), `gds.nvidia.com/http-port-name` (default `http`).

Requirement mapping: instantaneous state = write-through to the handling replica's view, siblings
within one refresh tick (2 s), other regions after Global Table replication + one tick (target
under 5 s); at-will participation = register/deregister with no provisioning, leases hide silent
departures; descriptive metadata = pool and cluster fields plus label selectors, answered from the
in-memory view; cross-region = stateless replicas in every region over one replicated table.

## API contract (proto package `nvidia.gds.v1`)

Files `api/proto/nvidia/gds/v1/{common,cluster,mcs,pool,server}.proto`, vendored
`api/proto/google/api/{annotations,http}.proto`. `go_package` =
`.../global-discovery/api/gen/go/nvidia/gds/v1;gdsv1`. Generated Go, grpc-gateway and
`gds.swagger.json` checked in under `api/gen/`, produced by `//go:generate buf generate`
(`api/gen.go`), guarded by `tools/ci/check-go-codegen`.

common.proto: `ClusterKey`, `ServiceKey`, `ExportKey`, `PoolKey`; `Lease{epoch, ttl, renewed_at,
expires_at, state ACTIVE|EXPIRED}`; `Condition{type,status,reason,message,last_transition_at}`;
`LabelSelector{match_labels}`; `EventType{SNAPSHOT_START, UPSERT, DELETE, SNAPSHOT_END, BOOKMARK}`.

cluster.proto `ClusterService`: RegisterCluster (PUT `/v1/clustersets/{cs}/clusters/{c}`, mints
`lease.epoch`), HeartbeatCluster (`:heartbeat`, needs current epoch, else ABORTED
`LEASE_EPOCH_MISMATCH`), DeregisterCluster (cascades everything the cluster owns), GetCluster,
ListClusters, WatchClusters, ListClusterSets. `Cluster{clusterset, cluster, region, zones,
provider, labels, agent_id, agent_version, lease, generation, registered_at, updated_at}`. TTL
default 30 s (min 10 s, max 5 m), heartbeat at ttl/3. Expiry evaluated at read time
(`now > expires_at + skew 2 s`); on expiry the cluster's endpoints disappear from every view,
its exports are kept but excluded from conflict resolution, its pools become UNHEALTHY; retained
`--retention` 24 h then compacted.

mcs.proto. `ServiceExportService` (writers): PutServiceExport (PUT
`.../clusters/{c}/exports/{ns}/{name}`, generation must exceed stored), DeleteServiceExport
(cascades endpoint set), ListServiceExports (controller resync), ReplaceEndpoints (PUT
`.../exports/{ns}/{name}/endpoints`, full-set replace, generation monotonic;
FAILED_PRECONDITION `STALE_GENERATION` with `PreconditionFailure.current_generation`).
`ServiceExportSpec{annotations, labels, export_creation_timestamp, ports[], type
CLUSTER_SET_IP|HEADLESS, session_affinity, session_affinity_config, ip_families[],
internal_traffic_policy, traffic_distribution}`; `Endpoint{addresses[], address_type,
conditions{ready,serving,terminating}, hostname, node_name, zone, ports[], attributes}`.
`ServiceImportService` (readers): GetServiceImport, ListServiceImports, WatchServiceImports
(`include_endpoints`, `namespace`, `selector`, `resume_revision`). `ServiceImport{type, ports,
ip_families, session_affinity(_config), annotations, labels (from winner), conflict{reason,
message, winning_cluster}, clusters[]{cluster, region, lease_state, participating, conflict,
endpoint counts, export_generation}, endpoints[]{cluster, region, endpoints[], generation}}` plus
per-cluster `ServiceExportStatus.conditions` (Valid/Ready/Conflict with mcs-api reasons
`NoConflicts, PortConflict, TypeConflict, SessionAffinityConflict, SessionAffinityConfigConflict,
AnnotationsConflict, LabelsConflict, InternalTrafficPolicyConflict, TrafficDistributionConflict,
IPFamilyConflict`, plus `ClusterLeaseExpired`). Merge rules ported from Cilium
(`orderSvcExportByPriority`, `mergePorts`, `intersectIPFamilies`, `checkConflictExport`).

pool.proto `PoolService`: RegisterPool (POST `/v1/clustersets/{cs}/pools`, idempotent on
(cluster, name), server-assigned `pool_id` unless given), UpdatePool (PATCH with FieldMask),
HeartbeatPool (`:heartbeat`, epoch, optional worker counts and `desired_state`), DeregisterPool,
GetPool, ListPools (filters region, dc, cluster, hardware_class, model, states, selector,
`include_endpoints`), WatchPools, ListRouters (+ WatchRouters). `Pool{clusterset, pool_id, name,
location{region, dc, cluster, zones}, hardware_class, models[]{model_id, aliases, attributes},
worker_count, ready_worker_count, frontends[]{role, url, service, port_name}, services[]{namespace,
name, role}, labels, desired_state ACTIVE|DRAINING, state REGISTERED|ACTIVE|DRAINING|UNHEALTHY|GONE
(derived), lease, generation, owner_id, endpoints[] (joined from imports of location.cluster)}`.
Pool lease default 60 s, heartbeat 20 s. `Router{cluster, region, namespace, name, type, cluster_lease_state, grpc_port, http_port,
dial_scheme, replicas[]{address, address_type, ready, hostname, zone}, labels, annotations}`;
the seed addresses are computed by the client from the ready replicas,
`<dial_scheme>://<address>:<grpc_port>`, never from the shared derived Service of the import.
Pools derived from exports: `state` is ACTIVE while the cluster lease is active and at least
one endpoint is ready, UNHEALTHY otherwise; when the API overlay exists its `desired_state`
(DRAINING) and worker counts apply, and a pool lease is only used for API-only pools
(`services[]` empty and no labelled export).

server.proto `ServerInfoService.GetServerInfo` -> `{region, version, view_epoch, endpoints[]{region,
grpc_url, http_url, primary}, server_time}` from `--advertise-endpoints`.

Watch semantics: SNAPSHOT_START, UPSERT per object, SNAPSHOT_END(revision), then deltas;
BOOKMARK every 5 s (keeps NLB 350 s idle timeout and REST chunked streams alive). Opaque
revision `view_epoch:seq`, ring buffer `--watch-history` 10000 per clusterset; resume replays if
same epoch and in buffer, else new snapshot. grpc-gateway streams newline-delimited JSON
`{"result":{...}}`. Errors are `google.rpc.Status` with `ErrorInfo{domain: gds.nvidia.com,
reason}`; reasons `STALE_GENERATION, LEASE_EPOCH_MISMATCH, CLUSTER_NOT_REGISTERED,
POOL_NAME_TAKEN, TOO_MANY_ENDPOINTS, TOO_MANY_WATCHERS, STORE_UNAVAILABLE, SHUTTING_DOWN`. Limits:
DNS-1123 identifiers, labels 64, annotations 32 / 16 KiB, ports 64, endpoints per export 1000,
attributes 16, models 256, 4 MiB messages. Well-known labels in `api/labels.go` (mirrored in
Rust): `gds.nvidia.com/role` (router | pool-frontend | kv-relay), `gds.nvidia.com/pool`.

## Server (`cmd/gds-server`, Go)

Packages: `internal/config` (stdlib flag + `GDS_` env fallback, Validate), `internal/server`
(gRPC `:9090` h2c, grpc-gateway `:8080` dialling loopback gRPC so streams work, admin `:9091` with
`/metrics /healthz /readyz /debug/pprof`; interceptors: recovery, logging, prometheus, otelgrpc,
auth), `internal/auth` (Authenticator/Principal, `NoopAuthenticator`), `internal/api`
(thin handlers + `validate.go` + `errors.go`), `internal/registry` (domain types, lease math, pool
state derivation) and `internal/registry/merge` (Cilium port), `internal/view` (per-clusterset
materialised view with indexes model->pools, label->imports; `refresher.go` ticks every
`--refresh-interval` 2 s calling `LoadClusterSet`, diffs, re-evaluates leases every 1 s, write
through `ApplyLocal` after each successful store write; `hub.go` subscribers, ring buffer,
snapshot streaming, per-watcher queue 1024 with lagged-resync instead of disconnect,
`--max-watchers` 5000; `compaction.go` idempotent retention cleanup), `internal/store` (see
below), `internal/metrics` (`nvcf_gds_*`: clusters/exports/endpoints/service_imports/pools gauges,
lease_expirations_total, watchers, watch_events_sent_total, watch_resync_total, view_refresh_*,
view_age_seconds, store_request_duration_seconds, store_conditional_failures_total,
compaction_deleted_total, build_info; plus go-grpc-middleware prometheus), `internal/version`
(x_defs). Logging zap JSON; tracing OTel OTLP (`--otel-enabled`, standard OTEL_* env). Leader-less:
every replica refreshes, serves watches, compacts (conditional deletes). Readiness: store Ping,
one refresh per known clusterset, view age < 3 ticks; shutdown: not-ready, health NOT_SERVING,
drain 5 s, close streams with UNAVAILABLE `SHUTTING_DOWN`, GracefulStop 25 s
(`terminationGracePeriodSeconds: 40`). `cmd/gds-admin`: `create-table`, `describe`,
`delete-cluster` using the same `schema.go`.

## Storage

`internal/store/store.go`: per-aggregate interfaces composed into `Store` (ClusterStore:
PutCluster/RenewClusterLease/GetCluster/DeleteCluster; ExportStore: PutExport/DeleteExport;
EndpointStore: PutEndpointSet/DeleteEndpointSet; PoolStore: PutPool/RenewPoolLease/DeletePool;
plus ListClusterSets, LoadClusterSet -> ClusterSetSnapshot, Ping, Close). Sentinels `ErrNotFound,
ErrLeaseEpochMismatch, ErrPoolNameTaken, ErrTooLarge, ErrUnavailable, ErrStaleGeneration{Current}`.
Optional `ChangeHinter` seam declared, unimplemented (DynamoDB Streams extension point).

DynamoDB single table `gds` (Global Table v2019.11.21, on-demand, PITR, TTL attr `expires_at`,
one LSI `by_updated_at` KEYS_ONLY created now for a later incremental refresh, unused in v1):

| Item | pk | sk |
|---|---|---|
| Clusterset index | `CSX` | `CS#<clusterset>` |
| Cluster | `CS#<clusterset>` | `CL#<cluster>` |
| ServiceExport | `CS#<clusterset>` | `EX#<cluster>#<namespace>#<name>` |
| Pool | `CS#<clusterset>` | `PL#<cluster>#<pool_id>` |
| Pool name index | `CS#<clusterset>` | `PN#<cluster>#<name>` |
| EndpointSet | `EP#<clusterset>#<cluster>` | `<namespace>#<name>` |

`body` is a serialized protobuf blob; small top-level attrs (`gen`, `lease_epoch`,
`lease_renewed_at`, `owner_id`, `updated_at`, `expires_at`) for conditions. Conditional writes:
registration `attribute_not_exists(pk) OR lease_epoch < :new`; heartbeat `lease_epoch = :epoch`;
puts `attribute_not_exists(pk) OR gen < :gen` with `ReturnValuesOnConditionCheckFailure=ALL_OLD`;
pool name via `TransactWriteItems`; cascade delete via prefix queries + `BatchWriteItem`.
`LoadClusterSet` = Query `CS#x` + parallel Query `EP#x#<cluster>` (eventually consistent, paginated).
Cross-region: single logical writer per item with monotonic `generation = max(prev+1, unix_ms)`
(enforced by both clients) makes Global Table LWW agree with generation order; watchers failing
over to another region get a fresh snapshot (different view_epoch). In-memory store
`internal/store/memory`; conformance suite `internal/store/storetest.Run` runs against memory and
`amazon/dynamodb-local` via testcontainers (Bazel tags `external`, `requires-docker`, `local`,
`env_inherit` as in `src/invocation-plane-services/grpc-proxy/proxy/geo/BUILD.bazel`).

## MCS controller (`cmd/gds-mcs-controller`, Go, controller-runtime)

Packages under `internal/mcs/`: `config`, `identity`, `export`, `importer`, `lease`, `poolexport`,
`metrics`, `testing/fakegds`, `integration` (envtest). Scheme: core, discovery/v1,
`sigs.k8s.io/mcs-api/pkg/apis/v1beta1`, unstructured `about.k8s.io/v1alpha1 ClusterProperty`.
Names: finalizer `gds.nvidia.com/service-export`; labels `gds.nvidia.com/managed-by=
gds-mcs-controller`, `gds.nvidia.com/clusterset`, `gds.nvidia.com/pool-id`; EndpointSlice
`endpointslice.kubernetes.io/managed-by=gds-mcs-controller.gds.nvidia.com`; KEP labels
`multicluster.kubernetes.io/service-name`, `multicluster.kubernetes.io/source-cluster`,
`kubernetes.io/service-name=<derived>`, annotation `multicluster.kubernetes.io/derived-service`;
leader lease `gds-mcs-controller.gds.nvidia.com`; metrics `nvcf_gds_mcs_controller_*`.

- Identity (`identity.Resolve`): flags `--clusterset-id/--cluster-id` win, else ClusterProperty
  `clusterset.k8s.io` / `cluster.clusterset.k8s.io`; region from `--region` or node
  `topology.kubernetes.io/region`; zones from nodes; `--write-cluster-properties` opt-in.
- Lease runnable: RegisterCluster then HeartbeatCluster at ttl/3; `ABORTED`/`NOT_FOUND`
  re-registers and triggers full export resend; `--deregister-on-shutdown` default false.
- Label-driven export reconciler (`export/autoexport.go`): watches Services with
  `gds.nvidia.com/export=true`; creates a ServiceExport of the same name owned by the Service
  (owner ref, label `gds.nvidia.com/managed-by`), deletes it when the label is removed; never
  touches ServiceExports it did not create. Also ensures exports for API-registered pools of its
  own cluster (`services[]`), labelled `gds.nvidia.com/pool-id` (replaces the separate
  `--auto-export-pools` runnable; the flag remains to disable the pool half).
- ServiceExport reconciler: For ServiceExport; Watches Service (same name), EndpointSlices by
  `kubernetes.io/service-name` (excluding own managed slices), and a channel source fed by the
  importer with own-cluster conditions. Steps: deletion -> DeleteServiceExport + remove finalizer;
  ensure finalizer; missing Service -> `Valid=False/NoService`; ExternalName ->
  `Valid=False/InvalidServiceType`; headless if `clusterIP: None` else CLUSTER_SET_IP;
  PutServiceExport when spec hash changed (`export_creation_timestamp` = ServiceExport
  creationTimestamp; the Service's `gds.nvidia.com/*` labels and annotations are forwarded as
  exported labels/annotations in addition to `spec.exportedLabels/Annotations`, so role and pool
  metadata reach GDS); ReplaceEndpoints when
  endpoint hash changed with `generation = max(prev+1, unix_ms)`, on STALE_GENERATION retry with
  `current+1`; status patch Valid (local) + Ready/Conflict (copied). Startup GC: ListServiceExports
  for own cluster, delete records without a local ServiceExport; `OnReconnect` clears sent hashes.
- Importer runnable (leader only): WatchServiceImports with `include_endpoints`; SNAPSHOT_START
  clears local snapshot map, UPSERT/DELETE apply, SNAPSHOT_END runs GC of managed ServiceImports and
  stale slices. Apply: namespace missing -> pending (Namespace informer re-applies) unless
  `--create-namespaces`; ServiceImport v1beta1 via server-side apply (skip unmanaged unless
  `--adopt-unmanaged-imports`); derived Service `derived-<lower(base32hex(sha256("ns/name")))[:10]>`
  (mcs-api algorithm reimplemented; ClusterIP for CLUSTER_SET_IP, `clusterIP: None` for HEADLESS,
  no selector, owner ref to ServiceImport); EndpointSlices `<derived>-<cluster>-<i>`, 100 per
  slice, per source cluster including the local one, ports filtered to merged names, owner ref to
  ServiceImport; status.clusters/conditions from GDS, `status.ips` from derived clusterIPs.
- Pool half of the auto-export reconciler: WatchPools filtered to own cluster; for state
  REGISTERED|ACTIVE|DRAINING ensure a ServiceExport per `services[]` labelled with the pool id;
  remove only labelled exports on GONE/DELETE.
- RBAC: serviceexports (get/list/watch/update/patch; create/delete when auto-export),
  serviceexports/status, serviceimports (+status) full, services full, namespaces get/list/watch
  (+create when flag), nodes read, endpointslices full, events, clusterproperties read (+write when
  flag), leases in release namespace.
- Flags: `--gds-url` (repeat/comma, env `GDS_URLS`), `--gds-tls-ca-file`,
  `--gds-allow-insecure-http`, `--clusterset-id`, `--cluster-id`, `--cluster-identity-source`,
  `--region`, `--cluster-labels`, `--lease-ttl 30s`, `--watch-namespaces`, `--create-namespaces`,
  `--adopt-unmanaged-imports`, `--auto-export-pools`, `--write-cluster-properties`,
  `--deregister-on-shutdown`, `--endpoints-per-slice 100`, `--max-concurrent-reconciles 4`,
  metrics/health/leader-elect/operator-namespace.
- Docs `docs/dev/global-discovery-clusterset-dns.md`: CoreDNS `multicluster` plugin for
  `clusterset.local` (not built here; EKS/k3s need a custom CoreDNS image), ClusterSetIP needs
  routable pod IPs (VPC peering), otherwise export Headless.

## Go client (`pkg/client`)

`Config{URLs, CAFile, TLS, AllowInsecureHTTP, ConnectTimeout 5s, RPCTimeout 10s,
PrimaryRetryInterval 5m, Backoff{200ms,30s,2.0,jitter 0.5}, MaxAttempts 4, UserAgent, Registerer,
Resolver}`; `New(ctx, cfg) (*Client, error)`, `Conn()`, `OnReconnect(func(url))`, sub-clients
`Clusters() ServiceExports() ServiceImports() Pools() Routers()`. Failover via a per-client custom
gRPC resolver scheme: `ResolveNow` re-resolves every hostname in URL order with
`resolver.Address.ServerName` per URL, service config `pick_first` (no shuffle) + retryPolicy on
UNAVAILABLE for unaries; timer re-resolves and returns to primary. Also merges
`GetServerInfo.endpoints` as fallbacks after the configured list. `WatchLoop[T]` (reconnect with
backoff, SNAPSHOT_START handling, OnResync), `PoolCache`/`RouterCache` (initial List, Watch
deltas, anti-entropy full List every TTL 30 s, `Stale` after 2x TTL; `Snapshot()`, `Updates()`,
`WaitSynced()`), generation helper. Metrics `nvcf_gds_client_*` via interceptors. Tests with
`fakegds` over bufconn and real listeners (failover, DNS re-resolve, resume, cache TTL).

## Rust crate (`crates/gds-client`)

Cargo: deps tonic (tls features), tonic-prost, prost, tokio, tokio-stream, tokio-util, futures,
thiserror, tracing, rand, http; optional `prometheus` (feature `metrics`), optional
`stargate-proto` (feature `stargate`). No reqwest, hickory, moka, serde. Re-exports
`gds_client::{tonic, prost}` so Dynamo never names versions; `GdsClient::from_channel` for host
injection. Proto: vendored copy under `crates/gds-client/proto/` (nvidia/gds/v1 + google/api),
compiled by `build.rs` with `tonic_prost_build` like `crates/proto/build.rs`; Bazel `diff_test`
targets against the Go module's exported `filegroup` plus `tools/sync-gds-proto.sh`.

Modules and public API: `error` (`Error` enum: NoEndpoints, InvalidUrl, AllEndpointsFailed,
Connect, Tls, Rpc(Status), RegistrationLost, Shutdown); `endpoints` (`EndpointSet::parse`,
`TlsOptions`, `Backoff`, `ConnectPolicy{connect_timeout, rpc_timeout, primary_retry_interval,
allow_insecure_http, backoff}`); `channel` (`ChannelManager::new/channel/mark_failed/current_url/
subscribe_connection`, new `Endpoint` per reconnect so DNS is re-resolved, round-robin on failure,
return-to-primary timer, merges ServerInfo endpoints); `client` (`GdsClient::new(GdsClientConfig)`,
register/update/heartbeat/deregister/get/list/watch pools, list/watch routers, list/watch service
imports, server_info; unary retries on UNAVAILABLE/DEADLINE_EXCEEDED up to 4); `cache`
(`PoolState{pools, revision, fetched_at, synced, stale}` with `by_cluster/by_model/active`;
`PoolCache::spawn(client, PoolCacheConfig{filter, ttl 30s, watch, stale_after}, CancellationToken)`
exposing `watch::Receiver<Arc<PoolState>>`, `snapshot`, `wait_synced`, `refresh_now`; list then
watch deltas then periodic full list = Kiro "fetch all pools on expiration"); `routers`
(`RouterCache`/`RouterState` same shape); `register` (`PoolSpec`, `PoolRegistration::register
(client, spec, stop)` with heartbeat task at ttl/3, `set_desired_state(Active|Draining)`,
`update`, `shutdown(drain)` -> Draining then DeregisterPool; `RegistrationState` watch);
`derived` (`derived_service_name(namespace, name)` = `"derived-" +
lower(base32hex_nopad(sha256("ns/name")))[..10]`, identical to mcs-api, with a golden test shared
with the Go side); `routers` also provides `router_seed_urls(router)` (one
`"<scheme>://<replica address>:<grpc_port>"` per ready replica, IPv6 bracketed),
`remote_watch_urls(state, own_cluster)` (the replica seeds of every other router cluster with a
live lease), `pylon_seed_urls(state)` (the replica seeds of every router cluster with a live
lease) and `derived_service_url(router)`
(`"<scheme>://derived-<hash>.<ns>.svc.cluster.local:<grpc_port>"`, the merged VIP, never a
seed); `stargate` feature
(`router_to_stargate_info` for diagnostics only); `metrics`. Tests: `tests/support/fake_gds.rs` in-process tonic server;
endpoints failover, register heartbeats with paused time, cache TTL/stale, routers conversions.
Bazel: `cargo_build_script` with PROTOC like `crates/proto/BUILD.bazel`, `rust_library`
`gds-client` and `gds-client-stargate` (features), `rust_test` unit + integration. Add to
workspace `members`, `[workspace.dependencies]`, MODULE.bazel `stargate_crates` manifest list;
repin lockfile through Bazel (`CARGO_BAZEL_REPIN=1 bazel sync --only=stargate_crates`).

## Integrations

- Stargate (`crates/stargate`): flags `--gds-url` (repeat, env `GDS_URLS`), `--gds-clusterset`,
  `--gds-cluster-id`, `--gds-tls-ca-cert-path`; validation in `startup.rs::validate_discovery_args`
  (http requires `--allow-insecure-remote-watch-http`). `discovery.rs` -> `discovery/mod.rs` +
  new `discovery/gds.rs`: `GdsDiscovery{inner: Box<dyn Discovery>, routers: RouterCache,
  own_cluster}`; trait gains defaulted `async fn discover_remote_watch_urls(&self) -> Vec<String>`;
  `control_plane/watch_stargates.rs` unions static `--remote-stargate-url` with the dynamic set
  each poll tick. Local peer discovery (DNS/self) stays authoritative. Chart
  `llm-request-router`: values `globalDiscovery.{urls, clustersetId, clusterId, tlsCaConfigMap,
  export.enabled}` adding labels `gds.nvidia.com/export=true`, `gds.nvidia.com/role=router` and
  annotation `gds.nvidia.com/dial-scheme` to the backend-router Service (the Service pylons dial;
  required for export when replicas > 1, else the ClusterIP Service). The export is label-driven;
  no ServiceExport template needed. Pylon in another cluster dials the replica seeds of each
  router cluster and k8s-router forwards by authority; the advertised `grpc_pylon_dial_addr` must
  be cluster-specific and the certificate must cover the dialed address (design 4.5).
- Pylon (`crates/pylon`, `crates/pylon-lib`): `InferenceServerRegistrationConfig.seeds` becomes
  `watch::Receiver<BTreeSet<String>>`; `run_watch_stargate_discovery` adds a `select!` arm on
  `seeds.changed()`; `--stargate-address` optional when `--gds-url` set (one required); GDS
  `RouterCache` + `pylon_seed_urls` feed the sender, static address unioned in. Update the
  `seeds` assertions in `crates/pylon/src/startup.rs` tests (lines ~1564, ~1617).
- Pylon Operator: flags `--router-source=static|gds`, `--gds-url`, `--gds-clusterset-id`,
  `--gds-tls-ca-configmap`; `internal/transport/render.go` renders `--gds-url/--gds-clusterset`
  (and CA mount) instead of `--stargate-address` when `gds`; `RouterCache` used only for a startup
  warning and `status.registration.routers` count; chart values `router.source`,
  `globalDiscovery.*`.
- Dynamo PoolRelay and pool frontends: not in this repo. A pool's chart labels its frontend
  Service (`role=pool-frontend`, `gds.nvidia.com/pool`, model annotations) and its relay Service
  (`role=kv-relay`; headless if Global Routers should subscribe to every replica, ClusterIP for a
  VIP). The crate's `PoolRegistration` is the optional overlay for worker counts and draining;
  document both in `crates/gds-client/README.md`.

## Helm charts, Bazel, CI

- `deploy/helm/global-discovery/global-discovery`: Deployment (3 replicas, rolling maxUnavailable
  0, readiness `/readyz:9091`, grace 40 s, `GDS_REGION`/`AWS_REGION`), ClusterIP Service,
  optional `externalService` (internal NLB annotations, health check `/readyz` on 9091),
  ServiceAccount (IRSA annotation documented), PDB, optional HPA, optional ServiceMonitor, zone
  topology spread, `values.schema.json`, `scripts/check-render.sh`, Makefile copied from
  pylon-operator; CI values `tools/ci/helm-validate-values/global-discovery.yaml`; `chart_map`
  entry in `tools/ci/check-helm-charts`.
- `deploy/helm/global-discovery-mcs-controller/global-discovery-mcs-controller`: pylon-operator
  shape, `crds/` (ServiceExport, ServiceImport from mcs-api `config/crd`, ClusterProperty) via
  `scripts/sync-crd.sh`, values `gds.urls`, `clusterset.id`, `cluster.{id,region,labels}`,
  `identity`, `lease`, `imports`, `pools.autoExport`, `watchNamespaces`, `leaderElection`, `crds`.
- Bazel: module `BUILD.bazel` with `gazelle:prefix`, `gazelle:proto disable_global`,
  `go_oci_image` for `gds-server` and `gds-mcs-controller` (+ entrypoint mode test); add module to
  `go.work.bazel` `use`; `bazel mod tidy` and `use_repo(go_deps, ...)` additions:
  `org_golang_google_genproto_googleapis_api`, `com_github_aws_aws_sdk_go_v2_service_dynamodb`,
  `com_github_aws_aws_sdk_go_v2_feature_dynamodb_attributevalue`, `io_k8s_sigs_mcs_api`
  (import only `pkg/apis/v1beta1` and `config/crd`; may need a gazelle_override).
- Docs under the module: `README.md`, `AGENTS.md`, `docs/{api,storage,iam,networking,
  consistency}.md` (table definition, IAM policy, NLB + Route53 names, REST/stream examples).

## Testing and verification

- Go: `make test` (`-race -coverpkg=./internal/...`, 85 percent gate), `make test-integration`
  (dynamodb-local via testcontainers, `-tags=integration`), `make test-envtest`
  (`KUBEBUILDER_ASSETS`), `bazel test //src/control-plane-services/global-discovery/...`.
  Merge tests port Cilium's named cases (conflict-type, conflict-port-name, ..., ipfamilies).
- Rust: `bazel test //src/libraries/rust/stargate/crates/gds-client/...` and
  `bazel test //src/libraries/rust/stargate/...` after stargate/pylon changes.
- Charts: `make -C deploy/helm/global-discovery lint template validate test` (same for the
  controller chart); `tools/ci/check-helm-charts`.
- Codegen guard: `tools/ci/check-go-codegen src/control-plane-services/global-discovery` and the
  Rust proto `diff_test`.
- E2e `tests/e2e/global-discovery-k3d` (Makefile + bash 3.2 `run.sh`, pattern of
  `tests/e2e/inference-endpoints-k3d`): phase 1 single k3d cluster, chart with
  `store.kind=memory`, REST register cluster/pool, filtered list, `curl -N` watch sees a bookmark;
  phase 2 three k3d clusters on one Docker network (`hub` with gds-server as NodePort; `a`, `b`
  with the controller, distinct pod/service CIDRs via `--k3s-arg --cluster-cidr/--service-cidr`,
  static `ip route` between node containers behind `E2E_CROSS_CLUSTER_ROUTES=1`): export sample
  Service in `a`, assert ServiceImport/derived Service/EndpointSlice (`source-cluster=a`, 2 ready)
  in `b`, ServiceExport conditions Valid/Ready in `a`, curl derived ClusterIP from `b`, scale down
  propagates, a Service labelled `gds.nvidia.com/export=true` with `role=pool-frontend` and
  `gds.nvidia.com/pool=demo` appears as a Pool in `GET /v1/clustersets/x/pools`, UpdatePool
  overlay sets DRAINING, API-registered pool with `services[]` creates a labelled ServiceExport,
  controller pod restart keeps imports, removing the label removes export and import; phase 3 (after integration
  steps) stargate with `globalDiscovery` export in `a`, pylon in `b` with `--gds-url` registers
  with it. Manual end-to-end: two gds-server processes on one dynamodb-local converge within one
  refresh interval.

## Work breakdown (ordered; each step builds and tests on its own)

Phase 0, setup: create worktree `~/projects/nvidia/nvcf/nvcf-2-gds` on
`feat/global-discovery-service`; module skeleton, `go.work.bazel`, MODULE.bazel deps, buf config,
Makefile, README/AGENTS.

Phase 1, core server (Kiro: barebones service, then MCS-backed APIs):
1. `common/pool/server.proto` + codegen; config, version, metrics, noop auth; store interfaces +
   memory store + storetest; registry types; `internal/api/pool.go` Register/Update/Get/List;
   server (gRPC, gateway, admin, health, shutdown); minimal chart + CI values; k3d phase 1 e2e.
2. `cluster.proto`, ClusterService, pool heartbeat + state derivation, view + refresher + hub
   (revisions, bookmarks, backpressure), WatchPools/WatchClusters/ListClusterSets, compaction,
   readiness on view age.
3. DynamoDB store (`store.go`, `codec.go`, `schema.go`), `gds-admin create-table`, dynamodb-local
   conformance test, `docs/storage|iam|consistency.md`.
4. `mcs.proto`, ServiceExportService/ServiceImportService, `registry/merge` (Cilium port + test
   table), import indexes and selectors, WatchServiceImports, pools derived from labelled exports
   merged with the API overlay, pool endpoint join, ListRouters / WatchRouters by role label,
   `api/labels.go`, shared derived-name golden test.
5. Production chart (NLB, IRSA, PDB, HPA, ServiceMonitor, advertiseEndpoints), `docs/networking.md`
   and `docs/api.md`, go_oci_image, codegen guard, OTel wiring, limits/validation, coverage gate.

Phase 2, Go client and controller:
6. `pkg/client` (resolver failover, Client, interceptors, metrics, WatchLoop, fakegds) + tests.
7. `PoolCache`/`RouterCache` + tests.
8. Controller config, identity, metrics, lease runnable + tests.
9. ServiceExport reconciler (+ startup GC) with unit and envtest scenarios.
10. Importer (apply, derived Service, slices, pending namespaces, GC) with unit and envtest.
11. Label-driven auto-export reconciler (Service label half and pool half) + tests.
12. Controller chart (+ CRDs sync, RBAC), clusterset DNS doc; e2e phase 2.

Phase 3, Rust crate:
13. Crate skeleton, vendored proto + diff_test, `error/endpoints/channel/client`, Cargo/Bazel/
    MODULE.bazel wiring, lockfile repin, fake GDS, tests.
14. `register.rs` (PoolRegistration) + tests. 15. `cache.rs`, `routers.rs` + tests.
16. `stargate` feature conversions + README describing PoolRelay wiring.

Phase 4, LLM stack integration:
17. Stargate: GdsDiscovery, trait default method, dynamic remote watch URLs, flags, validation,
    chart export values and ServiceExport template; tests.
18. Pylon: dynamic seeds, flags, startup wiring; update seed assertions; tests.
19. Pylon Operator: `--router-source=gds`, render, chart values; tests.
20. E2e phase 3; docs (`docs/dev/global-discovery-quickstart.md`), AGENTS/README updates.

## Risks and open points

- `sigs.k8s.io/mcs-api` may pull newer `k8s.io/*` than the repo's 0.34; import only
  `pkg/apis/v1beta1` and `config/crd`, add a gazelle_override if needed.
- Lockfile regeneration without cargo must go through Bazel; verify early in step 13.
- ClusterSetIP requires routable pod IPs across clusters (VPC peering); the controller cannot
  verify it; docs say when to export Headless.
- Writers must keep `generation` monotonic across restarts and region failover (clients enforce
  `max(prev+1, unix_ms)`); third-party REST writers could violate it.
- Per-partition write ceiling on `CS#<clusterset>` (heartbeats); mitigations are longer pool
  heartbeat intervals or per-cluster pool partitions (store-internal).
- View revisions give no cross-replica resume continuity; snapshots are streamed per object so
  reconnects are cheap; DynamoDB Streams or the `by_updated_at` LSI are the scaling paths.
- Pylon dynamic seeds change `InferenceServerRegistrationConfig`, touching existing tests.
- Dynamo PoolRelay and Python Global Router integration happen in the Dynamo repo later.

## Status

Workstream 1 (foundation), branch `feat/gds-ws1-foundation`, delivered under
`src/control-plane-services/global-discovery`:

- Module skeleton: `go.mod` (Go 1.26.5), Makefile (`build`, `test` with an
  85 percent coverage gate on `internal/`, `vet`, `lint`, `proto`,
  `proto-check`, `proto-breaking`), README, AGENTS, golangci config.
- Proto contract `nvidia.gds.v1` (`common`, `cluster`, `mcs`, `pool`,
  `server`) with REST annotations, vendored `google/api`, `buf.yaml`
  (STANDARD lint, no exceptions), `buf.gen.yaml`, `hack/proto.sh`, and the
  generated Go, grpc-gateway and OpenAPI code checked in. A gateway test pins
  every REST path, the JSON shape and the stream shape.
- `api/`: well-known labels and annotations, error and condition reasons,
  limits, `DerivedServiceName` with golden values cross-checked against the
  mcs-api algorithm.
- `internal/store`: interfaces, records, typed errors, the memory store and
  the `storetest` conformance suite.
- `internal/registry`: lease math, TTL clamping, generations, roles and pool
  state derivation.
- `internal/config`, `internal/version` and a `cmd/gds-server` stub that
  validates its flags and exits.
- Bazel: module in `go.work.bazel`, Gazelle BUILD files (the root
  `BUILD.bazel` now excludes `src/control-plane-services` per subtree so the
  root Gazelle manages this module), `org_golang_google_genproto_googleapis_api`
  added to `use_repo`, `x_defs` on `gds-server`.

Deviations from the plan: `PutPool` takes `PutPoolOptions{Now, MintLease}` so
that registration and update share one conditional write; store methods take
server time explicitly. Not done in ws1: CI wiring (the
`.github/workflows/bazel.yml` matrix row and the `check-go-codegen` guard),
`go_oci_image`, golangci-lint run (not installed locally; staticcheck is
clean).

Workstream 6 (Rust client), branch `feat/gds-ws6-rust`, delivered
`src/libraries/rust/stargate/crates/gds-client`:

- Wiring: workspace member and `[workspace.dependencies]` entry, the
  `stargate_crates` manifest list in MODULE.bazel, lockfiles repinned through
  Bazel (`CARGO_BAZEL_REPIN=true CARGO_BAZEL_REPIN_ALLOWLIST=stargate_crates
  bazel mod deps`). Cargo.lock gains only the gds-client package; the new
  workspace pins (prost-types 0.14.3, sha2 0.10.9, data-encoding 2.10.0)
  were already resolved.
- Vendored protos under `proto/`, compiled by `build.rs` with
  tonic-prost-build (server traits with default stubs, so fakes implement
  only what they need). One `diff_test` per file against
  `api/proto:proto_srcs`, with a genrule picking each file out of the
  filegroup, and `hack/sync-gds-proto.sh` (`--check` mode) in the module.
- Modules: `error`, `endpoints`, `channel`, `client`, `cache`, `routers`,
  `register`, `derived`, `labels`, `metrics` (feature) and `stargate`
  (feature), per the design 9.2 API. Watches follow 5.6 with opaque resume
  revisions; snapshots replace the cache at SNAPSHOT_END.
- Bazel: `cargo_build_script` with the repository protoc plus the
  well-known and descriptor protos, `gds-client`, `gds-client-stargate`
  (`metrics`, `stargate`), unit tests for both, and an integration test
  rooted at `tests/lib.rs`. `bazel test
  //src/libraries/rust/stargate/crates/gds-client/...` passes (33
  integration tests, 20 and 23 unit tests, 7 diff tests) and `bazel build
  //src/libraries/rust/stargate/...` succeeds. rustfmt and clippy
  (`-D clippy::all`) are clean through rules_rust.
- Tests use `tests/support/fake_gds.rs`, served on localhost ports for
  failover and over in-memory duplex streams on a paused clock for
  registration and cache timing.

Deviations from the plan:

- The sync script is `src/control-plane-services/global-discovery/hack/sync-gds-proto.sh`,
  not `tools/sync-gds-proto.sh`.
- Extra dependencies: prost-types (well-known types in generated code),
  sha2 and data-encoding (derived names).
- `GdsClient::new` is synchronous and connects on the first call.
  `ChannelManager::mark_failed` takes the failed `Connection` so a late
  report cannot drop a newer connection. `Error` gains `ConnectTimeout`;
  `RegistrationLost` is returned by updates after the heartbeat stopped.
- `PoolRegistration::update` takes a `PoolUpdate` (set fields become the
  field mask), `report_workers` feeds the next heartbeat, and
  `shutdown(drain: Duration)` keeps heartbeating while draining. A
  STALE_GENERATION answer is retried once above the stored generation.
- `seed_url` uses `https` when a router has no dial scheme; seeds skip
  routers whose cluster lease expired. Routers with the same namespace and
  name in several clusters share one derived Service and one seed URL.
- No Go client metric list existed, so this crate defines the
  `nvcf_gds_client_*` names and labels (crate README) for `pkg/client` to
  mirror.

Not done in ws6: Cargo commands (`cargo test`, `cargo clippy`, `cargo fmt`)
were not run because the machine has no cargo; the Bazel equivalents above
were. The Stargate, Pylon and Pylon Operator integration is a later
workstream.

Workstream 4 (Go client), branch `feat/gds-ws4-goclient`, delivered under
`src/control-plane-services/global-discovery/pkg`:

- `pkg/fakegds`: in-memory implementation of the five services for tests,
  served over bufconn or `127.0.0.1:0` (and `ServeAddr` to restart on the
  same address). Cluster and pool leases with epochs, writer generations
  with the STALE_GENERATION details, RegisterPool, UpdatePool with a field
  mask, HeartbeatPool and DeregisterPool, pools derived from labelled exports
  and merged with the API overlay (state from `internal/registry`), routers
  from role labels, simplified merged imports (oldest active export wins,
  ports unioned, no conflicts), watches with snapshots, deltas, bookmarks,
  revisions, per-watcher filters, resume from a history and re-snapshot on
  lag, `FailNext`, `CallCount`, `CloseWatches`, `SetServerInfo` and seeding
  helpers.
- `pkg/client`: `Config` with the plan defaults, `New`, `Close`, `Conn`,
  `OnReconnect`, `CurrentURL`, `URLs` and typed sub-clients (`Clusters`,
  `ServiceExports`, `ServiceImports`, `Pools`, `Routers`, `ServerInfo`).
  Failover through a per-client resolver scheme: every URL host re-resolved
  in order with the injected `Resolver`, `ServerName` per URL, `pick_first`
  without shuffling, a retry policy on UNAVAILABLE for the unary methods,
  `GetServerInfo.endpoints` appended as fallbacks, and a timer that probes
  the primary and moves the connection and the watch loops back.
  `WatchLoop[T]`, `PoolCache` and `RouterCache` (initial list, watch resumed
  from the list revision, anti-entropy list every TTL, `Stale` after 2 x TTL,
  `Snapshot`, `Updates`, `WaitSynced`, `Refresh`), `PoolState.ByModel`,
  `ByCluster`, `Active`, `RouterState.SeedURL` and `SeedURLs`,
  `NextGeneration` and `Generation`, error helpers (`Reason`,
  `StaleGeneration`, `IsLeaseEpochMismatch`), and the `nvcf_gds_client_*`
  metrics through interceptors and a stats handler.
- Tests against `pkg/fakegds` with the race detector: failover to a second
  listener and return to the primary, DNS re-resolution through a fake
  resolver, retry policy, advertised fallbacks, TLS server name per URL,
  metrics and deadlines, watch snapshot, resume, resync and idle handling,
  cache deltas, TTL re-list, stale flag and seed URLs.
- Docs: `pkg/client/README.md`; module README and AGENTS updated.
- Dependency: `github.com/prometheus/client_golang` v1.23.2 (already in the
  repository's Bazel graph); `google.golang.org/genproto/googleapis/rpc`
  became a direct requirement for the error details.

Deviations from the plan: the fake server lives in `pkg/fakegds` instead of
`internal/mcs/testing/fakegds` so other modules' tests can import it. The
client adds `Dialer`, `DialOptions` and `Logger` to `Config`, and
`ServerInfo()`, `CurrentURL()` and `URLs()` to the client. All URLs of one
client share a scheme. Addresses are passed to `pick_first` with a URL
prefix so the URL order is kept (no IPv4 and IPv6 interleaving) and each
connection is attributed to its URL. Returning to the primary also restarts
the `WatchLoop` streams, which otherwise stay on the old connection. The
caches reopen a watch that is silent for 30 s (`IdleTimeout`), since no
client keepalive is configured. `SeedURLs` returns one URL per router
cluster (the first router by namespace and name) and lists a URL shared by
several clusters once. Not done in ws4: an OpenTelemetry stats handler is
not wired by default (pass it in `DialOptions`), and staticcheck and
golangci-lint were not run (not installed locally); `go vet` and `gofmt` are
clean.

Workstream 3 (DynamoDB store), branch `feat/gds-ws3-dynamodb`, delivered under
`src/control-plane-services/global-discovery`:

- `internal/store/dynamodb`: `Store` on one table with the single-table
  layout above, protobuf bodies mapped from the store records to
  `nvidia.gds.v1` messages (`codec.go`), top-level `gen`, `lease_epoch`,
  `lease_renewed_at`, `owner_id`, `updated_at` and `expires_at`, the designed
  conditional writes, `TransactWriteItems` for pool names, `DeleteCluster`
  through prefix queries and `BatchWriteItem` chunks of 25 with retries, and
  a paginated, eventually consistent `LoadClusterSet` with bounded parallel
  endpoint partition queries. SDK errors map to `ErrUnavailable` and
  `ErrTooLarge`. `Options{Client, TableName, QueryConcurrency, Clock,
  ExpiryAfter}`; `NewClient` builds a client from the default AWS chain for
  the server wiring.
- `schema.go`: the table definition (string `pk`/`sk`, KEYS_ONLY LSI
  `by_updated_at` on `updated_at`, PAY_PER_REQUEST, deletion protection),
  `CreateTable`, `EnsureTTL` and `EnsurePointInTimeRecovery`;
  `testdata/create-table.json` is its CLI form.
- `cmd/gds-admin` with `create-table` (`--table`, `--region`, `--endpoint`,
  `--deletion-protection`, `--pitr`, `--ttl`, `--wait-timeout`), package
  `cmd/gds-admin/createtable` with `Run(ctx, args, stdout, stderr)`.
- Tests: the conformance suite against an in-memory fake of the DynamoDB API
  in `make test` (coverage gate kept), and against `amazon/dynamodb-local`
  2.6.1 through testcontainers-go behind the `integration` build tag
  (`make test-integration`, Bazel `dynamodb_integration_test` and
  `createtable_integration_test` tagged `external` and `requires-docker`),
  plus a two-replica test, a multi-page load, stale name reservations and
  the table definition. Integration tests skip without Docker.
- Docs: `docs/storage.md`, `docs/iam.md`, `docs/consistency-dynamodb.md`.
- Bazel: `com_github_aws_aws_sdk_go_v2_service_dynamodb` and
  `com_github_aws_aws_sdk_go_v2_feature_dynamodb_attributevalue` in
  `use_repo`; dynamodb v1.57.1 and attributevalue v1.20.37 keep the workspace
  on aws-sdk-go-v2 v1.41.5.

Deviations from the plan:

- An endpoint partition marker item (`CS#<clusterset>` / `EPX#<cluster>`)
  lets `LoadClusterSet` find endpoint sets of clusters without a
  registration, export or pool, which the conformance suite requires.
- `PutCluster` also pins the generation it read (`attribute_not_exists(pk) OR
  (lease_epoch < :new AND gen = :seen)`), and `PutPool` and `DeletePool`
  guard the pool with the generation, body and lease they read instead of
  `gen < :gen` alone, because they derive fields from the stored item. A pool
  heartbeat whose patch changes the body is a read plus an `UpdateItem`
  guarded by the body. A pool name reservation whose pool is gone (TTL) or
  renamed is taken over.
- `expires_at` is the last write or heartbeat plus 7 days, on cluster and pool
  items only. `Ping` uses `GetItem` of a missing key instead of
  `DescribeTable`, so the server role needs no table management action.
- The consistency document is `docs/consistency-dynamodb.md`.

Not done in ws3: wiring the store and `create-table` into gds-server (server
workstream), CI for the Docker-backed targets, replica management in
`gds-admin` (documented as AWS CLI and CloudFormation), regenerating the
root `dependencies.md`.

Workstream 4b (router seeds), branch `feat/gds-ws4b-router-seeds`, revises the pylon seeds of
both clients (design 4.5, decision row "Routers"):

- Problem: every router cluster exports the same router Service (for example
  `nvcf/llm-request-router-backend`), so by MCS semantics the exports merge into one
  ServiceImport and its derived VIP spreads over the k8s-router pods of all router clusters. A
  pylon could reach cluster B's k8s-router while registering with a Stargate pod of cluster A,
  which B cannot forward to.
- Decision: seeds are the per-cluster replica addresses from GDS. For each Router record the
  seeds are `<scheme>://<replica address>:<grpc_port>` for every ready replica, IPv6 addresses
  bracketed, the scheme from `dial_scheme` (default https; other schemes yield none). Routers
  whose cluster lease expired are skipped. Each router cluster contributes N URLs; pylon-lib
  already watches several seeds and fails over between them. Per-source-cluster derived
  Services (a KEP-1645 deviation) and unique router Service names (operational burden) were
  rejected.
- Go `pkg/client`: `RouterState.RouterSeedURLs(router)`, `SeedURLs()` (now per replica),
  `SeedRouters()` (for a router count) and `DerivedServiceURL(router)` (the merged VIP, kept for
  callers that want it). `SeedURL` is removed so no caller keeps the old meaning; dial schemes
  other than http and https yield no URL and `NewRouterCache` rejects such a `DefaultScheme`.
- Rust `gds-client`: `router_seed_urls(router)`, `pylon_seed_urls(state)`,
  `remote_watch_urls(state, own_cluster)` (the replica seeds of the other clusters) and
  `derived_service_url(router)`; `seed_url` is removed and the diagnostic
  `router_to_stargate_info` uses `derived_service_url`.
- Docs: the `Router` proto comment (generated Go, OpenAPI and the vendored Rust copy change in
  comments only), the dial-scheme annotation comments, `docs/api.md`, both client READMEs, and
  design 0, 2.5, 3, 4.3, 4.5, 4.6, 9.1, 9.2, 13 and 16.
- Verified: `GOWORK=off make test`, `make vet` and `make proto-check` in the module;
  `bazel test //src/control-plane-services/global-discovery/...` and
  `bazel test //src/libraries/rust/stargate/crates/gds-client/...`; rustfmt and clippy
  (`-D clippy::all -D warnings`) clean through the rules_rust aspects.

Open for the Stargate and Pylon integration: the `grpc_pylon_dial_addr` a router cluster
advertises must be specific to that cluster and reachable from the others (empty, so pylons dial
the Stargate pod, or a per-cluster k8s-router address); with dial scheme https the router
certificate must cover the dialed address (the chart issues DNS-name certificates only); the
router Service's gRPC port must equal its target port.

Workstream 2 (server core), branch `feat/gds-ws2-server`, delivered under
`src/control-plane-services/global-discovery`:

- `internal/registry/merge`: the Cilium ServiceImport merge (priority order
  with expired clusters skipped, port union, IP family intersection,
  conflict checks) with Cilium's named cases and the GDS cases.
- `internal/view`: per-clusterset view (clusters, merged imports, pools from
  labelled exports plus the API overlay, routers, model and label indexes),
  refresher with write-through and lease evaluation, watch hub (revisions,
  ring buffer, resume, bookmarks, lagged resync, watcher limit), compaction.
- `internal/api`, `internal/auth`, `internal/server`, `internal/metrics`,
  `internal/store/instrumented`, `internal/storefactory`; `gds-server`
  serves gRPC, REST and admin with the memory store; `gds-admin` has
  `describe` and `delete-cluster`. `docs/consistency.md` added.

Deviations from the plan: a write with generation 0 gets a server-assigned
generation; errors also use the reasons `INVALID_ARGUMENT`, `NOT_FOUND` and
`INTERNAL`; the default pool id is a name-based UUID of
`clusterset/cluster/name`; the merge compares port sets without order and
ignores exports that list no IP family; an import whose clusters all
expired keeps its shape; gRPC metrics are `nvcf_gds_grpc_server_*`;
compaction re-reads a cluster before deleting it because the store has no
conditional delete; the per-watcher queue flag stays `--watch-queue-size`.
Not done in ws2: the DynamoDB opener in `internal/storefactory` (ws3),
removal of empty clustersets from `ListClusterSets` (the store has no call
for it), `go_oci_image` and CI wiring.
