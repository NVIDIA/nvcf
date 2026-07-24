# AGENTS.md - Cloud Tasks

Cloud Tasks is an OSS/self-hosted Java service folded into the root `nvcf`
Bazel module. It is not a nested Bazel module and does not own a
`MODULE.bazel`, lockfile, `.bazelrc`, `.bazelversion`, downloader config, or
third-party dependency hub.

Maven POMs remain during coexistence, but Bazel uses source targets throughout
the monorepo. Do not generate or publish Maven-shaped project artifacts from
Bazel.

## Build And Test

Run Bazel from the monorepo root:

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/cloud-tasks/...

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/cloud-tasks/... \
  --test_tag_filters=-requires-docker
```

The Testcontainers suites are tagged `requires-docker` and run in the
dedicated Docker integration lane.

## Dependencies

The root `MODULE.bazel` owns the single `nv_third_party_deps` hub and the root
`maven_install.json` and `MODULE.bazel.lock`. BUILD targets here declare only
the direct labels they compile or run against. A coordinate being available in
the root hub does not put it on this service's runtime classpath.

Use direct monorepo labels for nv-boot libraries, for example:

```text
//src/libraries/java/nv-boot-parent/nv-boot-starter-core:nv_boot_starter_core
```

Do not introduce `@nv_boot_parent`, `@cloud_tasks`, `@maven`,
`local_path_override`, or `git_override` for code already in this repository.

## Layout

- `nvct-core/`: service library and gRPC code generation.
- `nvct-service/`: Spring Boot application and executable `app.jar`.
- `//rules/java`: shared Java, test, protobuf, and Spring Boot rules.
- `//tools/bazel/java`: shared executable helper implementations.
