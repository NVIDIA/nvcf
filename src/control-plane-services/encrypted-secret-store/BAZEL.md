# Bazel for Encrypted Secret Store

ESS is a Bazel-only component in the root `nvcf` module. Run every command in
this guide from the monorepo root.

## Setup

Install a full JDK 25, set `JAVA_HOME`, start Docker for integration tests, and
set a reusable output root:

```bash
export JAVA_HOME="<path-to-jdk-25>"
export PATH="${JAVA_HOME}/bin:${PATH}"
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"
```

The root owns `.bazelversion`, `.bazelrc`, `MODULE.bazel`,
`maven_install.json`, and `MODULE.bazel.lock`. ESS has no nested Bazel module
or dependency lock.

To clear only Bazel outputs:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" clean
```

Use `clean --expunge` only when the cache is corrupted.

## Build

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/ess/...

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/ess/ess-encryption:ess_encryption

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/ess/ess-core:ess_core

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/ess/ess-service:app
```

The executable output is:

```text
bazel-bin/src/control-plane-services/ess/ess-service/app.jar
```

Inspect its launcher and generated metadata:

```bash
unzip -p \
  bazel-bin/src/control-plane-services/ess/ess-service/app.jar \
  META-INF/MANIFEST.MF

unzip -p \
  bazel-bin/src/control-plane-services/ess/ess-service/app.jar \
  BOOT-INF/classes/git.properties

unzip -p \
  bazel-bin/src/control-plane-services/ess/ess-service/app.jar \
  BOOT-INF/classes/maven.properties
```

## Test

Run all tests without cached results:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/ess/... \
  --cache_test_results=no \
  --test_output=errors
```

Run unit tests without Docker:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" test \
  //src/control-plane-services/ess/ess-encryption:unit_tests \
  //src/control-plane-services/ess/ess-core:unit_tests \
  --cache_test_results=no \
  --test_output=errors
```

Run Docker-backed integration tests:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" test \
  //src/control-plane-services/ess/ess-encryption:integration_tests \
  //src/control-plane-services/ess/ess-core:integration_tests \
  //src/control-plane-services/ess/ess-service:tests \
  --cache_test_results=no \
  --test_output=errors
```

Run one class:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/ess/ess-core:unit_tests \
  --cache_test_results=no \
  --test_output=streamed \
  --test_arg=--exclude-classname='^(?!com.nvidia.ess.controller.SecretControllerTest$).*$'
```

Run one method:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/ess/ess-core:unit_tests \
  --cache_test_results=no \
  --test_output=streamed \
  --test_arg=--exclude-classname='^(?!com.nvidia.ess.controller.SecretControllerTest$).*$' \
  --test_arg=--include-methodname='.*#createSecret_onUnknownBodyProperty_400.*$'
```

Confirm the class and method names before using the focused examples.

JUnit and JaCoCo outputs are under:

```text
bazel-testlogs/src/control-plane-services/ess/<module>/<target>/test.log
bazel-testlogs/src/control-plane-services/ess/<module>/<target>/test.outputs/junit/TEST-junit-jupiter.xml
bazel-testlogs/src/control-plane-services/ess/<module>/<target>/test.outputs/jacoco.exec
bazel-testlogs/src/control-plane-services/ess/<module>/<target>/test.outputs/jacoco.xml
bazel-testlogs/src/control-plane-services/ess/<module>/<target>/test.outputs/index.html
```

The outer `test.xml` describes the Bazel shell wrapper. Use the JUnit file
under `test.outputs/junit` for Java test reporting.

## NOTICE and dependency delta

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  run //src/control-plane-services/ess:generate_notice -- \
  --update-metadata --write

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/ess:notice_check_test

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/ess:osrb_dependency_delta
```

Generated inventories and the license-grouped delta are under:

```text
bazel-bin/src/control-plane-services/ess/runtime_inventory.json
bazel-bin/src/control-plane-services/ess/osrb_dependency_delta.json
bazel-bin/src/control-plane-services/ess/osrb_dependency_delta.md
```

After changing root dependency inputs, regenerate the shared lock:

```bash
REPIN=1 bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  run @nv_third_party_deps//:pin
```

Do not edit either lockfile manually.

## Maven baseline parity

The import was validated against the executable Maven JAR built from source
commit `4d9983ec74e1a54009740b12c77e523c630f14f0`. The source version pins for
Nimbus JOSE+JWT 10.9.1, Tomcat 11.0.24, Logback 1.5.37, Logstash Logback
Encoder 7.4, and OpenTelemetry instrumentation annotations 2.21.0 are
preserved.

The root shared graph intentionally selects newer compatible versions for ASM,
Commons Collections, jnr-constants, jnr-posix, and JSR 305. Other inventory
differences are packaging-only:

- Bazel first-party ESS and nv-boot jars use target-derived names.
- Bazel omits compile-time Lombok from the executable runtime.
- The root excludes the legacy standalone AOP Alliance jar because Spring
  Framework 7 embeds those classes in `spring-aop`.
- Bazel retains Spring Boot starter marker jars. They contain dependency
  metadata and no application classes.

## Docker

Build the app and use the real Bazel output directory as the Docker context:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/ess/ess-service:app

BAZEL_BIN_DIR="$(
  bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" info bazel-bin
)"

docker build \
  -f src/control-plane-services/ess/ess-service/Dockerfile \
  --build-arg APP_JAR=app.jar \
  -t ess:bazel \
  "${BAZEL_BIN_DIR}/src/control-plane-services/ess/ess-service"
```

For local Cassandra, use the checked-in Compose bundle:

```bash
docker compose \
  -f src/control-plane-services/ess/local_env/docker-compose.yaml \
  -p ess-local up -d
```

Start ESS with the local profile and make the local configuration available at
the repository-relative path expected by the service:

```bash
docker run --rm \
  -p 8080:8080 \
  -p 9464:9464 \
  -e SPRING_PROFILES_ACTIVE=local \
  -e SPRING_CASSANDRA_CONTACT_POINTS=host.docker.internal \
  -v "$(pwd)/src/control-plane-services/ess/local_env:/workspace/local_env:ro" \
  -w /workspace \
  ess:bazel
```

Health is available at `http://localhost:8080/health`. Metrics are available at
`http://localhost:9464/metrics`.

## GitHub CI

`bazel-java-ci.json` registers ESS with the root GitHub workflow. The
`docker-host` lane provides Docker for Testcontainers tests. The descriptor
also registers `//src/control-plane-services/ess:runtime_inventory.json` with
the root dependency collector.
