# Bazel for Instance Cluster Management

ICMS is a two-module Spring Boot service imported into the root `nvcf` Bazel
module. Run every command in this guide from the monorepo root. The monorepo
copy is Bazel-only and does not contain project POMs. Any Maven build support
remains in the independent source repository.

## Bazel in Maven terms

ICMS keeps the familiar Maven directory layout. Bazel changes how the modules
and build actions are named.

| Maven idea | Bazel idea |
|---|---|
| A Maven module | A directory with a `BUILD.bazel` file, called a package |
| A POM dependency | An entry in a target's `deps` list |
| A plugin or parent-POM convention | A shared Bazel macro |
| A Maven goal | A Bazel target selected by a label |
| `mvn test` | `bazel test //src/control-plane-services/instance-cluster-management/...` |

A label has the package path before the colon and the target name after it:

```text
//src/control-plane-services/instance-cluster-management/icms-core:icms_core
```

## Project structure and targets

```text
instance-cluster-management/
  BUILD.bazel                 component-wide test data
  icms-core/
    BUILD.bazel
    src/main/java/            reusable core code
    src/main/resources/       core resources, when present
    src/test/java/            core tests
    src/test/resources/       core test resources
  icms-service/
    BUILD.bazel
    src/main/java/            Spring Boot application code
    src/main/resources/       application resources
    src/test/java/            service tests
    src/test/resources/       service test resources
```

Keep one `BUILD.bazel` at each Maven-like module root. Do not add Bazel package
boundaries below the standard source directories.

The important core targets are `icms_core`, `tests`, and `tests_coverage`. The
important service targets are `app_classes`, `app`, `tests`,
`tests_coverage`, and `icms-service-oss-image`.
`icms-service` depends on `icms_core`, just as a Maven application module would
depend on its core module.

## Shared Java macros

Both modules load shared macros from `//rules/java:defs.bzl`:

- `nvcf_java_library` compiles Java into a reusable library. The core library
  is `icms_core`. The service library is `app_classes` because the Spring Boot
  packaging target consumes it.
- `nvcf_java_test` is a macro that declares one native `java_test` target.
  The normal target name is `tests`. Bazel and IntelliJ use this same target.
- `nvcf_java_coverage_test` is a macro that declares the separate
  `tests_coverage` target. It runs `tests` and writes JUnit and JaCoCo reports
  for CI. Its `coverage_target` identifies the production library covered by
  the tests.
- `spring_boot_app` creates the executable service jar.
- `java_oci_image` creates the service container image.

Core and service modules use the same macros because the Bazel operations are
the same. Their directory and target names describe their different roles.

## Bazel terms by example

These terms describe different parts of the same declaration:

| Term | Meaning | ICMS example |
|---|---|---|
| Macro | A Starlark function that writes one or more rule calls for us | `nvcf_java_test(...)` |
| Rule | A Bazel building block that knows how to create an output | `java_test(...)` inside `//rules/java:defs.bzl` |
| Target | One named object created by a rule | `tests` in `icms-core` |
| Label | The full Bazel address of a target | `//src/control-plane-services/instance-cluster-management/icms-core:tests` |

For example, `icms-core/BUILD.bazel` contains this kind of macro call:

```starlark
nvcf_java_test(
    name = "tests",
    srcs = ICMS_CORE_TEST_SRCS,
    deps = ICMS_CORE_DEPS + [
        ":icms_core",
        # Other test dependencies are listed here.
    ],
)
```

The macro contains the actual `_java_test(...)` call. `_java_test` is a private
name for the standard `java_test` rule from `rules_java`. That rule declares
the `//src/control-plane-services/instance-cluster-management/icms-core:tests`
target.

The `nvcf_java_library(name = "icms_core", ...)` macro call declares the
`//src/control-plane-services/instance-cluster-management/icms-core:icms_core`
target. The `icms-service` `app_classes` target lists that label in `deps`.
This is the Bazel equivalent of the Maven service module depending on the core
module.

The separate `nvcf_java_coverage_test(name = "tests_coverage", ...)` macro call
declares an `sh_test` target. It runs `tests` for CI reports but does not own
the Java test source files.

## IntelliJ-compatible BUILD structure

The JetBrains Bazel plugin learns roots from Bazel targets. Keep these rules:

1. Give each `src/main/java` tree exactly one IDE-visible library owner.
2. Give each `src/test/java` tree exactly one IDE-visible native Java test
   owner. A compatibility fixture library may compile the same files only when
   it sets `ide_visible = False` and produces a downstream artifact.
3. Put `src/main/resources` only in production resources.
4. Put `src/test/resources` only in test resources.
5. Keep the resource helper targets generated by the macros. IntelliJ uses
   them to identify Resources Root and Test Resources Root.
6. Keep coverage and report targets separate from the native Java test. They
   must not own Java source files.

The canonical project view is `tools/intellij/.managed.bazelproject`. The
active file under `.bazelbsp` must enable `rules_java`, derive targets from
directories, and allow manual targets to sync. The managed file already
includes ICMS and these settings.

After changing a `BUILD.bazel` file, run a Bazel project resync in IntelliJ.
Do not mark roots manually because the next sync replaces those settings. A
correct sync marks main sources, test sources, main resources, and test
resources.

The Project view shows the filesystem, so Java packages can look like ordinary
directories there. Select Packages from the Project tool window's view menu to
see the Java package hierarchy. In Packages view, open the Options menu and
turn off Modules. Otherwise, IntelliJ shows Bazel targets such as
`app_classes` and `tests_coverage` as module names. Turn off Library Contents
too if external jars make the view noisy. Use Packages view for Java packages
and the Bazel tool window for Bazel targets.

Each `nvcf_java_test(name = "tests")` macro call declares exactly one native
`java_test` target named `tests`. This same-name rule lets the JetBrains Bazel
plugin offer gutter test actions. The new plugin does not add a per-test Run
action to the Java editor context menu. JetBrains tracks that feature gap in
[BAZEL-2755](https://youtrack.jetbrains.com/issue/BAZEL-2755). Right-click
actions remain available on targets in the Bazel tool window. Use
`tests_coverage` when JUnit or JaCoCo report files are required.

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

The fixture label is a compatibility `java_library` target. Managed Spot builds
depend on its label and exact output name:

```text
bazel-bin/src/control-plane-services/instance-cluster-management/icms-core/libtest_fixtures.jar
```

Its `no-ide` tag keeps it out of the IntelliJ project model. The native `tests`
target remains the IDE owner of `src/test/java`, so the Run gutter stays
available. Do not replace the fixture library with an alias to `tests`; an alias
changes the jar name and pulls monorepo-only runfiles into external Bzlmod
consumers.

The target keeps the test resources at its root, so `application-test.yaml`
and paths such as `requests/cluster_create_request.json` stay resolvable
through `ClassPathResource`. The `local_env/` Compose bundle ships beside it in
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
bazel-testlogs/src/control-plane-services/instance-cluster-management/icms-core/tests_coverage/test.outputs/junit/TEST-junit-jupiter.xml
bazel-testlogs/src/control-plane-services/instance-cluster-management/icms-core/tests_coverage/test.outputs/jacoco.xml
```

Service coverage outputs are under:

```text
bazel-testlogs/src/control-plane-services/instance-cluster-management/icms-service/tests_coverage/test.outputs/junit/TEST-junit-jupiter.xml
bazel-testlogs/src/control-plane-services/instance-cluster-management/icms-service/tests_coverage/test.outputs/jacoco.xml
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

## GitHub CI

`bazel-java-ci.json` registers ICMS with the root workflow. Its Docker-backed
tests select the `docker-host` lane. The workflow also selects the service for
shared Java configuration and nv-boot changes, and uploads the app jar, JUnit,
JaCoCo, NOTICE, inventory, and OSRB delta outputs.
