# AGENTS.md - nvcf-info

nvcf-info holds the shared `GET /info` response schema for the Rust services,
so `function-autoscaler` and `http-invocation` do not each define their own.
It is the Rust counterpart to `src/libraries/go/lib/pkg/version` and the Java
`nv-boot-starter-core` info controller.

## Usage

```rust
async fn get_info() -> axum::Json<nvcf_info::InfoResponse> {
    axum::Json(nvcf_info::info_response!("nvcf-my-service"))
}
```

Pass the deployed service name, matching how the Go services set
`version.Service` in their `BUILD.bazel` `x_defs`. Version and commit come from
the consuming service's `version_env` template in `crates/server/BUILD.bazel`,
which stamps `CARGO_PKG_VERSION` and `NVCF_GIT_COMMIT` on `--stamp` builds.

## Adding A Consumer

Add the service's Bazel target to `crates/server/BUILD.bazel` `deps`, and add
an entry to the list in this crate's `BUILD.bazel` if the service uses a
crate universe not already listed there. Two serde copies are two distinct
`Serialize` traits, so each universe needs its own build of `src/lib.rs`.

This crate is a Bazel dependency only. It is not a Cargo path dependency:
`cargo-bazel splice` roots each service workspace in a temp dir, so a path
pointing outside that directory resolves above the splice root. Consuming
crates therefore build and test under Bazel rather than cargo.

## Build And Test

Run Bazel from the monorepo root:

```bash
bazel test //src/libraries/rust/nvcf-info:all
```

Cargo works inside this crate, which has no cross-workspace path dependencies:

```bash
cargo fmt -p nvcf-info
cargo clippy -p nvcf-info --all-targets -- -D warnings
cargo test -p nvcf-info
```
