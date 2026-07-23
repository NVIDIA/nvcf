# AGENTS.md - nv_boot_parent

The NVCF Spring Boot platform library, as a standalone Bazel module
(`module(name = "nv_boot_parent")`). Owns the shared Spring BOM set and the
`nv-boot-*` starter `java_library` targets that every NVCF Java service depends
on.

## Build and test (from this directory)

```
cd src/libraries/java/nv-boot-parent
bazel build //...
bazel test //...
```

The module builds on its own with no reference to the repo root. Docker
integration tests are tagged `requires-docker` and run in the dedicated
integration lane, not the default `bazel test //...`.

## Maven dependencies

This module has its own `@maven` hub and its own committed `maven_install.json`.
After editing `maven.install` in `MODULE.bazel`, re-pin and commit the lockfile:

```
REPIN=1 bazel run @maven//:pin
```

The `boms` list in `MODULE.bazel` must stay identical to `SPRING_BOOT_BOM` in
`@nvcf_java_rules//:platform.bzl`. `//:bom_alignment_test` fails the build if it
drifts. Bumping a shared version (Spring, Spring Cloud, Testcontainers,
ShedLock) is a one-line edit in that `platform.bzl`, the matching edit here, then
a re-pin.

## Layout

- `nv-boot-starter-*/` - the platform starters, each a public `nv_boot_library`.
- `tools/bazel/` - `nv_boot_library` / `nv_boot_library_test` macros
  (`java.bzl`), Lombok wiring, JUnit 5 + JaCoCo runners, and the NOTICE
  generator (`generate_notice.py`).
- `NOTICE` - generated from this module's own `maven_install.json`.

## Consuming this module

In-tree, a service module uses `bazel_dep` + `local_path_override`. Out-of-tree,
`git_override` with `strip_prefix = "src/libraries/java/nv-boot-parent"`, which
works because `MODULE.bazel` is rooted here.
