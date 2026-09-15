# AGENTS.md - Encrypted Secret Store

ESS is an OSS/self-hosted Java service in the root `nvcf` Bazel module. It is
Bazel-only in this monorepo. Do not add project POMs, Maven settings, nested
Bazel configuration, or a component dependency lock.

## Build and test

Run commands from the monorepo root with a full JDK 25:

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/encrypted-secret-store/...

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/encrypted-secret-store/... \
  --cache_test_results=no \
  --test_output=errors
```

Integration tests are tagged `requires-docker`. They use Testcontainers and
need a running Docker daemon. Do not remove or modify pre-existing local
containers, clusters, or volumes during validation.

The executable is:

```text
bazel-bin/src/control-plane-services/encrypted-secret-store/ess-service/app.jar
```

See `BAZEL.md` for focused tests, generated outputs, NOTICE, Docker, and local
startup commands.

## Dependencies

The root `MODULE.bazel` and `maven_install.json` own
`@nv_third_party_deps`. BUILD targets must declare direct compile and runtime
edges. Use direct source labels for co-located nv-boot libraries.

Do not generate or publish Maven-shaped ESS artifacts from Bazel.

## NOTICE

The component NOTICE is derived from the exact jars in the Bazel app:

```bash
bazel run //src/control-plane-services/encrypted-secret-store:generate_notice -- \
  --update-metadata --write
bazel test //src/control-plane-services/encrypted-secret-store:notice_check_test
bazel build //src/control-plane-services/encrypted-secret-store:osrb_dependency_delta
```

Do not copy the standalone repository's root `LICENSE`, `NOTICE`, internal CI
configuration, or Maven repository settings into this subtree.
