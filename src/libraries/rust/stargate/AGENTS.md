# Stargate agent guide

This guidance covers the Rust workspace, including `stargate-k8s-router`, the
shared `stargate-forwarding` crate and the `gds-client` crate.

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

## gds-client

`crates/gds-client` is the Global Discovery Service (GDS) client: endpoint
failover, typed calls, pool and router caches, pylon and Stargate seed URLs,
and pool registration. See `crates/gds-client/README.md`.

- Keep it dependency-light so Dynamo can adopt it: no reqwest,
  hickory-resolver, moka or serde, and no dependency on other workspace
  crates except the optional `stargate-proto` (feature `stargate`).
- `crates/gds-client/proto/` is a vendored copy of
  `src/control-plane-services/global-discovery/api/proto`. Never edit it.
  Run `src/control-plane-services/global-discovery/hack/sync-gds-proto.sh`
  from the repository root after a contract change; the Bazel
  `vendored_proto_*_test` targets fail until the copies match.
- Metric names (`nvcf_gds_client_*`) and label values are shared with the
  Go client in the global-discovery module. Change both together.

From the repository root:

```sh
bazel test //src/libraries/rust/stargate/crates/gds-client/... --test_output=errors
```

From this directory:

```sh
cargo test --locked -p gds-client --all-features
cargo clippy --locked -p gds-client --all-features --all-targets -- -D warnings
```

## Code style

Use Rust 2024 and the workspace formatter and Clippy rules. Match existing
crate structure and use `tracing` for structured logs. Keep deployment
assumptions explicit when reviewing transport behavior.
