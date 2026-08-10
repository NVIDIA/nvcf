# Bazel for Notary

Notary is part of the root `nvcf` Bazel module. Run every Bazel command in this
guide from the monorepo root. The monorepo copy is Bazel-only and does not
contain project POMs. Any Maven build support remains in the independent source
repository.

## Shared configuration

The service does not own nested Bazel configuration:

- `.bazelversion` pins the Bazel release used by Bazelisk.
- `.bazelrc` holds repository defaults, including Java 25 and
  `--java_header_compilation=false`.
- `.bazel_downloader_config` rewrites supported download URLs. It does not
  select dependency versions.
- `MODULE.bazel` declares rule modules, BOMs, and dependency roots.
- `maven_install.json` is the generated exact lock for third-party Java
  coordinates. It does not publish Maven artifacts.
- `MODULE.bazel.lock` is the generated Bzlmod lock.

The root uses `local_jdk`. Install a full JDK 25 and set `JAVA_HOME`. Diagnose
the selected target and host toolchains with:

```bash
bazel cquery @bazel_tools//tools/jdk:current_java_runtime \
  --output=starlark \
  --starlark:expr='str(providers(target)["ToolchainInfo"].java_runtime.version)'

bazel cquery @bazel_tools//tools/jdk:current_host_java_runtime \
  --output=starlark \
  --starlark:expr='str(providers(target)["ToolchainInfo"].java_runtime.version)'
```

Both commands must print `25`. `bazel info java-home` reports the Bazel server
JVM and is not compiler toolchain proof.

## Output root and clean

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" clean
```

Use `clean --expunge` only to reset a corrupted cache.

## Build

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/notary/...

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/notary/notary-core:notary_core

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/notary/notary-service:app
```

The executable output is:

```text
bazel-bin/src/control-plane-services/notary/notary-service/app.jar
```

Inspect its launcher and generated build metadata:

```bash
unzip -p \
  bazel-bin/src/control-plane-services/notary/notary-service/app.jar \
  META-INF/MANIFEST.MF

unzip -p \
  bazel-bin/src/control-plane-services/notary/notary-service/app.jar \
  BOOT-INF/classes/git.properties
```

## Test

Run every Notary test without cached results:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/notary/... \
  --cache_test_results=no \
  --test_output=errors
```

Run one module:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/notary/notary-core:tests \
  --cache_test_results=no \
  --test_output=errors
```

Run one class:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/notary/notary-core:tests \
  --cache_test_results=no \
  --test_output=streamed \
  --test_arg=--exclude-classname='^(?!com.nvidia.notary.services.SigningServiceTest$).*$'
```

Run one method:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/notary/notary-core:tests \
  --cache_test_results=no \
  --test_output=streamed \
  --test_arg=--exclude-classname='^(?!com.nvidia.notary.services.SigningServiceTest$).*$' \
  --test_arg=--include-methodname='.*#sign.*$'
```

Each module writes real Java test and coverage outputs under:

```text
bazel-testlogs/src/control-plane-services/notary/<module>/tests/test.log
bazel-testlogs/src/control-plane-services/notary/<module>/tests/test.outputs/junit/TEST-junit-jupiter.xml
bazel-testlogs/src/control-plane-services/notary/<module>/tests/test.outputs/jacoco.exec
bazel-testlogs/src/control-plane-services/notary/<module>/tests/test.outputs/jacoco.xml
bazel-testlogs/src/control-plane-services/notary/<module>/tests/test.outputs/index.html
```

Use `test.outputs/junit/TEST-junit-jupiter.xml` for JUnit reporting. The nearby
outer `test.xml` describes one Bazel shell wrapper and is not the Java report.
Point Sonar at both module `jacoco.xml` files.

## NOTICE and OSRB delta

The component NOTICE is derived from exact jars nested under `BOOT-INF/lib`.
The component metadata adds only dependencies not already owned by nv-boot.

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  run //src/control-plane-services/notary:generate_notice -- \
  --update-metadata --write

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/notary:notice_check_test

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/notary:osrb_dependency_delta
```

The generated inventory and exact license-grouped delta are under:

```text
bazel-bin/src/control-plane-services/notary/runtime_inventory.json
bazel-bin/src/control-plane-services/notary/osrb_dependency_delta.json
bazel-bin/src/control-plane-services/notary/osrb_dependency_delta.md
```

## Dependency lock

The root shared dependency graph selects compatible versions for all Java
components. Notary's Maven source pins `commons-collections4` 4.4; the shared
graph already selects 4.5.0. This is an intentional parity difference and is
not overridden with a lower root pin.

The shared root pins `commons-logging` 1.4.0 because that active nv-boot parent
property override reaches the Maven executable runtime. The app also owns a
runtime edge to `spring-boot-jarmode-tools`, which Maven's Spring Boot repackage
goal injects into the executable jar.

The remaining executable-jar inventory differences are packaging-only:

- The shared graph selects JetBrains annotations 17.0.0 instead of Maven's
  13.0 release.
- Bazel omits Lombok from the runtime jar because it is a compile-time
  annotation processor.
- Bazel retains Spring Boot starter marker jars. They contain metadata and no
  classes; Maven's repackage goal omits them.
- Bazel first-party jars use target-derived names instead of Maven artifact
  names, but contain the same Notary and nv-boot classes.

Spring Framework 7 embeds the AOP Alliance API in `spring-aop`. The root excludes
the legacy standalone `aopalliance` jar so the Bazel executable matches Maven
and does not package duplicate `org.aopalliance` classes.

After changing a root dependency input, regenerate the shared lock:

```bash
REPIN=1 bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  run @nv_third_party_deps//:pin
```

Do not hand-edit `maven_install.json` or `MODULE.bazel.lock`.

## Docker

Build the app and resolve the real Bazel output directory:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/notary/notary-service:app

BAZEL_BIN_DIR="$(
  bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" info bazel-bin
)"

docker build \
  -f src/control-plane-services/notary/notary-service/Dockerfile \
  --build-arg APP_JAR=app.jar \
  -t notary:bazel \
  "${BAZEL_BIN_DIR}/src/control-plane-services/notary/notary-service"
```

The local profile needs an OAuth2 issuer, assertion issuer, and signing-key
file. Start it with values for your local issuer:

```bash
docker run --rm \
  -p 8080:8080 \
  -p 8181:8181 \
  -e SPRING_PROFILES_ACTIVE=local \
  -e AUTH_TOKEN_ISSUER="${AUTH_TOKEN_ISSUER}" \
  -e ASSERTION_ISSUER_URL="${ASSERTION_ISSUER_URL}" \
  -e VAULT_SECRETS_JSON_PATH=file:/run/notary/vault-secrets.json \
  -v "$(pwd)/src/control-plane-services/notary/notary-core/src/test/resources/vault-agent/integration-test-vault.json:/run/notary/vault-secrets.json:ro" \
  notary:bazel
```

Readiness is `http://localhost:8181/actuator/health/readiness`.

## GitHub CI

`bazel-java-ci.json` registers Notary with the root workflow. The workflow
discovers the component path, includes root Java tooling and dependency changes
as triggers, validates reverse dependencies after nv-boot changes, and uploads
the app jar, JUnit, JaCoCo, NOTICE, inventory, and OSRB delta outputs.

Notary has no Docker-backed tests, so it uses the `build-container` lane. The
root workflow currently runs the full matrix on each pull request and push,
even though dependency-aware selection logic is available.
