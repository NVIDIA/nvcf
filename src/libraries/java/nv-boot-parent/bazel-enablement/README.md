# nv-boot-parent Maven to Bazel Migration Plan

Date: 2026-07-13
Branch: `feat/bazel`

## Objective

Migrate `nv-boot-parent` from Maven-only builds to Bazel while keeping Maven available during the transition. The migration must preserve the published Maven parent/BOM contract for current consumers and provide a Bazel path for local source builds, local Maven artifact consumption, CI validation, and downstream Spring Boot application cutover.

During coexistence, `com.nvidia.boot:nv-boot-parent:pom` remains a published parent POM. Bazel must not become the production publish path until artifact, metadata, NOTICE/license, and downstream application parity are proven.

## Current Scope Decisions

- Keep Maven as the default build/publish path during the first migration phases.
- Defer publishing jars from Bazel until parity is proven.
- Migrate `nv-boot-parent` first.
- Migrate `nv-boot-managed-parent` after `nv-boot-parent` is working and validated.
- Use `<workspace>/nvct/cloud-tasks` as the first downstream validation target.
- Support two local development modes:
  - native Bazel source-to-source development across repos;
  - consuming locally installed Maven artifacts from `~/.m2`.
- Treat the existing Maven-generated `NOTICE` as the initial legal/license baseline. No new License/NOTICE input is required before the first Bazel scaffold; review becomes necessary once Bazel can produce a comparable report.

## Downstream Applications In Scope

| Project | Path | Parent shape | Migration-sensitive behavior |
|---|---|---|---|
| `cloud-tasks` | `<workspace>/nvct/cloud-tasks` | OSS app using `nv-boot-parent` and `nv-boot-bom` | First validation target; protobuf/gRPC generation, `nvct-core` test fixtures, `local_env/cassandra/**`, Spring Boot executable jar, Maven-style `target/app.jar` compatibility |
| `cloud-functions` | `<workspace>/cloud-functions` | OSS app using `nv-boot-parent` and `nv-boot-bom` | Larger OSS app; protobuf/gRPC generation, `nvcf-core` test fixtures, `local_env/aws/**`, `local_env/cassandra/**`, `local_env/nats/**`, `docker-compose.test.yml`, Spring Boot executable jar |
| `nvct-service` | `<workspace>/nvct-service` | Managed app using `nv-boot-managed-parent` and `nv-boot-managed-bom` | Later phase after managed parent migration; Spring Boot executable jar, `git.properties`, filtered resources, `nvct-core` tests classifier dependency, OpenAPI/job paths |
| `nvcf-service` | `<workspace>/nvcf-service` | Managed app using `nv-boot-managed-parent` and `nv-boot-managed-bom` | Later phase after managed parent migration; Spring Boot executable jar, `git.properties`, filtered resources, `nvcf-core` tests classifier dependency, LocalStack/NATS/Cassandra/OpenAPI job paths |

The first two validate `nv-boot-parent` directly. The managed services are intentionally later because `nv-boot-managed-parent` is not part of the first migration slice.

## Toolchain Recommendation

Use Bazelisk and commit `.bazelversion` once the Phase 1 scaffold proves the candidate works locally and in CI.

Recommended first candidate:

- Bazel: `9.1.1`, current patch LTS release as of 2026-07-13.
- Dependency mode: Bzlmod, not legacy `WORKSPACE`.
- Maven dependency resolver: `rules_jvm_external` `7.0`.
- Java: Temurin/OpenJDK 25, matching the current Maven `java.version=25`.
- Bazel Java config: validate `--java_language_version=25` and `--tool_java_language_version=25`; use a pinned remote/local Java 25 runtime only after Phase 1 confirms support in the CI image.

References checked on 2026-07-13:

- https://github.com/bazelbuild/bazel/releases/tag/9.1.1
- https://registry.bazel.build/modules/rules_jvm_external
- https://bazel.build/external/overview

## Target Bazel Layout

Expected top-level files once implementation starts:

- `.bazelversion`
- `.bazelrc`
- `MODULE.bazel`
- `MODULE.bazel.lock`
- `maven_install.json`
- root `BUILD.bazel`
- one `BUILD.bazel` per Maven module
- `tools/bazel/` for shared Java, test, metadata, publication, and application packaging macros

The first checked-in Bazel implementation should be intentionally small: dependency resolution, Java 25 toolchain settings, and one or two low-risk module targets before expanding across all modules.

Current implementation status is captured in `bazel-enablement/phase-1-2-status.md`.
Phase 2 is complete for the `nv-boot-parent` jar modules; Phase 3 owns source
jars, generated POM/BOM metadata, Maven publishing parity, and license/NOTICE
parity.

## Plugin Compatibility Matrix

| Maven plugin | Current role | Bazel requirement |
|---|---|---|
| `maven-compiler-plugin` | Compiles with Java 25 and Lombok annotation processing | Configure Java 25 toolchain and Lombok as an annotation processor dependency |
| `maven-source-plugin` | Publishes source jars | Generate source jars for each published starter jar |
| `maven-surefire-plugin` | Runs unit tests and carries Mockito `-javaagent` JVM args | Centralize `java_test` defaults for JUnit 5, Mockito agent, system properties, and Spring test runtime behavior |
| `maven-failsafe-plugin` | Runs integration tests where apps use it | Model integration suites with Bazel tags such as `integration`, `requires-docker`, and CI-selectable target groups |
| `jacoco-maven-plugin` | Produces JaCoCo coverage reports | `nv_boot_library_test` writes JaCoCo HTML/XML/exec reports under `bazel-testlogs/<module>/tests/test.outputs`; use those JaCoCo XML files for this repo's Sonar wiring. `bazel coverage` plus `tools/bazel/lcov_to_sonar_generic.py` is retained for standard `java_test` or future mixed workspaces that need combined LCOV/Sonar generic XML |
| `flatten-maven-plugin` | Produces clean publishable POMs | Generate or template equivalent POMs and diff them against Maven flattened POMs before Bazel publishing |
| `properties-maven-plugin` | Generates `maven.properties` in build outputs | Preserve artifact-level `maven.properties` for nv-boot library jars; treat full app-level effective-property dumps as a downstream Spring Boot app migration requirement |
| `git-commit-id-maven-plugin` | Managed by `nv-boot-parent`; downstream apps use it for `git.properties` | Generate equivalent `git.properties` through Bazel stamping/status commands and package it in runtime resources |
| `license-maven-plugin` | Generates root `NOTICE` | Keep Maven as the NOTICE baseline during coexistence; replace only after Bazel-generated report diff is approved |
| `spring-boot-maven-plugin` | Packages downstream executable Spring Boot jars | Downstream app requirement only; nv-boot-parent publishes normal parent POM, BOM POM, and library jars |
| `maven-jar-plugin` test-jar usage in apps | Publishes tests classifier/test fixtures | Provide Bazel test-fixture artifacts and preserve downstream CI unpack behavior during cutover |
| `protobuf-maven-plugin` usage in apps | Generates Java/gRPC sources | Add Bazel protobuf/gRPC rules for downstream app pilots such as `cloud-tasks` |

## Phased Plan

### Phase 0: Baseline and Design Gates

Goal: establish the current Maven baseline and migration contract before adding Bazel behavior.

Deliverables:

- Record Maven/JDK/toolchain versions and successful local Maven commands.
- Record artifact outputs: jars, source jars, flattened POMs, `maven.properties`, JaCoCo reports, surefire reports, and root `NOTICE`.
- Record coexistence rules and plugin parity requirements.
- Confirm first downstream validation target and local development workflows.
- Open/track CI opt-in work so Bazel jobs can coexist with Maven jobs.

Exit criteria:

- `mvn clean verify` passes locally.
- `mvn clean install` passes locally and installs `0.0.1-SNAPSHOT` artifacts into `~/.m2`.
- `mvn license:aggregate-add-third-party -DskipTests` succeeds and `NOTICE` is available as the baseline.
- No publication changes are introduced.

Current Phase 0 baseline is captured in `bazel-enablement/phase-0-baseline.md`.

### Phase 1: Minimal Bazel Scaffold

Goal: create a non-invasive Bazel build skeleton without replacing Maven.

Deliverables:

- Add `.bazelversion`, `.bazelrc`, `MODULE.bazel`, `MODULE.bazel.lock`, and `maven_install.json`.
- Configure Bzlmod with `rules_jvm_external`.
- Resolve Spring Boot, Spring Cloud, Testcontainers, Lombok, Mockito, JaCoCo, WireMock, Cassandra, OpenTelemetry, and first-party dependencies.
- Add root-level Bazel smoke targets that do not alter Maven output.
- Add CI opt-in command examples using the agreed GitLab flag.

Exit criteria:

- `bazel mod tidy` or equivalent module sync succeeds.
- Maven and Bazel dependency resolution can run on a developer machine.
- Maven build remains unchanged and passing.

### Phase 2: Library Build and Test Parity

Goal: model all `nv-boot-parent` modules as Bazel Java targets.

Deliverables:

- `java_library` targets for every jar module.
- `java_test` targets for unit tests.
- Shared macro defaults for Java 25, Lombok, Mockito `-javaagent`, JUnit 5, Spring resources, and test JVM flags.
- Resource packaging parity for `META-INF/spring`, `spring.factories`, and module resources.
- Test target grouping that separates ordinary unit tests from Docker/Testcontainers-heavy tests if needed.

Exit criteria:

- `bazel build //...` succeeds.
- `bazel test //...` succeeds or has explicitly documented tags/exclusions for environment-bound tests.
- Maven and Bazel compiled class/resource inventories are comparable.

### Phase 3: Artifact and Metadata Parity

Goal: make Bazel produce Maven-compatible artifacts without switching production publishing.

Deliverables:

- Jar and source jar outputs per module.
- Generated or templated POMs comparable to Maven flattened POMs.
- Generated artifact-level `maven.properties` resources for published nv-boot library jars.
- Documented app-level `maven.properties` and `git.properties` requirements for downstream Spring Boot applications.
- Dependency list comparison between Maven and Bazel.
- NOTICE/license report comparison against Maven-generated `NOTICE`.

Exit criteria:

- Maven and Bazel jar contents match except for expected timestamps/build metadata.
- Flattened/generated POM diffs are reviewed and accepted.
- License/NOTICE differences are reviewed and accepted before replacing Maven license generation.

### Phase 4: CI Coexistence

Goal: add Bazel to CI without changing the default Maven path.

Deliverables:

- CI team adds Bazel toolchain support and opt-in jobs.
- Maven remains default build/test/publish.
- A special GitLab variable enables Bazel jobs, for example:

```yaml
variables:
  ENABLE_BAZEL_BUILD: "true"
```

- Bazel jobs publish test reports and coverage in CI-consumable locations.
- Bazel cache/remote cache strategy is documented but not required for first enablement.

Exit criteria:

- CI can run Maven-only by default.
- CI can run Bazel on demand with the feature flag.
- No production artifacts are published by Bazel.

Tracking ticket: NVCF-11033.

### Phase 5: First Downstream Pilot With cloud-tasks

Goal: validate `nv-boot-parent` Bazel outputs with the first real downstream application.

Validation target:

- `<workspace>/nvct/cloud-tasks`

Required pilot coverage:

- Build `cloud-tasks` against Bazel-built `nv-boot-parent` outputs.
- Validate native Bazel source override workflow.
- Validate local Maven artifact workflow using `0.0.1-SNAPSHOT` artifacts from `~/.m2`.
- Preserve `nvct-core` test-fixture/test-jar behavior or provide a Bazel replacement.
- Preserve protobuf/gRPC generation.
- Preserve `local_env/cassandra/**` test assets.
- Preserve executable Spring Boot app jar behavior and CI-compatible `target/app.jar` compatibility copy, if downstream jobs still expect that path.

Exit criteria:

- `cloud-tasks` can compile and test with Bazel-built `nv-boot-parent`.
- Docker/OpenAPI jobs have either identical artifact paths or coordinated Bazel replacements.
- Maven build remains available for rollback.

Current status: the Maven-consumer path has passed for `cloud-tasks` using
Bazel-produced NV Boot artifacts staged into `~/.m2/repository` as
`0.0.2-SNAPSHOT`. The validated flow updated `cloud-tasks` root `pom.xml` so
the parent version and `nv-boot.version` both pointed at `0.0.2-SNAPSHOT`, then
ran `mvn clean install` successfully. Native Bazel source override workflow and
full downstream Bazel app migration are still future work.

### Phase 6: Broader Downstream Cutover

Goal: move downstream applications one by one after `cloud-tasks` proves the migration shape.

Suggested order:

1. `cloud-tasks`
2. `cloud-functions`
3. `nvct-service`
4. `nvcf-service`

Each app cutover must validate Spring Boot executable jar packaging,
`git.properties`, app-level `maven.properties`, coverage/reporting, test
fixtures, generated sources, container/OpenAPI paths, and rollback to Maven.

### Phase 7: Managed Parent And Source Consumption

Goal: apply the validated Bazel-native consumption model to the managed parent
and downstream applications.

Deliverables:

- `nv-boot-managed-parent` migrated using the corrected nv-boot-parent pattern.
- Downstream Bazel consumers using source dependencies instead of Maven-shaped
  jars from URM.
- Maven publication retained through Maven while Maven consumers still exist.
- Migration of `nv-boot-managed-parent` and managed BOM/starter artifacts using lessons from `nv-boot-parent`.

Exit criteria:

- At least one release-equivalent downstream validation succeeds with Bazel artifacts.
- Maven parent/BOM compatibility is preserved until all consumers agree to retire it.

## Local Development Workflows

### Native Bazel Source Workflow

Use this when actively changing `nv-boot-parent` and a downstream application together.

Preferred mechanism:

```bash
bazel test //... --override_module=nv_boot_parent=<nv-boot-parent-root>
```

An equivalent committed or developer-local `local_path_override(...)` can be used when the downstream repo owns the override. Do not commit user-specific absolute paths unless the repo has a local-only include convention.

Reference:

- https://bazel.build/reference/command-line-reference#flag--override_module
- https://bazel.build/rules/lib/globals/module#local_path_override

### Local Maven Artifact Workflow

Use this when the downstream build should consume jars/POMs from the local Maven repository, similar to Maven `0.0.1-SNAPSHOT` development.

Producer:

```bash
cd <nv-boot-parent-root>
mvn clean install
```

Consumer:

- Configure `rules_jvm_external` with a developer-local Maven repository entry before remote repositories, for example a `file://` URL pointing at `~/.m2/repository`.
- Declare the `com.nvidia.boot:*:0.0.1-SNAPSHOT` artifacts needed by the consuming project.
- Repin or refresh Bazel dependency resolution after reinstalling local snapshots, because Bazel lockfiles and action keys are deliberately more static than Maven snapshot resolution.

This workflow is useful for compatibility testing, but the native source override workflow should be preferred for day-to-day multi-repo development because it avoids stale local snapshot jars.

### Remote Publishing

Do not publish Maven-shaped jars from the Bazel toolchain. Maven remains the
remote publish path for Maven consumers during coexistence, and downstream
Bazel consumers should depend on `nv-boot-parent` source targets through Bzlmod.

A temporary Bazel publish bridge was validated and then removed after we
realized it was the wrong long-term direction. The durable migration path is
boring and Bazel-native: source dependencies for Bazel consumers, Maven
publication for Maven consumers until those consumers cut over.

## Open Questions for Later Phases

- Exact CI image/package source for Bazelisk and Temurin 25.
- Whether the Bazel publish bridge should use the existing Maven `settings.xml`
  credentials permanently, or move to another approved internal credential
  pattern after CI pilot validation.
- Whether Spring Boot executable jars should be produced by an existing rule or a small internal packaging rule.
- Final Legal/OSRB acceptance criteria for replacing Maven `license-maven-plugin` output.
- Final CI artifact path and Sonar job wiring for Bazel-generated generic
  coverage XML.
