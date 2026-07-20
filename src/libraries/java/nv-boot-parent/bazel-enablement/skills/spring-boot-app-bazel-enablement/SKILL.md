---
name: spring-boot-app-bazel-enablement
description: Add a Bazel-native build alongside an existing single- or multi-module Maven Spring Boot application. Trigger for minimal requests such as "Enable Bazel for the OSS/self-hosted Spring Boot project" or "Enable Bazel for the managed Spring Boot project" when the user supplies only a repository path and branch. Use the matching profile and playbook to discover and implement Bzlmod source dependencies, the shared nv_third_party_deps hub, Maven-scope/runtime parity, tests, coverage, executable packaging, runtime metadata, conditional OSS NOTICE, Docker, optional OpenAPI publication, and opt-in GitLab CI. Keep Maven independent during coexistence and never publish Maven-shaped project artifacts from Bazel.
---

# Spring Boot App Bazel Enablement

## Invocation Contract

The user only needs to provide intent, repository path, and branch:

```text
Enable Bazel for the OSS/self-hosted Spring Boot project.

Repository: <absolute-path>
Branch: <branch>
```

or:

```text
Enable Bazel for the managed Spring Boot project.

Repository: <absolute-path>
Branch: <branch>
```

Treat either request as sufficient to select this skill, read the matching
playbook, inspect the repository and inherited CI, implement the full workflow,
validate it, and update its documentation. Do not ask the user to restate the
implementation checklist or discovery steps. Ask only when required external
state cannot be discovered safely, such as an unavailable upstream repository,
an unpushed source commit, or missing credentials for a requested remote proof.
Check worktree status before editing, preserve existing changes, and do not
commit or push unless the user explicitly asks.

The managed profile always omits NOTICE. The OSS/self-hosted profile determines
NOTICE behavior from the repository's distribution and compliance contract.

## Core Rules

- Keep Maven and Bazel as independent paths during coexistence.
- Do not generate, install, or publish Maven-shaped project artifacts from
  Bazel.
- Consume shared first-party libraries as Bazel source targets through Bzlmod,
  not as Maven artifacts from URM.
- If the app consumes nv-boot targets, use the exact shared third-party hub name
  `nv_third_party_deps` in every contributing module and application.
- Use `build --java_header_compilation=false` in `.bazelrc`. Keep this exact
  spelling consistent across repositories; it avoids Lombok/annotation-
  processing header-jar surprises and is clearer than the shorthand form.
- When `rules_spring` needs Bazel 9's external Java-rule autoload compatibility,
  set `common --incompatible_autoload_externally=+@rules_java`, not `build`.
  `common` also covers `query` and `cquery`; prove all three command surfaces
  with `bazel query //...`, `bazel build //...`, and `bazel test //...`.
- Set `fetch_sources = True` on the shared Java dependency hub so source jars
  are pinned and available consistently to downstream applications.
- Keep platform-specific native tools such as protobuf compiler plugins out of
  the Java dependency hub. Fetch them with checksum-pinned Bazel repository
  rules; `rules_jvm_external` 7.0 cannot repin `fetch_sources = True` when
  an executable-classifier artifact advertises a missing source jar.
- Build the product app jar/container in the application repository; do not ask
  a parent/library repository to package the app.
- A container image is a Bazel-native application product and may be published
  by the Bazel CI path. This is distinct from publishing Maven-shaped jars or
  POMs, which remains forbidden.
- Do not call a container publication Bazel-proven merely because a Bazel job
  ran in the same pipeline. Trace the Docker job's downloaded artifact and
  prove that the jar inside the image is the Bazel-built jar.
- Use Bazel-declared packaging tools. Never invoke a host `jar` command or rely
  on `JAVA_HOME` to mutate the executable app jar.
- Use the JaCoCo agent with `dumponexit=true` and the Bazel-declared JaCoCo CLI.
  Preserve JUnit's exit status in the wrapper and do not add custom Java that
  calls JaCoCo internal runtime APIs.
- When Spring Framework 7 tests switch among cached application contexts that
  own embedded servers, watch for context pause/restart failures. If a server
  cannot safely rebind on Linux, scope
  `-Dspring.test.context.cache.pause=never` to the affected test target. Do not
  attribute this to Bazel merely running tests in one JVM; prove the failure
  through `DefaultContextCache.restartContextIfNecessary` and the server bind
  exception first.
- Inspect the target reactor before using patterns from any earlier migration.
  Reuse the architecture, not project names, dependencies, ports, or profiles.
- Never add an earlier project's module, dependency, fixture, profile, port,
  CI job, or source override unless the target repository itself requires it. The
  OSS/self-hosted reference is a generic procedure, not a file-copy template.
- Treat the Maven dependency scopes as a behavioral contract. A Bazel target
  may compile and still be wrong when Maven-optional/provided libraries leak
  into its runtime or a Maven starter's runtime closure is missing.
- Classify the application before implementation. The same product can have an
  open-source/self-hosted reactor and a separate internal managed app with
  different modules, compliance requirements, dependencies, and CI behavior.
- Treat NOTICE and OpenAPI publication as independent project capabilities.
  Managed apps omit NOTICE without requiring separate user confirmation.
  Generate NOTICE for OSS/self-hosted projects only when required by their
  distribution contract. Preserve OpenAPI generation and its downstream
  commit/MR workflow only when the existing managed app has it; do not add
  either capability merely because another app uses it.

## Workflow

1. Check both application and upstream-library worktrees before editing.
2. Inspect root/module POMs, dependency management, plugins, profiles,
   resources, tests, proto generation, Dockerfiles, and `.gitlab-ci.yml`.
   Include inherited CI jobs and any generated-spec publication workflow.
3. Identify the application profile, library modules, executable Spring Boot
   modules, main classes, test fixtures, Docker/Testcontainers inputs, runtime
   metadata consumers, NOTICE requirement, and optional OpenAPI contract.
4. Add the Bazel foundation: `.bazelversion`, `.bazelrc`, `MODULE.bazel`,
   `MODULE.bazel.lock`, `maven_install.json`, and root/module BUILD files.
   Keep `fail_on_missing_checksum = True`; document and review any proven
   repository-specific exception. Use the exact Java header setting
   `build --java_header_compilation=false`. If `rules_spring` requires the
   external Java-rule autoload option, put it under `common` and verify plain
   `query`/`cquery`, not only build and test.
5. Add upstream first-party source dependencies with `bazel_dep` plus a pinned
   `git_override`. Discover and record why the selected commit is the current
   pushed, validated baseline; do not silently copy a historical commit from
   another application. Use `--override_module` only while validating
   unpublished upstream changes locally.
6. Build all third-party dependencies through one `nv_third_party_deps` hub.
   Add upstream modules to `known_contributing_modules`, repin, and verify the
   final app has one resolved hub. Set `fetch_sources = True`. If code
   generation needs a platform-specific native executable, pin it separately
   with a Bazel repository rule and SHA-256 rather than adding it to the Java
   hub. For a repository's first pin with `rules_jvm_external` 7.0, export a
   zero-byte app-owned `maven_install.json`, declare
   `lock_file = "//:maven_install.json"`, and let the pin target replace it.
   Do not copy another project's lockfile as a seed. When an upstream module
   already contributes the same hub with its own lock, temporarily remove that
   `bazel_dep`/override and its `known_contributing_modules` entry, pin only
   the app-owned roots, restore every contributor, and repin the merged graph.
   Finally repin and build/test without `--override_module`; a remote-source
   build alone does not prove remote-source repinning.
7. Add Bazel Java library, test-fixture, and test targets close to each module.
   Preserve resources and integration fixtures as runfiles. For a root-package
   managed app, account for `native.package_name()` being empty when computing
   source paths or generated target names. Include both the app repository's
   own relative-path config (for example `local_env/vault/secrets.json`) and
   source-built upstream fixture bundles when tests require both.
   Classify every Maven dependency as runtime, optional, provided, or test.
   Bazel `java_library.deps` normally contribute to downstream runtime, unlike
   Maven optional/provided dependencies. Put optional/provided jars behind a
   private `neverlink = True` compile-only target, or another proven
   compile-only boundary. Public first-party source targets must expose only
   their Maven-equivalent consumer runtime closure. Mechanically scan every
   consumed upstream module POM for optional/provided dependencies and compare
   all of them with its public BUILD target; do not stop after the first Spring
   context or packaged-jar failure. If an upstream target leaks optional
   dependencies, prefer fixing that target. A downstream
   `java_import` parity adapter is temporary only: document the upstream defect
   and owner, keep the source-built jar, declare the exact runtime roots, and
   do not present the adapter as the reusable architecture.
8. Add a Spring Boot executable jar target using the established packaging
   rule. Preserve every direct Spring Boot starter from the Maven POM as a
   Bazel dependency root and runtime edge. Direct component jars may be needed
   for strict Java compilation, but they do not replace a starter such as
   `spring-boot-starter-webmvc` and its embedded servlet-engine closure.
   Generate only runtime metadata the application actually consumes,
   including `git.properties` when replacing `git-commit-id-maven-plugin`.
   Require a source reference or runtime test for each generated metadata file;
   do not generate `maven.properties` or `pom.properties` merely because Maven
   previously packaged them.
   Generate metadata as declared Bazel outputs and merge it with the app using
   the platform-specific `@rules_java//toolchains:singlejar` executable. Supply
   the Spring Boot `Main-Class` and application `Start-Class` manifest lines
   explicitly. If Maven previously used `versions:set` plus Spring Boot
   repackaging, put `NEXT_VERSION` in the manifest `Implementation-Version` as
   well as `git.build.version`: Spring Boot may expose manifest application
   metadata before nv-boot consults `git.properties`.
9. Add JaCoCo HTML/XML/exec output to real uncached test execution and wire
   Sonar to the generated XML. Run JUnit as a Bazel Java executable with the
   agent using `dumponexit=true`, then invoke a Bazel-declared JaCoCo CLI from a
   shell test. Preserve the JUnit failure status even when report generation
   also runs.
10. For an OSS/self-hosted project that requires NOTICE, generate it from the
    third-party jars actually nested in the executable app. Keep license
    metadata checked in and add a deterministic drift test. Treat local and
    remote POM XML as untrusted;
    use a hardened parser or reject DTD and entity declarations before parsing.
    Fail if a packaged jar version differs from `maven_install.json`. For the
    managed profile, omit the entire NOTICE mechanism automatically and record
    the profile decision in `BAZEL.md`.
11. Build and run the Docker image from the real Bazel output directory. Mount
    required local profile/config directories and validate the intended app
    profile. Determine whether the Maven app is servlet, reactive, or non-web.
    For a servlet app, verify the packaged runtime contains the selected
    embedded server (for example Tomcat, Jetty, or Undertow); for a reactive
    app, verify its actual reactive server closure. Do not add both MVC and
    WebFlux unless Maven does. Check the real readiness/health endpoint, not
    only process liveness.
12. Add an opt-in Bazel CI job guarded by `ENABLE_BAZEL_BUILD: "true"`. Keep
    Maven default, retain test/coverage/app artifacts plus NOTICE when
    applicable, and do not add a Bazel Maven-artifact deploy job.
    For Docker sidecars, place Bazel's output root outside the project checkout
    but on a volume shared with the Docker service. Do not use the build
    container's private `/tmp` when Compose bind-mounts sandbox files.
    Rewrite internal SSH Git overrides to same-GitLab job-token HTTPS URLs in
    CI, while retaining developer-friendly SSH URLs in `MODULE.bazel`.
13. When proving container publication, add a separate opt-in Docker job that
    downloads the archived Bazel app jar. Keep the existing Maven Docker path
    as the default, use an isolated validation image name/tag, reuse the
    established registry authentication and `NEXT_VERSION`/MR tagging rules,
    and verify the jar inside the pushed image matches the Bazel CI artifact.
    Propagate `NEXT_VERSION` into runtime build metadata as well as the image
    tag when the application exposes that version.
    Account for inherited Docker templates that run `git stash`: copy the
    archived Bazel jar into an ignored staging directory before that step, or
    otherwise prove the artifact remains present. Record the jar checksum in
    the Bazel job and verify the checksum after extracting the jar from the
    pushed image. Scan the isolated GitLab image and isolate the NGC proof by
    repository name or tag so it cannot overwrite the Maven image.
    Also inspect inherited scripts for optional GitLab predefined variables
    used under `set -u`. Manual branch pipelines omit source-specific
    variables such as `CI_MERGE_REQUEST_IID` and `CI_COMMIT_TAG`; initialize
    every one read by the inherited script with `${VARIABLE:-}` before
    invoking it, while preserving real values in MR and tag pipelines.
    An inherited NGC push may also exit successfully when an upstream dotenv
    report sets `SKIP_SEMANTIC_RELEASE=true` for a branch build. A job-level
    YAML variable cannot override that dotenv value. In the separately named,
    manual validation push, export `SKIP_SEMANTIC_RELEASE=false` before
    replaying the inherited push `before_script`; retain the opt-in rule,
    isolated image name, scan dependency, and manual gate.
    Verify the NGC credential's repository scope before choosing a separate
    validation repository. If it may tag an established repository but cannot
    create a new one, retain the separate GitLab validation repository and copy
    to the established NGC repository with an unmistakable
    `bazel-validation-*` tag. Also inspect whether the inherited scan job is
    allowed to fail: a `needs` edge alone does not gate publication in that
    case. Require nonempty scan policy artifacts in the NGC job before copying.
    After the inherited copy, resolve both the GitLab source and NGC
    destination manifest digests with `regctl image digest`, require exact
    equality, and retain the destination reference and both digest values as
    job artifacts.
14. When the existing managed-app pipeline generates OpenAPI specifications,
    preserve the complete behavior: build or download the selected app jar,
    start all required fixtures, run every configured application variant,
    produce the same filenames, and leave them where the existing downstream
    commit/push/MR jobs expect them. During coexistence, keep the Maven default
    and add an opt-in Bazel proof using the archived Bazel app jar. Before Maven
    is disabled, prove the Bazel path creates the expected specs and completes
    the downstream repository workflow. Do not add this path to managed apps
    whose current pipeline does not generate OpenAPI. Prefer keeping the
    established generation job name and filenames so downstream `needs` remain
    valid. Give that job direct access to the version-computation dotenv
    artifact when it verifies `NEXT_VERSION`; do not assume dotenv values
    propagate through another job. Archive source-built fixture jars by querying
    their Bazel target outputs rather than downloading Maven classifier jars.
15. Validate locally, pin pushed upstream commits, rerun without local
    overrides, then validate in GitLab CI.
16. Update `BAZEL.md`, `bazel-enablement/roadmap.md`,
    `bazel-enablement/status.md`, and `bazel-enablement/handoff.md`. Do not
    declare the work complete while any are missing. Update this skill when a
    new reusable lesson is proven.
    `BAZEL.md` must be useful to a Maven-experienced Bazel beginner and include:
    configuration-file explanations; dependency and lockfile ownership;
    portable output-root setup; clean; build/test all; build/test one module;
    one test class; one test method; logs; JaCoCo/Sonar outputs; conditional
    NOTICE; app jar; Docker build and local-profile run; local source override;
    and the exact opt-in CI variables. Explain `.bazelrc` and
    `.bazel_downloader_config` in simple terms as described in the selected
    playbook.
17. Before finalizing, compare Maven's runtime dependency/jar set with the jars
    under the Bazel app's `BOOT-INF/lib`. Investigate every difference rather
    than accepting similar jar size or successful startup as parity. Missing
    starter closures and leaked optional/provided integrations are failures.

## Shared Hub Contract

An app that consumes nv-boot source targets must use:

```python
maven.install(
    name = "nv_third_party_deps",
    fetch_sources = True,
    known_contributing_modules = [
        "nv_boot_parent",
        # Other modules contributing third-party roots.
    ],
    ...
)
use_repo(maven, "nv_third_party_deps")
```

Public targets use labels such as `@nv_third_party_deps//:...`; changing the
hub spelling creates a different repository. The hub contains third-party jars
only. First-party application and nv-boot libraries remain source targets.

## Validation Minimum

Run real tests, not cache-only checks:

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/<app>-bazel-cache"

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //... \
  --cache_test_results=no \
  --test_output=errors \
  --test_env=DOCKER_HOST \
  --test_env=DOCKER_TLS_VERIFY \
  --test_env=DOCKER_TLS_CERTDIR \
  --test_env=DOCKER_CERT_PATH

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" build //...
```

Also verify:

- ConsoleLauncher writes real JUnit XML to
  `test.outputs/junit/TEST-junit-jupiter.xml`, and its test count matches the
  console summary. Do not publish Bazel's outer `sh_test` `test.xml`, which
  contains one wrapper testcase;
- JaCoCo HTML/XML/exec files exist for each test target;
- the executable app launches through Spring Boot's launcher;
- its manifest contains the expected `Main-Class` and `Start-Class` after any
  metadata merge;
- `git.properties` and any required runtime properties are present;
- the app runtime contains one third-party hub and no duplicate dependency
  families;
- Maven runtime dependencies and Bazel `BOOT-INF/lib` have been compared, with
  every difference explained and intentional;
- direct Spring Boot starters retain their runtime closures, including the
  expected embedded server for servlet/reactive applications;
- Maven optional/provided dependencies do not leak through public first-party
  Bazel targets unless the application independently owns them;
- every consumed public target that splits optional/provided dependencies has
  an upstream runtime-closure analysis test, while the application still
  proves final `BOOT-INF/lib` parity;
- dependency families shared by build tooling and production code resolve to
  compatible versions rather than a partial family upgrade;
- NOTICE matches the app runtime when NOTICE is required;
- existing managed-app OpenAPI generation produces every expected spec and
  completes its downstream commit/MR workflow when that capability is enabled;
- the Docker image starts with its intended profile/configuration;
- any published Bazel image contains the exact archived Bazel app jar, uses an
  isolated proof tag, and reports the expected CI build version;
- Maven still builds independently during coexistence.

Treat Docker socket failures seen only inside an agent sandbox as local
execution-environment troubleshooting. Do not add socket overrides to canonical
project or CI commands unless the same requirement is reproduced in the target
environment.

## Detailed Reference

Read `references/application-profiles.md` first to classify the repository and
its optional capabilities. Read `references/bazel-beginner-docs.md` for every
profile before writing repository documentation. For a separate internal root application, read
`references/managed-app-playbook.md`. For an open-source/self-hosted reactor,
read `references/oss-self-hosted-playbook.md`.
