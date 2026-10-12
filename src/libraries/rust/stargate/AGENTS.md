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

## Code style

Use Rust 2024 and the workspace formatter and Clippy rules. Match existing
crate structure and use `tracing` for structured logs. Keep deployment
assumptions explicit when reviewing transport behavior.
