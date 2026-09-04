# AGENTS.md

## Repo Role
- Crate: `nvcf-info`
- Language: Rust
- Purpose: shared GET /info response schema (service/version/commit) for
  NVCF's Rust services, so `function-autoscaler` and `http-invocation` do not
  hand-duplicate the struct. Mirrors `src/libraries/go/lib/pkg/version` for
  the Go services and `nv-boot-starter-core` for the Java services.

## Design

### The macro takes the service name
`version` and `commit` come from the calling crate's `CARGO_PKG_VERSION` and
`NVCF_GIT_COMMIT`, stamped by the consuming service's
`crates/server/BUILD.bazel` `version_env` `expand_template`. The service name
is passed in explicitly. That matches the Go services, which set
`version.Service` to a deployed service name in each `BUILD.bazel` `x_defs`
(`nvcf-grpc-proxy`, `nvcf-ratelimiter`), not to the package name. It is also
required under Bazel, where `CARGO_PKG_NAME` resolves to the rules_rust target
name (`rs_autoscaler_lib`), not the Cargo package name.

### It is a macro, not a function
Rust has no equivalent to Go's linker `-X` package-variable stamping, so this
crate cannot read a caller's build-time version itself. `env!`/`option_env!`
resolve against whichever crate's compilation they textually appear in, so
`info_response!` expands inside the calling crate and reads that crate's own
stamped values. A plain function here would report nvcf-info's own metadata.

### One Bazel target per crate universe
Each Rust service resolves third-party crates from its own `crate_universe`
repo, so `@rs_autoscaler_crates//:serde` and `@nvcf_invocation_crates//:serde`
are distinct crates and therefore distinct `Serialize` traits. `BUILD.bazel`
builds `src/lib.rs` once per universe (`nvcf-info-autoscaler`,
`nvcf-info-invocation`) so each service gets an `InfoResponse` implementing
its own serde's `Serialize`. A single shared target compiles but fails at the
`axum::Json` bound in one of the two callers.

### Bazel-only dependency, not a Cargo path dependency
`cargo-bazel splice` copies each service workspace into a temp dir rooted at
that workspace, so a Cargo path dependency pointing outside the service
directory resolves above the splice root and fails to load. Consuming services
therefore depend on the Bazel target only. A consequence: `cargo build` and
`cargo test` in `function-autoscaler` and `http-invocation` cannot resolve
`nvcf_info`. Use `bazel test` for those crates. Bazel is the canonical CI and
release path for both.

### No axum dependency
The two services pin different axum majors, so this crate stays
framework-agnostic and callers wrap `InfoResponse` in their own `axum::Json`.

## Usage
```rust
async fn get_info() -> axum::Json<nvcf_info::InfoResponse> {
    axum::Json(nvcf_info::info_response!("nvcf-my-service"))
}
```

## Build & Test
```bash
bazel test //src/libraries/rust/nvcf-info:all
cargo fmt -p nvcf-info
cargo clippy -p nvcf-info --all-targets -- -D warnings
cargo test -p nvcf-info
```

Cargo works inside this crate because it has no cross-workspace path
dependencies of its own.
