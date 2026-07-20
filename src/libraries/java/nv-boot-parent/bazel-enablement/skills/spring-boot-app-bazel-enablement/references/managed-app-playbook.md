# Internal Managed Spring Boot App Playbook

Use this reference for a separate repository whose root is one executable
Spring Boot app and whose shared core/parent code lives in other repositories.

## Target Shape

Create two root targets:

- a private `app_classes` Java library for compilation, tests, and packaging;
- an `app` Spring Boot executable jar target.

Do not expose `app_classes` as a published product library and do not create a
Maven-shaped application artifact. The deployable products are `app.jar` and
the container image.

Do not implement NOTICE generation for this profile. Managed applications are
internal, and the profile itself supplies that decision; the initial user
prompt does not need to repeat it.

Root BUILD packages need special care:

- `native.package_name()` is empty;
- source roots must be `src/main/java`, not `/src/main/java`;
- verify rules_spring's generated jar label for an empty package rather than
  assuming a subpackage naming formula.
- Put any Bazel 9 external Java-rule autoload compatibility option required by
  `rules_spring` under `common` in `.bazelrc`, then verify plain `bazel query
  //...`; `build` scope does not apply to query/cquery.

## Dependency Ownership

The app owns the final `maven_install.json` and merged
`@nv_third_party_deps` graph. Upstream source modules contribute roots through
the same hub name. Put only app-owned roots or version overrides in the app's
`MODULE.bazel`.

Typical first-party labels are source targets from:

- the shared core repository;
- nv-boot-parent;
- the managed nv-boot parent.

Keep internal SSH Git URLs for developers. In GitLab CI, configure an
`insteadOf` rewrite to same-GitLab HTTPS with `CI_JOB_TOKEN` before Bazel
fetches source overrides.

## Tests and Fixtures

Run the existing integration test, not a synthetic smoke test. A managed app
may need both:

- its checked-in `local_env` data at a root-relative runtime path;
- an upstream source-built test-fixtures target containing Cassandra/Compose
  assets and Java fixture classes.

Declare both. External runfiles keep their external repository path and do not
automatically replace a managed workspace root path.

Use the JaCoCo agent with `dumponexit=true`, then the public JaCoCo CLI. Pass
`--reports-dir=${TEST_UNDECLARED_OUTPUTS_DIR}/junit` to ConsoleLauncher and
require `test.outputs/junit/TEST-junit-jupiter.xml`. Confirm its counts and
nonempty JaCoCo exec/XML/HTML outputs. Do not publish Bazel's outer `test.xml`,
which contains one shell-wrapper testcase. Validate at least one documented
method filter and preserve JUnit's nonzero exit status ahead of report-tool
failures.

## App Metadata

Generate only metadata the runtime consumes. For nv-boot managed apps this is
normally `git.properties`, including full/abbreviated commit and build version.

When Maven CI sets the POM with `NEXT_VERSION`, Spring Boot repackaging places
that value in manifest `Implementation-Version`. Reproduce this dynamically in
the Bazel jar. Setting only `git.build.version` is insufficient when Spring
Boot's application-info property source has already supplied a manifest
version.

Do not add `maven.properties`, generated POMs, or `pom.properties` unless code
inspection proves the managed app consumes them.

## Docker

Keep the Maven Docker default while permitting a Bazel input:

```dockerfile
ARG APP_JAR=target/app.jar
ADD ${APP_JAR} /usr/share/app.jar
```

Copy Bazel's output symlink to a regular ignored file inside the Docker context
before building. A narrow `.dockerignore` should admit the Dockerfile, Maven's
default jar, and the staged Bazel jar only.

Run the image with the real local profile, mount root-relative config where the
container workdir expects it, start the fixture database, check health/OpenAPI,
and compare the jar extracted from the image with the staged Bazel artifact.

## CI and OpenAPI

Use independent opt-ins for build/test, OpenAPI, and Docker publication when
those workflows need separate proof. Any workflow-specific flag should also
cause the Bazel build job to run.

Archive:

- `app.jar` and its SHA-256;
- JUnit and JaCoCo outputs;
- any source-built fixture jar needed by a later job.

Resolve a fixture output from its public Bazel target with `cquery
--output=files`; do not download a Maven tests-classifier artifact in the new
Bazel path.

For an existing OpenAPI workflow:

1. Keep the established generation job name and output filenames.
2. Make Maven and Bazel build jobs optional artifact inputs.
3. Under the Bazel flag, stage the archived jar at the historical runtime path
   or parameterize that path without changing Maven's default.
4. Extract the archived source-built fixture jar for database assets.
5. Directly need the version-computation job when checking `NEXT_VERSION`.
6. Run every existing app variant and leave artifacts where the downstream
   commit/push/MR job already expects them.
7. Prove generation on a feature branch without mutating the documentation
   repo, then prove the downstream MR path under an approved default-branch
   run before disabling Maven.

For Docker publication, use an isolated validation image name, verify the jar
inside the image against the archived SHA, scan it, and keep the registry push
manual until the proof is accepted.
