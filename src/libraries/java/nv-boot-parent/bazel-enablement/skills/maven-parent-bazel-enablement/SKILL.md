---
name: maven-parent-bazel-enablement
description: Add a Bazel-native build alongside an existing Maven parent or multi-module Java project. Use when Codex needs to enable Bazel for a Maven parent/aggregator such as nv-boot-parent or nv-boot-managed-parent with Bzlmod and rules_jvm_external, expose first-party Bazel source targets to downstream consumers, and keep the independent Maven build/publish path working during coexistence. Do not generate or publish Maven-shaped project artifacts from Bazel.
---

# Maven Parent Bazel Enablement

## Core Rule

Keep Maven and Bazel coexisting until the user explicitly cuts over. Maven parent POMs, BOMs, and publication contracts remain authoritative for downstream consumers unless the user has approved a Bazel publishing replacement.

Use `MODULE.bazel` as the Bazel dependency/version source of truth. Do not create a second version-bearing dependency metadata file unless the target project has a stronger local convention.

Prefer Bazel-native generation when it removes Maven CLI reliance, duplicated dependency truth, or CI ambiguity. Prefer boring, standard JDK or shell tooling when it is obvious, maintainable, and not a Maven bridge. Do not add custom code solely for a small hermeticity gain unless the user accepts that tradeoff or CI proves it necessary.

For downstream Bazel consumers, prefer source-target consumption through Bzlmod
or another Bazel-native source dependency. Do not publish Maven-shaped jars from
the Bazel toolchain unless the user explicitly approves that bridge. Maven
consumers should keep using the Maven build/publish path during coexistence.

## Workflow

1. Inspect the Maven reactor before editing:
   - root `pom.xml`, module POMs, BOM POMs, `.mvn/`, `.gitlab-ci.yml`;
   - parent/BOM hierarchy, plugin management, dependency management, module list;
   - special plugins: `spring-boot-maven-plugin`, `git-commit-id-maven-plugin`, `maven-surefire-plugin`, `flatten-maven-plugin`, `properties-maven-plugin`, `license-maven-plugin`, `jacoco-maven-plugin`.
2. Create or update Bazel foundation:
   - `.bazelversion`, `.bazelrc`, `.bazel_downloader_config`, `MODULE.bazel`,
     `maven_install.json`, root `BUILD.bazel`;
   - use the exact setting `build --java_header_compilation=false`; do not emit
     the shorthand `--nojava_header_compilation`;
   - Bzlmod with `rules_jvm_external`; prefer checked-in lock/pin files;
   - `MODULE.bazel.lock` and `maven_install.json` should be committed.
   - Keep `fail_on_missing_checksum = True`. Any repository-specific exception
     must be proven, narrowly documented, and reviewed as a supply-chain risk.
   - Set `fetch_sources = True` so the shared hub pins Java source jars for
     parent and downstream application development.
   - Keep platform-specific native tools out of the Java dependency hub. Pin
     them with Bazel repository rules and SHA-256 checksums; with
     `rules_jvm_external` 7.0, an executable-classifier artifact that has no
     source jar can make a source-enabled repin fail during lockfile hashing.
   - Give the `rules_jvm_external` install repo a neutral, application-wide
     name such as `nv_third_party_deps`, instead of generic `maven` when this
     project will be consumed as a Bazel module by downstream repos.
   - When a downstream module consumes these source targets, make its
     `maven.install` use that same name and list the upstream module in
     `known_contributing_modules`. Verify the final app has one resolved hub.
3. Add module build/test targets:
   - keep `BUILD.bazel` close to each Maven module;
   - name library/test macros generically, e.g. `nv_boot_library` and `nv_boot_library_test`, not version-specific names;
   - add Docker/Testcontainers tags where needed;
   - keep Maven and Bazel test logs understandable.
   - when a `sh_test` wrapper runs JUnit ConsoleLauncher, pass
     `--reports-dir=${TEST_UNDECLARED_OUTPUTS_DIR}/junit` and publish
     `test.outputs/junit/TEST-junit-jupiter.xml`. Bazel's outer `test.xml`
     reports one wrapper testcase and is not the JUnit report.
   - classify Maven runtime, optional, provided, and test dependencies before
     translating them. Bazel `deps` normally propagate to downstream runtime,
     so put Maven optional/provided dependencies behind private
     `neverlink = True` compile-only targets or another proven compile-only
     boundary. Public Bazel targets must expose the same consumer runtime
     closure as Maven.
   - add a lightweight analysis test for each split public target that fails
     when known optional/provided artifact families appear in its
     `JavaInfo.transitive_runtime_jars`; this catches regressions before a
     downstream application packages them.
4. Add Bazel-native packaging for the product being built:
   - library modules expose ordinary Bazel Java targets;
   - Spring Boot apps use a proven Spring Boot packaging rule and produce the
     executable app jar/container input;
   - do not generate or deploy Maven-shaped artifacts from Bazel unless the
     user explicitly requests a temporary compatibility experiment.
5. Add explicit pin handling:
   - use `MODULE.bazel` for pinned artifact versions;
   - prefer BOM-managed versionless roots;
   - document intentional CVE overrides next to the affected coordinates;
   - inspect `maven_install.json` for partially upgraded library families when
     build tools and production code share one hub; pin the missing family
     members only when no imported BOM manages them;
   - repin `maven_install.json` after dependency-root or version changes.
   - for the first pin with `rules_jvm_external` 7.0, export a zero-byte
     project-owned `maven_install.json`, declare its `lock_file`, and let the
     pin target replace it; do not seed the project from another repository;
   - when validating a downstream owner whose upstream source module already
     contributes the same hub and owns another lock, bootstrap downstream
     roots without that contributor, restore the complete contributor list,
     repin the merged graph, and finally repin without a local override.
6. Validate in layers:
   - `bazel test //... --cache_test_results=no --test_output=errors`;
   - inspect the final app/runtime graph for duplicate external hubs and
     mismatched dependency families;
   - build the executable app/container path where applicable;
   - create a temporary downstream source consumer and compare its Maven
     runtime jars with the Bazel app's `BOOT-INF/lib`; investigate leaked
     optional/provided integrations and missing starter/servlet-engine
     closures;
   - keep `mvn clean install` working during coexistence.
   - inspect the ConsoleLauncher XML itself and prove its test count matches
     the console summary. Make the wrapper fail when the real Jupiter XML is
     absent, while preserving a nonzero JUnit status ahead of report-tool
     failures.
7. Document what is complete and what remains:
   - Maven coexistence, JaCoCo/Sonar, license/NOTICE, Spring Boot executable app packaging, downstream source validation.
   - Explain `MODULE.bazel`, `maven_install.json`, and `MODULE.bazel.lock` in
     `BAZEL.md`, including which file developers edit, which files Bazel
     generates, how to repin, and which files must be committed.
   - Explain `.bazelversion`, `.bazelrc`, and `.bazel_downloader_config` in
     simple Maven-user terms. State that `.bazelrc` stores shared Bazel
     defaults, while `.bazel_downloader_config` only rewrites repository-rule
     download URLs and does not select dependency versions or hold secrets.
8. For CI coexistence:
   - keep Maven default and add Bazel as an opt-in job;
   - prefer Bazelisk as `bazel`, honoring `.bazelversion`;
   - when private Bzlmod Git overrides use GitLab SSH URLs, do not assume the
     CI image includes an SSH client; keep the developer URL and use a scoped
     `git config url.*.insteadOf` rewrite to same-GitLab HTTPS authenticated by
     `CI_JOB_TOKEN` before invoking Bazel;
   - keep Bazel output/cache roots outside `$CI_PROJECT_DIR`;
   - when a Docker sidecar consumes Testcontainers/Compose bind mounts from
     Bazel sandboxes, put the output root on a volume shared by the build and
     Docker containers, such as GitLab Kubernetes executor `/builds`; never use
     a container-private `/tmp` path for that case;
   - pass Docker/Testcontainers variables into Bazel tests with `--test_env`;
   - ensure inherited Maven jobs still receive `NEXT_VERSION`.

## Important Patterns

- Keep direct build/test/NOTICE/tool dependencies as explicit
  `MODULE.bazel` roots when that is easier to understand. Do not remove a root
  only because an unrelated starter currently pulls it transitively.
- For published/shared Bazel targets, avoid public labels that reference a
  generic external repo such as `@maven`; use a neutral shared name such as
  `@nv_third_party_deps` so downstream Bzlmod consumers resolve the upstream
  and application dependencies through the same repository.
- The hub name describes its contents, not its resolver or owning project. It
  contains third-party dependency targets generated by `rules_jvm_external`;
  it does not contain first-party source targets or imply that Bazel publishes
  Maven-shaped project artifacts.
- A shared hub name is an application dependency boundary, not a per-repository
  vanity name. A downstream source consumer must contribute to the same hub
  name as the upstream labels it consumes. Two hubs can silently package
  duplicate and version-skewed jars into one Spring Boot app.
- Keep code generators and their runtime libraries compatible. If an upstream
  proto rule bundles a newer generator than the application runtime, select a
  matching pinned tool through Bazel instead of mixing runtime versions.
- Keep the Maven path's existing `maven.properties` behavior during
  coexistence. Generate a Bazel properties resource only when source inspection
  or a runtime test proves a Bazel target consumes it; plugin presence alone is
  not justification.
- Do not add remote Maven publish/deploy from Bazel by default. Maven remains
  the remote publishing path during coexistence; Bazel consumers should consume
  source targets through Bzlmod or an equivalent source dependency.
- Use Bazel-provided packaging tools such as the platform-specific
  `@rules_java//toolchains:singlejar`. Do not invoke a host `jar` command or
  rely on `JAVA_HOME`; CI images can provide Bazel and a Java runtime while
  omitting JDK utility commands from `PATH`.
- An upstream module's `.bazelrc` does not propagate through Bzlmod. When
  source targets use Lombok, every consuming root must set
  `build --java_header_compilation=false`; otherwise Turbine can fail while
  compiling the external source target. Prove this with a temporary downstream
  consumer instead of relying only on the upstream repository's build.
- The root consumer owns the pinned `maven_install.json` for its final merged
  third-party graph. On the first pin with `rules_jvm_external` 7.0, export a
  zero-byte owner lock and declare `lock_file`; the pin target replaces it.
  A contributor that already owns a lock may require the tested two-stage
  bootstrap described above. Commit the generated lock and separately prove a
  final repin against remote pinned source, not only a remote build/test.
- A public first-party `java_library` target's `deps` become part of consumer
  runtime unless a compile-only boundary says otherwise. Produce a complete
  module-by-module inventory of every Maven optional/provided dependency and
  map each coordinate to a private `neverlink` or other verified compile-only
  boundary. Include transitive integration families such as Cassandra, MVC,
  WebFlux, servlet APIs, and Spring Cloud; do not stop after the first consumer
  failure. A downstream app adapter is a temporary defect workaround, not the
  target architecture.
- When only Lombok is optional, do not duplicate the module-level split if the
  shared library macro already injects Lombok through a `neverlink` annotation
  target. Verify the macro once and keep module BUILD files straightforward.
- Preserve direct Spring Boot starter roots in downstream applications.
  Individual component jars needed for strict compilation do not replace a
  starter's embedded server and runtime closure.
- When `singlejar` merges metadata into a Spring Boot executable jar, supply
  `Main-Class` and `Start-Class` explicitly. `singlejar` does not preserve the
  source jar manifest automatically.
- For Java 25 projects, set both Bazel Java language and runtime versions
  explicitly. Do not rely on the Bazel server JVM or host `java` default to
  match the Maven build JDK.
- When migrating application tests, watch for test utilities that call
  `ClassPathResource#getFile()` or otherwise assume Maven's loose
  `target/test-classes` layout. Prefer stream-based classpath resource reads so
  tests work when Bazel packages resources in jars.
- Treat `NEXT_VERSION is not set` failures before Maven starts as CI
  DAG/variable propagation issues, not Maven build failures.
- Do not start downstream app validation until the upstream source targets
  build and test successfully through Bzlmod.
- For library/parent NOTICE generation, derive the closure from explicit
  shipped production roots. For executable apps, derive NOTICE from the actual
  third-party jars nested in the app runtime artifact.
- Confirm the project's distribution and compliance requirement before adding
  NOTICE machinery. If the owner explicitly excludes NOTICE for an internal-only
  project, omit the generator/check targets and record that decision in
  `BAZEL.md` instead of copying unused infrastructure.
- Treat POM XML used for NOTICE metadata as untrusted, whether it came from a
  local cache or a remote repository. Use a hardened XML parser or reject DTD
  and entity declarations before parsing.
- Make missing explicit NOTICE roots and runtime-jar/lockfile version drift
  hard failures. Classifier-only transitive edges may lack ordinary artifact
  metadata and should not be mistaken for stale roots.
- Invoke Python and other script tools used by Bazel actions through declared
  Bazel executable targets and toolchains, not a host `python3` lookup.

## When Details Matter

Read `references/nv-boot-parent-playbook.md` when implementing or reviewing a migration. It contains the validated nv-boot-parent Phase 3 structure, target patterns, helper file responsibilities, validation commands, and known limitations.
