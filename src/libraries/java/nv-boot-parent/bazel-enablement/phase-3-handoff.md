# Phase 3 Handoff

Date: 2026-07-14
Branch: `feat/bazel`

## Restart Resume

After restarting Codex, resume with:

```text
Continue Phase 3 from <nv-boot-parent-root>/bazel-enablement/phase-3-handoff.md. Read phase-3-status.md too, check git status, and retry the GitLab MR if the GitLab MaaS OAuth is available.
```

Repository:

```text
<nv-boot-parent-root>
```

Current branch:

```text
feat/bazel
```

## Superseding Update: 2026-07-17

The Bazel-to-Maven artifact bridge described in older sections of this handoff
has been removed. Bazel consumers now use nv-boot source targets directly.
Maven remains solely responsible for Maven parent/BOM and jar publication
during coexistence.

Removed from the Bazel path:

- generated publication POMs and Maven-named jars;
- local Maven install and remote deploy targets;
- `tools/bazel/maven.bzl` and `tools/bazel/publication_deps.bzl`;
- generated publication dependency metadata and its drift test;
- parent POM, BOM POM, and module `maven_artifact` targets.

NOTICE now uses the explicit shipped roots in
`tools/bazel/notice_roots.json`. The shared third-party dependency hub is named
`nv_third_party_deps` in upstream and downstream modules. Any publication
targets or commands later in this file are historical evidence, not current
instructions. Use `BAZEL.md` and the repo-owned enablement skill for current
commands.

Current validation:

- nv-boot-parent Bazel build passed with 31 source/build targets and NOTICE
  metadata for 208 third-party dependencies;
- all 13 nv-boot-parent Bazel test targets passed uncached;
- the independent Maven reactor passed `mvn clean install` for all 13 modules;
- Cloud Tasks built successfully and all three Bazel test targets passed
  against this local cleaned nv-boot-parent module through `--override_module`.

Current remaining work:

1. Commit and push nv-boot-parent, then update Cloud Tasks' `git_override` to
   that commit.
2. Repin and validate Cloud Tasks without `--override_module`.
3. Run the opt-in Bazel GitLab pipeline while the default Maven job remains
   green.

A second repo-owned skill now captures the downstream application pattern:

```text
bazel-enablement/skills/spring-boot-app-bazel-enablement
```

Use it for Cloud Tasks-style application migrations such as cloud-functions.
It complements, rather than replaces, `maven-parent-bazel-enablement`.

First restart actions:

1. Read this file and `bazel-enablement/phase-3-status.md`.
2. Run `git status --short` to confirm the same worktree state.
3. Push the validated nv-boot-parent changes.
4. Pin Cloud Tasks to the pushed upstream commit, repin without a local
   override, validate, and run CI.

## Current State

Phase 1 and Phase 2 have been pushed to the remote Git server.

The Bazel build is established for the `nv-boot-parent` jar modules using:

- Bazel `9.1.1`
- Bzlmod
- `rules_jvm_external` `7.0`
- Java 25 toolchain
- `nv_boot_library`
- `nv_boot_library_test`
- JUnit ConsoleLauncher with flat, ASCII, no-color test logs
- Central Lombok annotation processor configuration

Maven remains canonical for published artifacts during coexistence.
`nv-boot-parent:pom` must remain a published Maven parent POM.

The Phase 3 Maven-shaped packaging experiment succeeded and was subsequently
removed after direct downstream Bazel source consumption was established.
`bazel-enablement/phase-3-status.md` is retained as a historical record.

## Important Decisions

- `cloud-tasks` is the first downstream validation target. Maven-consumer
  validation has passed using Bazel-produced `0.0.2-SNAPSHOT` artifacts staged
  into the local Maven repository. A temporary URM publish experiment with
  version `15665e3b` also passed, but that bridge has since been removed.
- New downstream-consumption lesson from `cloud-tasks`: Bazel-native consumers
  should depend on `nv-boot-parent` source targets directly, not on
  Bazel-produced Maven-shaped jars. To make that clean, `nv-boot-parent` and
  downstream applications contribute to the neutral shared
  `@nv_third_party_deps` repository instead of exposing a generic external
  `@maven` repo in the public target graph. The hub contains third-party jars,
  not first-party nv-boot artifacts.
- Test machines are expected to have Docker.
- Cassandra Docker/Testcontainers tests are part of
  `//nv-boot-starter-cassandra:tests`; there is no separate
  `docker_tests` Bazel target.
- Keep Maven and Bazel coexisting. Maven remains the default build/publish path
  until Bazel publishing/reporting parity has been reviewed.

## Resume Note: 2026-07-17

Cloud-tasks attempted to consume this repo through Bzlmod:

```python
bazel_dep(name = "nv_boot_parent", version = "0.0.0")

git_override(
    commit = "a7359539d3ac04edef222f637bbd3685b056e42b",
    module_name = "nv_boot_parent",
    remote = "ssh://git@gitlab-master.nvidia.com:12051/nvcf/nvcf-libraries/java/nv-boot-parent.git",
)
```

The downstream labels were changed from `@maven//:com_nvidia_boot_*` to labels
such as:

```text
@nv_boot_parent//nv-boot-starter-core:nv_boot_starter_core
@nv_boot_parent//nv-boot-starter-cassandra:nv_boot_starter_cassandra
@nv_boot_parent//nv-boot-starter-observability:nv_boot_starter_observability
```

This exposed a problem in nv-boot-parent's Bazel-native dependency boundary.
The starter targets used to reference third-party jars through generic labels
such as `@maven//:org_springframework_boot_spring_boot_health`. In a downstream
Bzlmod consumer, that generic `@maven` label resolves against the consumer's
Maven repo, not a private nv-boot-parent repo. That forced downstream projects
to duplicate nv-boot-parent's entire third-party Maven root list, which is not
acceptable.

Completed upstream fix on 2026-07-17:

1. Renamed the nv-boot-parent third-party dependency hub from generic `maven`
   to neutral `nv_third_party_deps` in `MODULE.bazel`.
2. Updated `use_repo(...)` accordingly.
3. Replaced `@maven//...` labels across nv-boot-parent BUILD files and
   `tools/bazel/*.bzl` with `@nv_third_party_deps//...`.
4. Repinned `maven_install.json` with:

```bash
REPIN=1 bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  run @nv_third_party_deps//:pin
```

5. Refreshed Bazel-native NOTICE metadata because the repin changed the
   Guava-related resolved transitive closure:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  run //:generate_notice -- --update-metadata --write
```

6. Validated:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  build //... \
  --verbose_failures

bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache \
  test //... \
  --cache_test_results=no \
  --test_output=errors
```

Result: `bazel build //...` passed and 15/15 Bazel tests passed, including
`//nv-boot-starter-cassandra:tests`.

Next downstream task:

1. Commit and push this nv-boot-parent change.
2. Update `cloud-tasks` `git_override` to the pushed commit hash.
3. Remove temporary third-party roots that were added only to work around the
   old leaked `@maven` boundary.
4. Repin cloud-tasks and retry direct source consumption without adding
   nv-boot-parent's private third-party dependency roots to cloud-tasks.

## Phase 3 Proposed Order

1. Add Bazel source jar generation for each published starter/library module.
2. Generate or template Maven POM metadata for Bazel-built artifacts.
3. Generate or template BOM metadata for `nv-boot-bom` parity.
4. Add local Maven install parity so Bazel-built jars can be consumed locally
   like `mvn install`.
5. Keep local Maven-shaped artifacts for compatibility validation, but keep
   Maven as the remote publish path during coexistence.
6. Add or document JaCoCo/Sonar parity.
7. Add or document license/NOTICE parity.
8. Create a reusable Codex skill for Maven-parent-to-Bazel migration, with
   `nv-boot-managed-parent` as the next intended consumer.

## Validation Commands

Run Bazel tests without reusing cached test results:

```bash
bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache test //... \
  --cache_test_results=no \
  --test_output=errors
```

Run Maven coexistence check:

```bash
mvn clean install
```

Repin Maven dependencies after `MODULE.bazel` artifact/BOM changes:

```bash
REPIN=1 bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache run @nv_third_party_deps//:pin
```

Build all local Maven-shaped artifact targets:

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
        //nv-boot-starter-telemetry:maven_artifact \
        //nv-boot-starter-telemetry:maven_artifact
```

Remote publish/deploy from Bazel has been removed. Maven remains the remote
publish path for Maven consumers.

## Known Report Locations

Bazel test logs:

```text
bazel-testlogs/<module>/tests/test.log
```

Maven Surefire reports:

```text
<module>/target/surefire-reports/
```

Maven JaCoCo reports:

```text
<module>/target/site/jacoco/
<module>/target/jacoco.exec
```

## Next Task

Continue Phase 3 from the current local artifact parity point.

The following are complete:

- parent POM packaging/install target;
- BOM POM packaging/install target;
- Maven-shaped binary jar, sources jar, generated parent-inheriting POM, and
  local-install target for every published jar module;
- centralized Bazel publication coordinate helpers in
  `tools/bazel/publication_deps.bzl`;
- module-owned published POM metadata in each module's `BUILD.bazel`;
- local artifact macros in `tools/bazel/maven.bzl`;
- generated jar module POMs that inherit managed dependency versions from
  `nv-boot-parent` / imported BOMs instead of repeating Spring-managed pins;
- `MODULE.bazel` is the manually edited source of truth for Bazel/Coursier root
  coordinates and explicit version pins;
- `tools/bazel/maven_publish_properties.json` contains only publish-time Maven
  property metadata and no version values;
- `tools/bazel/generated_maven_deps.bzl` is regenerated from `MODULE.bazel`
  plus that publish-property metadata;
- module-local publish pins use Maven property references and property values
  generated from `MODULE.bazel`;
- `//tools/bazel:maven_deps_check_test` runs the generated-metadata drift check
  as part of `bazel test //...`;
- `rules_shell` is now an explicit Bzlmod dependency for that shell test, so
  `MODULE.bazel.lock` was updated;
- root-level `maven.properties` remains in generated jars intentionally because
  the Maven build writes it through `properties-maven-plugin`. For nv-boot
  library jars this is artifact-level metadata, not an app-level effective
  Maven property dump;
- temp Maven repo validation under `${TMPDIR:-/tmp}/nv-boot-parent-phase3-m2`;
- temp Maven consumer compile validation under
  `${TMPDIR:-/tmp}/nv-boot-parent-phase3-consumer`.

The temp consumer imports the Bazel-installed `nv-boot-bom` and depends on
multiple NV Boot modules without explicit versions.

## Latest Validation

The most recent validation after making `MODULE.bazel` the source of truth for
Bazel dependency versions and keeping generated publication POMs on the
parent-inheriting, managed-version model:

- kept coordinate helper functions in `tools/bazel/publication_deps.bzl`
  without storing duplicate version values there;
- replaced the version-bearing dependency JSON with
  `tools/bazel/maven_publish_properties.json` plus
  `tools/bazel/update_maven_deps.py --write/--check`;
- added `//tools/bazel:maven_deps_check` to fail if generated dependency
  metadata drifts from `MODULE.bazel` or the publish-property metadata;
- added `//tools/bazel:maven_deps_check_test` so `bazel test //...` also fails
  on generated dependency metadata drift;
- fixed `update_maven_deps.py` so commented example coordinates in
  `MODULE.bazel` are ignored and the `artifacts = [` header match is more
  tolerant;
- replaced Spring copy-paste POM metadata defaults with NVIDIA/nv-boot defaults
  and made those fields overridable in `tools/bazel/maven.bzl`;
- kept each module's published POM dependency list close to that module in its
  own `BUILD.bazel`;
- emitted managed dependencies without `<version>` and module-local pins through
  `pinned_dep(...)` coordinates backed by generated Maven metadata;
- built all local Maven-shaped artifact targets successfully;
- ran `bazel --output_user_root=${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache test //... --cache_test_results=no --test_output=errors`;
- result: 12 / 12 Bazel test targets passed, including
  `//tools/bazel:maven_deps_check_test`;
- scanned generated POM `<version>` tags and confirmed Spring, Spring Boot,
  Spring Cloud, Jakarta, Jackson, Micrometer, and OpenTelemetry dependencies are
  inherited rather than pinned;
- historically validated version rewriting through the now-removed Bazel
  remote-deploy bridge, while retaining `${json-patch.version}` and the
  generated `json-patch.version` property;
- rebuilt and reinstalled parent, BOM, and all 11 jar artifacts into
  `${TMPDIR:-/tmp}/nv-boot-parent-phase3-m2`;
- reran the temp Maven consumer compile against the Bazel-installed artifacts
  with a fresh Maven cache at
  `${TMPDIR:-/tmp}/nv-boot-parent-phase3-consumer-m2-neutral-metadata`;
- after Claude follow-up fixes, installed parent, BOM, and all 11 jar artifacts
  into `${TMPDIR:-/tmp}/nv-boot-parent-phase3-m2-post-review.hUuJz7`;
- reran a fresh Maven consumer compile from
  `${TMPDIR:-/tmp}/nv-boot-parent-phase3-consumer-post-review.BtPHSl` with fresh
  cache `${TMPDIR:-/tmp}/nv-boot-parent-phase3-consumer-m2-post-review.xIWTRW`;
- confirmed the consumer compiled `ConsumerSmoke.class` and resolved NV Boot
  artifacts from the fresh file repository;
- installed the parent POM, BOM POM, and all 11 jar module artifacts into a
  Maven repository for local compatibility validation;
- updated `<workspace>/nvct/cloud-tasks/pom.xml`
  locally so the parent version and `nv-boot.version` both point to
  `0.0.2-SNAPSHOT`;
- ran `mvn clean install` from
  `<workspace>/nvct/cloud-tasks`;
- result: passed. The Maven reactor built `cloud-tasks`, `nvct-core`, and
  `nvct-service` against the Bazel-produced NV Boot artifacts from the local
  Maven repository;
- inspected `cloud-tasks/nvct-service/target/app.jar` and confirmed it contains
  `BOOT-INF/classes/maven.properties`, `BOOT-INF/classes/git.properties`,
  `META-INF/maven/com.nvidia.nvct/nvct-service-oss/pom.xml`, and
  `META-INF/maven/com.nvidia.nvct/nvct-service-oss/pom.properties`;
- ran `git diff --check`;
- result: clean.

The local Maven install and Bazel validations may need to run outside the
sandbox because Bazel server startup, Docker/Testcontainers, and local Maven
repository writes can be restricted.

## GitLab MR Context

The user asked to read this MR for another Bazel migration example:

```text
https://gitlab-master.nvidia.com/ngc/cloud/secrets/encryped-secret-store/-/merge_requests/36
```

GitLab MaaS OAuth is working now. The MR was read with:

```text
project_id = ngc/cloud/secrets/encryped-secret-store
merge_request_iid = 36
```

Key observations from that MR:

- it pins relatively few Maven versions;
- it keeps version constants in `MODULE.bazel`;
- it centralizes Java helper macros such as `nv_java_library` /
  `nv_java_test`;
- it uses a GitLab `ENABLE_BAZEL_BUILD` flag for opt-in Bazel CI;
- it was useful as a comparison point, but for `nv-boot-parent` we are keeping
  module publication metadata close to each module and avoiding a second
  hand-maintained Bazel-owned publication version map.

## CI Trial Handoff

Current project-local CI path:

- `.gitlab-ci.yml` has an opt-in `bazel-build-test` job guarded by
  `ENABLE_BAZEL_BUILD == "true"`;
- Maven remains the default build/publish path during coexistence;
- the job uses the shared NVCF DIND runner shape and stages Bazel outputs under
  `bazel-ci-artifacts/`;
- `BAZEL_OUTPUT_USER_ROOT` must stay outside `$CI_PROJECT_DIR`; the current
  value is `/tmp/nv-boot-parent-bazel-cache`.

The earlier Bazel remote-publish job has been removed. Maven remains the
remote publish path for Maven consumers; Bazel consumers should use source
targets through Bzlmod.

For the temporary `feat/bazel` branch workflow, enabling `ENABLE_BAZEL_BUILD`
sets `NEXT_VERSION` to `${CI_COMMIT_SHORT_SHA}` so inherited Maven jobs do not
fail before Maven starts.

CI commits landed on `feat/bazel`:

- `e7eb800 Move Bazel CI output root outside checkout`
  - fixed Bazel recursing into a repo-local `.bazel-cache` during `//...`;
- `41eff69 Use Bazel singlejar for Maven artifact packaging`
  - fixed CI image failures where `jar` was not on `PATH`;
  - Maven-shaped binary/source jars now use Bazel's Java toolchain
    `singlejar`;
- `52258f8 Pass Docker environment into Bazel CI tests`
  - passes `DOCKER_HOST`, `DOCKER_TLS_VERIFY`, `DOCKER_TLS_CERTDIR`, and
    `DOCKER_CERT_PATH` into Bazel's restricted test environment with
    `--test_env`;
  - this is expected to address Testcontainers not finding Docker inside the
    Cassandra Bazel test action.

Pipeline log notes:

- `/tmp/pipeline/p2.txt` failed because `BAZEL_OUTPUT_USER_ROOT` was under
  `$CI_PROJECT_DIR/.bazel-cache`; fixed by `e7eb800`;
- `/tmp/pipeline/p3.txt` failed because Maven artifact packaging invoked host
  `jar`; fixed by `41eff69`;
- `/tmp/pipeline/p4.txt` checked out `41eff69`, so it did not include
  `52258f8`;
- in `/tmp/pipeline/p4.txt`, `bazel build //...` succeeded and 13 of 14 Bazel
  test targets passed;
- the only failed target in `/tmp/pipeline/p4.txt` was
  `//nv-boot-starter-cassandra:tests`;
- the Cassandra failure was Testcontainers failing to find a Docker environment
  from inside the Bazel test action:

```text
java.lang.IllegalStateException: Previous attempts to find a Docker environment failed.
```

Interpretation:

- the DIND service itself is reachable from the CI job shell because
  `docker version` succeeded in `/tmp/pipeline/p4.txt`;
- Bazel test actions do not automatically inherit all CI environment
  variables, so Testcontainers did not see the Docker/TLS settings in that run;
- rerun the pipeline after `52258f8` is pushed and confirm whether
  `//nv-boot-starter-cassandra:tests` can now see Docker.

Follow-up pipeline log:

- `/tmp/pipeline/p5.txt` checked out `52258f82`, so it included
  `52258f8 Pass Docker environment into Bazel CI tests`;
- the logged Bazel test command included:
  `--test_env=DOCKER_HOST`, `--test_env=DOCKER_TLS_VERIFY`,
  `--test_env=DOCKER_TLS_CERTDIR`, and `--test_env=DOCKER_CERT_PATH`;
- all 14 Bazel test targets passed in CI;
- `//nv-boot-starter-cassandra:tests` passed in 61.9s, confirming the
  Docker/Testcontainers environment propagation fix worked;
- `bazel build //...` completed successfully after the tests;
- JaCoCo XML report paths were generated and written to
  `bazel-sonar.properties`;
- the archive artifact upload succeeded with `201 Created` after finding
  10,580 matching artifact files/directories under `bazel-ci-artifacts/`;
- the JUnit report upload succeeded with `201 Created` after finding 14
  `bazel-ci-artifacts/bazel-testlogs/**/test.xml` files;
- the final line of `/tmp/pipeline/p5.txt` is `Job succeeded`;
- next CI cleanup candidate: reduce the artifact payload. The current
  `cp -RL bazel-bin bazel-ci-artifacts/bazel-bin` and
  `cp -RL bazel-testlogs bazel-ci-artifacts/bazel-testlogs` approach proves the
  path works, but it may make artifact upload slower than needed. Consider
  preserving only generated Maven-shaped jars/POMs, `test.log`, `test.xml`,
  JaCoCo `test.outputs` files, and coverage HTML entry points.

Maven job follow-up:

- `/tmp/pipeline/maven-p5.txt` now contains the actual `maven-build` job log;
- the job used image `maven:3.9-eclipse-temurin-25`;
- it checked out `52258f82`, the same commit as the successful Bazel job;
- it downloaded artifacts from `bazel-build-test (363694144)`;
- it failed before running any `mvn` command because `NEXT_VERSION` was not set:

```text
NEXT_VERSION is not set, this indicate a dependency issue in the pipeline jobs.
Current job should either have 'needs' undefined or defined but with 'compute-next-release-version' added to the list.
```

- the job ended with `ERROR: Job failed: command terminated with exit code 1`;
- there is no Maven compile/test/package failure in this log. This is a CI DAG
  or variable-propagation issue around `NEXT_VERSION`;
- likely interaction to inspect tomorrow: the branch-only opt-in workflow rule
  for `ENABLE_BAZEL_BUILD == "true" && CI_COMMIT_BRANCH == "feat/bazel"`
  allowed this pipeline to run, but the Maven job did not receive the
  `compute-next-release-version` dotenv output;
- follow-up change made after reading the Maven log: the special branch-only
  workflow rule now sets `NEXT_VERSION` to `${CI_COMMIT_SHORT_SHA}`, matching
  the shared `compute-next-release-version` fallback for non-default branches;
- possible next steps to evaluate tomorrow:
  - rerun the branch-only opt-in pipeline and confirm `maven-build` gets
    `NEXT_VERSION`;
  - run the opt-in Bazel validation through a merge request pipeline as a
    closer approximation of normal CI;
  - adjust the temporary branch-only pipeline shape so Maven jobs either get
    `NEXT_VERSION` or are skipped/manual for that special branch-validation
    path;
  - keep the long-term CI-team requirement: Bazel opt-in should coexist with
    default Maven jobs, but shared CI must ensure Maven jobs still receive
    `NEXT_VERSION`.

Known CI image asks for the CI team / Jira `NVCF-11033`:

- hard requirement: install Bazelisk as the `bazel` executable and have it
  honor `.bazelversion`;
- do not make a fixed Bazel binary the only `bazel` command;
- current repo `.bazelversion` is `9.1.1`, but the image in the trial logs had
  Bazel `8.4.0`;
- the repo builds libraries with Java 25 via Bazel config:
  `--java_language_version=25`, `--java_runtime_version=remotejdk_25`,
  `--tool_java_language_version=25`, and
  `--tool_java_runtime_version=remotejdk_25`;
- system JDK 25 on `PATH` is useful for diagnostics and future publish flows,
  but Bazelisk plus `.bazelversion` should control the Bazel tool version;
- include basic utilities used by Bazel/test setup, especially `file`; the
  current image repeatedly logs
  `external/bazel_tools/tools/test/test-setup.sh: line 405: file: command not found`;
- keep Docker/Testcontainers support through the existing DIND runner setup;
- keep the Bazel output/cache root outside the checkout, for example
  `/tmp/nv-boot-parent-bazel-cache`.

Jira connector note:

- an attempt to update `NVCF-11033` through the Jira MCP failed with
  `OAuth authorization required`;
- the available Jira update tool also states that `description` cannot be
  updated through that path, so after OAuth is restored the practical path may
  be adding the Bazelisk/Java 25 details as a Jira comment unless another Jira
  editing path is available.

## Historical Remaining Work

The remainder of this section records the state before the 2026-07-17 cleanup.
Its publication targets and commands no longer exist.

The next likely task is to finish the remaining nv-boot-parent publication
gaps, then carry the plugin-parity lessons into downstream app migration.

Top-level `BAZEL.md` has been added as the developer-facing guide for this repo,
not another migration status document. It includes:

- Bazel 9.1.1 / Bzlmod / Java 25 toolchain notes and Docker requirement for
  Testcontainers;
- commands to build and test the whole repo;
- commands to build and test one module at a time;
- commands to run one test class or one test method with JUnit
  ConsoleLauncher filters via `--test_arg`;
- artifact commands for Maven-shaped jars/POMs and where outputs appear;
- local Maven install and `0.0.2-SNAPSHOT` style consumer validation commands;
- note that the old Bazel remote-publish bridge was removed and Maven remains
  the remote publish path during coexistence;
- dependency update flow for `MODULE.bazel`, `update_maven_deps.py`, and
  `REPIN=1 bazel ... @nv_third_party_deps//:pin`;
- Bazel JaCoCo report generation during `bazel test`, plus a note that Sonar
  should use those JaCoCo XML files for `nv_boot_library_test` targets;
- CI opt-in handoff in `bazel-enablement/ci-bazel-handoff.md` with the
  `ENABLE_BAZEL_BUILD: "true"` job shape, artifact paths, and Sonar coverage
  property wiring;
- Bazel-native License/NOTICE generation from module publication roots,
  `maven_install.json`, and checked-in upstream artifact metadata in
  `tools/bazel/notice_metadata.json`;
- project-local `.gitlab-ci.yml` `bazel-build-test` trial job guarded by
  `ENABLE_BAZEL_BUILD`;
- checklist for adding a new module;
- known remaining gaps: shared CI template rollout, parent/BOM POM generation
  after Maven coexistence, deferred remote snapshot deployment parity, and
  downstream Spring Boot executable app packaging.

For nv-boot-parent itself, Bazel only needs to publish the parent POM, BOM POM,
and normal library jars. It does not need Spring Boot executable jar packaging.
Known remaining nv-boot-parent gaps:

- shared CI pipeline implementation for the documented opt-in Bazel path;
- TODO, post-coexistence: generate parent/BOM POM metadata from Bazel instead
  of copying checked-in `pom.xml` files, after Maven is no longer the canonical
  parent/BOM source for downstream consumers.

Historical proof: a temporary Bazel remote-publish experiment deployed 13
Bazel-built parent/BOM/library artifacts with version `15665e3b`, and
`cloud-tasks` passed `mvn clean install` after its root POM was updated locally
so both the `nv-boot-parent` parent version and `nv-boot.version` property
pointed at `15665e3b`. That proved Maven artifact compatibility, but the
bridge has been removed because it is not the target Bazel consumption model.

Deferred TODO, only if requirements change: remote Maven snapshot deploy parity
is not in scope today. Local Maven consumption uses the normal
`0.0.x-SNAPSHOT` overwrite layout, and the project does not currently need
remote snapshot deployments.

Bazel-native status notes:

- build/test, JaCoCo coverage, module jar/POM generation, local Maven install,
  dependency metadata generation, and License/NOTICE generation are
  Bazel-native;
- dependency-root audit kept direct build/test/publication/tool roots in
  `MODULE.bazel` even when some are currently transitively reachable. The
  goal is to avoid duplicate version ownership, not to hide direct roots behind
  unrelated transitives;
- `CoverageConsoleLauncher` remains intentionally custom because it keeps JUnit
  execution and JaCoCo HTML/XML generation inside one Bazel `java_test` target
  and writes reports directly under `bazel-testlogs/.../test.outputs`;
- remote deploy from Bazel has been removed; Maven remains the remote publish
  path during coexistence;
- parent/BOM POM publication still consumes checked-in `pom.xml` files during
  coexistence and must be replaced by generated metadata before those POM files
  can be deleted;
  then `jar` on `PATH`.

For downstream Spring Boot applications, the `cloud-tasks` app jar inspection
showed that app migrations will need parity for:

- full `properties-maven-plugin` output in `BOOT-INF/classes/maven.properties`;
- app-owned `git-commit-id-maven-plugin` output in
  `BOOT-INF/classes/git.properties`;
- Maven artifact metadata in `META-INF/maven/.../pom.xml` and
  `META-INF/maven/.../pom.properties`;
- Spring Boot executable jar layout generated today by
  `spring-boot-maven-plugin`;
- CI version mutation through
  `mvn --quiet versions:set versions:commit -DnewVersion=${NEXT_VERSION}` plus
  `-Dspring.application.version=${NEXT_VERSION}`.

Legacy Kaizen context explains why `maven.properties` should remain visible:
`kaizen-core-starter` loaded it as a Spring `PropertySource` for banner,
metrics, and tracing values such as `kaizen-spring-boot-parent.version` and
`spring-boot.version`. Current `nv-boot-starter-core` does not load
`maven.properties`; it relies on `nv-boot-core-defaults.properties` and
`git.properties`. Therefore the current nv-boot-parent Bazel jars should keep
artifact-level `maven.properties`, but should not invent one property per
resolved nv-boot dependency.

After these nv-boot-parent gaps are closed, downstream app work can introduce a
Spring Boot app macro/rule for executable jars and app-level metadata.

A reusable Codex skill for this Maven-parent to Bazel migration pattern now
exists at:

```text
$HOME/.codex/skills/maven-parent-bazel-enablement
```

An identical repo-owned copy is checked in under:

```text
bazel-enablement/skills/maven-parent-bazel-enablement
```

Keep the installed local copy updated from the repo copy, or reinstall/copy it
from the repo before using the skill in another Codex environment.

`nv-boot-managed-parent` remains the next intended consumer.

Latest downstream application learning from cloud-tasks:

- `generate_notice.py` now also supports `--runtime-jar` and repeatable
  `--first-party-group`, allowing an app NOTICE to be derived from the actual
  third-party jars packaged under `BOOT-INF/lib`.
- Bzlmod source consumers must merge into the same named
  `rules_jvm_external` install as upstream public labels. Separate upstream and
  application hubs produced a 218 MB app with duplicate/version-skewed jars;
  the merged hub produced a 142 MB app matching Maven's size.
- Proto code generators must stay compatible with runtime dependencies.
  cloud-tasks kept its intentional gRPC 1.63 pin and supplied matching
  platform generator executables through `rules_jvm_external` rather than
  upgrading only part of the runtime family.

## Stop Point: CodeRabbit Cleanup And Cloud Tasks CI Completion

Checkpoint date: 2026-07-17.

The current uncommitted nv-boot-parent changes address the still-valid
CodeRabbit findings from MR `!82`:

- all committed `/Users/sasaxena` and `/private/tmp` references were replaced
  with portable placeholders, `$HOME`, or `${TMPDIR:-/tmp}`;
- POM XML used by NOTICE metadata refresh rejects DTD and entity declarations;
- the existing NOTICE test includes malicious XML regression cases and passes;
- both reusable Bazel enablement skills carry the new portability and XML
  parsing guidance, and their installed copies are synchronized.

The CodeRabbit comment about missing values in the removed publication CLI is
obsolete and requires no replacement implementation.

Cloud Tasks Phase 6 is complete. Its GitLab `bazel-build-test` job `366805692`
passed at commit `1125f64cac0bd7ad9f85a6b04b430b0e7715c438` with Bazel 9.1.1,
Java 25, and Docker-in-Docker. The uncached core, service, and NOTICE tests
passed; the full build passed; NOTICE reported 260 third-party dependencies;
and Bazel, Sonar, and JUnit artifacts uploaded successfully.

Resume order:

1. Check git status in nv-boot-parent and cloud-tasks.
2. Confirm the review-cleanup and Phase 6 documentation commits are present on
   `feat/bazel`.
3. Request a fresh nv-boot-parent MR review with `@coderabbitai review`.
4. Begin `nv-boot-managed-parent` enablement with the
   `maven-parent-bazel-enablement` skill, unless the team chooses another
   downstream application first.

## CodeRabbit Dependency And NOTICE Follow-up

Checkpoint date: 2026-07-17.

The next MR review pass found five still-valid issues and they are addressed in
the current nv-boot-parent worktree:

- `fail_on_missing_checksum` is now `True`; repinning succeeded against all
  configured repositories without a checksum exception.
- `asm-analysis` and `asm-util` are explicitly aligned to ASM 9.9. JaCoCo had
  already raised `asm`, `asm-commons`, and `asm-tree` to 9.9 in the shared hub,
  while production `jnr-ffi` otherwise left the other two modules at 5.0.3.
- explicit NOTICE roots missing from `maven_install.json` fail fast. Missing
  classifier-only transitive metadata remains skippable.
- runtime NOTICE scans reject a packaged jar whose path version differs from
  the lockfile entry.
- Bazel NOTICE actions invoke `//tools/bazel:generate_notice_tool`, a
  `rules_python` `py_binary`, instead of looking up host `python3`.

The repository documentation directory is now `bazel-enablement` in both
nv-boot-parent and cloud-tasks, with internal links updated. The canonical
repo-owned skills now live under
`bazel-enablement/skills`.

Validation completed in nv-boot-parent:

```text
bazel run //:generate_notice -- --update-metadata --write  passed (208 dependencies)
bazel build //:third_party_notice                           passed
bazel test //tools/bazel:notice_check_test --cache_test_results=no  passed
bazel test //... --cache_test_results=no                            passed (13 / 13)
bazel build //...                                          passed (32 targets)
mvn clean install                                         passed (13-module reactor)
```

The Maven run was an optional coexistence regression check because `NOTICE`
changed; it did not rewrite the Bazel-maintained NOTICE content.

Next resume order:

1. Commit and push nv-boot-parent, then capture its new commit SHA.
2. Update cloud-tasks `nv_boot_parent` `git_override` to that SHA and repin.
3. Validate cloud-tasks Bazel NOTICE, tests, app build, and Maven coexistence.
4. Commit the cloud-tasks `bazel-enablement` rename and upstream pin together.
5. Reply to or resolve the MR threads after CodeRabbit can see the pushed SHA.

## JaCoCo CLI Completion

Checkpoint date: 2026-07-18.

The custom Java coverage launcher has been replaced in Cloud Tasks and
nv-boot-parent. Tests now run JUnit with the JaCoCo agent configured with
`dumponexit=true`; a small Bazel shell test preserves the JUnit exit status and
then invokes the Bazel-declared JaCoCo CLI for XML and HTML generation. This
removes the internal JaCoCo runtime API dependency and all custom Java from the
coverage toolchain.

Validation completed:

- a focused Cloud Tasks method passed and a deliberate no-match selection
  returned JUnit's failure status while still producing coverage;
- a clean Cloud Tasks run passed 682 of 683 discovered core tests with one
  skipped, both service tests, NOTICE validation, and `bazel build //...`;
- a clean nv-boot-parent run passed all 13 test targets and its full build;
- nv-boot-parent Cassandra ran 59 tests successfully;
- every Java test target in both repositories produced nonempty exec, XML, and
  HTML coverage outputs;
- repository-wide searches found no old launcher or internal-agent references.

Docker Desktop socket arguments used for one Codex-run Cassandra validation
were caused by the Codex sandbox and the local Docker Desktop socket location.
They are not a Bazel requirement and were not added to normal project or CI
commands.

## Real JUnit XML Correction

Checkpoint date: 2026-07-19.

The JaCoCo CLI test runner now gives JUnit ConsoleLauncher a dedicated reports
directory under `test.outputs/junit` and requires its
`TEST-junit-jupiter.xml` file. GitLab publishes that file instead of Bazel's
outer `sh_test` `test.xml`, which describes only the single shell wrapper.
JUnit failures still take precedence over report or coverage failures.

A clean, uncached `bazel test //...` passed all 17 test targets. Every generated
Jupiter XML test count matched the corresponding ConsoleLauncher summary,
including 865 registry tests and 59 Cassandra tests.

## Notary Skill Forward Test

Checkpoint date: 2026-07-19.

The installed `spring-boot-app-bazel-enablement` skill was rerun from a clean
`feat/bazel` checkout of the Notary OSS reactor with only the repository,
branch, and pushed `nv-boot-parent` commit supplied. Without commit or push, it
produced the two-module source build, executable app jar, real JUnit XML,
JaCoCo, runtime NOTICE, beginner `BAZEL.md`, Docker path, opt-in CI, and handoff
documents. Clean validation passed 61 core and 2 service tests under both Bazel
and Maven, and the Docker readiness/JWKS checks passed.

The required runtime-parity comparison caught missing Prometheus and OTLP
families before completion, proving that the existing parity gate works. The
only reusable skill gap found was `.bazelrc` scope: the Bazel 9 external
Java-rule autoload compatibility option must use `common`, not `build`, so
plain `query` and `cquery` inherit it. The canonical and installed skills now
state that requirement explicitly.

## Deferred Shared Java Tooling Pilot

After Notary CI acceptance, evaluate a separate Bzlmod module provisionally
named `nv-bazel-java-tools` (or `nv-bazel-tools`). Start only with the stable,
substantially duplicated JaCoCo shell runner and a small generic test primitive.
Keep project-specific Java wrappers, Spring Boot metadata policy, NOTICE, proto,
`.bazelrc`, CI, and workspace-status behavior local until a genuinely common
API is proven.

The pilot must work both as its own root module and as an external Bzlmod
dependency. Shared Starlark should bind its internal tools with definition-site
`Label("//...")` values rather than hardcoded `@nv_boot_parent` labels. Prove
unchanged JUnit exit behavior and JaCoCo exec/XML/HTML output in one consumer
before considering broader rollout or updating the reusable skills. Do not make
this pilot a prerequisite for Notary or reopen completed repositories merely to
deduplicate files.
