# Stargate agent guide

This guidance covers the Rust workspace, including `stargate-k8s-router` and
the shared `stargate-forwarding` crate.

## Assumed deployment invariants

- `stargate-k8s-router` is deployed only for `raw-quic` tunnel traffic.
  HTTP/3 tunnel traffic does not pass through this router.
- Raw QUIC request streams are bidirectional. Prioritizing bidirectional
  acceptance over unidirectional acceptance in the raw QUIC relay is intentional.
- Optional HTTP/3 and WebTransport implementations, flags, and tests do not
  change this deployment assumption. Do not infer an HTTP/3 stream fairness
  requirement for the raw QUIC router from their presence.

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
