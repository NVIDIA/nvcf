# AGENTS.md - http-invocation

Native Rust image-source subtree for the NVCF invocation service.

## Build and Test

Bazel is the canonical CI path. The retired standalone Rust CI template from
`cds/cicd-pipelines` is not used here.

```bash
bazel build //...
bazel test //... --flaky_test_attempts=3
bazel build //crates/server:image_index
bazel run //crates/server:image_load
```

Push targets are declared under `//nvidia-internal`. Use them only with the
right `DOCKER_CONFIG` credentials:

```bash
bazel run //nvidia-internal:image_push_nvcf_dev
bazel run //nvidia-internal:image_push_ncp_dev
```

After `Cargo.lock` changes, repin crate metadata:

```bash
CARGO_BAZEL_REPIN=1 bazel sync --only=nvcf_invocation_crates
```

CI subproject id: `http-invocation`. Native Bazel validation and release wiring
live in `tools/ci/subproject-validations.yaml`, an internal GitLab CI config
not present in this public snapshot.

## Cargo Development

Cargo remains useful for local service iteration:

```bash
cargo run --package nvcf-invocation-service --bin server --release -- \
  -c crates/server/resources/settings-stg-local.yaml
```

The staging-local config connects to staging NVCF. Keep secrets and bearer
tokens out of committed config and examples.

## Local Gotchas

- `MODULE.bazel` owns the Rust toolchain, `rules_rust`, `crate_universe`,
  `rules_oci`, and the distroless base image.
- OSS mirror builds may need a public replacement for the `nvcr.io` distroless
  base image before `bazel build //...` works without NGC credentials.
- Changes to request/response API shape may need follow-up changes in
  `src/clis/nvcf-cli` so the CLI keeps parity with the service. The
  CLI is hand-written against the public types in
  `crates/server/src/nvcf_api`.
