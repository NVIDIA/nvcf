# AGENTS.md - nvct_cloud_tasks

The NVCF cloud-tasks Java (Spring Boot) service, as a standalone Bazel module
(`module(name = "nvct_cloud_tasks")`). Depends on `nvcf_java_rules` (shared
macros + multi-arch image helper) and `nv_boot_parent` (the Spring platform
starters), both via `bazel_dep` + `local_path_override`.

## Build and test (from this directory)

```
cd src/control-plane-services/cloud-tasks
bazel build //...
bazel test //... --test_tag_filters=-requires-docker
```

The module builds on its own with no reference to the repo root. The Spring +
Testcontainers integration tests are tagged `requires-docker` (and `exclusive`)
and run in the dedicated Docker integration lane, not the default fast matrix.

## Maven dependencies

Own `@maven` hub and own committed `maven_install.json`. After editing
`maven.install` in `MODULE.bazel`, re-pin and commit:

```
REPIN=1 bazel run @maven//:pin
```

The `boms` list must stay identical to `SPRING_BOOT_BOM` in
`@nvcf_java_rules//:platform.bzl`; `//:bom_alignment_test` enforces it.

## Layout

- `nvct-core/` - service library plus the gRPC `nvct.proto` codegen
  (`nvct_java_grpc_compile` in `tools/bazel/proto.bzl`, driven by the
  `protoc-gen-grpc-java` native plugins declared in `MODULE.bazel`).
- `nvct-service/` - the `@SpringBootApplication` entry point and the
  `nvcf_spring_boot_image` multi-arch OCI image target (`:image_index`).
- `tools/bazel/` - Lombok wiring, the gRPC plugin `proto_plugin`, JaCoCo runner.
