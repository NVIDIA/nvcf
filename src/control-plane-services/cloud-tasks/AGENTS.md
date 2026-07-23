# AGENTS.md - cloud_tasks

The NVCF cloud-tasks Java (Spring Boot) service, as a standalone Bazel module
(`module(name = "cloud_tasks")`). Depends on `nv_boot_parent` (the Spring
platform starters) via `bazel_dep` plus `local_path_override`. Maven and Bazel
coexist; Maven stays the default build during coexistence.

## Build and test (from this directory)

```
cd src/control-plane-services/cloud-tasks
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/cloud-tasks-bazel-cache"
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" build //...
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" test //... --test_tag_filters=-requires-docker
```

The module builds on its own with no reference to the repo root. The Spring plus
Testcontainers integration tests are tagged `requires-docker` and run in the
dedicated Docker integration lane, not the default fast matrix.

## Dependency hub

Third-party dependencies resolve through one shared hub named
`nv_third_party_deps` (never `@maven`). `maven.install` lists `nv_boot_parent`
in `known_contributing_modules`, so this service and the nv-boot starters
resolve a single merged third-party graph. This root owns and re-pins the merged
`maven_install.json`:

```
REPIN=1 bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" run @nv_third_party_deps//:pin
```

Both `maven_install.json` and `MODULE.bazel.lock` are committed. Lombok needs
`build --java_header_compilation=false` (via `--nojava_header_compilation` in
this module's `.bazelrc`).

## Layout

- `nvct-core/` - service library plus the gRPC `nvct.proto` codegen (driven by
  the `protoc-gen-grpc-java` native plugins declared in `MODULE.bazel` and the
  macros in `tools/bazel/proto.bzl`).
- `nvct-service/` - the `@SpringBootApplication` entry point and the Spring Boot
  executable app jar (`tools/bazel/spring_boot.bzl`).
- `tools/bazel/` - the `nv_boot_library` macros (`java.bzl`), Lombok wiring, the
  gRPC plugin, the JaCoCo runner, and the NOTICE generator.
