# Karmada MCS in Dynamo compared with the GDS MCS controller

Discussion note. Analyses the Multi-Cluster Services (MCS) implementation the upstream Dynamo
Global Router work builds on (Karmada, plus the pieces Dynamo adds around it) and compares it
with the GDS design in `global-discovery-service-design.md`, where GDS itself is the MCS
implementation. GDS interactions are part of both halves. No design change is made here.

Sources, as of 2026-10-27 (head SHAs in `upstream-review-state.md`):

- dynamo #16963: `examples/global-router/cdk/{README.md, lib/multi-cluster.ts, crds/}`, the
  EKS and Karmada setup for the two-region POC (Ohio hub, West member).
- dynamo #17331 and #17332: `lib/global-discovery-service/src/backends/kubernetes/{mcs,hub,
  endpoints,cache}.rs`, `backends/registered.rs`, `peers.rs`, `main.rs`: the discovery
  service's Kubernetes stores (`--store karmada` and `--store mcs`).
- dynamo #17412: `deploy/operator/internal/multicluster/serviceexport.go` and
  `controller/dgd_service_export_reconciler.go`: the operator creates the ServiceExports.
- DEP #103 at aeba94e93, the "Full Kubernetes" column of the components table.

## 1. How Karmada MCS works, and how Dynamo wires it

### 1.1 Karmada's model

Karmada is a hub-and-member multi-cluster control plane. One cluster hosts the Karmada API
server (its own kube-apiserver and etcd, separate from the hosting cluster's), and every
participating cluster, the hub cluster included, joins as a member. In Pull mode a
`karmada-agent` in each member dials the hub; the hub never dials the members.

Its MCS support is the SIG Multicluster API reinterpreted through Karmada's propagation model:

1. An operator creates a Service and a `ServiceExport` (`multicluster.x-k8s.io/v1alpha1`) in
   the member cluster that runs the pods.
2. The agent watches the exported Service's EndpointSlices and copies each one up to the Karmada
   control plane, into an execution-space namespace `karmada-es-<cluster>`, renamed
   `imported-<cluster>-<source slice>` and labelled `endpointslice.kubernetes.io/managed-by=
   endpointslice-controller.karmada.io`. Nothing is imported anywhere yet.
3. To consume the Service from a cluster, an operator creates a `ServiceImport` on the Karmada
   control plane and a `PropagationPolicy` that selects it and names the destination clusters.
   With the `PropagateDeps` feature gate on, Karmada delivers the `ServiceImport` and, as its
   dependencies, a `derived-<service>` Service and the collected slices into each destination
   cluster. The derived Service has a ClusterIP and lists the pods of every exporting cluster,
   the destination's own pods included.

Points that matter for the comparison:

- Karmada does not use the KEP-1645 labels. Imported slices carry `kubernetes.io/service-name=
  derived-<service>`, not `multicluster.kubernetes.io/service-name`, and there is no
  `multicluster.kubernetes.io/source-cluster` label. The source cluster is only recoverable
  from the slice name, `imported-<cluster>-<source slice>`.
- The derived Service name is `derived-<service>`, not the KEP's `derived-<hash>`.
- Imports are not automatic. Every (Service, destination cluster) pair needs a `ServiceImport`
  plus a `PropagationPolicy` on the hub. The CDK README documents this as a manual step.
- Kubernetes truncates `<service>-` in slice names at 58 characters, so a Service longer than
  57 characters (55 in the operator, which also accounts for the `derived-` label value)
  produces imported slices whose names no longer identify the source cluster.
- The control plane is a single point of state. In the CDK deployment its etcd uses a
  `hostPath` volume and is not pinned to a node; if the pod moves, the hub loses its state and
  "does not recover by itself: install the control plane again and register the members again".
- Karmada 1.19.0 is pinned everywhere (chart, CRDs, `karmadactl`); the CDK refuses other
  versions because the CRDs and setup images move together.

### 1.2 What the CDK adds (dynamo #16963)

For every cluster with `multiCluster` set, `MultiClusterSupport` does four things:

- Adds a security group rule admitting all TCP from each peer VPC CIDR to the EKS cluster
  security group, because MCS slices list pod addresses and a Global Router in Ohio dials a
  West PoolRelay pod directly. A second rule admits ICMP type 3 code 4 (fragmentation needed)
  so path MTU discovery works across the inter-region peering.
- Excludes the peer VPC CIDRs from source NAT in the AWS VPC CNI
  (`AWS_VPC_K8S_CNI_EXCLUDE_SNAT_CIDRS`), so cross-cluster traffic keeps its pod source address.
  This is what makes `relay_identities` (source-address admission) possible.
- Installs the `ServiceExport` and `ServiceImport` CRDs taken from the Karmada 1.19.0 chart,
  with overwrite semantics and retained on rollback, because deleting a CRD deletes every
  export and import.
- In the hub only, and only with `-c karmada=true`, installs the Karmada 1.19.0 control plane
  Helm chart into `karmada-system`, images pinned, `PropagateDeps` on, API server behind an
  internal NLB on TCP 5443 restricted to the member VPC CIDRs. CDK never uninstalls it.

Joining is manual: install `karmadactl` on each bastion, mint a token on the hub, run
`karmadactl register` on each member with `--cluster-name` equal to the Global Router
`site_id` (`aws-ohio`, `aws-west`); the hub cluster registers itself from a different namespace
to avoid clobbering the control plane's own kubeconfig Secret.

The README also records that Services and exports must be created in the workload cluster and
not propagated by Karmada, because a propagated Service gives its slices Karmada's labels and,
in a Karmada 1.19 test, deleting such an export made the agent delete the Service's
EndpointSlice too.

### 1.3 What the operator adds (dynamo #17412)

The Dynamo operator gains `multiCluster.mcsImplementation` (`""`, `mcs`, or `karmada`) in its
configuration and a feature gate that checks the `multicluster.x-k8s.io/v1alpha1` ServiceExport
API exists. When set, a `DynamoGraphDeployment` (DGD) annotated
`nvidia.com/global-router-pool: "true"` gets two `ServiceExport` objects owned by the DGD:
`<dgd>-pool-relay` and `<dgd>-frontend`. Removing the annotation withdraws them; clearing the
implementation leaves them in place until the DGD is deleted. For Karmada the DGD name is
limited so both Service names stay at 55 characters.

The operator does not create the PoolRelay Service itself in this PR, and it creates no
`ServiceImport` or `PropagationPolicy`; the import side stays manual on the hub.

### 1.4 What the discovery service adds (dynamo #17331, #17332)

The HTTP discovery service has two Kubernetes stores, selected by `--store karmada` or
`--store mcs`, differing only in how imported slices are named (`McsImplementation::Karmada`
versus `::Standard`). Both read EndpointSlices only; they never read `ServiceImport` objects.
The service runs in a cluster that receives the imports (or on the Karmada control plane
itself) with a kube client and one slice watch per directory.

- Pool directory (`hub.rs`): one pool per (source cluster, namespace, DGD name). The DGD name
  is the imported Service name with `--pool-service-suffix` (`-pool-relay`) removed; the
  source cluster is the KEP label in Standard mode or parsed out of `imported-<cluster>-...`
  in Karmada mode. A pool is live while at least one PoolRelay endpoint is Ready. The pool's
  `relay_identities` are all addresses of the Ready PoolRelay pod with the lowest address, so
  every service instance binds the same pod. Frontend URLs come from the slices of
  `<dgd>-frontend` on the named `http` port, reduced to one address family. The pool's
  `location.region` is set to the cluster name because MCS knows nothing else.
- Replica directory (`endpoints.rs`): Global Router replicas from the imported slices of the
  router's exported Service (`--router-namespace`, `--router-service`, `--router-port-name`),
  served to PoolRelays as `GET /routers`.
- Registration join (`registered.rs`): a pool is listed only while MCS shows a Ready PoolRelay
  and the PoolRelay has called `POST /pools` within the lease. MCS supplies addresses and
  liveness; the registration supplies model, capabilities, capacity, location. Each covers the
  other's gap: MCS drops a pool within seconds of its export or pod going away, and the lease
  drops a pool whose cluster has lost contact with Karmada while its slice still says Ready.
- Peer sharing (`peers.rs`): registrations live in the memory of the instance a PoolRelay
  talked to. With `--peer-service` (the service's own Service, exported through MCS) each
  instance polls every other instance's `GET /registrations` twice per heartbeat interval over
  plain HTTP and keeps copies for the remaining lease. Copies are never re-shared.
- Staleness (`cache.rs`): the slice watch waits for the first complete list before serving;
  until then `/healthz` and `GET /pools` return 503. A watch failing for 60 s turns the view
  `Estimated` (lists keep serving the last snapshot); 120 s quiet ends the failure run.

### 1.5 Data flow (Full Kubernetes mode, as the DEP describes it)

```
workload cluster (member, site aws-west)          Karmada control plane (hub, Ohio)         router cluster (member, aws-ohio)
operator: DGD annotated global-router-pool          karmada-es-aws-west/                       derived-<dgd>-pool-relay Service
  -> ServiceExport <dgd>-pool-relay   --agent-->      imported-aws-west-<slice>  --PropagationPolicy-->  imported-aws-west-<slice>
  -> ServiceExport <dgd>-frontend                   (operator-created ServiceImport)          derived-<dgd>-frontend Service
PoolRelay pod  --POST /pools (timer)--> discovery service (in its own cluster)                 discovery service (--store karmada)
               <--GET /routers------                 |                                             watches imported slices
                                                     +-- GET /registrations peer polling ----------> joins with registrations
PoolRelay --gRPC state (direction TBD)--> Global Router replica (pod IP from GET /routers)       Global Router --GET /pools--> service
Global Router --HTTP--> frontend pod IP (from pool.frontends) over VPC peering, no SNAT
```

Everything with a pod IP crosses the VPC peering directly; the security group and SNAT
exclusion exist for exactly that.

## 2. The GDS design in the same terms

GDS is the MCS implementation; there is no separate multi-cluster control plane.

- Writers: `gds-mcs-controller` in every cluster. A Service labelled `gds.nvidia.com/export=
  true` (or an explicit `ServiceExport`, v1beta1, with v1alpha1 served through the same CRD)
  becomes `PutServiceExport` plus `ReplaceEndpoints` to GDS. The controller holds a cluster
  lease (`RegisterCluster`, heartbeat at TTL/3).
- Store: stateless `gds-server` replicas in every region over one DynamoDB Global Table.
  Region loss is survived by DNS failover to another region's replicas; there is no hub state
  to rebuild.
- Readers: the same controller in every cluster runs `WatchServiceImports` and materialises,
  in every cluster and without per-pair configuration, the KEP objects: `ServiceImport`
  (v1beta1), the `derived-<hash>` Service, and EndpointSlices named `<derived>-<cluster>-<i>`
  labelled `multicluster.kubernetes.io/service-name` and `multicluster.kubernetes.io/
  source-cluster`, managed-by `gds-mcs-controller.gds.nvidia.com`.
- Conflicts across exporting clusters are resolved server side with Cilium's oldest-export-wins
  rules and reported as `ServiceExport` conditions.
- Pools and routers are first-class in the same API: pools derive from labelled exports
  (`gds.nvidia.com/role=pool-frontend|kv-relay`, `gds.nvidia.com/pool`) with an optional API
  overlay (`RegisterPool`, `HeartbeatPool`, `DRAINING`), routers from exports labelled
  `role=router`. Liveness is the cluster lease plus ready endpoints, evaluated at read time.
- Consumers (Stargate, Pylon, Pylon Operator, a future PoolRelay) read GDS directly through
  the Rust and Go clients, or use the derived VIP the controller created locally.

```
cluster a                                      GDS (region-local replicas, global table)           cluster b
Service (labels) -> controller -> ServiceExport --PutServiceExport/ReplaceEndpoints-->  view  --WatchServiceImports--> controller
controller --RegisterCluster/Heartbeat-->                                                         ServiceImport + derived-<hash> + slices
pylon/relay --RegisterPool/HeartbeatPool (optional overlay)-->                                    stargate/pylon --ListRouters/WatchPools-->
```

## 3. Side by side

| Topic | Karmada MCS as Dynamo uses it | GDS MCS |
|---|---|---|
| Who implements MCS | Karmada (third party, 1.19.0 pinned); Dynamo consumes its output | GDS server plus `gds-mcs-controller`; GDS is the implementation |
| Control plane | One Karmada API server and etcd in the hub cluster; members join in Pull mode | None beyond GDS itself: stateless replicas per region over a replicated table |
| Multi-region | Single hub. Hub down or etcd pod moved: imports freeze, state lost, re-init and re-join by hand | Any region serves; writers and watchers fail over by DNS; no state to rebuild |
| Membership | `karmadactl register` with a short-lived token per cluster, manual on a bastion | `RegisterCluster` by the controller at start, identity from flags or `ClusterProperty` |
| Export API version | `v1alpha1` (Karmada CRDs) | `v1beta1` storage; `v1alpha1` served by the same CRD, so #17412's objects are accepted |
| Export creation | Dynamo operator from the DGD annotation (two fixed Service names); otherwise by hand | Service label (`gds.nvidia.com/export=true`), explicit `ServiceExport`, or `services[]` of an API-registered pool |
| Import creation | Manual: `ServiceImport` plus `PropagationPolicy` per Service on the hub, naming destination clusters | Automatic in every cluster that runs the controller; `--watch-namespaces` to narrow |
| Imported objects | `derived-<service>` Service and `imported-<cluster>-<slice>` slices; no KEP labels; `ServiceImport` present only where propagated | `ServiceImport`, `derived-<hash>` Service, `<derived>-<cluster>-<i>` slices with KEP labels and `spec.ips` |
| Source cluster identity | Parsed from the slice name; breaks past 57-character Service names | `multicluster.kubernetes.io/source-cluster` label; cluster name in GDS records |
| Conflicts between exporters | Not handled; same-named exports are merged blindly, which is why their pool key splits by source cluster | Oldest export wins (ports, type, affinity, labels); losers get `Conflict` conditions |
| Headless and session affinity | Not considered; derived Service is a plain ClusterIP | `ClusterSetIP` or `Headless` by Service type; `sessionAffinity` carried through |
| Liveness of a pool | A Ready PoolRelay endpoint in the imported slice, and a registration within a 15 s lease | Cluster lease (30 s) and ready endpoints from the export; optional pool lease (60 s) for API-only pools; `DRAINING` state |
| Pool metadata source | `POST /pools` by the PoolRelay (model, capabilities, capacity, location) | Service labels and annotations forwarded by the controller, plus the optional API overlay |
| Pool location | `region = cluster name`; zone and datacenter unknown | Real `region`, `dc`, `zones` from the cluster record and labels |
| Router discovery for the pool side | `GET /routers`: pod IPs from the router Service's imported slices | `ListRouters`/`WatchRouters`: per-cluster router replica addresses from the role=router export; the client builds seeds |
| Service HA | One instance per cluster holds registrations in memory; `--peer-service` polls peers over plain HTTP; no shared store | Stateless replicas; the table is the shared state |
| Staleness signalling | `Exact` or `Estimated` completeness; 503 until first list; watchers not told when a dead watch goes stale | `view_age_seconds` drives readiness; cluster lease expiry removes endpoints; watchers likewise keep their last snapshot on a failed refresh |
| Network prerequisites | Routable pod IPs, all-TCP security group rule from peer VPC CIDRs, ICMP frag-needed, SNAT exclusion, Karmada API NLB reachable from members | Routable pod IPs for ClusterSetIP imports (or export Headless); GDS reachable over its own NLB and Route53 names; nothing else cluster to cluster |
| Transport security | Plaintext gRPC to the relay, plain HTTP between service instances; mTLS by sidecar | TLS to GDS by CA file in both clients; no auth in v1 (seam only) |
| Version coupling | Karmada chart, CRDs, `karmadactl` must match (1.19.0) | mcs-api v0.5.2 CRDs synced by script; no third-party control plane |

## 4. GDS interactions, both ways

In the upstream design GDS is downstream of MCS: it reads what Karmada imported, adds the
registration overlay, and answers `GET /pools` and `GET /routers`. In our design GDS is
upstream of MCS: it is the store the controller writes to and reads from, and the KEP objects
in each cluster are a projection of it. The two are not in conflict; they compose.

What works today without changes:

- Their service in `--store mcs` (Standard) mode reads exactly the labels our importer writes
  (`multicluster.kubernetes.io/service-name` for the Service name,
  `multicluster.kubernetes.io/source-cluster` for the cluster). Pointed at a cluster running
  `gds-mcs-controller`, it would list pools and router replicas from our imports with no
  Karmada installed. The 57-character name limit does not apply because our slice names do not
  carry the source cluster.
- Their operator's `v1alpha1 ServiceExport` objects are accepted by our controller: the
  chart's CRD serves `v1alpha1` and stores `v1beta1`, and the two schemas are identical for
  the fields in use. A DGD annotated `nvidia.com/global-router-pool` would therefore be
  exported by our controller as `<dgd>-pool-relay` and `<dgd>-frontend` without the
  `gds.nvidia.com/*` labels, so it would appear in our MCS layer but not as a Pool, because
  our pool derivation keys on the `gds.nvidia.com/pool` label. Either the operator adds our
  labels to the Services it creates, or our controller infers roles from the suffixes (now
  flags on their side, `--pool-service-suffix` and `--frontend-service-suffix`).

What does not line up:

- The source of pool metadata. Theirs comes from the PoolRelay's `POST /pools`; ours from
  Service labels plus an optional overlay. A PoolRelay that speaks only their HTTP contract
  cannot register with our server (gRPC and grpc-gateway REST under `/v1/clustersets/...`).
  The compatibility surface proposed in `upstream-dynamo-comparison.md` section 7 closes this.
- Liveness semantics. Theirs is "Ready relay endpoint and fresh registration". Ours is
  "cluster lease active and ready endpoints", with the API overlay able to say `DRAINING`.
  Ours tolerates a pool with no relay at all (Pylon-fronted pools); theirs defines a pool by
  its relay.
- Region. Theirs sets region to the cluster name because Karmada gives nothing else; our
  cluster record has a real region. If their service read our imports it would still mislabel
  region, unless it also read our `ListClusters`.
- Peer sharing. Their instances find each other through their own exported Service and copy
  registrations over HTTP. On our MCS layer that mechanism works unchanged (the service's
  Service gets `gds.nvidia.com/export=true`), but it is redundant next to a shared store.

## 5. Assessment

Karmada gives Dynamo a working MCS quickly for a two-cluster POC, and the upstream code is
careful about Karmada's edges (name lengths, label absence, propagation pitfalls, dual stack).
It is not a fit for the HLD's fault-tolerance requirement, and the upstream work itself routes
around it in two places: the registration join exists because a frozen hub still says Ready,
and peer sharing exists because there is no shared store. Both are consequences of the hub
model.

The GDS design removes the hub, makes imports automatic, keeps the KEP labels so standard
consumers (including the upstream Standard-mode store) work, resolves export conflicts, and
carries real location data. What it lacks relative to the upstream work is the relay-centric
pool contract (`relay_identities`, `capabilities`, `capacity`, the `POST /pools` JSON) and the
operator integration, both of which are additive.

The practical reading: the upstream Full Kubernetes mode is "MCS plus a registry". Our design
is the same split with GDS playing both roles. Replacing Karmada with `gds-mcs-controller`
under their service is a configuration change for them (`--store mcs`), not a redesign.

## 6. Recommendations

1. Offer `gds-mcs-controller` upstream as the Standard MCS implementation for the Full
   Kubernetes mode, with the two-region e2e as evidence. The ask of them is nothing beyond
   `--store mcs`.
2. Add the small compatibility pieces already listed in `upstream-dynamo-comparison.md`
   section 7: `relay_identities`, `capabilities`, `capacity`, `namespace` on the Pool;
   role inference from configurable suffixes in our controller; a `/pools` and `/routers`
   JSON surface serving their shapes.
3. Raise upstream, with the Karmada evidence above: the `region = cluster` default (read the
   About API or a label instead), the hub single point of failure and `hostPath` etcd in the
   CDK, and that `ServiceImport` creation is manual per cluster pair.
4. Keep the registration direction neutral, as before. Nothing in the Karmada path forces
   pull; their `GET /routers` exists precisely so the relay can dial out.
