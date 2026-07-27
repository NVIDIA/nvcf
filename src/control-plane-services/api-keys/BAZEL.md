# Bazel for API Keys

API Keys is a single-module application in the root `nvcf` Bazel module. Run
every Bazel command in this guide from the monorepo root. Maven commands still
run from `src/control-plane-services/api-keys`.

## Shared configuration

The service does not own nested Bazel configuration:

- `.bazelversion` selects the Bazel release used by Bazelisk.
- `.bazelrc` stores repository defaults, including Java 25 and
  `--java_header_compilation=false`. It is configuration, not a lockfile.
  `common` options apply to query, build, test, and other commands. `build`
  options apply to build and commands that inherit build options.
- `.bazel_downloader_config` rewrites supported external download URLs. It
  does not select dependency versions and contains no credentials.
- `MODULE.bazel` declares Bazel rule modules, BOMs, and dependency roots.
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

Use one portable output root:

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" clean
```

Use `clean --expunge` only to reset a corrupted cache.

## Build

Build every API Keys target:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/api-keys/...
```

Build only the private application classes for compile feedback:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/api-keys:app_classes
```

`app_classes` is an internal input for tests and packaging. It is not a
published library or a synthetic core module.

Build the executable Spring Boot jar:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/api-keys:app
```

The executable output is:

```text
bazel-bin/src/control-plane-services/api-keys/app.jar
```

Inspect the launcher and generated build metadata:

```bash
unzip -p \
  bazel-bin/src/control-plane-services/api-keys/app.jar \
  META-INF/MANIFEST.MF

unzip -p \
  bazel-bin/src/control-plane-services/api-keys/app.jar \
  BOOT-INF/classes/git.properties
```

The manifest must name Spring Boot's `JarLauncher` and
`com.nvidia.apikeys.App`. The executable runtime must include Tomcat because
API Keys is a servlet application.

## Test and coverage

The test target starts Cassandra through Testcontainers and Docker Compose.
Run it with a working Docker daemon:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/api-keys:tests \
  --cache_test_results=no \
  --test_output=errors \
  --test_env=DOCKER_HOST \
  --test_env=DOCKER_TLS_VERIFY \
  --test_env=DOCKER_TLS_CERTDIR \
  --test_env=DOCKER_CERT_PATH
```

The same target represents the complete single-module service suite. Run one
test class with:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/api-keys:tests \
  --cache_test_results=no \
  --test_output=streamed \
  --test_arg=--exclude-classname='^(?!com.nvidia.apikeys.services.HashingServiceTest$).*$'
```

Run one method by combining the class filter with a method-name filter:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/api-keys:tests \
  --cache_test_results=no \
  --test_output=streamed \
  --test_arg=--exclude-classname='^(?!com.nvidia.apikeys.services.HashingServiceTest$).*$' \
  --test_arg=--include-methodname='.*#sha256_testVectors.*$'
```

Real Java test and coverage outputs are under:

```text
bazel-testlogs/src/control-plane-services/api-keys/tests/test.log
bazel-testlogs/src/control-plane-services/api-keys/tests/test.outputs/junit/TEST-junit-jupiter.xml
bazel-testlogs/src/control-plane-services/api-keys/tests/test.outputs/jacoco.exec
bazel-testlogs/src/control-plane-services/api-keys/tests/test.outputs/jacoco.xml
bazel-testlogs/src/control-plane-services/api-keys/tests/test.outputs/index.html
```

Use `test.outputs/junit/TEST-junit-jupiter.xml` for JUnit reporting. The nearby
outer `test.xml` contains one Bazel shell-wrapper testcase and is not the Java
report. Point Sonar at:

```text
src/control-plane-services/api-keys/tests/test.outputs/jacoco.xml
```

## NOTICE and OSRB delta

The checked component `NOTICE` is derived from exact jars under the executable
jar's `BOOT-INF/lib`. API Keys metadata owns only entries not already owned by
the shared nv-boot baseline.

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  run //src/control-plane-services/api-keys:generate_notice -- \
  --update-metadata --write

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/api-keys:notice_check_test

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/api-keys:osrb_dependency_delta
```

Generated outputs are under:

```text
bazel-bin/src/control-plane-services/api-keys/runtime_inventory.json
bazel-bin/src/control-plane-services/api-keys/osrb_dependency_delta.json
bazel-bin/src/control-plane-services/api-keys/osrb_dependency_delta.md
```

Do not run the standalone Maven NOTICE generator in this imported subtree.

## Dependency lock

All Java components share `@nv_third_party_deps`. A coordinate in the shared
hub is available for BUILD targets but is not automatically added to this
service's classpath. API Keys uses direct source labels for co-located nv-boot
libraries.

After changing a root Java dependency input, repin from the monorepo root:

```bash
REPIN=1 bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  run @nv_third_party_deps//:pin
```

Do not hand-edit `maven_install.json` or `MODULE.bazel.lock`.

## Docker

Build the app and resolve the real Bazel output directory:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/api-keys:app

BAZEL_BIN_DIR="$(
  bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" info bazel-bin
)"

docker build \
  -f src/control-plane-services/api-keys/Dockerfile \
  --build-arg APP_JAR=app.jar \
  -t api-keys:bazel \
  "${BAZEL_BIN_DIR}/src/control-plane-services/api-keys"
```

Start the local Cassandra dependency:

```bash
docker compose \
  -f src/control-plane-services/api-keys/local_env/docker-compose.yml \
  up -d
```

Run the application with the `local` profile:

```bash
docker run --rm \
  --name api-keys-service \
  --mount "type=bind,source=$(pwd)/src/control-plane-services/api-keys,target=/home/app,readonly" \
  -e SPRING_PROFILES_ACTIVE=local \
  -e SPRING_CASSANDRA_CONTACT_POINTS=host.docker.internal \
  -p 8080:8080 \
  -p 9090:9090 \
  api-keys:bazel
```

The bind mount provides `local_env/vault/secrets.json`. The host address lets
the application container reach Cassandra published by Docker Compose.
Readiness is `http://localhost:9090/admin/health/readiness`; the public health
endpoint is `http://localhost:8080/health`.

After validation, stop Cassandra:

```bash
docker compose \
  -f src/control-plane-services/api-keys/local_env/docker-compose.yml \
  down
```

## Maven coexistence

Maven remains independent:

```bash
cd src/control-plane-services/api-keys
mvn clean package
```

Bazel does not install or publish Maven-shaped project artifacts.

## GitHub CI

`bazel-java-ci.json` registers API Keys with the root workflow. Its Docker-
backed tests select the `docker-host` lane. The workflow also selects the
service for shared Java configuration and nv-boot changes, and uploads the app
jar, JUnit, JaCoCo, NOTICE, inventory, and OSRB delta outputs.

The detector contains dependency-aware selection, but current policy runs the
full matrix on each pull request and push for regression safety.
