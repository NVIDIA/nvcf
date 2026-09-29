# AGENTS.md - Notary

Notary is an OSS/self-hosted Java service in the root `nvcf` Bazel module. It
does not own a nested module, lockfile, Bazel configuration, or third-party
dependency hub.

The monorepo copy is Bazel-only and contains no project POMs. Bazel consumes
nv-boot through direct source labels and produces the executable application
jar. Keep any Maven build support in the independent source repository. Do not
restore project POMs or add Maven build instructions here.

## Build and test

Run commands from the monorepo root:

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/notary/...

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/notary/... \
  --cache_test_results=no \
  --test_output=errors
```

The tests use in-process Spring Boot and WireMock servers. They do not require
Docker. CI therefore routes this component to the `build-container` lane.

## Dependencies

The root `MODULE.bazel` and `maven_install.json` own the shared
`@nv_third_party_deps` hub. BUILD targets declare direct compile and runtime
edges. A coordinate in the hub is not automatically on Notary's classpath.

Use direct labels for nv-boot source targets:

```text
//src/libraries/java/nv-boot-parent/nv-boot-starter-core:nv_boot_starter_core
```

The source Maven build pins `commons-collections4` 4.4. The monorepo shared
dependency graph selects the compatible 4.5.0 version. Keep that intentional
parity difference documented instead of adding a lower root pin.

## NOTICE

The component `NOTICE` is derived from jars under the Bazel-built
`notary-service:app`. Generate and check it through the root-owned rules:

```bash
bazel run //src/control-plane-services/notary:generate_notice -- \
  --update-metadata --write
bazel test //src/control-plane-services/notary:notice_check_test
bazel build //src/control-plane-services/notary:osrb_dependency_delta
```

Do not restore the source repository's Maven NOTICE generator or copy its
standalone `LICENSE` or `NOTICE` files into this subtree.
