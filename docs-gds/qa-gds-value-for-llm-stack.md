# Q&A: why GDS and MCS help the unmodified LLM stack

Status: 2026-10-26. Intermediate discussion note. It records a question asked during the
design review and the answer; it does not change the design in
`global-discovery-service-design.md`.

## Question

Why are GDS and MCS beneficial to the unmodified LLM stack (gateway, Stargate, Pylon)? The
stack seems to work around all discovery issues with `WatchStargates`, assuming the seed is
serving. Is there a way to make the seed more reliable, or to simply point the stack at GDS to
consume Stargate addresses directly, without MCS?

## Answer

Short answer: for gateway, Stargate and Pylon as they stand, nearly all the value is in GDS as
a dynamic, replicated seed source. MCS adds little for that stack specifically; its payoff is
cross-cluster addressability and Kubernetes-native consumption, which matter for the Dynamo
pool fabric more than for the LLM stack.

### What the unmodified stack lacks

`WatchStargates` is a good mesh once a pylon is in it: every seed returns the full replica set
plus `watch_stargate_urls`, and the pylon recomputes its desired watch set on every snapshot,
so router churn inside a known cluster is handled. The static parts are the edges of that mesh:

- Bootstrap. Pylon takes one `--stargate-address`. If that Service, its NLB or the whole
  router cluster is down when a pylon starts or restarts, the pylon has nothing to follow. A
  running pylon survives a seed outage only as long as its other watch streams stay up; its
  topology is in memory, not persisted.
- Region membership. The recursive seeds come from each router's `--remote-stargate-url` list.
  Adding or removing a router region is a chart change rolled to every other region, and until
  it rolls, pylons in the new region are invisible to the old ones. That is the HLD's FR7
  failure at the router layer rather than the pool layer.
- Addressability. Each router cluster needs a dial address that pylons in other clusters can
  reach (an internal NLB plus DNS plus certificate SANs), configured by hand per cluster. The
  gateway has no view of which clusters exist beyond `ListModels` counts per `cluster_id`.
- No catalog. Nothing records what a cluster is (region, hardware, lifecycle). Draining a
  cluster for blue/green means deregistering pylons one by one; there is no "this pool is
  draining" signal the router can act on ahead of time.

### Making the seed more reliable without GDS

Three cheap steps exist today:

1. pylon-lib already accepts a list of seeds (`InferenceServerRegistrationConfig.seeds`); only
   the CLI exposes one. A repeatable flag gives multi-seed bootstrap.
2. A health-checked Route53 name for the seed gives regional failover.
3. Persisting the last known router set to disk lets a restarted pylon rejoin without the seed.

Those fix bootstrap but not region membership or the catalog, because the static
`--remote-stargate-url` lists remain the source of truth.

### Pointing the stack at GDS directly, without MCS

Yes, and the design already contains this path: `RouterCache` feeding `pylon_seed_urls` and
Stargate's `discover_remote_watch_urls`. GDS becomes the replicated, multi-region, lease-backed
seed source, and routers stop carrying remote lists.

MCS contributes nothing to that path any more: since design v0.3 the seeds are the per-cluster
k8s-router replica pod addresses GDS already carries (reachable over VPC peering), not an MCS
VIP. Without MCS the same seeds work; if pod IPs were not routable, GDS could instead hand out
a dialable URL per router cluster (an annotation such as a pylon dial address on the exported
Service, forwarded like `gds.nvidia.com/dial-scheme`), and pylons would dial each router
cluster's internal NLB exactly as they do today across regions.
Everything else (TLS, authority and SNI forwarding, ClientIP affinity) is unchanged. For the
LLM stack alone, GDS without MCS is a coherent and smaller deployment.

### What MCS buys that GDS as a seed source does not

- No per-cluster NLB, DNS record or certificate SAN work for cross-cluster reachability. The
  derived VIP is a local ClusterIP that kube-proxy maps to remote pod IPs over the peering;
  adding a router cluster is zero network configuration in every consumer cluster.
- Readiness-aware, replica-level endpoints materialised as ordinary Kubernetes objects.
  Anything in the cluster can consume them (the gateway, CoreDNS, NetworkPolicy tooling, Dynamo
  components, kubectl) without linking a GDS client library.
- A standard API that other implementations also speak, so the same exports work if a cluster
  later runs a different MCS controller or DNS plugin.
- Symmetric connectivity, which makes the pull-versus-push question for the PoolRelay a policy
  choice rather than a network constraint.

Those are properties the Dynamo pool fabric needs (Global Routers dispatching HTTP to pool
frontends across regions, relays and frontends as plain Services) and the LLM stack does not,
because Pylon already solved reachability with an outbound tunnel.

### Framing

MCS is in the design because the requirement was MCS-API consumption from any cluster and
because the Global Router needs it. The LLM stack benefits mostly from the GDS half. Nothing in
this note changes the design; the "GDS without MCS" address extension is recorded here as a
possible later option only.
