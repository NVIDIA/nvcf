# Network interaction comparison: Stargate and Pylon today versus the Global Router HLD

Status: v0.1, 2026-10-26. Intermediate analysis for the GDS design
(`global-discovery-service-design.md`). Code facts: `nvcf/nvcf-2` branch
`feat/inference-in-a-box` at `eb29b23ea` (Stargate, Pylon, Pylon Operator, chart
`deploy/helm/llm-request-router`) and `ai-dynamo/dynamo` main at `95cadce97d` (KV DC Relay).
Design facts: Global Router HLD [Kiro Edition] and its "LLD: The Global View".

Vocabulary as in the design: router = Stargate = Global Router; Pylon = Pool Relay; pool =
the HLD's routing unit.

## 1. Stargate and Pylon today

Every connection is initiated by the pool side (Pylon) and terminates at a Stargate pod. The
gateway and the router never dial into a pool.

| Connection | Dialer and listener | Protocol and port | Cardinality | What flows |
| :---- | :---- | :---- | :---- | :---- |
| Discovery | Pylon dials the seed `--stargate-address` (one URL, normally the backend-router Service, or a remote region's dial address) | gRPC `WatchStargates`, 50071 | one stream per seed, recursively following `watch_stargate_urls` | Full snapshot of replicas every 1 s, heartbeat 5 s: per-pod authority hostname, `grpc_pylon_dial_addr`, remote seeds |
| Registration | Pylon dials every Stargate replica through the k8s-router, selected by HTTP/2 authority | gRPC bidirectional `RegisterInferenceServer`, 50071 | pylon pods times router replicas | Full snapshot every 1 s: `inference_server_id`, URL, models with `ModelStats` (18 fields), `cluster_id`, bearer token |
| Reverse tunnel | Pylon dials every Stargate replica through the k8s-router, selected by SNI | QUIC, UDP 50072 (`raw-quic` default, `http3`, `webtransport`) | pylon pods times router replicas, one fresh stream per request | Inference requests and responses; 1 s heartbeats with stats; Stargate probes `/health` through it |
| Upstream | Pylon dials the inference server | HTTP, in-cluster Service URL | one per pylon | Proxied requests |
| Ingress | Gateway dials Stargate | HTTP 8000 via the ClusterIP Service; gRPC `ListModels` 50073 | load balanced, pod-local answers | Requests; model listing |
| Router peers | Stargate queries the headless Service | DNS SRV every 1 s | per pod | Peer list published in `WatchStargates` |
| Observability | Pylon Operator scrapes pylon pods | HTTP 9089 | per pod | Registration and tunnel state (source of the `Registered` condition) |

Shape. One headless Service covers all router replicas and one backend-router Service
(`sessionAffinity: ClientIP`) fronts the `stargate-k8s-router` pods. Per-replica identity is a
hostname used as HTTP/2 authority and QUIC SNI; it is never resolved. Each Stargate pod keeps
registrations in memory and does not replicate them, which is why Pylon fans out to every
replica. Multi-region today is a hand-maintained list of remote `WatchStargates` URLs followed
recursively, so a pylon ends up with streams and tunnels to every router replica in every
listed region. The direct connectivity mode (Stargate dials pylon's `quic://` listener) exists
but Pylon Operator does not use it. Authentication is a per-cluster bearer today, moving to
per-pod projected tokens (registration SRD); TLS on the registration listener is being added.

## 2. The HLD and LLD components

| Connection | Dialer and listener | Protocol | Cardinality | What flows |
| :---- | :---- | :---- | :---- | :---- |
| Ingress | Client to regional load balancer to Global Frontend | HTTP | per region | Requests; text tokenization at the Global Frontend |
| Pool selection and dispatch | Global Frontend to Global Router to Pool Frontend | HTTP (Dynamo frontend API), possibly cross-region | per request | The request; accepted or rejected by pool admission control; retry on another pool |
| Pool catalog, readiness, KV, load | Global Router replica dials the PoolRelay (pull, DEP 11225) | gRPC server streams `WatchKvPoolCatalog`, `SubscribeServingReadiness`, `SubscribeKvPool`, `SubscribeKvPoolLoad`; plaintext HTTP/2 on `--bind` (5561 in the Kubernetes guide) | every router replica times every pool (all-to-all) or hierarchical | Revisioned full snapshots plus deltas; 1 s load window; 10 s heartbeats |
| Registration | PoolRelay to Global Router replicas (push) or router discovers the relay (pull); open question | gRPC `RegisterPool`, `Heartbeat`, `DeregisterPool` if push | pools times replicas | Location, hardware, models, worker count, frontend endpoints, lifecycle |
| Pool and router discovery | PoolRelay and Global Router dial GDS | REST or gRPC | per relay, per replica | Routers list; pools list |
| Replica sync | Global Router to Global Router | existing event transport, new messages | replicas times replicas | Reserved load, session placement |
| Pool internals | Workers and frontends to PoolRelay | DC event plane (NATS or ZMQ) and discovery plane (etcd or Kubernetes) | inside the pool | KV events, load metrics, frontend load frames, affinity events |

Connectivity assumption: a flat network through VPC peering (HLD 2.5), so any pod can dial any
other pod across regions. The PoolRelay is off the request path.

## 3. Compare and contrast

| Dimension | Stargate and Pylon (built) | HLD and LLD (designed) | Consequence for GDS and MCS |
| :---- | :---- | :---- | :---- |
| Control-plane direction | Pool side dials out; the control plane never dials in | Inverted: the LLD specifies pull (router dials the relay's server); push registration undecided | GDS serves both: Routers for push, Pools with relay endpoints for pull. MCS makes connectivity symmetric, so direction is policy, not a network constraint |
| Why that direction | Reverse tunnel lets NAT'd or firewalled pools work and keeps inbound credentials off the router | Flat peering assumed; a pool that cannot accept inbound connections breaks pull | The design stays neutral. If a pool may be unreachable inbound, push (the Pylon pattern) is the robust choice |
| Request path | Through the control-plane connection: Stargate proxies over the pylon-initiated QUIC tunnel | Separate: Global Router dispatches HTTP directly to the Pool Frontend; the relay carries no requests | Cross-region requests need a routable Pool Frontend address (section 5) |
| State carried | Per server every 1 s in the registration stream and tunnel heartbeats; per-pod memory | Per pool, aggregated by the relay; revisioned snapshots and deltas; replicas sync reserved load and sessions | GDS carries none of it: membership and metadata only, with a 5 s target |
| Fan-out | pylon pods times router replicas; the k8s-router gives one dial address per router cluster | pools times router replicas (all-to-all) or hierarchical; one relay per pool | Same shape; the derived VIP of the backend-router or relay Service is the one-address-per-cluster MCS gives both |
| Discovery of the other side | Static seed plus recursive `WatchStargates`; peers by headless DNS | GDS (FR7) | GDS replaces the static seed for Pylon, the static remote list for Stargate and the static JSON pool list for the Global Router |
| Per-replica identity | Per-pod hostname as authority and SNI, never resolved; k8s-router forwards | Per-replica streams from each relay; no addressing scheme specified | Routers in GDS are per cluster with replica lists; Stargate consumers need only the cluster address, Dynamo consumers can use replica endpoints |
| Liveness | Stream close and idle timeouts (60 s, 300 s max); registration SRD adds a grace TTL | Relay heartbeats, lease expiry, draining before frontends stop | GDS cluster lease (30 s) plus ready endpoints; pool overlay for DRAINING; hot-path liveness stays on direct streams |
| HA of the pool side | N pylon pods, N registrations, no Service in front | PoolRelay HA model open (LLD question 7) | Service type decides: headless export for per-replica subscription, ClusterSetIP for a VIP; GDS returns both |
| Identity and transport security | Per-cluster bearer, per-pod OIDC planned; QUIC TLS; gRPC TLS being added | Relay plaintext; mTLS as a sidecar; private NLB bounds the trust domain | GDS v1 has no auth, matching the HLD trust assumption; the Authenticator seam mirrors the SRD pattern |
| Multi-region today | Manual remote URL lists; cross-region streams and tunnels from every pylon | Regional ingress, cross-region dispatch allowed, regional preference as a cost input | GDS gives every region the same catalog; MCS gives local VIPs for remote services; Route53 gives regional GDS entry points |

## 4. Implications for the GDS and MCS design

1. The design already matches the one pattern both systems share: one relay-like client per
   pool fanning out to every router replica through a single dialable address per router
   cluster. The derived VIP plus k8s-router authority forwarding preserves Stargate's mechanics,
   and the same VIP serves a Dynamo PoolRelay pushing to Global Routers.
2. Direction is the real divergence and GDS is neutral on it. Pull needs the relay Service
   exported and reachable inbound; push needs routers exported. Both roles are exported, so
   whichever DEP 11225 settles on works. The Pylon evidence favours push for resilience; this is
   a question for the HLD owners rather than for GDS.
3. GDS stays off both hot paths. Stargate shows that per-second per-server state belongs on
   direct streams and the HLD says the same for pool state. A 2 s refresh and 5 s bookmarks fit
   membership and would be wrong for load.
4. The cross-region request path was implicit in the design and is now explicit (section 5 and
   design section 4.3).
5. Pylon's reverse tunnel is stronger than the HLD assumes it needs. If a pool sits behind a
   non-peered boundary, the MCS VIP does not help because pod IPs are unreachable; only an
   outbound-dialing relay works. Routable pod IPs are therefore a stated prerequisite of the MCS
   options, not an implementation detail.

## 5. The cross-region request path

The HLD dispatches a request from a Global Router in region A to a Pool Frontend in region B.
What that path needs:

Protocol. Dynamo's Global Router today dispatches over the Dynamo request plane
(`runtime.endpoint("<ns>.router.generate")`), which presumes a discovery plane (etcd or
Kubernetes) and an event plane (NATS) shared with the pool. Neither spans clusters or regions.
Cross-region dispatch therefore has to be HTTP (or gRPC) to the Pool Frontend, which is also the
HLD's request step 4 and its open question 5 (Dynamo Frontend versus Pingora as the proxy
layer). The Pool record must carry a dialable Pool Frontend address per router cluster, not
only the pool's Dynamo namespace.

Reachability. Four ways for region A to reach a frontend in region B:

| Option | Requires | GDS provides | Controller provides |
| :---- | :---- | :---- | :---- |
| ClusterSetIP import of the frontend Service | VPC peering; routable, non-overlapping pod CIDRs; security groups allowing A's pod CIDR to B's frontend port | The import, so the client computes `derived-<hash>.<ns>.svc.cluster.local:<port>` locally; `ready_endpoint_count` | Derived Service and mirrored EndpointSlices; kube-proxy DNATs on the router's node to B's pod IP, no extra hop |
| Headless import | Same network assumptions | Per-replica endpoints in the pool record | Mirrored EndpointSlices; the router picks a replica |
| Regional LoadBalancer or PrivateLink in front of the frontend | No peering; one LB per pool | `frontends[].url` on the pool record | Nothing; the pool opts out of MCS for its data path |
| Reverse tunnel (Stargate pattern: a Pylon at the pool dials the Global Router cluster) | Outbound reachability only | Router discovery for the pylon | Export of the router Service only |

Health propagation lag. In one cluster kube-proxy drops a dead pod from a Service in about a
second. Through MCS the same change travels: EndpointSlice change, controller
`ReplaceEndpoints`, DynamoDB replication, up to one 2 s refresh tick, watch delivery, controller
apply in the router cluster. Roughly 3 to 6 s. During that window a VIP in region A still sends
new connections to a dead frontend in B. The Global Router's retry-next-best covers it; the
controller treats EndpointSlice readiness changes as the fast path (no coalescing beyond the
content hash) and propagates `serving` and `terminating`.

Draining order. The HLD requires a departing pool to leave the catalog before its frontends
stop. Deleting the frontend Service or removing the export label deletes the import everywhere
and the VIP vanishes, breaking in-flight responses. Safe sequence: set the pool overlay to
DRAINING (routers stop choosing it), keep the export until in-flight traffic completes, then
remove the Service. The auto-export reconciler keeps a pool's exports while it is DRAINING and
removes them only on GONE.

Reachability is per cluster, not global. GDS holds one catalog; whether region A can reach pool
B right now is a property of the A-to-B peering that only a router in A can observe. The HLD
already has each replica measure per-pool latency and tombstone bad pools. GDS supplies
candidates; routers supply reachability.

CNI and policy. NetworkPolicy cannot select remote pods (the KEP leaves this undefined), so
cross-cluster allow rules are CIDR based. A selectorless derived Service with controller-written
EndpointSlices works with kube-proxy in iptables or IPVS mode and with Cilium's kube-proxy
replacement. IP families must match across clusters (the merge intersects them). QUIC over a
ClusterSetIP VIP is a UDP DNAT and works where TCP does, which matters only if Stargate's tunnel
ever replaces the HTTP dispatch.
