# Phase 3 Status

> Historical record: the Maven-shaped Bazel artifact targets and helper files
> documented below were removed on 2026-07-17. Maven now owns Maven publication,
> while Bazel consumers use first-party source targets. Current instructions are
> in `BAZEL.md` and `phase-3-handoff.md`.

Date: 2026-07-14
Branch: `feat/bazel`

## Current Focus

Phase 3 added Maven-shaped packaging and local install support while Maven
remains canonical for publication during coexistence. The Bazel remote
publish/deploy bridge was removed after validation because downstream Bazel
consumers should use Bazel source targets, not URM Maven-shaped artifacts.

`cloud-tasks` validation has started after local artifact/package parity became
meaningful enough to exercise a real Maven consumer.

## Published Artifact Targets

Maven-shaped artifact support now covers:

```text
//:parent_pom
//:parent_pom_install_local
//nv-boot-bom:bom
//nv-boot-bom:bom_install_local
//nv-boot-mock-servers-test:maven_artifact
//nv-boot-mock-servers-test:maven_artifact_install_local
//nv-boot-starter-audit:maven_artifact
//nv-boot-starter-audit:maven_artifact_install_local
//nv-boot-starter-cassandra:maven_artifact
//nv-boot-starter-cassandra:maven_artifact_install_local
//nv-boot-starter-core:maven_artifact
//nv-boot-starter-core:maven_artifact_install_local
//nv-boot-starter-data-migration-notification:maven_artifact
//nv-boot-starter-data-migration-notification:maven_artifact_install_local
//nv-boot-starter-exceptions:maven_artifact
//nv-boot-starter-exceptions:maven_artifact_install_local
//nv-boot-starter-jwt:maven_artifact
//nv-boot-starter-jwt:maven_artifact_install_local
//nv-boot-starter-observability:maven_artifact
//nv-boot-starter-observability:maven_artifact_install_local
//nv-boot-starter-registries:maven_artifact
//nv-boot-starter-registries:maven_artifact_install_local
//nv-boot-starter-reloadable-properties:maven_artifact
//nv-boot-starter-reloadable-properties:maven_artifact_install_local
//nv-boot-starter-telemetry:maven_artifact
//nv-boot-starter-telemetry:maven_artifact_install_local
```

New Bazel helpers:

```text
tools/bazel/maven.bzl
tools/bazel/publication_deps.bzl
tools/bazel/maven_publish_properties.json
tools/bazel/generated_maven_deps.bzl
tools/bazel/update_maven_deps.py
```

`MODULE.bazel` is the manually edited source of truth for Bazel/Coursier root
Maven coordinates and explicit version pins. `tools/bazel/maven_publish_properties.json`
contains only publish-time property metadata for the few module-local pins that
need Maven property references in generated POMs; it does not contain versions.
`tools/bazel/generated_maven_deps.bzl` is generated from `MODULE.bazel` plus
that publish-property metadata so publication helpers can resolve property
names to the versions owned by `MODULE.bazel`.

`publication_deps.bzl` contains Maven publication coordinate helpers, but it
does not own version numbers. Module `BUILD.bazel` files own their published
POM dependency lists locally, and use those helpers so most dependencies can
inherit versions from `nv-boot-parent` and the imported BOMs.

Generated publication POMs either inherit versions from the parent/BOM or, for
module-local pins, emit both the Maven property and the dependency property
reference from generated metadata. For example, the audit POM contains
`<json-patch.version>1.13</json-patch.version>` and
`<version>${json-patch.version}</version>`, with the version derived from
`MODULE.bazel`.

Each published jar module passes `artifact_name`, `description`, and
`published_deps` directly to `nv_boot_local_maven_artifact`. This keeps the
module's Maven publication contract close to the module without adding a second
Bazel-owned hand-maintained version map for publication.

`maven.bzl` includes low-level packaging helpers plus the preferred module
macros:

- `nv_boot_publish_pom`, which generates a Maven-publish-shaped POM.
- `nv_boot_maven_pom_artifact`, which creates Maven-named POM-only artifacts
  and local-install targets for POM-packaged publications.
- `nv_boot_maven_artifact`, which creates Maven-named jar, sources jar, POM,
  and local-install targets.
- `nv_boot_local_maven_pom_artifact`, which wraps POM-only local artifact
  generation with the shared local version.
- `nv_boot_local_maven_artifact`, which wraps jar artifact generation using
  module-owned publication metadata and centralized coordinate helpers.

New scripts:

```text
tools/bazel/src/main/java/com/nvidia/boot/bazel/CoverageConsoleLauncher.java
tools/bazel/install_maven_artifact.sh
tools/bazel/lcov_to_sonar_generic.py
tools/bazel/lcov_to_sonar_generic_test.sh
tools/bazel/maven_deps_check_test.sh
tools/bazel/update_maven_deps.py
```

Each jar module artifact target creates Maven-named outputs:

```text
bazel-bin/<module>/<artifactId>-0.0.1-SNAPSHOT.jar
bazel-bin/<module>/<artifactId>-0.0.1-SNAPSHOT-sources.jar
bazel-bin/<module>/<artifactId>-0.0.1-SNAPSHOT.pom
```

Local installs also write checksum sidecars (`.sha1` and `.md5`) so the temp
repository can be used as a Maven file repository without checksum warnings.

The binary jar currently includes:

- compiled classes from the existing `nv_boot_library` output;
- `src/main/java` source files, matching the Maven parent resource behavior;
- `src/main/resources` files;
- `META-INF/maven/<groupId>/<artifactId>/pom.xml`;
- `META-INF/maven/<groupId>/<artifactId>/pom.properties`;
- a minimal `maven.properties`.

The generated jar module POMs intentionally inherit from
`com.nvidia.boot:nv-boot-parent:<version>`. Dependencies that are managed by
`nv-boot-parent`, Spring Boot, Spring Cloud, Testcontainers, or another imported
BOM are emitted without `<version>`. Non-managed module-local pins use
`pinned_dep(...)`, which resolves group ID, artifact ID, property name, and
property value through `generated_maven_deps.bzl`.

Pinned dependency update workflow:

1. Update the explicit artifact version in `MODULE.bazel`.
2. Run `python3 tools/bazel/update_maven_deps.py --write`.
3. If the artifact or BOM coordinate set changed, run
   `REPIN=1 bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache run @nv_boot_maven//:pin`.
4. Run `bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache test //...`.

The sources jar includes:

- `src/main/java` source files;
- `src/main/resources` files;
- `META-INF/maven/<groupId>/<artifactId>/pom.xml`;
- `META-INF/maven/<groupId>/<artifactId>/pom.properties`.

## Commands

Build all Maven-shaped artifacts:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  build //:parent_pom \
        //nv-boot-bom:bom \
        //nv-boot-mock-servers-test:maven_artifact \
        //nv-boot-starter-audit:maven_artifact \
        //nv-boot-starter-cassandra:maven_artifact \
        //nv-boot-starter-core:maven_artifact \
        //nv-boot-starter-data-migration-notification:maven_artifact \
        //nv-boot-starter-exceptions:maven_artifact \
        //nv-boot-starter-jwt:maven_artifact \
        //nv-boot-starter-observability:maven_artifact \
        //nv-boot-starter-registries:maven_artifact \
        //nv-boot-starter-reloadable-properties:maven_artifact \
        //nv-boot-starter-telemetry:maven_artifact
```

Regenerate dependency metadata after editing `MODULE.bazel` or
`tools/bazel/maven_publish_properties.json`:

```bash
python3 tools/bazel/update_maven_deps.py --write
```

Check generated dependency metadata for drift:

```bash
python3 tools/bazel/update_maven_deps.py --check

bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  build //tools/bazel:maven_deps_check

bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  test //tools/bazel:maven_deps_check_test
```

Run tests with per-target JaCoCo HTML/XML report generation:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  test //nv-boot-starter-core:tests \
  --cache_test_results=no \
  --test_output=errors
```

The generated report files are preserved under:

```text
bazel-testlogs/nv-boot-starter-core/tests/test.outputs/index.html
bazel-testlogs/nv-boot-starter-core/tests/test.outputs/jacoco.xml
bazel-testlogs/nv-boot-starter-core/tests/test.outputs/jacoco.exec
```

Bazel native coverage is not the primary coverage path for this repo because
`nv_boot_library_test` uses `use_testrunner = False` and runs JUnit
ConsoleLauncher directly. Use the JaCoCo XML files above for nv-boot-parent
Sonar coverage.

For standard Bazel `java_test` targets, or future mixed workspaces that need one
combined generic report, run Bazel native coverage and convert the combined
LCOV report to Sonar generic coverage XML:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  coverage //... \
  --cache_test_results=no \
  --test_output=errors \
  --combined_report=lcov \
  --instrumentation_filter=//nv-boot

python3 tools/bazel/lcov_to_sonar_generic.py \
  --input bazel-out/_coverage/_coverage_report.dat \
  --output ${TMPDIR:-/tmp}/nv-boot-parent-sonar-coverage.xml
```

CI/Sonar can consume that generic XML through:

```text
sonar.coverageReportPaths=<path-to-sonar-generic-coverage.xml>
```

For Java Sonar coverage, CI can also publish the per-target JaCoCo XML files
with:

```text
sonar.coverage.jacoco.xmlReportPaths=<comma-separated-jacoco.xml-paths>
```

Run tests and build all Maven-shaped artifacts in one command:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  test //... \
       //:parent_pom \
       //nv-boot-bom:bom \
       //nv-boot-mock-servers-test:maven_artifact \
       //nv-boot-starter-audit:maven_artifact \
       //nv-boot-starter-cassandra:maven_artifact \
       //nv-boot-starter-core:maven_artifact \
       //nv-boot-starter-data-migration-notification:maven_artifact \
       //nv-boot-starter-exceptions:maven_artifact \
       //nv-boot-starter-jwt:maven_artifact \
       //nv-boot-starter-observability:maven_artifact \
       //nv-boot-starter-registries:maven_artifact \
       //nv-boot-starter-reloadable-properties:maven_artifact \
       //nv-boot-starter-telemetry:maven_artifact \
  --cache_test_results=no \
  --test_output=errors
```

Install one artifact into the default local Maven repository:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  run //nv-boot-starter-data-migration-notification:maven_artifact_install_local
```

Install parent POM, BOM POM, and jar modules into a temporary Maven repository
for validation. The local install helper defaults to `~/.m2/repository`; set
`MAVEN_REPO` to keep validation isolated:

```bash
MAVEN_REPO=${TMPDIR:-/tmp}/nv-boot-parent-phase3-m2 \
  bazel-bin/<module>/maven_artifact_install_local.sh
```

Remote publish/deploy from Bazel has been removed. Maven remains the remote
publish path during coexistence, and downstream Bazel consumers should consume
this repo through source targets.

## Validation Completed

Focused pilot tests:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  test //nv-boot-starter-data-migration-notification:tests \
  --cache_test_results=no \
  --test_output=errors
```

Result: passed.

All Maven-shaped jar module artifacts:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  build //nv-boot-mock-servers-test:maven_artifact \
        //nv-boot-starter-audit:maven_artifact \
        //nv-boot-starter-cassandra:maven_artifact \
        //nv-boot-starter-core:maven_artifact \
        //nv-boot-starter-data-migration-notification:maven_artifact \
        //nv-boot-starter-exceptions:maven_artifact \
        //nv-boot-starter-jwt:maven_artifact \
        //nv-boot-starter-observability:maven_artifact \
        //nv-boot-starter-registries:maven_artifact \
        //nv-boot-starter-reloadable-properties:maven_artifact \
        //nv-boot-starter-telemetry:maven_artifact
```

Result: passed.

Full Bazel suite:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache test //... \
  --cache_test_results=no \
  --test_output=errors
```

Result: 11 / 11 test targets passed at this phase-3 checkpoint. Later commits add
test targets; see the higher totals in the sections below for the current count.

Local install validation used:

```bash
MAVEN_REPO=${TMPDIR:-/tmp}/nv-boot-parent-phase3-m2 \
  bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  run //:parent_pom_install_local

MAVEN_REPO=${TMPDIR:-/tmp}/nv-boot-parent-phase3-m2 \
  bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  run //nv-boot-bom:bom_install_local

MAVEN_REPO=${TMPDIR:-/tmp}/nv-boot-parent-phase3-m2 \
  bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  run //nv-boot-starter-data-migration-notification:maven_artifact_install_local
```

Result: installed the parent POM, BOM POM, all 11 binary jars, all 11 sources
jars, all 11 jar POMs, checksum files, and `_remote.repositories` markers
under:

```text
${TMPDIR:-/tmp}/nv-boot-parent-phase3-m2/com/nvidia/boot/<artifactId>/0.0.1-SNAPSHOT/
```

The installed parent POM and BOM POM were diffed against their checked-in POMs
with no differences. Generated jar module POMs were scanned to confirm that
Spring, Spring Boot, Spring Cloud, Jakarta, Jackson, Micrometer, and
OpenTelemetry dependencies rely on inherited versions instead of repeating
BOM-managed versions. Module-local pinned dependencies keep Maven property
references such as `${nats.version}` and get the corresponding `<properties>`
entries from metadata generated from `MODULE.bazel`. Repeated temp installs work, and
installed artifact files are normalized to mode `0644`.

Historical note: an earlier Bazel remote-deploy bridge successfully rewrote
artifact metadata to a requested version, but that path has since been removed.
The current design keeps Maven as the remote publish mechanism and uses Bazel
source targets for Bazel consumers.

Design review follow-up:

- Fixed `update_maven_deps.py` so commented example coordinates inside
  `MODULE.bazel` are ignored.
- Made the `artifacts = [` array header match more tolerant.
- Added `//tools/bazel:maven_deps_check_test`, backed by `rules_shell`, so
  `bazel test //...` catches generated dependency metadata drift.
- Replaced Spring copy-paste POM metadata in generated POMs with NVIDIA/nv-boot
  defaults and made those fields overridable in `tools/bazel/maven.bzl`.
- Kept root-level `maven.properties` in generated jars intentionally because
  the Maven build writes that file through `properties-maven-plugin`. The
  Bazel-generated nv-boot library jars currently use this file for artifact
  identity metadata only; it is not intended to mirror a consuming app's full
  effective Maven property set or resolved dependency graph.

Temp Maven consumer validation:

```text
${TMPDIR:-/tmp}/nv-boot-parent-phase3-consumer/
```

The consumer uses:

- `com.nvidia.boot:nv-boot-parent:0.0.1-SNAPSHOT` as its parent;
- `com.nvidia.boot:nv-boot-bom:0.0.1-SNAPSHOT` as an imported BOM;
- `com.nvidia.boot:nv-boot-starter-core`;
- `com.nvidia.boot:nv-boot-starter-exceptions`;
- `com.nvidia.boot:nv-boot-starter-data-migration-notification`;
- `com.nvidia.boot:nv-boot-starter-registries`;
- `com.nvidia.boot:nv-boot-starter-telemetry`.

The module dependencies above do not specify versions; the imported
`nv-boot-bom` supplies the module versions.

Validation command:

```bash
mvn -s settings.xml \
  -Dmaven.repo.local=${TMPDIR:-/tmp}/nv-boot-parent-phase3-consumer-m2-neutral-metadata \
  -Dorg.slf4j.simpleLogger.log.org.apache.maven.cli.transfer.Slf4jMavenTransferListener=warn \
  clean compile
```

Result: passed. The consumer compiled against five Bazel-installed NV Boot jar
modules while resolving the parent POM, BOM POM, and those modules from
`file://${TMPDIR:-/tmp}/nv-boot-parent-phase3-m2`. This was repeated after moving
BOM-managed publication dependencies to versionless generated POM entries. No
NV Boot `.lastUpdated` failure markers were present in the fresh consumer Maven
cache.

Post-review fresh Maven consumer validation:

```text
Installed Bazel artifacts:
${TMPDIR:-/tmp}/nv-boot-parent-phase3-m2-post-review.hUuJz7

Fresh Maven consumer:
${TMPDIR:-/tmp}/nv-boot-parent-phase3-consumer-post-review.BtPHSl

Fresh Maven consumer cache:
${TMPDIR:-/tmp}/nv-boot-parent-phase3-consumer-m2-post-review.xIWTRW
```

Validation command:

```bash
mvn -s settings.xml \
  -Dmaven.repo.local=${TMPDIR:-/tmp}/nv-boot-parent-phase3-consumer-m2-post-review.xIWTRW \
  -Dorg.slf4j.simpleLogger.log.org.apache.maven.cli.transfer.Slf4jMavenTransferListener=warn \
  clean compile
```

Result: passed. The fresh consumer compiled `ConsumerSmoke.class` against the
Bazel-installed parent POM, BOM POM, and NV Boot module jars. Maven resolved the
NV Boot artifacts from the fresh file repository, and the installed POMs kept
the expected parent/BOM relationships plus pinned property references such as
`${json-patch.version}` and `${awssdk-java.version}`.

Downstream `cloud-tasks` Maven consumer validation:

```text
Consumer project:
<workspace>/nvct/cloud-tasks

Validation version:
0.0.2-SNAPSHOT
```

Bazel-produced NV Boot artifacts were installed into the default local Maven
repository for compatibility validation:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  run //:parent_pom_install_local

bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  run //nv-boot-bom:bom_install_local

bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  run //nv-boot-starter-core:maven_artifact_install_local
```

The command created Maven-layout artifacts under:

```text
$HOME/.m2/repository/com/nvidia/boot/<artifactId>/0.0.2-SNAPSHOT/
```

The `cloud-tasks` root POM was updated locally for the validation only:

```diff
- <version>2.0.1</version>
+ <version>0.0.2-SNAPSHOT</version>

- <nv-boot.version>2.0.1</nv-boot.version>
+ <nv-boot.version>0.0.2-SNAPSHOT</nv-boot.version>
```

Validation command:

```bash
cd <workspace>/nvct/cloud-tasks
mvn clean install
```

Result: passed. The Maven reactor built `cloud-tasks`, `nvct-core`, and
`nvct-service` against the Bazel-produced `0.0.2-SNAPSHOT` NV Boot parent, BOM,
and starter artifacts from the local Maven repository.

Downstream `cloud-tasks` Maven consumer validation with version `15665e3b`:

An earlier experiment deployed Bazel-produced NV Boot artifacts to URM with
version `15665e3b`. That experiment proved the generated artifacts were Maven
compliant, but it is no longer the intended Bazel path. The `cloud-tasks` root
POM was updated locally for the validation only:

```diff
- <version>2.0.1</version>
+ <version>15665e3b</version>

- <nv-boot.version>2.0.1</nv-boot.version>
+ <nv-boot.version>15665e3b</nv-boot.version>
```

Validation command:

```bash
cd <workspace>/nvct/cloud-tasks
mvn clean install
```

Result: passed. This confirms a real Maven downstream project can consume the
Bazel-built `nv-boot-parent`, `nv-boot-bom`, and starter artifacts when they
are presented as Maven artifacts. Future Bazel consumers should consume source
targets instead.

The resulting Spring Boot app jar was inspected at:

```text
<workspace>/nvct/cloud-tasks/nvct-service/target/app.jar
```

It contains:

```text
BOOT-INF/classes/maven.properties
BOOT-INF/classes/git.properties
META-INF/maven/com.nvidia.nvct/nvct-service-oss/pom.xml
META-INF/maven/com.nvidia.nvct/nvct-service-oss/pom.properties
```

Observed plugin implications:

- `git.properties` is generated by `git-commit-id-maven-plugin` declared by
  `cloud-tasks/nvct-service`.
- `maven.properties` is generated by `properties-maven-plugin` inherited from
  `nv-boot-parent`.
- `pom.properties` and `pom.xml` are Maven artifact metadata.
- `cloud-tasks/.gitlab-ci.yml` mutates project versions with
  `mvn --quiet versions:set versions:commit -DnewVersion=${NEXT_VERSION}` and
  passes `-Dspring.application.version=${NEXT_VERSION}` during Maven test,
  build, and deploy jobs.

Additional `maven.properties` investigation:

- Legacy `kaizen-core-starter` loaded classpath `maven.properties` as a Spring
  `PropertySource` so banner text and runtime metrics/tracing code could read
  properties such as `kaizen-spring-boot-parent.version` and
  `spring-boot.version`.
- Current `nv-boot-starter-core` does not load `maven.properties`; it loads
  `nv-boot-core-defaults.properties` and `git.properties` for
  `spring.application.version` fallback behavior.
- The downstream `cloud-tasks/nvct-service` app-level `maven.properties`
  contains the app/root project property `nv-boot.version`, but it does not
  contain one property per resolved nv-boot starter dependency. The starter
  versions are resolved through the imported `nv-boot-bom`; each nested
  dependency jar carries its own `META-INF/maven/.../pom.properties` and
  artifact-level `maven.properties`.
- Therefore, nv-boot-parent's Bazel output should preserve per-artifact Maven
  metadata for parent/BOM/library publication, but it should not generate
  dependency-graph entries in a consuming app's `maven.properties`. Full
  app-level effective-property dumps are a future downstream Spring Boot app
  migration concern.

The removed remote-deploy bridge was also validated against a local
`file://` Maven repository before deletion. Keep that as historical proof only;
do not recreate the bridge unless the migration strategy changes again.

Bazel JaCoCo report generation during ordinary tests:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  test //nv-boot-starter-core:tests \
  --cache_test_results=no \
  --test_output=errors
```

Result: passed. The test generated:

```text
bazel-testlogs/nv-boot-starter-core/tests/test.outputs/index.html
bazel-testlogs/nv-boot-starter-core/tests/test.outputs/jacoco.xml
bazel-testlogs/nv-boot-starter-core/tests/test.outputs/jacoco.exec
```

Bazel native coverage and Sonar generic XML conversion were also smoke-tested,
but JaCoCo XML under `test.outputs` is the supported path for these
`nv_boot_library_test` targets:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  coverage //nv-boot-starter-core:tests \
  --cache_test_results=no \
  --test_output=errors \
  --combined_report=lcov \
  --instrumentation_filter=//nv-boot-starter-core

python3 tools/bazel/lcov_to_sonar_generic.py \
  --input bazel-out/_coverage/_coverage_report.dat \
  --output ${TMPDIR:-/tmp}/nv-boot-parent-sonar-coverage.xml
```

Result: passed as a converter smoke test. For CI coverage in this repo, prefer
the generated `bazel-testlogs/<module>/tests/test.outputs/jacoco.xml` files.

## Historical Limitations Before Final Cleanup

This section records the state before the Bazel-to-Maven publication bridge
and related compatibility targets were removed. It is retained as migration
history and is superseded by the current checkpoint below.

- Full downstream Spring Boot application packaging parity is not complete.
  Maven-generated app jars contain a large effective-property dump in
  `BOOT-INF/classes/maven.properties`, app-owned `git.properties`, Spring Boot
  `BOOT-INF` layout, and app Maven metadata. That work belongs to downstream app
  migration, not to the nv-boot-parent library publication targets.
- Bazel-generated NV Boot library jars intentionally contain only artifact-level
  `maven.properties` metadata, plus standard `META-INF/maven/...` metadata.
- BOM POM local-install support copies the checked-in `nv-boot-bom/pom.xml`;
  generating BOM metadata from Bazel module metadata is not implemented yet.
- Maven file-repository validation works for the current non-unique
  `0.0.1-SNAPSHOT` artifacts, but timestamped snapshot metadata has not been
  implemented.
- A Bazel publish bridge is implemented and validated against both a local
  `file://` Maven repository and a GitLab manual URM publish pilot. The pilot
  deployed 13 artifacts with `NEXT_VERSION=15665e3b` to
  `https://urm.nvidia.com/artifactory/sw-nvcf-maven` using repository id
  `nvcf`.
- Downstream Maven consumption from URM is validated. `cloud-tasks` passed
  `mvn clean install` with both `nv-boot-parent` and `nv-boot.version` set to
  `15665e3b`.
- Bazel JaCoCo HTML/XML report generation and Sonar generic XML conversion are
  implemented. A project-local opt-in `bazel-build-test` job has been added to
  `.gitlab-ci.yml`, and the shared-pipeline handoff is documented in
  `bazel-enablement/ci-bazel-handoff.md`; the shared CI template has not been
  updated yet.
- License/NOTICE parity is not implemented yet.

## Reusable Skill

A reusable Codex skill has been created for this Maven-parent-to-Bazel
migration pattern:

```text
$HOME/.codex/skills/maven-parent-bazel-enablement
```

The next intended consumer is `nv-boot-managed-parent`.

## Current Checkpoint: CodeRabbit Cleanup And Downstream CI Proof

Checkpoint date: 2026-07-17.

- Reviewed CodeRabbit feedback on nv-boot-parent MR `!82` and verified each
  finding against the current branch.
- Replaced committed personal paths and macOS-only `/private/tmp` examples
  across developer, migration, and skill documentation with portable
  placeholders, `$HOME`, and `${TMPDIR:-/tmp}`.
- Hardened `tools/bazel/generate_notice.py` so POM XML from local caches or
  remote repositories rejects DTD and entity declarations before parsing.
- Added malicious-POM regression coverage to
  `//tools/bazel:notice_check_test`; the target passes uncached.
- Updated both repository-owned Bazel enablement skills with the portable
  cache and hardened POM parsing rules, validated their YAML/frontmatter, and
  synchronized the installed copies under `$HOME/.codex/skills`.
- Skipped CodeRabbit's missing-value CLI finding because the Bazel-to-Maven
  publication parser and deploy path no longer exist.
- Cloud Tasks completed the downstream Linux CI proof at commit
  `1125f64cac0bd7ad9f85a6b04b430b0e7715c438`. GitLab job `366805692` used
  Bazel 9.1.1, Java 25, and Docker-in-Docker; all three Bazel test targets and
  the full build passed, NOTICE covered 260 dependencies, and Bazel/Sonar/JUnit
  artifacts uploaded successfully.

The next repository-level action is to commit and push this review cleanup,
then request a fresh CodeRabbit pass with `@coderabbitai review`. The next
planned migration consumer remains `nv-boot-managed-parent` unless priorities
change.

## Current Checkpoint: Strict Resolution And Review Closure

Checkpoint date: 2026-07-17.

- Enabled missing-checksum failures in the shared third-party hub and repinned
  successfully; no repository exception was required.
- Aligned all five resolved ASM modules to 9.9 because JaCoCo and production
  JNR dependencies share the hub.
- Added NOTICE validation for stale explicit roots and packaged runtime version
  drift, while preserving classifier-only dependency handling.
- Declared `rules_python` directly and routed Bazel NOTICE actions through a
  `py_binary` instead of host `python3`.
- Regenerated `NOTICE` and metadata for ASM 9.9; the dependency count remains
  208.
- Renamed the documentation tree to `bazel-enablement` in nv-boot-parent and
  cloud-tasks and updated path references.
- Full nv-boot-parent validation passed: 13 / 13 uncached tests and 32 / 32
  build targets.
- The optional 13-module `mvn clean install` coexistence check also passed and
  did not rewrite the Bazel-maintained NOTICE content.

Cloud-tasks must pin the new nv-boot-parent commit after these changes are
pushed, repin its shared hub, and rerun its downstream validation.
