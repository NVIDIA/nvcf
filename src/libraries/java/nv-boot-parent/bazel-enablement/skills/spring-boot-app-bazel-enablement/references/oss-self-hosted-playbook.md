# OSS/Self-Hosted Spring Boot Bazel Enablement Playbook

Use this reference for an OSS/self-hosted Spring Boot reactor. It records
reusable architecture proven by earlier migrations; it is not a project
template. Derive every module name, dependency, main class, group, port,
profile, fixture, CI job, and source dependency from the target repository.
Never add a prior project's module, profile, or CI variable unless the target
repository independently requires it.

## Proven Architecture

- Maven remains the default build/publish path during coexistence.
- Bazel produces source libraries, test fixtures, test reports, coverage,
  runtime NOTICE, the Spring Boot executable app jar, and the container input.
- Bazel publishes no Maven-shaped project artifacts.
- Shared first-party libraries are consumed through Bzlmod source targets.
- All contributors to the final app use one exact third-party hub name:
  `nv_third_party_deps`.
- The shared Java hub uses `fetch_sources = True`.
- Native code-generation tools are checksum-pinned Bazel repositories, not
  Java-hub artifacts.

Local command examples assume this portable, application-specific cache root:

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/<app>-bazel-cache"
```

## Recommended Phases

### Phase 1: Discovery And Foundation

- Inventory Maven modules, parent/BOM chains, plugin behavior, Java version,
  repositories, direct dependencies, profiles, and CI.
- Add Bazel/Bzlmod foundation and pin files.
- Enable source-jar fetching on the shared Java hub. Keep native executables
  such as platform-specific protobuf plugins outside that hub; in
  `rules_jvm_external` 7.0 an executable classifier with no source jar can
  make source-enabled repinning fail while hashing the lockfile.
- Keep Maven untouched except for changes independently required by Maven.
- Use `build --java_header_compilation=false` exactly; do not use the shorter
  `--nojava_header_compilation` spelling in generated repositories.
- If Bazel 9 compatibility with `rules_spring` requires
  `--incompatible_autoload_externally=+@rules_java`, declare it with `common`
  in `.bazelrc`. A `build`-scoped option can leave `bazel query` and `cquery`
  broken even when build/test pass. Validate `bazel query //...` explicitly.

### Phase 2: Library Modules

- Add Bazel Java library targets with direct external labels.
- Model generated proto/grpc sources with compatible generator/runtime
  versions.
- Add reusable test-fixture targets where service tests depend on library test
  utilities.

### Phase 3: Tests

- Run JUnit ConsoleLauncher with `--fail-if-no-tests` so empty discovery fails.
- Pass `--reports-dir=${TEST_UNDECLARED_OUTPUTS_DIR}/junit`, require the
  resulting `TEST-junit-jupiter.xml`, and verify its counts against the
  console summary. Preserve JUnit's nonzero status ahead of JaCoCo/reporting
  failures. Bazel's outer `sh_test` `test.xml` is not a Java test report.
- Preserve test resources as jar resources or runfiles according to how code
  opens them.
- Mark fixed-port Docker suites `exclusive` and `requires-docker`.
- Pass Docker environment variables through Bazel's restricted test
  environment.
- Generate JaCoCo HTML, XML, and exec output as part of each real test run.
  Use `dumponexit=true`, preserve the JUnit process status in a shell test, and
  invoke the Bazel-declared JaCoCo CLI afterward. Do not call the JaCoCo agent's
  internal runtime API from custom Java.

### Phase 4: Executable Application

- Compile app classes/resources as a private Java library.
- Package the executable jar with the established Spring Boot rule.
- Put loader classes at the root, app classes under `BOOT-INF/classes`, and
  runtime jars under `BOOT-INF/lib`.
- Generate `git.properties` from Bazel workspace status when runtime code reads
  git commit data.
- Generate `maven.properties` or `pom.properties` only when application runtime
  behavior actually consumes those values. Do not generate `pom.xml`.
- Preserve direct Spring Boot starter roots from Maven. Component jars needed
  for strict compilation do not replace the starter's runtime closure.

### Phase 5: Docker And Runtime

- Build the app jar, then use `bazel info bazel-bin` to resolve the real output
  directory for the Docker build context. A workspace `bazel-bin` symlink can
  point outside the repository context and Docker will reject it.
- Preserve the Dockerfile's Maven default `APP_JAR` while passing the Bazel jar
  explicitly for the Bazel path.
- Bind-mount any repository-relative local configuration expected by the app.
- Use container-reachable addresses such as `host.docker.internal` where the
  local profile otherwise points to `127.0.0.1`.

### Phase 6: NOTICE, CI, And Cutover Proof

- Derive NOTICE from actual third-party jars nested in the executable app.
- Exclude first-party groups and check in license metadata.
- Add an opt-in Bazel GitLab job using Bazelisk, `.bazelversion`, Java 25, and
  Docker-in-Docker.
- Retain app jar, library jar if useful, JUnit XML, JaCoCo outputs, NOTICE, and
  Sonar coverage properties.
- Keep Maven jobs default and do not add a Bazel Maven-artifact deploy job.
- If container publication is in scope, add a separate opt-in job that consumes
  the archived Bazel app jar and publishes under an isolated proof tag before
  any cutover.

## Upstream Source Consumption

Use a normal Bzlmod dependency plus a Git override:

```python
bazel_dep(name = "nv_boot_parent", version = "0.0.0")

git_override(
    module_name = "nv_boot_parent",
    remote = "ssh://git@<gitlab-host>:<port>/<path>/nv-boot-parent.git",
    commit = "<pushed-commit>",
)
```

Before the upstream change is pushed, validate locally without committing a
machine-specific path:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build --override_module=nv_boot_parent=/absolute/local/path //...
```

Place `--override_module` after `build`, `test`, or `run`; it is a command
option, not a startup option.

After pushing upstream:

1. Update the Git override to the pushed commit.
2. Repin without a local override.
3. Build and test without a local override.
4. Run CI so GitLab proves it can fetch the source dependency.

For private GitLab SSH URLs in CI, use a scoped `git config url.*.insteadOf`
rewrite to the job-token HTTPS URL rather than committing credentials.

## One Shared Third-Party Hub

Every app consuming nv-boot public targets must use the exact name
`nv_third_party_deps`:

```python
maven = use_extension("@rules_jvm_external//:extensions.bzl", "maven")
maven.install(
    name = "nv_third_party_deps",
    artifacts = [...],
    boms = [...],
    known_contributing_modules = [
        "nv_boot_parent",
        "protobuf",
        "rules_proto_grpc_java",
    ],
    lock_file = "//:maven_install.json",
    ...
)
use_repo(maven, "nv_third_party_deps")
```

Do not copy nv-boot's entire root list into the app. Let nv-boot contribute its
own roots; add only application-owned roots. Repin the merged extension.

An earlier migration showed that two independent hubs can inflate an app and
mix incompatible dependency-family versions. The lesson is one shared hub;
the earlier project's module names and dependency list are not part of this
playbook.

## Dependency And Proto Rules

- Put direct dependency labels in the BUILD target that uses them.
- Add root coordinates to `MODULE.bazel` only when the hub must resolve them.
- Prefer BOM-managed versionless roots.
- Pin CVE overrides, tools, and unmanaged artifacts explicitly with comments.
- Keep code generators and runtime libraries on compatible versions across all
  supported platforms.
- Repin with:

```bash
REPIN=1 bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  run @nv_third_party_deps//:pin
```

For the first pin in a repository, do not copy another application's lockfile.
With `rules_jvm_external` 7.0, use an exported zero-byte owner lock and let the
pin target replace it:

1. Export `maven_install.json` from the root BUILD file.
2. Create it as a zero-byte file and set
   `lock_file = "//:maven_install.json"`.
3. Run the pin command and commit the generated dependency files.

If an upstream source module already contributes the same named hub with its
own lock, the fully layered graph may not expose the pin repository while the
root owner has no valid lock. Bootstrap it in two stages:

1. Temporarily remove the contributing module's `bazel_dep` and override and
   remove it from `known_contributing_modules`. Keep every other required
   contributor, such as `protobuf`.
2. Pin the application-owned artifacts/BOMs into the exported zero-byte lock.
3. Restore the complete contributor declarations and repin the merged graph.
4. Remove any local `--override_module` and repin once more against the pinned
   remote commit, then build/test from a fresh output root.

Record these as separate evidence: a remote-source build/test proves source
fetching, while only the final remote-source pin proves lock regeneration.

## Maven And Bazel Runtime Parity

Compilation is not runtime parity. Before considering packaging complete:

1. Record Maven's runtime dependency/jar set for each executable module.
2. List the jars under the Bazel app's `BOOT-INF/lib`.
3. Compare the two sets by coordinates and versions.
4. Explain every difference. Similar total size is not proof.

Apply these rules while resolving differences:

- Keep each direct Maven Spring Boot starter as an application-owned
  `MODULE.bazel` root and a BUILD runtime edge. Direct Spring component labels
  may still be needed because Bazel enforces strict Java dependencies.
- Determine whether the app is servlet, reactive, or non-web. A servlet app
  must package its selected embedded server; a reactive app must package its
  selected reactive server. Do not add MVC, WebFlux, Cassandra, or another
  integration merely because classes compile against it.
- Maven `optional` and `provided` dependencies are compile-time capabilities,
  not public runtime dependencies. Model them with private `neverlink` targets
  or another verified compile-only boundary.
- Audit every consumed first-party module mechanically: list each
  optional/provided POM coordinate and identify its BUILD compile-only
  boundary. Do not stop after correcting the first dependency that breaks a
  Spring context.
- Audit first-party public Bazel targets the same way. If an nv-boot target
  leaks optional integrations, fix the upstream target when possible. If an
  application must temporarily wrap the source-built jar in `java_import`,
  document the upstream defect and owner, declare only the Maven-equivalent
  runtime roots, and mark the adapter for removal. Never silently duplicate
  this workaround across services.
- Prefer an upstream analysis test over repeating artifact-name exclusions in
  every application. The upstream test should inspect the public target's
  `JavaInfo.transitive_runtime_jars` for its known optional/provided families.
  It is an early guard only; still compare the application's packaged
  `BOOT-INF/lib` with Maven because application-owned dependencies can change
  the final closure.
- Repeat tests, NOTICE generation, app-jar inspection, Docker startup, and the
  real readiness check after every runtime-closure correction.
- Where the upstream library supports multiple integration families, validate
  representative consumers: a minimal/non-integration consumer, servlet,
  reactive, and integration-specific consumers such as Cassandra. The minimal
  consumer must not acquire optional families; an integration consumer must
  acquire one only through its own explicit dependency.

## Maven Plugin Behavior

Map behavior selectively:

- `maven-surefire-plugin`: Bazel test targets and JUnit XML/logs.
- `jacoco-maven-plugin`: per-target JaCoCo HTML/XML/exec generated during tests.
- `license-maven-plugin`: runtime-jar NOTICE generation and drift test.
- `git-commit-id-maven-plugin`: app-owned `git.properties` from workspace
  status.
- `spring-boot-maven-plugin`: executable jar packaging in the app module.
- `properties-maven-plugin`: runtime properties only when code consumes them.
- `flatten-maven-plugin`: no Bazel replacement when Bazel publishes no POM.

Do not add plugin names as Bazel tools merely because they exist in Maven.

## Test And Coverage Checks

Use `--cache_test_results=no` and inspect the JUnit summary. Very short passes
can indicate no test discovery; enforce `--fail-if-no-tests`.

Expected coverage outputs:

```text
bazel-testlogs/<module>/tests/test.outputs/index.html
bazel-testlogs/<module>/tests/test.outputs/jacoco.xml
bazel-testlogs/<module>/tests/test.outputs/jacoco.exec
```

Point Sonar at the JaCoCo XML files. Preserve test logs and JUnit XML in CI.

The proven target shape is a private JUnit `java_binary` plus a visible
`sh_test`. The shell target runs JUnit, captures its status, verifies that the
agent wrote `jacoco.exec` on JVM shutdown, invokes the JaCoCo CLI for XML/HTML,
and returns the JUnit status first. Keep the classfiles target and source files
in runfiles so reports contain source-level data.

Spring Framework 7 defaults its test context cache pause mode to
`ON_CONTEXT_SWITCH`. In an earlier migration, switching among cached Spring Boot test
contexts paused an embedded gRPC server and then restarted the same context.
On Linux the server attempted to rebind its previously assigned random port
before the old listener had released it. JUnit therefore reported successful
test methods but failed test containers with a stack containing:

```text
DefaultContextCache.restartContextIfNecessary
Failed to start bean 'shadedNettyGrpcServerLifecycle'
bind(..) failed: Address already in use
```

Scope this JVM flag to a test target only after confirming that signature:

```starlark
jvm_flags = ["-Dspring.test.context.cache.pause=never"]
```

This restores the earlier keep-running behavior for cached contexts. It is not
a general requirement for Bazel, JaCoCo, or tests that share one JVM. Also do
not confuse missing host utilities reported after the test, such as Bazel's
`file: command not found` while classifying undeclared coverage outputs, with
the JUnit failure that determined the test status.

For class and method runs with ConsoleLauncher scanning, filter class and
method names through `--test_arg`; do not combine explicit selectors with
classpath scanning.

## Spring Boot And Git Metadata

Build app classes as a private library and pass that target to the Spring Boot
packaging rule. The product target should be a single executable app jar, not a
library target pretending to be the service.

Generate the git keys actually used by startup code, such as:

```text
git.closest.tag.name
git.commit.id.abbrev
git.commit.id.full
git.tags
```

Use Bazel workspace status so normal builds are reproducible and commit data is
available without invoking Maven. Inspect the final jar contents and launch it
with `java -jar` before Docker validation.

When CI supplies a release identity such as `NEXT_VERSION`, pass it through
workspace status or a declared build setting and use the local snapshot only as
a fallback. Applying `NEXT_VERSION` to the container tag alone leaves embedded
`maven.properties`/`pom.properties` inconsistent with the published image.

If metadata must be added after `rules_spring` creates the executable jar,
generate each metadata file as a declared Bazel output and merge the app jar
and resources with the platform-specific
`@rules_java//toolchains:singlejar` executable in the execution configuration.
Do not copy the jar and invoke `jar uf`, search `PATH`, or fall back to
`JAVA_HOME`. A CI image can have Bazel 9.1.1 and Java 25 while still reporting
`jar: command not found`.

`singlejar` does not automatically preserve the source jar manifest. Pass at
least these lines when producing the final executable jar:

```text
Main-Class: org.springframework.boot.loader.launch.JarLauncher
Start-Class: <application-main-class>
```

After packaging, inspect the manifest and metadata entries, then run the
launcher smoke test. A successful merge without those manifest lines creates
a structurally populated jar that is not a valid `java -jar` application.

## Runtime NOTICE

For an executable app, the app jar is the dependency-distribution source of
truth. Inspect its `BOOT-INF/lib` entries, map third-party jars through
`maven_install.json`, exclude application-owned groups, and combine that graph
with checked-in license/name/URL metadata.

The normal check must be deterministic and offline. Keep remote POM access only
in an explicit metadata refresh command. Commit NOTICE and metadata changes
together.

Treat POM XML from both local caches and remote repositories as untrusted. Use
a hardened parser or reject DTD and entity declarations before parsing metadata.
Reject runtime jars whose packaged version differs from the matching
`maven_install.json` entry. Keep checksum enforcement enabled, and invoke the
NOTICE generator through a Bazel-declared Python executable rather than host
`python3` from a build action.

## Docker Pattern

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //<service>:app

BAZEL_BIN_DIR="$(
  bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" info bazel-bin
)"

docker build \
  -f <service>/Dockerfile \
  --build-arg APP_JAR=app.jar \
  -t <service>:bazel \
  "${BAZEL_BIN_DIR}/<service>"
```

Run the image with the intended profile, required ports, service addresses,
and bind mounts. A launcher-only smoke test is useful but does not replace a
profile-aware container run.

## CI Pattern

The opt-in job should:

- run only when `ENABLE_BAZEL_BUILD == "true"`;
- use an image with Bazelisk and Java 25;
- verify `bazel --version` matches `.bazelversion`;
- provide Docker-in-Docker to Testcontainers;
- keep the Bazel output root outside `CI_PROJECT_DIR` but on a filesystem
  shared by the build and Docker service containers when Compose uses bind
  mounts. With GitLab's Kubernetes executor, use `/builds`, not the build
  container's private `/tmp`;
- run uncached tests, then build all targets;
- preserve exit status while still collecting artifacts;
- copy symlinked Bazel outputs with `cp -L`/`cp -RL`;
- publish `test.outputs/junit/TEST-junit-jupiter.xml` as the JUnit report and
  JaCoCo XML paths for Sonar; never publish the one-test outer `test.xml`;
- never deploy Maven-shaped artifacts.

Inherited templates may enable `set -u` and read GitLab variables that exist
only for certain pipeline sources. A manually started branch pipeline does not
define `CI_MERGE_REQUEST_IID` or `CI_COMMIT_TAG`, for example. Inspect the
complete inherited script and safely initialize every optional variable it
reads while preserving values supplied to MR and tag pipelines:

```bash
export CI_MERGE_REQUEST_IID="${CI_MERGE_REQUEST_IID:-}"
export CI_COMMIT_TAG="${CI_COMMIT_TAG:-}"
```

The release-version job can also set `SKIP_SEMANTIC_RELEASE=true` in a dotenv
report for a manually started branch pipeline. The inherited NGC job treats
that as a successful no-op. Dotenv variables outrank job variables, so setting
`SKIP_SEMANTIC_RELEASE: "false"` under the job's `variables` is ineffective.
For a separately named, opt-in, manually gated validation push, export the
override before replaying the inherited NGC setup:

```yaml
before_script:
  - export SKIP_SEMANTIC_RELEASE="false"
  - !reference [.docker-publish, before_script]
```

Do not apply this override to the default Maven/release publication job. Keep
the GitLab validation image name isolated and require the Bazel Docker build
and scan jobs first. A scan template may declare failures as allowed, in which
case `needs` does not prove that scanning succeeded. Require its nonempty policy
artifacts in the NGC job before copying the image.

NGC credentials may be scoped to an established repository and reject creation
of a separate validation repository even after login succeeds. Verify that
scope. When necessary, keep the intermediate GitLab repository separate but
copy to the established NGC repository under an unmistakable
`bazel-validation-*` tag. Repository or tag isolation is acceptable as long as
the proof cannot overwrite the Maven release image.

The inherited branch path may copy successfully without printing or checking a
digest because its post-push manifest check is MR-only. Add a post-copy proof
to the dedicated validation job:

```bash
source_digest="$(regctl image digest "${CONTAINER_IMAGE}")"
ngc_digest="$(regctl image digest "${TARGET_NGC_IMAGE}")"
test "${ngc_digest}" = "${source_digest}"
```

Retain both digests and the NGC image reference as artifacts. Digest equality
proves the NGC manifest is the exact GitLab image whose embedded app jar was
already compared with the Bazel build artifact.

If the Docker daemon reports that a file bind mount is a directory, compare
the host path from the error with the volumes shared between the job and Docker
service containers. A path under a container-private Bazel sandbox is visible
to the test JVM but absent from the sidecar daemon.

## Container Publication Proof

A successful registry push is not automatically a Bazel publication proof.
An inherited Docker job may still declare `needs: [maven-build]` and copy the
Maven `target/app.jar`, even when the pipeline also ran Bazel successfully.

Use this sequence during coexistence:

1. Keep the existing Maven Docker build/publish jobs unchanged and default.
2. Add a separately named job guarded by the Bazel opt-in flag. Make it need
   the Bazel build/test job with artifacts enabled.
3. Copy the archived `bazel-bin/<service>/app.jar` into an explicit Docker
   context path. Do not run Maven or consume a jar produced by Maven. If the
   inherited Docker template runs `git stash`, stage the Bazel jar under an
   ignored directory so it cannot be stashed away.
4. Reuse the organization's existing registry login and
   `NEXT_VERSION`/MR-tag behavior, but add a Bazel validation suffix to the
   image name or tag so the proof cannot overwrite the Maven image.
5. Push to the intermediate GitLab registry first. Exercise any NGC copy/push
   job manually until the Bazel path is accepted.
6. Record the archived jar checksum before the Docker job. Pull the pushed
   image, extract the application jar, compare checksums, and inspect
   `git.properties` and version properties. Run the organization's scanner on
   the isolated Bazel validation image before the manual NGC copy.
7. Verify the NGC destination manifest and digest after the manual copy.
8. Run the pulled image with the documented profile and configuration mounts
   when runtime validation is required.

One validated OSS/self-hosted implementation used a separate Docker-publication
flag, isolated validation image, scan, and manual NGC copy. Derive the actual
flag and image names from the target repository; never copy another product's
names. Keep the normal Maven image name and jobs untouched during coexistence.

Record the exact source commit, Bazel job, Docker job, destination image and
digest, jar checksums, embedded version, and runtime result. Registry
authentication alone proves only the transport path; artifact identity proves
the Bazel path.

## Completion Evidence

Record in status/handoff docs:

- exact upstream commit used by the app;
- repin command and lockfiles changed;
- build and uncached test results with test counts and durations;
- app jar path, size, and runtime hub count;
- git/runtime metadata entries inspected;
- coverage and NOTICE paths;
- Docker profile run result;
- published image reference/digest and proof that its app jar matches the Bazel
  CI artifact;
- GitLab job result while Maven remains green;
- remaining phases and explicit deferrals.

Do not declare completion until the repository contains `BAZEL.md` plus
`bazel-enablement/roadmap.md`, `bazel-enablement/status.md`, and
`bazel-enablement/handoff.md`. `BAZEL.md` must contain commands for clean,
build/test all, build/test one module, one test class, one test method, logs,
coverage/Sonar, conditional NOTICE, app jar, Docker build, local-profile run,
local source override, and every opt-in CI variable.
