# Bazel for Instance Cluster Management

ICMS is a two-module Spring Boot service imported into the root `nvcf` Bazel
module. Run every Bazel command in this guide from the monorepo root. Maven
commands still run from `src/control-plane-services/instance-cluster-management`.

## Shared configuration

The service does not own nested Bazel configuration:

- `.bazelversion` selects the Bazel release used by Bazelisk.
- `.bazelrc` stores repository defaults, including Java 25 and
  `--java_header_compilation=false`.
- `MODULE.bazel` declares Bazel rule modules, BOMs, and dependency roots.
- `maven_install.json` is the generated exact lock for third-party Java
  coordinates.
- `MODULE.bazel.lock` is the generated Bzlmod lock.

The root uses `local_jdk`. Install a full JDK 25 and set `JAVA_HOME`.

## Output root and clean

Use one portable output root:

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" clean
```

Use `clean --expunge` only to reset a corrupted cache.

## Build

Build every ICMS target:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" build //src/control-plane-services/instance-cluster-management/...
```

Build the core library only:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" build //src/control-plane-services/instance-cluster-management/icms-core:icms_core
```

Build the test-fixtures target consumed by `icms-service` tests and by
downstream Bzlmod consumers:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" build //src/control-plane-services/instance-cluster-management/icms-core:test_fixtures
```

The fixtures jar is:

```text
bazel-bin/src/control-plane-services/instance-cluster-management/icms-core/libtest_fixtures.jar
```

The fixtures jar keeps the test resources at its root, so `application-test.yaml`
and paths such as `requests/cluster_create_request.json` stay resolvable through
`ClassPathResource`. The `local_env/` Compose bundle ships beside it in
`libintegration_local_env_resources.jar`, which reaches consumers through
`runtime_deps`. Both land at the classpath root, matching the Maven tests jar.

`IntegrationTest` resolves `local_env/docker-compose.test.yml` from the
classpath and only falls back to the working directory, so downstream consumers
outside this monorepo take the bundle from that jar instead of vendoring a copy.
A consumer pinned to a commit that predates the bundle fails during
`IntegrationTest` static initialization with `Missing classpath resource
local_env/docker-compose.test.yml`.

Build the executable Spring Boot jar:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" build //src/control-plane-services/instance-cluster-management/icms-service:app
```

The executable output is:

```text
bazel-bin/src/control-plane-services/instance-cluster-management/icms-service/app.jar
```

## Test and coverage

ICMS integration tests use Docker Compose, Cassandra, NATS, and Testcontainers.
Run them with a working Docker daemon:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" test //src/control-plane-services/instance-cluster-management/... --cache_test_results=no --test_output=errors --test_env=DOCKER_HOST --test_env=DOCKER_TLS_VERIFY --test_env=DOCKER_TLS_CERTDIR --test_env=DOCKER_CERT_PATH
```

When running locally, also preserve `PATH` so Testcontainers can find the host
`docker compose` CLI, and `HOME` so Docker Desktop can discover CLI plugins:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" test //src/control-plane-services/instance-cluster-management/... --cache_test_results=no --test_output=errors --test_env=PATH --test_env=HOME --test_env=DOCKER_HOST --test_env=DOCKER_TLS_VERIFY --test_env=DOCKER_TLS_CERTDIR --test_env=DOCKER_CERT_PATH
```

Core coverage outputs are under:

```text
bazel-testlogs/src/control-plane-services/instance-cluster-management/icms-core/tests/test.outputs/junit/TEST-junit-jupiter.xml
bazel-testlogs/src/control-plane-services/instance-cluster-management/icms-core/tests/test.outputs/jacoco.xml
```

Service coverage outputs are under:

```text
bazel-testlogs/src/control-plane-services/instance-cluster-management/icms-service/tests/test.outputs/junit/TEST-junit-jupiter.xml
bazel-testlogs/src/control-plane-services/instance-cluster-management/icms-service/tests/test.outputs/jacoco.xml
```

## NOTICE and OSRB delta

The checked component `NOTICE` is derived from exact jars under the executable
jar's `BOOT-INF/lib`. ICMS metadata owns only entries not already owned by the
shared nv-boot baseline.

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" run //src/control-plane-services/instance-cluster-management:generate_notice -- --update-metadata --write

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" test //src/control-plane-services/instance-cluster-management:notice_check_test

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" build //src/control-plane-services/instance-cluster-management:osrb_dependency_delta
```

Do not run a standalone Maven NOTICE generator in this imported subtree.

## Dependency lock

All Java components share `@nv_third_party_deps`. A coordinate in the shared hub
is available for BUILD targets but is not automatically added to this service's
classpath. ICMS uses direct source labels for co-located nv-boot libraries.

After changing a root Java dependency input, repin from the monorepo root:

```bash
REPIN=1 bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" run @nv_third_party_deps//:pin
```

Do not hand-edit `maven_install.json` or `MODULE.bazel.lock`.

## Docker

Build the app and resolve the real Bazel output directory:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" build //src/control-plane-services/instance-cluster-management/icms-service:app

BAZEL_BIN_DIR="$(
  bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" info bazel-bin
)"

docker build -f src/control-plane-services/instance-cluster-management/icms-service/Dockerfile --build-arg APP_JAR=app.jar -t instance-cluster-management:bazel "${BAZEL_BIN_DIR}/src/control-plane-services/instance-cluster-management/icms-service"
```

Start local dependencies:

```bash
docker compose -f src/control-plane-services/instance-cluster-management/local_env/docker-compose.yml up -d
```

Run the application with the `local` profile:

```bash
docker run --rm --name instance-cluster-management --mount "type=bind,source=$(pwd)/src/control-plane-services/instance-cluster-management,target=/home/app,readonly" -e SPRING_PROFILES_ACTIVE=local -p 8080:8080 instance-cluster-management:bazel
```

After validation, stop dependencies:

```bash
docker compose   -f src/control-plane-services/instance-cluster-management/local_env/docker-compose.yml   down
```

## Maven coexistence

Maven remains independent:

```bash
cd src/control-plane-services/instance-cluster-management
mvn clean package
```

Bazel does not install or publish Maven-shaped project artifacts.

## GitHub CI

`bazel-java-ci.json` registers ICMS with the root workflow. Its Docker-backed
tests select the `docker-host` lane. The workflow also selects the service for
shared Java configuration and nv-boot changes, and uploads the app jar, JUnit,
JaCoCo, NOTICE, inventory, and OSRB delta outputs.
