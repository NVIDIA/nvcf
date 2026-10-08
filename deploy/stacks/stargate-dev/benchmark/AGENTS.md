# Stargate development benchmark

This is a standalone Linux Rust 2024 workspace. Spark remains the load generator.
The CLI owns plans, workload generation, Docker or detached Pod execution,
acceptance, reconciliation, and offline summaries. Deployment provisioning stays
in `../scripts/`.

## Build and test

Run from this directory with current stable Rust:

```sh
rustup run stable cargo build --locked --release
```

```sh
rustup run stable cargo fmt
```

```sh
rustup run stable cargo fmt --check
```

```sh
rustup run stable cargo test --locked --all-targets
```

```sh
rustup run stable cargo clippy --locked --all-targets -- -D warnings
```

Tests use native stand-ins for Docker and kubectl. They need Linux process APIs,
Bash, coreutils, `setsid`, `flock`, `grep`, `tar`, and rustup. They do not require cluster access
or a Docker daemon. CLI fixtures retain their own executable copy for resume.
The repository's `.github/workflows/build-test.yml` runs these Rust checks.

## Contracts

- Keep suite parsing and workload recipes deterministic. Preserve checked numeric
  bounds, workload ordering, and disjoint cold-capacity prompt ranges.
- Keep effective inputs immutable. Resume must validate them and reconcile owned
  work before checking current controls or starting traffic.
- A lost acknowledgement is not evidence that a launch failed. Never relaunch it.
- Require container or process ownership and stop proof for cleanup. Leave an
  uncertain stop blocking rather than marking it complete.
- Publish acceptance only after every required report and environment check.
  Preserve raw evidence; rendering reads accepted artifacts and launches no work.
- Keep credentials out of arguments, logs, and persisted command records.

Use `artifact.rs` for shared atomic JSON, hashing, and managed-path operations.
Keep external effects in `campaign.rs`, `cluster.rs`, `pod.rs`, and `process.rs`.
Follow the existing explicit transport enum and typed plan structure. The crate
forbids unsafe Rust and denies Clippy warnings.

`../loadtest/suite.yaml` is the canonical input. `../loadtest/reduced.yaml` selects
the reduced pairs. Operator commands and artifact semantics are documented in
[PLAN.md](../PLAN.md#rust-benchmark-workflow). Update that section when the CLI or
acceptance contract changes.
