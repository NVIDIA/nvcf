# Phase 1 and 2 Status

Date: 2026-07-13
Branch: `feat/bazel`

## Summary

The Bazel scaffold is in place and Phase 2 library build/test parity is complete
for the `nv-boot-parent` jar modules.

Implemented:

- Bazel version pin: `.bazelversion` set to `9.1.1`.
- Bzlmod module: `MODULE.bazel`.
- Bazel dependency lockfiles: `MODULE.bazel.lock` and `maven_install.json`.
- Maven dependency resolution through `rules_jvm_external` `7.0`.
- Java 25 Bazel configuration using `remotejdk_25`.
- Shared Java helper macros in `tools/bazel/java.bzl`, including `nv_boot_library` and `nv_boot_library_test`.
- Lombok annotation processor configuration in `tools/bazel/BUILD.bazel`.
- JUnit 6 console-launcher based `java_test` wrapper with Mockito `-javaagent`.
  The wrapper passes the generated test jar explicitly to JUnit and uses
  `--fail-if-no-tests` so zero-test discovery cannot report a false pass.
- Bazel library and test targets for every `nv-boot-parent` jar module:
  - `//nv-boot-mock-servers-test:nv_boot_mock_servers_test`
  - `//nv-boot-starter-audit:nv_boot_starter_audit`
  - `//nv-boot-starter-cassandra:nv_boot_starter_cassandra`
  - `//nv-boot-starter-core:nv_boot_starter_core`
  - `//nv-boot-starter-data-migration-notification:nv_boot_starter_data_migration_notification`
  - `//nv-boot-starter-exceptions:nv_boot_starter_exceptions`
  - `//nv-boot-starter-jwt:nv_boot_starter_jwt`
  - `//nv-boot-starter-observability:nv_boot_starter_observability`
  - `//nv-boot-starter-registries:nv_boot_starter_registries`
  - `//nv-boot-starter-reloadable-properties:nv_boot_starter_reloadable_properties`
  - `//nv-boot-starter-telemetry:nv_boot_starter_telemetry`
- Cassandra Docker/Testcontainers tests are included in the default Bazel graph
  through `//nv-boot-starter-cassandra:tests`, which is tagged `integration` and
  `requires-docker`.
- Fixed-port WireMock suites are tagged `exclusive` so Bazel does not run them
  concurrently:
  - `//nv-boot-mock-servers-test:tests`
  - `//nv-boot-starter-registries:tests`

Not implemented yet:

- Source jar generation.
- POM/BOM generation.
- Bazel Maven publishing.
- Bazel NOTICE/license replacement.
- JaCoCo/Sonar parity.
- Downstream `cloud-tasks` validation.

## Commands Verified

Dependency pinning:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache run @nv_boot_maven//:pin
```

Bazel build/test:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache test //...
```

Result:

- Passed.
- Current Bazel surface includes 15 non-test targets and 11 test targets.
- All 11 Bazel test targets passed.
- Docker-backed Cassandra tests are part of `//...` and passed locally through
  `//nv-boot-starter-cassandra:tests`.

Forced test execution with logs/errors:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache test //... \
  --cache_test_results=no \
  --test_output=errors
```

Result:

- Passed.
- All 11 Bazel test targets executed real JUnit tests.
- Total elapsed time: about `00:42`; critical path: about `00:29`.

Maven coexistence check:

```bash
mvn clean verify
```

Result:

- Passed after Phase 2 edits.
- 13 Maven reactor modules succeeded.
- Total time: about `01:58`.

## Implementation Notes

### Version Ownership

`MODULE.bazel` is intentionally limited to root coordinates:

- imported BOMs;
- high-level starter/module artifacts needed to seed the Maven graph;
- parent-owned explicit pins that are not otherwise pulled transitively;
- build/test tools such as Lombok and the JUnit Platform console launcher.

When a version is managed by an imported BOM, the root coordinate is declared
without a version. When the root project or a module property owns the version,
the root coordinate keeps the explicit version only if that coordinate is needed
as a root.

BUILD files still declare direct `@nv_boot_maven//:` labels for jars whose classes are
imported by source or test code. Those labels may come from transitive artifacts
in `maven_install.json`; they do not need corresponding root entries in
`MODULE.bazel`.

For example, `nv-boot-starter-observability` directly imports
`io.opentelemetry.semconv.ExceptionAttributes`, so its BUILD file depends on
`@nv_boot_maven//:io_opentelemetry_semconv_opentelemetry_semconv`. `MODULE.bazel` does
not pin `opentelemetry-semconv`; it keeps the unversioned
`io.micrometer:micrometer-tracing-bridge-otel` root that pulls the semconv
artifact through Maven resolution. The JUnit Platform console standalone
artifact is still declared as `org.junit.platform:junit-platform-console-standalone:6.0.3`
because the Maven resolver did not receive a managed version for that tool
artifact.

### JUnit Discovery

JUnit 6 ConsoleLauncher does not discover tests in Bazel's generated test jar
when invoked with a bare `--scan-classpath`, because Bazel may collapse the long
JVM classpath into a manifest classpath jar and JUnit only scans directories on
the JVM system classpath by default. The shared `nv_boot_library_test` macro
therefore passes the generated test jar explicitly with `--class-path` and
`--scan-classpath`, and includes `--fail-if-no-tests`.

`nv-boot-starter-reloadable-properties` has tests that intentionally use
Maven-style filesystem paths such as `src/test/resources/...`. A root genrule
mirrors that module's test resources into Bazel runfiles at the same relative
path, and the test target adds `src/test/resources` as a JUnit classpath root so
file-based fixture discovery works under Bazel.

### Java 25 Runtime

The local shell and Maven use Temurin/OpenJDK `25.0.2`, but Homebrew Bazel initially reported Java 21 for its local Java runtime. Building Java 25 helper classes and then executing them on Java 21 caused class-version failures.

Resolution:

- `.bazelrc` uses `remotejdk_25` for both target and tool Java runtimes.
- `.bazelrc` sets Java language and tool language version to `25`.

### Maven Resolver Lockfile

`rules_jvm_external` with `resolver = "maven"` requires a lock file to generate the pinned `@maven` repository. An empty v3 `maven_install.json` was added first, then populated by `bazel run @nv_boot_maven//:pin`.

Resolution:

- `MODULE.bazel` declares `lock_file = "//:maven_install.json"`.
- `maven_install.json` is committed and should be updated with:

```bash
REPIN=1 bazel run @nv_boot_maven//:pin
```

### Lombok and Header Compilation

Lombok changes public APIs, so the Bazel Java plugin is declared with `generates_api = True`. Bazel then attempted Turbine header compilation, but Lombok does not support Turbine.

Resolution:

- `.bazelrc` sets `--java_header_compilation=false`.
- This favors correctness and Maven parity for the migration over header-compilation performance.

### JUnit 6 Launcher

Spring Boot `4.0.7` resolved JUnit `6.0.3`. The JUnit Platform console launcher now expects subcommands.

Resolution:

- The Bazel test macro invokes:

```text
execute
--include-classname=.*(Test|IntegrationTest)
--fail-if-no-tests
--class-path=$(location :<test-target>.jar)
--scan-classpath=$(location :<test-target>.jar)
```

### Mockito Agent

Maven Surefire attaches Mockito as a JVM agent. The Bazel test macro mirrors that behavior with:

```text
-javaagent:$(location @nv_boot_maven//:org_mockito_mockito_core)
```

## Phase 2 Module Notes

### Dependency Declarations

The Bazel BUILD files intentionally declare direct dependencies more explicitly
than Maven poms. Maven's compile classpath includes transitive dependencies from
starters and BOM-managed artifacts, while Bazel Java strict-deps requires a
direct edge for imported types such as:

- `tools.jackson.core:jackson-core`
- `com.fasterxml.jackson.core:jackson-databind`
- `io.cloudevents:cloudevents-api`
- `io.netty:netty-transport`
- `org.slf4j:slf4j-api` for Lombok `@Slf4j` generated fields
- Spring Security OAuth2 sub-artifacts used for constants and JWT types

### BOM/POM Modules

`nv-boot-bom` remains Maven-only for Phase 2 because it is a `pom` module, not a
jar module. Bazel POM/BOM generation belongs to Phase 3.

## Next Steps

Recommended Phase 3 work:

1. Generate source jars for each published starter jar.
2. Generate or template POMs comparable to Maven flattened POMs.
3. Generate `maven.properties` and `git.properties` equivalents.
4. Compare Maven and Bazel jar contents/resources.
5. Compare Bazel dependency/license output against the Maven `NOTICE` baseline.
6. Start the `cloud-tasks` downstream pilot once artifact metadata parity is ready.

The next modules should reuse the existing macros and add only module-specific direct deps/resources. Testcontainers, WireMock, and Docker-dependent tests should be tagged explicitly when they enter the Bazel graph.
