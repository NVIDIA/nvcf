# Stargate development benchmark

This is a standalone Linux Rust 2024 workspace. Spark remains the load generator.
The CLI owns plans, workload generation, detached execution in a Spark Pod,
acceptance, reconciliation, and offline summaries. Deployment provisioning stays
in `../scripts/`.

## Build and test

Run from this directory. `rust-toolchain.toml` pins the toolchain:

```sh
cargo build --locked --release
cargo fmt --check
cargo test --locked --all-targets
cargo clippy --locked --all-targets -- -D warnings
```

Tests use a native stand-in for kubectl. They need Linux process APIs, Bash,
coreutils, `setsid`, `flock`, `grep`, and `tar`. The test support compiles the
stand-in with the `rustc` on `PATH`; through rustup, that is the pinned
toolchain. Tests do not require cluster access. CLI fixtures retain their own
executable copy for resume.
The repository's `.github/workflows/build-test.yml` runs these Rust checks.

Meaningful changes must also satisfy the
[Stargate mock integration and performance gates](../../../../src/libraries/rust/stargate/AGENTS.md#mandatory-pull-request-validation).
The kubectl stand-in does not satisfy the runtime integration gate. Run a
campaign against real Stargate with mock inference backends and retain accepted
artifacts. Measurement or workload changes need a fixed workload and independent
raw measurements so changed reporting cannot hide a performance regression.

## Local end-to-end check

To run the CLI against real clusters without AWS, bring up the `local-a` and
`local-b` k3d regions from
[README.md](../README.md#run-locally-on-a-workstation), then run from this
directory:

```sh
cargo run --locked --release -- verify --region local-a --peer-region local-b
```

`cluster.rs` embeds each environment file with `include_str!`. When you add a
region file under `../environments/`, add it there as well. The helmfile reads
every file in that directory as an environment.

## Contracts

- Keep suite parsing and workload recipes deterministic. Preserve checked numeric
  bounds, workload ordering, and disjoint cold-capacity prompt ranges.
- Keep effective inputs immutable. Resume must validate them and reconcile owned
  work before checking current controls or starting traffic.
- A lost acknowledgement is not evidence that a launch failed. Never relaunch it.
- Require process ownership and stop proof for cleanup. Leave an
  uncertain stop blocking rather than marking it complete.
- Publish acceptance only after every required report and environment check.
  Preserve raw evidence; rendering reads accepted artifacts and launches no work.
- Keep credentials out of arguments, logs, and persisted command records.

Use `artifact.rs` for shared atomic JSON, hashing, and managed-path operations.
Keep external effects in `campaign.rs`, `cluster.rs`, `pod.rs`, and `process.rs`.
Follow the existing typed plan structure. The crate
forbids unsafe Rust and denies Clippy warnings.

`../loadtest/suite.yaml` is the canonical input. `../loadtest/reduced.yaml` selects
the reduced pairs. Operator commands and artifact semantics are documented in
[README.md](../README.md#rust-benchmark-workflow). Update that section when the CLI or
acceptance contract changes.
