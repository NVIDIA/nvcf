# AGENTS.md - Instance Cluster Management

Instance and Cluster Management Service (ICMS) is a two-module OSS/self-hosted
Java service in the root `nvcf` Bazel module. It is not a nested Bazel module
and does not own a `MODULE.bazel`, lockfile, `.bazelrc`, `.bazelversion`, or
third-party dependency hub.

The monorepo copy is Bazel-only and contains no project POMs. Bazel consumes
nv-boot through direct source labels and produces the executable application
jar. Keep any Maven build support in the independent source repository. Do not
restore project POMs or add Maven build instructions here.

## Layout

- `icms-core/`: service library, REST controllers, Cassandra repositories, and
  scheduled tasks.
- `icms-service/`: Spring Boot application and executable `app.jar`.
- `local_env/`: Docker Compose bundle for Cassandra, LocalStack, NATS, and
  Vault. Both modules symlink it as `local_env`.
- `//rules/java`: shared Java, test, and Spring Boot rules.

## Build and test

Run commands from the monorepo root:

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/instance-cluster-management/...

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/instance-cluster-management/... \
  --cache_test_results=no \
  --test_output=errors
```

The test targets start Cassandra and NATS through Testcontainers and Docker
Compose. They are tagged `requires-docker` and run in the GitHub `docker-host`
lane. Running them locally also needs `--test_env=PATH` so Testcontainers can
find the host `docker compose` CLI, and `--test_env=HOME` so Docker Desktop can
discover CLI plugins.

`icms-core:test_fixtures` publishes the core test sources so
`icms-service:tests` can reuse them. Update both when test fixtures change.

See `BAZEL.md` for coverage output paths and the Docker image build.

## Dependencies

The root `MODULE.bazel` and `maven_install.json` own `@nv_third_party_deps`.
BUILD targets declare compile and runtime edges. A coordinate being available in
the shared hub does not put it on this service's classpath. Use direct labels
for co-located nv-boot targets, for example:

```text
//src/libraries/java/nv-boot-parent/nv-boot-starter-core:nv_boot_starter_core
```

After changing a root Java dependency input, repin from the monorepo root and
regenerate the dependency rollup:

```bash
REPIN=1 bazel run @nv_third_party_deps//:pin
GITHUB_TOKEN="$(gh auth token)" go run -C tools/collect-dependencies .
```

The collector needs `go`, `cargo`, and `helm` on `PATH`. Without `cargo` or
`helm` it silently moves unrelated Rust crates and Helm charts into the
`Unresolved` section of `dependencies.md`.

Do not hand-edit `maven_install.json` or `MODULE.bazel.lock`.

## NOTICE

Generate and check the runtime-derived component NOTICE with:

```bash
bazel run //src/control-plane-services/instance-cluster-management:generate_notice -- \
  --update-metadata --write
bazel test //src/control-plane-services/instance-cluster-management:notice_check_test
bazel build //src/control-plane-services/instance-cluster-management:osrb_dependency_delta
```

Do not run a standalone Maven NOTICE generator in this subtree.

## Java style

- Keep Java lines at or below 100 characters where practical. Extract helpers or
  constants instead of leaving long inline expressions.
- Use private static final constants for repeated error and log messages.
- Use `StringUtils.isBlank(...)` for null-or-blank string validation.
- Read test resources through `ClassPathResource.getInputStream()`, never
  `getFile()`. Resources live inside jars under Bazel, so `getFile()` fails.
- In tests, import frequently used nested classes and use `var` for obvious
  local variables when it keeps lines short.
