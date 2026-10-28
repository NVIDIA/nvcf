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

# gds-client

Rust client for the Global Discovery Service (GDS), the multi-region pool and
router registry of the Dynamo Global Router. Stargate, Pylon and the Dynamo
PoolRelay use it. The server, the Go client and the proto contract live in
`src/control-plane-services/global-discovery`; the design is
`docs-gds/global-discovery-service-design.md` (sections 4.5, 4.6, 4.7, 5 and
9.2).

The crate provides:

- endpoint failover over an ordered list of GDS URLs (`endpoints`, `channel`)
- typed calls with deadlines and retries (`client`)
- TTL-cached, watch-driven pool and router views (`cache`, `routers`)
- the seed URLs pylons, PoolRelays and Stargates dial (`routers`)
- pool registration with heartbeats and a draining shutdown (`register`)
- typed pool agent registration for Pylon and the PoolRelay (`register`)
- derived Service names and the well-known labels (`derived`, `labels`)

## Dependencies and features

The crate depends on tonic (with TLS), tonic-prost, prost, prost-types,
tokio, tokio-stream, tokio-util, futures, thiserror, tracing, rand, http,
sha2 and data-encoding. It has no reqwest, hickory-resolver, moka or serde
dependency, so Dynamo can adopt it as is. It re-exports `tonic`, `prost` and
`prost_types`; use `gds_client::tonic` instead of pinning another version.

| Feature | Adds |
| :---- | :---- |
| `metrics` | `GdsClientMetrics`, the `nvcf_gds_client_*` Prometheus metrics (`prometheus` dependency) |
| `stargate` | `stargate::router_to_stargate_info` for diagnostics (`stargate-proto` dependency) |

## Usage

### Create a client

```rust
use gds_client::{GdsClient, GdsClientConfig, TlsOptions};

let client = GdsClient::new(GdsClientConfig {
    // The global name first, optional per-region fallbacks after it.
    urls: vec![
        "https://gds.example.internal".to_string(),
        "https://gds.us-east-1.example.internal".to_string(),
    ],
    tls: TlsOptions::from_ca_file("/etc/gds/ca.crt")?,
    ..GdsClientConfig::default()
})?;
```

`GdsClient::new` connects lazily. A value without a scheme is taken as
`https://`. `http://` URLs are rejected unless `policy.allow_insecure_http` is
set. `GdsClient::from_channel` wraps a tonic `Channel` the host built (custom
connector, in-process transport); retries apply, failover does not.

### Stargate: remote watch URLs

A Stargate adds the seed URLs of every router cluster other than its own (one
per ready router replica) to `watch_stargate_urls`, unioned with the static
`--remote-stargate-url` list on every poll tick.

```rust
use gds_client::{RouterCache, RouterCacheConfig, remote_watch_urls};

let routers = RouterCache::spawn(client.clone(), RouterCacheConfig::new("prod"), stop.clone());
// On every poll tick:
let dynamic_urls = remote_watch_urls(&routers.snapshot(), "cluster-a");
```

### Pylon: dynamic seeds

A pylon opens `WatchStargates` on the seeds of every router cluster, its own
included. Feed the seeds into a watch channel and let the registration loop
react to changes.

```rust
use std::collections::BTreeSet;

use gds_client::{RouterCache, RouterCacheConfig, pylon_seed_urls};

let routers = RouterCache::spawn(client.clone(), RouterCacheConfig::new("prod"), stop.clone());
let mut updates = routers.subscribe();
let (seeds_tx, seeds_rx) = tokio::sync::watch::channel(BTreeSet::new());
tokio::spawn(async move {
    loop {
        let seeds = pylon_seed_urls(&updates.borrow_and_update());
        seeds_tx.send_if_modified(|current| {
            let changed = *current != seeds;
            if changed {
                *current = seeds;
            }
            changed
        });
        if updates.changed().await.is_err() {
            break;
        }
    }
});
```

A seed is one ready replica of a router, as GDS reports it:
`<scheme>://<replica address>:<grpc_port>`, with IPv6 addresses in brackets
(`router_seed_urls(router)`). The scheme is the router's
`gds.nvidia.com/dial-scheme` (`https` when unset; other values yield no
seeds). The port is the router Service's gRPC port, so the Service must use
the same number for the port and its target port, as the llm-request-router
chart does. Replicas that are not ready, routers without a gRPC port and
routers whose cluster lease expired are skipped. Each router cluster
contributes one URL per stargate-k8s-router (or Stargate) pod; the pylon
watches all of them and fails over between them.

Seeds are per cluster on purpose. Every cluster deploys the same router
Service (for example `nvcf/llm-request-router-backend`), and by MCS semantics
exports of one namespace and name merge into one import. Its derived Service
spreads over the k8s-router pods of all router clusters, so a pylon dialing
it could reach cluster B's k8s-router while registering with a Stargate pod
of cluster A, which B cannot forward to. `derived_service_url(router)` still
returns that merged VIP,
`<scheme>://derived-<hash>.<namespace>.svc.cluster.local:<grpc_port>`, for
callers that want it; it is never a seed.

Pylon dials the gRPC seeds only. Its QUIC tunnel goes to the address in the
Stargate registration ack, so the router's `quic_port(router)` (annotation
`gds.nvidia.com/quic-port-name`, default `quic`) is informational for it.

### Pylon: pool agent registration

With `--gds-pool-name` a pylon also registers itself as the PYLON agent of
its pool: the cluster is `--cluster-id`, the agent id
`--inference-server-id`, and the `models` attribute lists the served model
ids. The registration gives GDS the process identity and model list a
Service cannot carry, and keeps the pool visible for the agent lease when no
Service is exported yet. Pylon deregisters the agent after its tunnel drain
on shutdown; the agent lease covers a failed deregistration.

```rust
use std::collections::BTreeMap;

use gds_client::v1::FrontendRole;
use gds_client::{AgentRegistration, AgentSpec};

let mut spec = AgentSpec::new("prod", "cluster-b", "llama-blue", FrontendRole::Pylon, "pylon-0");
spec.attributes = BTreeMap::from([("models".to_string(), "meta/llama-3.1-70b".to_string())]);
let agent = AgentRegistration::register(client.clone(), spec, stop.clone()).await?;

agent.set_attributes(attributes); // sent with the next heartbeat
agent.shutdown().await?; // after the tunnel drain
```

A frontend process that embeds Pylon exports one Service annotated
`gds.nvidia.com/roles: pool-frontend,pylon`; GDS turns it into an OPENAI and
a PYLON agent backed by the same endpoints. Its Pylon may still register
with `--gds-pool-name` to add its identity and model list; GDS merges the
label and API sources of each role (`source` `BOTH`).

### Dynamo PoolRelay: agent registration and router seeds

A PoolRelay registers itself as the KV_RELAY agent of its pool and
subscribes to every Global Router replica. It opens its state streams over
gRPC on the routers' gRPC port and uses no QUIC. `relay_seed_urls(state)`
returns `<scheme>://<replica address>:<grpc_port>` for every ready replica of
every router whose cluster lease is live, its own cluster included;
`router_relay_urls(router)` does the same for one router. Today these equal
the Pylon seeds. They are separate functions so that a relay listener or
scheme can diverge later without touching Pylon.

```rust
use gds_client::v1::FrontendRole;
use gds_client::{
    AgentRegistration, AgentSpec, RouterCache, RouterCacheConfig, relay_seed_urls,
};

let spec = AgentSpec::new("prod", "cluster-c", "llama-blue", FrontendRole::KvRelay, pod_name);
let agent = AgentRegistration::register(client.clone(), spec, stop.clone()).await?;

let routers = RouterCache::spawn(client.clone(), RouterCacheConfig::new("prod"), stop.clone());
let mut updates = routers.subscribe();
loop {
    let urls = relay_seed_urls(&updates.borrow_and_update());
    // Open state streams to new URLs and close the removed ones.
    if updates.changed().await.is_err() {
        break;
    }
}
agent.shutdown().await?; // on SIGTERM
```

A pool whose only live agent is the relay stays REGISTERED: routers learn
its KV cache facts (`kv_relay_live`) but need a live ingress agent (OPENAI or
PYLON) to send it requests. Both Pylon and the PoolRelay may register as
agents of the same pool; they share its `pool_id`.

### Dynamo PoolRelay: pool registration

Pools exported through labelled Services need no API calls. The registration
overlay adds worker counts and the DRAINING signal, and registers pools that
are not Kubernetes Services (a static frontend URL).

```rust
use std::time::Duration;

use gds_client::v1::{FrontendEndpoint, FrontendRole, ModelServed};
use gds_client::{DesiredState, PoolRegistration, PoolSpec};

let mut spec = PoolSpec::new("prod", "cluster-c", "llama-blue");
spec.models = vec![ModelServed {
    model_id: "meta/llama-3.1-70b".to_string(),
    ..ModelServed::default()
}];
spec.frontends = vec![FrontendEndpoint {
    role: FrontendRole::Openai as i32,
    url: "https://llama-blue.example.internal".to_string(),
    ..FrontendEndpoint::default()
}];
spec.owner_id = pod_name;
let registration = PoolRegistration::register(client.clone(), spec, stop.clone()).await?;

registration.report_workers(16, 14); // sent with the next heartbeat
registration.set_desired_state(DesiredState::Draining).await?; // blue/green cut-over
registration.shutdown(Duration::from_secs(60)).await?; // on SIGTERM
```

The relay uses `PoolRegistration` only for what agents do not carry: worker
counts, the DRAINING signal and pools that are not Kubernetes Services.

### Global Router: pool view

```rust
use gds_client::{PoolCache, PoolCacheConfig};

let pools = PoolCache::spawn(client.clone(), PoolCacheConfig::new("prod"), stop.clone());
let state = pools.wait_synced().await?;
for pool in state.active().filter(|pool| pool.ready_endpoint_count > 0) {
    // choose a pool, dial its OPENAI frontend
}
```

`PoolState` here is the cache view (pools keyed by `pool_id`, revision,
`fetched_at`, `synced`, `stale`) with `by_cluster`, `by_model` and `active`
iterators. The derived pool state enum is `gds_client::v1::PoolState`.

## Failover

- URLs are tried in configured order. Every connection attempt builds a new
  tonic `Endpoint`, so host names are resolved again and Route53 failover
  applies on the next reconnect.
- A call that fails with UNAVAILABLE or DEADLINE_EXCEEDED, or a watch stream
  that breaks, marks the endpoint failed. The next call connects to the next
  candidate: the configured URLs, then the `grpc_url` values GDS advertises in
  `GetServerInfo.endpoints` (fetched after every connect), wrapping around.
- While the client is off the primary it tries the primary every
  `primary_retry_interval` (5 min) and switches back once it connects. Caches
  reopen their watch on the new connection.
- Unary calls make up to `max_attempts` (4) attempts with a per-attempt
  deadline (`rpc_timeout`, 10 s) and a jittered exponential backoff (200 ms
  to 30 s). Other codes are returned at once; `Error::reason()` and
  `Error::details()` expose the GDS `ErrorInfo`.
- `subscribe_connection()` publishes `Connected { url, generation, primary }`
  and `Disconnected` states.

## Caches

`PoolCache` and `RouterCache` run one background task each:

1. List, and publish the full set with its revision.
2. Watch from that revision. GDS replays the missed deltas or, after a
   failover or a replica restart, sends a fresh snapshot. Snapshot objects
   are collected between SNAPSHOT_START and SNAPSHOT_END and replace the view
   at once; UPSERT and DELETE deltas apply directly; BOOKMARK only advances
   the revision. Revisions are opaque.
3. Every `ttl` (30 s) close the watch and list again (anti-entropy).

A broken watch is reopened from the last revision with backoff. The view is
`stale` when GDS was not heard from (a list, an event or a bookmark) for
`stale_after` (60 s); the last known objects stay available. `wait_synced`
waits for the first list or snapshot, `refresh_now` lists at once, and
dropping the cache or cancelling its token stops the task.

## Registration

- `PoolRegistration::register` calls `RegisterPool` and fails if that fails.
  A background task then heartbeats every `lease_ttl / 3` (default lease
  60 s), jittered down by up to 20 percent, carrying the worker counts and
  the desired state.
- A heartbeat answered with NOT_FOUND (registration gone) or ABORTED
  (`LEASE_EPOCH_MISMATCH`) registers the pool again with the same `pool_id`
  and the current spec. Other failures publish `RegistrationState::Retrying`
  and retry with backoff, never later than the heartbeat interval.
- Every write carries a generation `max(previous + 1, unix_ms)`. A
  STALE_GENERATION answer moves the generation past the stored one and
  retries once.
- `shutdown(drain)` sets DRAINING, keeps heartbeating for `drain`, stops the
  heartbeat and calls `DeregisterPool`. Dropping the registration or
  cancelling its token stops the heartbeat without deregistering; the lease
  then expires.

`AgentRegistration` follows the same rules for one pool agent:

- `AgentRegistration::register` calls `RegisterPoolAgent` without a pool id,
  so GDS finds the pool by (cluster, pool name) or creates it, and fails if
  that call fails. The heartbeat task runs every third of the lease GDS
  granted (60 s unless `lease_ttl` asks otherwise; GDS clamps it to
  [10 s, 5 m]), with the jitter, backoff, generations and re-registration on
  NOT_FOUND or ABORTED described above.
- `set_attributes` replaces the attributes. Heartbeats carry them until GDS
  acknowledges them. A heartbeat cannot clear attributes, so an empty map is
  sent with a new registration.
- `state()` and `subscribe()` publish an `AgentRegistrationState`: the phase
  (`Registered`, `Retrying`, `Stopped` or `Deregistered`), the pool id, the
  lease epoch, the derived pool state and the last error.
- `shutdown()` stops the heartbeat and calls `DeregisterPoolAgent` at once;
  there is no drain phase. A failed deregistration is returned, the phase
  turns `Stopped` and the agent lease expires. Dropping the registration or
  cancelling its token stops the heartbeat without deregistering.

## Metrics

With the `metrics` feature, register the metrics once per registry and attach
them to a client with
`client.with_metrics(GdsClientMetrics::register(&registry)?)`; clones share
them. The names and labels are the contract for the Go client
(`pkg/client`) too.

| Metric | Type | Labels |
| :---- | :---- | :---- |
| `nvcf_gds_client_requests_total` | counter | `method`, `code` |
| `nvcf_gds_client_request_duration_seconds` | histogram | `method` |
| `nvcf_gds_client_retries_total` | counter | `method` |
| `nvcf_gds_client_connection_attempts_total` | counter | `result` |
| `nvcf_gds_client_failovers_total` | counter | `reason` (`error`, `return_to_primary`) |
| `nvcf_gds_client_connected` | gauge | |
| `nvcf_gds_client_watch_events_total` | counter | `kind`, `type` |
| `nvcf_gds_client_watch_resyncs_total` | counter | `kind`, `reason` (`list`, `snapshot`) |
| `nvcf_gds_client_cache_objects` | gauge | `kind` |
| `nvcf_gds_client_cache_stale` | gauge | `kind` |
| `nvcf_gds_client_pool_registrations_total` | counter | `result` |
| `nvcf_gds_client_pool_heartbeats_total` | counter | `result` (`success`, `failure`, `reregister`) |

`code` uses the canonical gRPC spelling (`OK`, `Unavailable`, ...). Counters
are pre-initialised for every known label value. The pool registration and
heartbeat counters count `PoolRegistration` calls only; agent registrations
show in `nvcf_gds_client_requests_total` under the `RegisterPoolAgent`,
`HeartbeatPoolAgent` and `DeregisterPoolAgent` methods.

## Vendored protos

`proto/` is a byte-identical copy of
`src/control-plane-services/global-discovery/api/proto` (`nvidia/gds/v1` and
`google/api`), so a plain Cargo build of this crate needs nothing outside the
stargate workspace. Never edit it by hand. After a contract change, run from
the repository root:

```bash
src/control-plane-services/global-discovery/hack/sync-gds-proto.sh
```

A Bazel `diff_test` per file (`vendored_proto_*_test`) fails while the copies
differ; `sync-gds-proto.sh --check` does the same check without Bazel. A new
proto file needs an entry in the script and in `_VENDORED_PROTOS` in
`BUILD.bazel`.

## Build and test

From the repository root:

```bash
bazel build //src/libraries/rust/stargate/crates/gds-client/...
bazel test //src/libraries/rust/stargate/crates/gds-client/... --test_output=errors
```

Bazel targets: `gds-client` (no features), `gds-client-stargate` (`metrics`
and `stargate`), unit tests for both, `gds-client_integration_test`
(`tests/lib.rs`), and the vendored proto diff tests. With Cargo, from
`src/libraries/rust/stargate`:

```bash
cargo test --locked -p gds-client --all-features
cargo clippy --locked -p gds-client --all-features --all-targets -- -D warnings
```

The integration tests run an in-process fake GDS (`tests/support/fake_gds.rs`)
on localhost ports for failover and over in-memory duplex streams for tests
on a paused Tokio clock.
