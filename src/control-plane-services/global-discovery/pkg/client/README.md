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
- `PoolCache` and `RouterCache`, watch-driven caches with a TTL re-list;
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
`ByCluster(cluster)` and `Active()` (state ACTIVE, the pools a router may
choose).

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
status.Registration.Routers = len(routers.Snapshot().SeedURLs())
```

### MCS controller

The controller registers its cluster, heartbeats every TTL/3, re-registers
on `IsLeaseEpochMismatch` or NOT_FOUND, writes exports and endpoint sets with
a `Generation` per record, resends everything from an `OnReconnect` hook,
and runs a `WatchLoop` on `WatchServiceImports` with endpoints, garbage
collecting in `Synced`.

## Seed URLs

A pylon in cluster B reaches the routers of cluster A through the derived
Service of A's router import in B (design 4.5):

```text
<scheme>://<api.DerivedServiceName(namespace, name)>.<namespace>.svc.<cluster domain>:<grpc_port>
```

`RouterState.SeedURL(router)` computes it. The scheme is the router's
`dial_scheme`, else `RouterCacheConfig.DefaultScheme` (default https); the
cluster domain is `RouterCacheConfig.ClusterDomain` (default
`cluster.local`). It returns "" for a router without a gRPC port.

`RouterState.SeedURLs()` returns one URL per router cluster in cluster order:
the URL of the cluster's first router (by namespace and name) whose cluster
lease has not expired and that has a gRPC port. Clusters that export the same
namespace and name share one import and therefore one derived Service; their
URL is listed once.

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
