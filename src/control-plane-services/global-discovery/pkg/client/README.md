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

# GDS Go client

Package `pkg/client` is the Go client of the Global Discovery Service (GDS):
module `github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery`,
import path `.../global-discovery/pkg/client`. Its consumers are the
gds-mcs-controller, Pylon Operator and, optionally, the LLM API gateway. The
design is section 9.1 of `docs-gds/global-discovery-service-design.md`.

A `Client` holds one gRPC connection for unary calls and watches. On top of
it the package provides:

- typed sub-clients: `Clusters()`, `ServiceExports()`, `ServiceImports()`,
  `Pools()`, `Routers()` and `ServerInfo()`;
- endpoint failover with DNS re-resolution and a return to the primary URL;
- `WatchLoop`, a reconnecting watch with snapshot and resume handling;
- `PoolCache` and `RouterCache`, watch-driven caches with a TTL re-list,
  with router seed and pool agent helpers;
- `AgentRegistration`, one process instance's pool agent registration, kept
  alive by a heartbeat;
- `NextGeneration` and `Generation` for writer generations, and helpers that
  read GDS error details;
- `nvcf_gds_client_*` Prometheus metrics.

`pkg/fakegds` is an in-memory GDS server for tests of code that uses this
package (see Testing).

## Creating a client

```go
c, err := client.New(ctx, client.Config{
	URLs:       []string{"https://gds.prod.example.internal", "https://gds.us-west-2.prod.example.internal"},
	CAFile:     "/etc/gds/ca.crt",
	UserAgent:  "pylon-operator",
	Registerer: prometheus.DefaultRegisterer,
})
if err != nil {
	return err
}
defer c.Close()
```

`New` validates the configuration and returns at once; the client connects
in the background and calls wait for the connection within their deadline.

| Field | Default | Meaning |
| :---- | :---- | :---- |
| `URLs` | required | Ordered GDS URLs. One scheme for all; ports default to 443 (https) and 80 (http). |
| `CAFile`, `TLS` | system roots | CA bundle, or a base `tls.Config`. `CAFile` and `TLS.RootCAs` are exclusive. |
| `AllowInsecureHTTP` | false | Permit `http://` URLs. |
| `ConnectTimeout` | 5 s | One connection attempt or DNS lookup. |
| `RPCTimeout` | 10 s | Deadline of unary calls whose context has none; negative disables. |
| `PrimaryRetryInterval` | 5 m | Probe and return to the primary URL; negative disables. |
| `Backoff` | 200 ms to 30 s, x2, jitter 0.5 | Unary retries, connection attempts, watch reconnects, failed cache lists. |
| `MaxAttempts` | 4 | Attempts of a unary call failing with UNAVAILABLE; 1 disables retries; at most 5. |
| `UserAgent` | `nvcf-gds-client-go` | gRPC user agent prefix. |
| `Registerer` | nil | Receives the metrics; nil registers nothing. |
| `Resolver` | `net.DefaultResolver` | Resolves URL host names on every reconnect. |
| `Dialer` | TCP | Replaces the TCP dialer (tests, bufconn). |
| `DialOptions` | none | Extra gRPC dial options, for example an OpenTelemetry stats handler. |
| `Logger` | `slog.Default()` | Failover, watch and cache logs. |

`Close` stops the background work and closes the connection. Stop watch
loops and caches first; they keep retrying against a closed client
otherwise.

## Failover

Every consumer takes an ordered list of URLs, normally the global Route53
name first and the per-region names after it (design 4.7).

- A per-client gRPC resolver (a unique scheme per `Client`, registered with
  `grpc.WithResolvers`) resolves every URL host name in order with
  `Config.Resolver` when the connection starts and whenever gRPC asks for a
  re-resolution, which it does after every lost or failed connection. Route53
  failover therefore applies on the next reconnect. Re-resolutions are spaced
  by at least one second.
- Each address carries its URL's host as the TLS server name and HTTP/2
  authority, so one connection can verify certificates of several names.
- The service config selects `pick_first` without shuffling: the client
  connects to the first address that answers, in URL order, and stays there
  while it is healthy. Address families are not interleaved.
- After each new connection the client calls `GetServerInfo` and appends the
  advertised `endpoints[].grpc_url` values that are valid, of the client's
  scheme and not configured already, after the configured URLs.
- Every `PrimaryRetryInterval` a client connected to another URL re-resolves
  and probes the primary URL with `GetServerInfo` over a separate connection.
  When the probe succeeds it moves the connection to the primary and restarts
  its `WatchLoop` streams there. Calls in flight finish on the old
  connection.
- All replicas serve the same data, so any replica will do. A watch that
  reconnects to another replica receives a fresh snapshot.

`OnReconnect(func(url string))` registers a hook that runs after every
connection the client establishes after its first one. Hooks run one at a
time on a background goroutine; reconnects during a hook coalesce into one
call with the latest URL. `CurrentURL()` and `URLs()` report the state.

## Retries and deadlines

Unary calls without a deadline get `RPCTimeout`. The service config adds a
gRPC retry policy on UNAVAILABLE for every unary GDS method with
`MaxAttempts` and `Backoff`; it applies to `Conn()` users too. Other codes
are not retried. Watches are not retried by gRPC; `WatchLoop` reconnects
them.

## Writers: generations and errors

Writes carry a generation that must exceed the stored one. Use
`NextGeneration(prev)`, `max(prev + 1, unix milliseconds)`, or a
`Generation` per record:

```go
gen := &client.Generation{}
req := &gdsv1.ReplaceEndpointsRequest{ /* ... */ Generation: gen.Next()}
_, err := c.ServiceExports().ReplaceEndpoints(ctx, req)
if cur, ok := client.StaleGeneration(err); ok {
	gen.Observe(cur)
	req.Generation = gen.Next()
	_, err = c.ServiceExports().ReplaceEndpoints(ctx, req)
}
```

`Reason(err)` returns the GDS error reason; `IsLeaseEpochMismatch` and
`IsClusterNotRegistered` cover the lease cases: register again and resend
state.

## Watches

`WatchLoop` keeps one watch open:

```go
loop := client.NewWatchLoop(c, client.WatchLoopConfig[*gdsv1.ServiceImport]{
	Name:  client.WatchServiceImports,
	Watch: c.ServiceImports().Watcher(&gdsv1.WatchServiceImportsRequest{Clusterset: cs, IncludeEndpoints: true}),
	Handler: client.WatchHandler[*gdsv1.ServiceImport]{
		SnapshotStart: func() { seen = map[string]bool{} },
		Upsert:        apply,
		Delete:        remove,
		Synced:        func(string) { garbageCollect(seen) },
	},
	OnResync: func() { log.Info("full resync") },
})
go loop.Run(ctx)
```

- `SNAPSHOT_START` calls `SnapshotStart` (discard local state) and clears the
  resume revision, so a stream that breaks during a snapshot is never
  resumed from the revision before it.
- `UPSERT` and `DELETE` call `Upsert` and `Delete` and advance the revision.
- `SNAPSHOT_END` calls `Synced` with the revision; the state is complete.
- `BOOKMARK` calls `Bookmark` and advances the revision.
- A stream that ends or fails is reopened after `Backoff` (reset after a
  stream that delivered events) with the last revision as
  `resume_revision`. The server replays the missed deltas or sends a new
  snapshot. A new snapshot after the loop had synced calls `OnResync` first.
- `IdleTimeout` reopens a stream that delivered nothing, not even a bookmark,
  for that long. The server sends bookmarks every 5 s.
- Each reopen increments `nvcf_gds_client_watch_reconnects_total{watch=Name}`.

## Caches

`PoolCache` and `RouterCache` hold the pools or routers that match a filter:

1. A full `ListPools` (or `ListRouters`), retried with backoff until it
   succeeds. `WaitSynced` returns after it.
2. A `WatchLoop` resumed from the list's revision applies deltas. A new
   snapshot is collected aside and swapped in at `SNAPSHOT_END`, so a
   snapshot never exposes a partial state.
3. A full list every `TTL` (default 30 s) replaces the state (anti-entropy,
   the Kiro "fetch all pools on expiration"). A failed list is retried with
   backoff, at most every TTL.
4. `Stale` is set when no list and no watch event (bookmarks included)
   succeeded for `StaleAfter` (default 2 x TTL). The state is kept. It clears
   on the next success.

`Snapshot()` returns an immutable state; never modify the protos it holds.
`Updates()` subscribes a channel that receives a value after the pools,
`Synced` or `Stale` changed (not after bookmarks); changes coalesce, so read
`Snapshot()` after each receive. Call it once per consumer. The channel is
closed when `Run` returns. `Refresh` lists at once; `LastError` returns the
error of the last list. `Run` blocks until its context is done; with
controller-runtime add it as `manager.RunnableFunc(cache.Run)`. Give each
cache in a process its own `Name`: it labels the cache and watch metrics.

`PoolState` offers `Get(poolID)`, `ByModel(model)` (model id or alias),
`ByCluster(cluster)`, `Active()` (state ACTIVE, the pools a router may
choose) and `Ingress()` (pools with a live ingress agent). Per pool:

- `IngressLive(pool)` reports a live ingress: `ready_ingress_count` above 0
  (ready OPENAI and PYLON endpoints) or a live OPENAI or PYLON agent. A pool
  reached only through Pylon's reverse tunnel has no OPENAI frontend and a
  `ready_endpoint_count` of 0, but a live ingress. `Ingress()` does not
  check the state, so it includes DRAINING pools; a pool whose only live
  agent is its KV relay and an API-only pool without agents are not
  included.
- `Agent(pool, role)` returns the pool's agent with a role, or nil. GDS keeps
  one agent per role, merged from the role's exported Service and its
  `RegisterPoolAgent` registrations (`source` LABEL, API or BOTH). Its
  `lease`, `agent_id` and `attributes` mirror the freshest registration.
- `Registrations(pool, role)` returns the agent's registrations, one per
  process instance (for example one per Pylon pod), sorted by agent id.
  Registrations whose lease expired stay listed until GDS removes them;
  check `lease.state`, or read the agent's `active_registration_count`.
- `KVRelayLive(pool)` reports a live KV relay (Dynamo PoolRelay):
  `kv_relay_live` or a live KV_RELAY agent.

### LLM API gateway or Global Router

```go
pools, err := client.NewPoolCache(c, client.PoolCacheConfig{
	Filter: &gdsv1.ListPoolsRequest{Clusterset: cs, IncludeEndpoints: true},
})
go pools.Run(ctx)
if err := pools.WaitSynced(ctx); err != nil {
	return err
}
updates := pools.Updates()
for range updates {
	st := pools.Snapshot()
	if st.Stale {
		log.Warn("GDS pool data is stale")
	}
	rebuildRoutingTable(st.Active())
}
```

A pool's dispatch address is its `frontends[]` entry with role OPENAI: a
static `url`, or a `service` that resolves to the derived Service in the
caller's cluster, `api.DerivedServiceName(namespace, name)` +
`.<namespace>.svc.cluster.local` with the port named `port_name`.

### Pylon Operator

```go
routers, err := client.NewRouterCache(c, client.RouterCacheConfig{
	Filter: &gdsv1.ListRoutersRequest{Clusterset: cs},
})
go routers.Run(ctx)
if err := routers.WaitSynced(ctx); err == nil && len(routers.Snapshot().SeedURLs()) == 0 {
	log.Warn("GDS knows no router; pylons will wait")
}
status.Registration.Routers = len(routers.Snapshot().SeedRouters())
```

A router that also dispatches through Pylon tunnels selects among the
ACTIVE pools and reads the agents:

```go
for _, p := range st.Active() {
	pylon := client.Agent(p, gdsv1.FrontendRole_FRONTEND_ROLE_PYLON) // nil without a Pylon agent
	table.Add(p, pylon.GetLive(), client.KVRelayLive(p))
}
```

### MCS controller

The controller registers its cluster, heartbeats every TTL/3, re-registers
on `IsLeaseEpochMismatch` or NOT_FOUND, writes exports and endpoint sets with
a `Generation` per record, resends everything from an `OnReconnect` hook,
and runs a `WatchLoop` on `WatchServiceImports` with endpoints, garbage
collecting in `Synced`.

## Seed URLs

A pylon reaches the routers of a cluster through the ready replicas of that
cluster's Router record (design 4.5). Each replica is one seed:

```text
<scheme>://<replica address>:<grpc_port>
```

IPv6 addresses are bracketed. The scheme is the router's `dial_scheme`, else
`RouterCacheConfig.DefaultScheme` (default https); a scheme other than http
or https yields no seeds. The port is the router Service's gRPC port, so the
Service must use the same number for the port and its target port (the
llm-request-router chart does).

- `RouterState.RouterSeedURLs(router)` returns the seeds of one router, one
  per ready replica, sorted and without duplicates. Replicas that are not
  ready or have an unusable address are skipped.
- `RouterState.SeedURLs()` returns the seeds of every router whose cluster
  lease has not expired, in router order (cluster, namespace, name), each URL
  once. A router cluster therefore contributes N URLs, one per
  stargate-k8s-router pod (or Stargate pod when the router Service selects
  Stargate directly). The pylon opens `WatchStargates` on each of them and
  fails over between them.
- `RouterState.SeedRouters()` returns the routers that contribute seeds, for
  example to report a router count.

Seeds are per cluster on purpose. Every cluster deploys the same router
Service (for example `nvcf/llm-request-router-backend`), and by MCS semantics
exports of one namespace and name merge into one ServiceImport. Its derived
Service therefore spreads over the k8s-router pods of all router clusters,
and a pylon dialing it could reach cluster B's k8s-router while registering
with a Stargate pod of cluster A, which B cannot forward to.

`RouterState.DerivedServiceURL(router)` still returns that derived Service,

```text
<scheme>://<api.DerivedServiceName(namespace, name)>.<namespace>.svc.<cluster domain>:<grpc_port>
```

with the cluster domain from `RouterCacheConfig.ClusterDomain` (default
`cluster.local`). Use it only where the merged VIP is wanted; it is never a
seed.

### Relay seeds, QUIC and other ports

Two kinds of pool-side process dial routers from the same Router record
(design 4.5). Pylon uses `SeedURLs`; the Dynamo PoolRelay opens its state
streams over gRPC and uses `RelayURLs`:

- `RouterState.RouterRelayURLs(router)` returns the relay seeds of one
  router and `RouterState.RelayURLs()` those of every router whose cluster
  lease has not expired, with the rules and order of the pylon seeds. Today
  they equal `RouterSeedURLs` and `SeedURLs`, since the relay dials the same
  gRPC listener. They are separate functions so that a relay listener or
  scheme can diverge without touching Pylon; use the one of your process.
- `RouterState.RouterQUICAddrs(router)` returns
  `<replica address>:<quic_port>` per ready replica (IPv6 bracketed, sorted,
  once each), or nil when the router has no QUIC port. The QUIC port comes
  from the port named by `gds.nvidia.com/quic-port-name` (default `quic`).
  It is informational for Pylon, whose tunnel address comes from the
  registration ack. The dial scheme and the cluster lease are not checked.
- `Router.Ports` lists every port of the exported Service with its name,
  protocol and number, for consumers whose protocol GDS does not name.

## Pool agents

A pool agent is one process kind of a pool: the OpenAI frontend (OPENAI),
the Dynamo PoolRelay (KV_RELAY) or Pylon (PYLON) (design 4.6). Besides the
label source (the role's exported Service), every process instance
registers itself with `RegisterPoolAgent` under its own agent id; GDS keys
registrations by (cluster, pool name, role, agent id), and the agent is live
while any of them is. `AgentRegistration` makes one registration and keeps
its lease alive, the Go counterpart of the Rust `AgentRegistration`:

```go
reg, err := client.RegisterAgent(ctx, c, client.AgentSpec{
	Clusterset: cs,
	Cluster:    clusterID,
	PoolName:   "llama-blue",
	Role:       gdsv1.FrontendRole_FRONTEND_ROLE_PYLON,
	AgentID:    podName,
	Attributes: map[string]string{"models": "llama-3"},
})
if err != nil {
	return err
}
defer reg.Close(context.WithoutCancel(ctx))
reg.SetAttributes(map[string]string{"models": "llama-3,mistral"})
```

| Field | Default | Meaning |
| :---- | :---- | :---- |
| `Clusterset`, `Cluster`, `PoolName` | required | The pool. GDS creates it when it does not exist, under the deterministic pool id of (clusterset, cluster, pool name), so a registration and a labelled export converge on one pool. |
| `Role` | required | OPENAI, KV_RELAY or PYLON. |
| `AgentID` | required | The process instance, for example a Pylon `inference_server_id` or a relay pod name; at most 253 bytes (`MaxAgentIDLength`). |
| `Attributes` | none | Writer-owned facts of this registration, at most 16 entries. |
| `LeaseTTL` | 60 s | Agent lease; GDS clamps it to [10 s, 5 m]. |
| `HeartbeatInterval` | lease / 3 | A third of the lease GDS granted, else of `LeaseTTL`. |

- `RegisterAgent` validates the spec and calls `RegisterPoolAgent` with a
  new `Generation`; after STALE_GENERATION it retries once above the stored
  generation. It returns the error of a failed first registration. Its
  context bounds that call only.
- A goroutine heartbeats every interval, shortened by up to 20 percent of
  jitter. Heartbeats carry the agent id and this registration's epoch. On
  NOT_FOUND (GDS lost the registration) or ABORTED with
  `LEASE_EPOCH_MISMATCH` (a registration under the same agent id minted a
  new epoch) it registers again with the current spec. Other errors are
  retried after the client's `Backoff`, at most every interval; the lease
  expires if they last longer than the lease.
- `SetAttributes` replaces the attributes; the next heartbeat sends them
  and later registrations carry them. A heartbeat replaces attributes only
  with a non-empty map, so clearing them registers again (a new epoch).
- `State()` returns the pool id, the derived pool state, the epoch and lease
  of this registration as of the last success, the consecutive failures,
  the last error, and the `Stopped` and `Deregistered` flags. The epoch
  comes from the response's `registration`, not from the merged agent,
  which mirrors the freshest registration of the role. `Updates()`
  subscribes a channel that receives a value when one of them changes
  (plain lease renewals do not count) and is closed after `Close` or when
  the `Client` is closed.
- `Close(ctx)` stops the heartbeat and calls `DeregisterPoolAgent` with the
  agent id. The agent stays while its export or another registration
  remains, the pool while an export, the pool registration or another agent
  remains. When the deregistration fails, the lease expires. Close the
  registration before the `Client`; closing the client stops the heartbeat.
- Make one registration per process instance: N Pylon pods are N
  `AgentRegistration`s with N agent ids, and their heartbeats never fence
  each other. Two registrations that share an agent id take each other's
  epoch on every heartbeat.

`Pools()` also exposes the RPCs directly: `RegisterAgent`, `HeartbeatAgent`
and `DeregisterAgent`; heartbeat and deregistration requests name the
registration with `agent_id`.

## Metrics

Registered on `Config.Registerer` when it is set. Clients sharing a
registerer share the collectors.

| Metric | Type | Labels | Meaning |
| :---- | :---- | :---- | :---- |
| `nvcf_gds_client_requests_total` | counter | `rpc`, `code` | Calls by RPC and gRPC code; a watch counts once, when it ends. Pre-initialised for every RPC and code. |
| `nvcf_gds_client_request_duration_seconds` | histogram | `rpc` | Unary call duration, retries included. |
| `nvcf_gds_client_endpoint_failovers_total` | counter | | New connections to a different URL than the previous one, the return to the primary included. |
| `nvcf_gds_client_connected` | gauge | `url` | 1 for the URL in use, 0 for the other configured and advertised URLs. |
| `nvcf_gds_client_watch_reconnects_total` | counter | `watch` | Watch reopens. Pre-initialised for `clusters`, `service_imports`, `pools`, `routers`. |
| `nvcf_gds_client_cache_age_seconds` | gauge | `cache` | Seconds since a running cache last confirmed its data. |

## Testing

`pkg/fakegds` implements the five services in memory with leases, epochs,
generations, derived pools and routers, merged imports and watches with
revisions, bookmarks and resume. It serves over bufconn or `127.0.0.1:0`:

```go
srv := fakegds.New()
run, _ := srv.ServeLocal()
defer run.Stop()
srv.PutExport("prod", "a", "llm", "backend-router", fakegds.RouterSpec(50071, 0, "https"), fakegds.ReadyEndpoints("10.0.0.1"))
c, _ := client.New(ctx, client.Config{URLs: []string{run.URL()}, AllowInsecureHTTP: true})
```

`FailNext(method, code)` injects failures, `CloseWatches` ends open watches,
`Running.Stop` drops a listener and `ServeAddr` restarts it on the same
address. See the package documentation for the differences from gds-server.

## Limitations

- One scheme for all URLs: a client is either TLS or plaintext. Advertised
  fallbacks of the other scheme are ignored.
- The custom dialer bypasses gRPC's HTTPS proxy support; GDS is reached
  inside the VPC.
- No keepalive is configured by default, because the server's enforcement
  policy is not known to the client; pass `grpc.WithKeepaliveParams` in
  `DialOptions` if needed. Caches use a 30 s watch idle timeout instead.
