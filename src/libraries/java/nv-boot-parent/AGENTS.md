# AGENTS.md - nv-boot-parent

nv-boot-parent provides the NVCF Spring Boot platform libraries inside the root
`nvcf` Bazel module. It is not a nested Bazel module and does not own a
`MODULE.bazel`, lockfile, `.bazelrc`, `.bazelversion`, downloader config, or
third-party dependency hub.

Maven POMs remain during coexistence for Maven consumers. Bazel consumers use
the public starter source targets directly; Bazel does not generate or publish
Maven-shaped nv-boot artifacts.

## Build And Test

Run Bazel from the monorepo root:

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/libraries/java/nv-boot-parent/...

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/libraries/java/nv-boot-parent/... \
  --test_tag_filters=-requires-docker
```

Tests tagged `requires-docker` run in the dedicated Docker integration lane.

## Dependencies

The root `MODULE.bazel` owns the one `nv_third_party_deps` hub and the root
`maven_install.json` and `MODULE.bazel.lock`. Starter BUILD targets declare
their own direct compile/runtime edges. Optional APIs use private `neverlink`
compile helpers plus runtime-classpath tests so frameworks such as Cassandra
are not added to unrelated consumers.

Do not introduce `@nv_boot_parent`, `@cloud_tasks`, `@maven`,
`local_path_override`, or `git_override` for code already in this repository.

## Layout

- `nv-boot-starter-*/`: public nv-boot starter source targets.
- `//rules/java`: shared Java, test, runtime-scope, protobuf, and Spring Boot
  rules.
- `//tools/bazel/java`: shared executable helper implementations.
- `NOTICE`, `notice_roots.json`, and `notice_metadata.json`: nv-boot's
  monorepo NOTICE inputs and generated output.

The root `.bazelrc` owns Java 25 and
`build --java_header_compilation=false`.
