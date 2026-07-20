# nv-boot-parent Bazel Migration Playbook

Use this reference when applying the validated nv-boot-parent coexistence
pattern to another Maven parent or multi-module Java project.

## Baseline Decisions

- Toolchain: Bazel 9.1.1, Bzlmod, `rules_jvm_external` 7.0, and Java 25.
- Maven remains responsible for Maven parent/BOM and jar publication while
  Maven consumers remain.
- Bazel consumers use first-party Bazel source targets through Bzlmod.
- Do not generate, locally install, or remotely publish Maven-shaped project
  artifacts from Bazel.
- `MODULE.bazel` is the manually edited source of truth for third-party roots,
  imported BOMs, and explicit version pins.
- Commit both `MODULE.bazel.lock` and `maven_install.json`.
- Prefer straightforward Bazel, JDK, and shell mechanisms over custom code when
  they provide the required behavior.
- Use `build --java_header_compilation=false` exactly in `.bazelrc`.

Local command examples assume this portable, project-specific cache root:

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/<project>-bazel-cache"
```

## Third-Party Dependency Hub

Use one neutral repository name for the third-party graph shared by upstream
libraries and the final application:

```python
maven = use_extension("@rules_jvm_external//:extensions.bzl", "maven")
maven.install(
    name = "nv_third_party_deps",
    ...
)
use_repo(maven, "nv_third_party_deps")
```

Public BUILD targets reference labels such as:

```text
@nv_third_party_deps//:org_springframework_boot_spring_boot
```

The name describes the repository's contents, not its implementation. The hub
contains third-party dependency targets. `rules_jvm_external` currently
resolves them from Maven-compatible repositories, but the hub contains no
first-party nv-boot or application targets and implies no Maven publication.

A downstream source consumer must:

- use the same `maven.install.name`;
- list the upstream module in `known_contributing_modules`;
- add only its own additional third-party roots;
- verify the final Spring Boot app contains one
  `rules_jvm_external++maven+<hub>` runtime graph.

Two independent hubs can package duplicate or version-skewed libraries into
one app. Earlier downstream validation demonstrated substantial app inflation
and incompatible dependency-family versions before the hubs were unified.

## Build Targets

Keep BUILD targets close to the Maven modules they represent:

```python
load("//tools/bazel:java.bzl", "nv_boot_library", "nv_boot_library_test")

nv_boot_library(
    name = "module_library",
    srcs = glob(["src/main/java/**/*.java"]),
    resources = glob(["src/main/resources/**"]),
    deps = [...],
    visibility = ["//visibility:public"],
)

nv_boot_library_test(
    name = "tests",
    srcs = glob(["src/test/java/**/*.java"]),
    coverage_library = ":module_library",
    deps = [
        ":module_library",
        ...
    ],
)
```

Library targets produce ordinary Bazel Java jars. They do not produce POMs,
Maven-named jars, publication sources jars, or local Maven install scripts.

When the library uses Lombok, set `build --java_header_compilation=false` in the
library repository and every downstream root application. `.bazelrc` files are
not transitive through Bzlmod, and Turbine cannot process Lombok. Validate the
published source-target shape with a temporary consumer whose `.bazelrc` does
not rely on the upstream checkout.

## Public Target Runtime Parity

Maven optional/provided dependencies are not normal consumer runtime
dependencies. Bazel `java_library.deps` normally are. Before exposing a public
source target:

1. Classify every module dependency from its POM as runtime, optional,
   provided, or test.
2. Keep required runtime dependencies on the public target.
3. Put optional/provided jars needed to compile source behind a private
   `neverlink = True` target, or another verified compile-only boundary.
4. Record a complete module-by-module mapping from every optional/provided POM
   coordinate to its compile-only BUILD boundary. Do not use one passing
   downstream app as evidence that unconsumed modules are correct.
5. Build temporary downstream consumers through Bzlmod for the supported
   profiles: minimal/non-integration, servlet, reactive, and an optional
   integration such as Cassandra where applicable.
6. Compare Maven runtime jars with each consumer app's `BOOT-INF/lib` and
   explain every difference.

Use an explicit split that is visible in the module BUILD file:

```starlark
REQUIRED_DEPS = [
    # Normal Maven compile/runtime dependencies.
]

# Maven optional/provided integrations compile this module, but must not become
# runtime dependencies of every downstream Bazel application.
OPTIONAL_COMPILE_DEPS = [
    # Optional/provided APIs and the strict-deps labels needed with them.
]

java_library(
    name = "optional_compile_deps",
    exports = OPTIONAL_COMPILE_DEPS,
    neverlink = True,
    visibility = ["//visibility:private"],
)

nv_boot_library(
    name = "public_library",
    deps = REQUIRED_DEPS + [":optional_compile_deps"],
    # Other attributes omitted.
)

nv_boot_library_test(
    name = "tests",
    deps = [":public_library"] + REQUIRED_DEPS + OPTIONAL_COMPILE_DEPS,
    # Other attributes omitted.
)
```

Keep the comment above the optional list: without it, a future maintainer can
reasonably mistake the split for needless duplication. Do not create this
module-level split when Lombok is the only optional dependency and the shared
library macro already supplies Lombok through a `neverlink` target.

Add a small analysis test for every affected public target. It should inspect
`JavaInfo.transitive_runtime_jars` and fail when a known optional/provided
artifact family is exported. With Bazel 9, load the provider explicitly:

```starlark
load("@rules_java//java/common:java_info.bzl", "JavaInfo")
```

Keep forbidden artifact lists next to the affected BUILD target so the intent
is reviewable. Include the guard in `bazel test //...`. This is an early
regression check, not a replacement for representative downstream consumers
or the final application `BOOT-INF/lib` comparison.

This is especially important for integration libraries that compile support
for Cassandra, MVC, WebFlux, servlet APIs, Spring Cloud, or similar optional
frameworks. Do not let those integrations auto-configure merely because the
library source references their classes. If an app needs a temporary
`java_import` adapter around a source-built jar, record the upstream defect and
remove the adapter after the public target is corrected.

## Real JUnit XML

When a Bazel `sh_test` wraps JUnit ConsoleLauncher, Bazel creates an outer
`test.xml` containing one testcase for the shell wrapper. It does not describe
the Java tests. Have ConsoleLauncher write declared undeclared outputs instead:

```bash
junit_report_dir="${TEST_UNDECLARED_OUTPUTS_DIR}/junit"
mkdir -p "${junit_report_dir}"
"${junit_runner}" "$@" --reports-dir="${junit_report_dir}"
```

Require a nonempty
`test.outputs/junit/TEST-junit-jupiter.xml`, verify that its `tests`,
`failures`, and `errors` values match the console summary, and publish that
file from GitLab:

```yaml
artifacts:
  reports:
    junit:
      - bazel-ci-artifacts/bazel-testlogs/**/test.outputs/junit/TEST-junit-jupiter.xml
```

Do not publish `bazel-testlogs/**/test.xml`. Preserve status precedence in a
combined JUnit/JaCoCo wrapper: return the JUnit status when nonzero; otherwise
return a missing/failed JUnit or coverage report status. Re-run full and
focused class/method selections to prove reports and filtering both work.

For a Spring Boot application, use the established Spring Boot packaging rule
to create the executable app jar. Do not make parent/library repositories
produce executable Spring Boot jars.

## Dependency Updates

When code needs a new third-party dependency:

1. Add the external label to the owning BUILD target.
2. Add its root coordinate to `MODULE.bazel` if the shared hub does not already
   expose it.
3. Omit the version when an imported BOM manages it.
4. Pin a version only for an intentional override, build tool, or unmanaged
   artifact.
5. Keep checksum enforcement enabled and inspect the resolved lock for a
   partially upgraded dependency family. If a build tool upgrades only part of
   a family used by production code, explicitly align the remaining unmanaged
   modules and document why.
6. Add a shipped production root to the parent/library NOTICE root file when
   applicable.
7. Repin and validate.

```bash
REPIN=1 bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  run @nv_third_party_deps//:pin
```

For a repository's first pin with `rules_jvm_external` 7.0, export a zero-byte
project-owned `maven_install.json`, set
`lock_file = "//:maven_install.json"`, and let the pin target replace it. Never
use another project's lockfile as a bootstrap seed.

When a temporary downstream consumer owns the merged hub and an upstream
source module already contributes that hub with its own lock, bootstrap in two
stages: temporarily remove the upstream `bazel_dep`/override and contributor
entry, pin downstream-owned roots, restore the complete contributor list, and
repin the merged graph. If a local override was used, repin once more without
it. A build/test against remote source is not evidence that remote repinning
works.

For a CVE override, add explicit coordinates after the BOM-managed root and
comment why they exist. Remove the override after the imported BOM supplies an
acceptable version.

## License And NOTICE

First confirm whether the project requires NOTICE output. Do not copy NOTICE
infrastructure into an internal-only repository when its owner explicitly says
it is out of scope; record that decision in `BAZEL.md` instead. When NOTICE is
required, follow the patterns below.

For a parent/library repository, keep a small checked-in JSON file of direct
shipped production roots, for example `tools/bazel/notice_roots.json`. Generate
the transitive NOTICE closure from that file, `maven_install.json`, and
checked-in license metadata.

For an executable application, derive NOTICE from the actual third-party jars
nested in the app jar. Exclude first-party groups explicitly.

Do not retain POM-generation or Maven-publication macros merely to obtain a
NOTICE dependency list. Do not invoke Maven, `license-maven-plugin`, or project
POM files from the Bazel NOTICE path.

Treat upstream POM XML as untrusted for both local-cache and remote-repository
metadata refreshes. Use a hardened parser or reject DTD and entity declarations
before parsing.

Fail when an explicit NOTICE root is absent from `maven_install.json`. For
application runtime NOTICE, also fail when the version encoded in a packaged
jar path differs from the lockfile version. Run the generator through a
Bazel-declared Python executable so build actions do not depend on host
`python3`.

Typical commands:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  run //:generate_notice -- --update-metadata --write

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //tools/bazel:notice_check_test \
  --cache_test_results=no \
  --test_output=errors
```

## Maven Plugin Parity

Translate plugin behavior, not plugin names:

- `maven-surefire-plugin`: Bazel Java/JUnit test targets with readable logs.
- `jacoco-maven-plugin`: test-integrated JaCoCo HTML, XML, and exec output.
- `license-maven-plugin`: Bazel-native NOTICE generation and drift test.
- `git-commit-id-maven-plugin`: generate `git.properties` for executable app
  packaging when application code expects it.
- `spring-boot-maven-plugin`: Spring Boot executable app packaging in app
  repositories only.
- `properties-maven-plugin`: preserve a generated properties resource only
  where runtime code actually consumes it.
- `flatten-maven-plugin`: no Bazel equivalent is needed when Bazel does not
  publish Maven POMs.

Maven may continue using its existing plugins during coexistence. That does
not require Bazel to call Maven or emulate Maven publication.

## Application Migration Notes

- Pin Bazel Java compile and runtime versions to the same release as Maven.
- Model Docker/Testcontainers fixture directories as runfiles.
- Prefer stream-based classpath resource reads over
  `ClassPathResource#getFile()` because Bazel packages resources in jars.
- Keep proto/gRPC generators aligned with runtime libraries. If a rule bundles
  a newer generator than the chosen runtime, select a compatible pinned tool
  through the shared third-party hub.
- Package and run the executable Spring Boot jar before declaring application
  migration complete.
- Preserve direct Spring Boot starter roots. Component jars used for strict
  compilation do not replace a starter's runtime closure. Verify servlet apps
  contain their selected embedded server and reactive apps contain theirs.

## Configuration Files In BAZEL.md

Explain these files in plain language for Maven users:

- `.bazelversion`: exact Bazel version read by Bazelisk.
- `.bazelrc`: checked-in defaults for Bazel commands, including Java 25 and
  `build --java_header_compilation=false`; it is not a lockfile and must not
  contain secrets or workstation paths.
- `.bazel_downloader_config`: URL rewrites for downloads made by Bazel
  repository rules; it does not select versions, replace `MODULE.bazel`
  repositories, or contain credentials.
- `MODULE.bazel`: developer-edited dependency/module input.
- `maven_install.json` and `MODULE.bazel.lock`: generated locks that are
  committed and never hand-edited.

## CI Coexistence

- Keep Maven CI as the default while Maven consumers remain.
- Add Bazel as an opt-in job controlled by `ENABLE_BAZEL_BUILD: "true"`.
- Prefer Bazelisk and honor `.bazelversion`.
- Use Java 25 and keep Bazel output roots outside `$CI_PROJECT_DIR`.
- For private GitLab SSH source overrides, use a scoped
  `git config url.*.insteadOf` rewrite to job-token-authenticated HTTPS before
  Bazel runs. This avoids requiring `ssh` in the CI image and keeps credentials
  out of `MODULE.bazel`.
- Pass Docker variables into Testcontainers-backed Bazel tests.
- Retain JUnit, JaCoCo, NOTICE, and executable app artifacts as appropriate.
- Do not add a Bazel Maven publish/deploy job.

## Validation

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" build //...

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //... \
  --cache_test_results=no \
  --test_output=errors

mvn clean install
```

For downstream validation, consume the upstream module through Bzlmod and test
the final application. The downstream root must own and repin
`maven_install.json` for the final merged hub. A local `--override_module` is
useful before the upstream commit is pushed; replace it with the pinned Git
override for CI.

## Historical Wrong Turn

nv-boot-parent temporarily generated Maven-named jars, POMs, sources jars,
local install scripts, and a remote deploy bridge from Bazel. That work proved
format compatibility but was unnecessary for Bazel consumers, which can use
source targets directly. The bridge and its metadata synchronization helpers
were removed. Do not reproduce that path unless the user explicitly requires a
temporary interoperability experiment and accepts its maintenance cost.
