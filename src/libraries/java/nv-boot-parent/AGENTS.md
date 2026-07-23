# AGENTS.md - nv_boot_parent

The NVCF Spring Boot platform library, as a standalone Bazel module
(`module(name = "nv_boot_parent")`). Owns the shared Spring BOM set and the
`nv-boot-*` starter `java_library` targets that every NVCF Java service depends
on. Maven and Bazel coexist here; Maven stays authoritative for Maven consumers.

## Build and test (from this directory)

```
cd src/libraries/java/nv-boot-parent
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache"
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" build //...
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" test //...
```

The module builds on its own with no reference to the repo root. Docker
integration tests are tagged `requires-docker` and run in the dedicated
integration lane, not the default `bazel test //...`.

## Dependency hub

Third-party dependencies resolve through one shared hub named
`nv_third_party_deps` (never `@maven`). Downstream services contribute to the
same hub and list this module in `known_contributing_modules`, so the final
application resolves a single third-party graph. Public BUILD labels look like
`@nv_third_party_deps//:org_springframework_boot_spring_boot`.

`MODULE.bazel` is the edited source of truth for BOM-managed roots and explicit
pins. After editing `maven.install`, re-pin and commit the lockfile:

```
REPIN=1 bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" run @nv_third_party_deps//:pin
```

Both `maven_install.json` and `MODULE.bazel.lock` are committed and never
hand-edited.

## Layout

- `nv-boot-starter-*/` - the platform starters, each a public `nv_boot_library`.
- `tools/bazel/` - `nv_boot_library` / `nv_boot_library_test` macros
  (`java.bzl`), Lombok wiring, JUnit 5 + JaCoCo runners, and the NOTICE
  generator (`generate_notice.py`).
- `NOTICE` - generated from this module's own `maven_install.json`.

Lombok needs `build --java_header_compilation=false`; that flag lives in this
module's `.bazelrc` and must also be set in every downstream consuming root,
because `.bazelrc` is not transitive through Bzlmod.

## Consuming this module

In-tree, a service module uses `bazel_dep(name = "nv_boot_parent", version =
"0.0.0")` plus `local_path_override`. See
`src/control-plane-services/cloud-tasks/MODULE.bazel` for the reference wiring.
