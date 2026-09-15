# AGENTS.md - API Keys Service

API Keys is a single-module OSS/self-hosted Java service in the root `nvcf`
Bazel module. Do not create a synthetic core module, nested Bazel module,
lockfile, repository configuration, or third-party dependency hub.

The monorepo copy is Bazel-only and contains no project POM. Bazel consumes
nv-boot through direct source labels and produces the executable application
jar. Keep any Maven build support in the independent source repository. Do not
restore project POMs or add Maven build instructions here.

## Build and test

Run commands from the monorepo root:

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/api-keys/...

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/api-keys/... \
  --cache_test_results=no \
  --test_output=errors
```

The test target starts Cassandra through Testcontainers and Docker Compose. It
is tagged `requires-docker` and runs in the GitHub `docker-host` lane.

## Dependencies

The root `MODULE.bazel` and `maven_install.json` own
`@nv_third_party_deps`. BUILD targets declare compile and runtime edges. Use
direct labels for co-located nv-boot targets.

## NOTICE

Generate and check the runtime-derived component NOTICE with:

```bash
bazel run //src/control-plane-services/api-keys:generate_notice -- \
  --update-metadata --write
bazel test //src/control-plane-services/api-keys:notice_check_test
bazel build //src/control-plane-services/api-keys:osrb_dependency_delta
```

Do not restore the standalone Maven NOTICE template or copy its repository
`LICENSE` or generated `NOTICE`.
