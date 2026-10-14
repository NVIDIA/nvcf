# Stargate agent guide

This guidance covers the Rust workspace, including `stargate-k8s-router` and
the shared `stargate-forwarding` crate.

## Assumed deployment invariants

- `stargate-k8s-router` supports `raw-quic` and `webtransport` tunnel traffic.
  Plain `http3` tunnel traffic uses an L4 path and does not pass through this router.
- Raw QUIC request streams are bidirectional. During drain, prioritizing
  bidirectional acceptance over unidirectional acceptance is intentional.
- WebTransport uses a separate session bridge. Do not infer a plain HTTP/3
  stream fairness requirement for the raw QUIC relay from that implementation.

For router transport changes, inspect `crates/stargate-k8s-router/src/quic.rs`
and `crates/stargate-forwarding/src/lib.rs`. The
[transport guide](docs/tunnel-transports.md) describes available protocol
implementations and their routing paths.

## Build and verification

Run commands from this directory. For router and relay Rust changes:

```sh
cargo build --locked -p stargate-k8s-router
cargo test --locked -p stargate-k8s-router -p stargate-forwarding -- --test-threads=1
cargo clippy --locked -p stargate-k8s-router -p stargate-forwarding --all-targets -- -D warnings
cargo fmt --all -- --check
```

For other Rust changes, select the affected workspace packages with `-p`.
For documentation changes, check whitespace and link targets.

## Mandatory Pull Request validation

Before marking a Pull Request ready or merging, run at least one local, AWS,
or CI mock integration test for every meaningful Stargate change. This covers
Stargate, router/relay, Pylon, mock engines, authentication, dependencies, image
pins, related charts, deployment settings, benchmark logic, and CI changes
that affect how these components build or run. Pure documentation and formatting
changes are exempt; explain the exemption in the Pull Request.

- Build the affected binaries or images from the candidate commit. Send real
  requests through the changed path with mock inference backends. Check
  registration/readiness, response correctness, completion, and streaming when
  applicable. Exercise the affected transport and routing policies.
- Choose a relevant loopback integration test such as
  `scripts/e2e-last-cluster.sh`, a local k3d deployment, an isolated AWS mock
  deployment, or a CI job that runs equivalent request traffic. See the
  [development stack](../../../../deploy/stacks/stargate-dev/README.md) for
  deployment, streaming checks, and benchmark campaigns.
- Unit tests, chart rendering, mocked kubectl, microbenchmarks, and readiness
  checks alone do not satisfy this integration gate. A CI job must exercise
  the affected runtime path; a green build alone does not count.
- Follow the root local QA safety rules. Give runs unique resources and
  artifacts. Serialize campaigns that share backends or mutate deployment
  state; separate output directories do not isolate caches, queues, or resets.
- Record the tested commit, commands, environment, image digests, policies,
  workload, acceptance results, and retained artifact links in the Pull Request.
  Retest after changes that alter the validated behavior. Blocked or failed
  validation keeps the Pull Request unready and blocks merge.

## Performance regression gate

For changes that can affect request handling, routing, transport, caching,
concurrency, scheduling, resource limits, or performance measurements, compare
the merge-base baseline and candidate on the same test environment before
marking the Pull Request ready or merging.

- Use release builds and identical hardware, resource allocation, topology,
  mock-engine settings, policy, load-generator version, workload/seed, offered
  load, concurrency, cache state, warmup, and run duration. Change only the
  component or setting under test. Exclude unrelated traffic and overlapping
  campaigns. Local and AWS measurements must be compared within their own
  environments.
- Run at least three measured repetitions per revision, alternate revision
  order, and report individual results and variation. Choose representative
  sustained load, including saturation, affinity, mixed sessions, or transport
  profiles relevant to the change. A short smoke run alone is insufficient.
- Compare successful throughput, p95/p99 time to first token and end-to-end
  latency, failures/timeouts/retries, and CPU/memory where relevant. Include
  admission failures and cancellations, not only completed-request latency.
- Retain raw baseline and candidate reports and a comparison table. Algorithm
  comparisons within one build do not establish revision-level performance.
  Explain unavailable metrics and collect another observable for that risk.
- Investigate any worse result. Any reproducible degradation blocks merge;
  inconclusive or noisy results require more evidence. An intentional tradeoff
  requires explicit user approval with the measured impact before merge.
  Mock results support the tested workload, not production capacity claims.

## Routing methods

`crates/stargate/src/load_balancer/routing_methods.json` lists every
load-balancing algorithm name, alias, and routing-expression parameter. When
you add, rename, or remove one, update that file and run
`cargo test --locked -p stargate --lib routing_method`. A failure names the
other code, docs, and configs to update.

## Code style

Use Rust 2024 and the workspace formatter and Clippy rules. Match existing
crate structure and use `tracing` for structured logs. Keep deployment
assumptions explicit when reviewing transport behavior.
