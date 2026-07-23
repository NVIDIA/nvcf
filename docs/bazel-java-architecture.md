# Java in the NVCF Monorepo: Target Bazel Architecture

Status: target architecture. Supersedes the interim root-workspace setup where
all Java code shared one root Maven hub and one lockfile.

## Goals

- Every Java library and service is an independent Bazel module. Each builds,
  tests, and pins its dependencies on its own: `cd <module> && bazel build //...`
  works with no reference to the repo root.
- A service can be consumed from outside the monorepo by module name, pulling
  only its subtree, via `git_override` with `strip_prefix`.
- One shared source of truth for the Spring Boot platform (BOM plus common
  starters) so 10+ services do not each re-declare and drift on versions.
- Parity with the rest of the repo: Go and Rust services are already
  self-contained nested modules with their own `MODULE.bazel` and CI matrix
  row. Java stops being the exception.

## Non-goals

- No shared root Maven hub. `@nv_third_party_deps` at the root and the root
  `//:maven_install.json` are retired for Java.
- No cross-module dependence through absolute source labels
  (`//src/libraries/java/nv-boot-parent/...`). Cross-module edges go through
  `bazel_dep`.

## Topology

```
rules/java/                         module: nvcf_java_rules   (shared macros)
  MODULE.bazel
  defs.bzl                          nvcf_java_library, nvcf_spring_boot_image,
  platform.bzl                      java_junit5_test, SPRING_BOOT_BOM, versions
src/libraries/java/
  nv-boot-parent/                   module: nv_boot_parent    (platform library)
    MODULE.bazel                    owns the Spring BOM + shared starters
    maven_install.json              its own lockfile
    nv-boot-*/BUILD.bazel           java_library targets, public
src/control-plane-services/
  cloud-tasks/                      module: nvct_cloud_tasks  (service)
    MODULE.bazel                    bazel_dep nv_boot_parent + nvcf_java_rules
    maven_install.json              its own lockfile: service-only deltas
    nvct-core/BUILD.bazel
    nvct-service/BUILD.bazel        nvcf_spring_boot_image -> OCI image
  <next java service>/              same shape, one per service
```

Three module kinds:

- Rules module (`nvcf_java_rules`, at `rules/java`). Holds the macros and the
  single versions/BOM definition. No application code. Consumed by every Java
  module via `bazel_dep` + `local_path_override`. This mirrors how
  `rules/oci-destinations` (module `nvcf_nvcr_destinations`) is already
  consumed by infra/cassandra.
- Platform library module (`nv_boot_parent`). Owns the canonical Spring Boot
  BOM and the shared `nv-boot-*` starters as `java_library` targets. It has its
  own Maven hub. Services depend on it, not on raw Spring coordinates it
  already provides.
- Service modules (`nvct_cloud_tasks`, and each future service). One image per
  service via `nvcf_spring_boot_image`. Depends on `nv_boot_parent` and
  `nvcf_java_rules`. Declares only the Maven coordinates it uses directly and
  that the platform does not already export.

## Dependency edges

In-monorepo builds resolve cross-module edges with `local_path_override`, so
local source always wins over any registry copy. Paths are relative to the
consuming module directory.

`rules/java/MODULE.bazel`:

```python
module(name = "nvcf_java_rules", version = "0.1.0")
bazel_dep(name = "rules_java", version = "9.3.0")
bazel_dep(name = "rules_jvm_external", version = "7.0")
bazel_dep(name = "contrib_rules_jvm", version = "0.27.0")
bazel_dep(name = "rules_oci", version = "2.2.7")
bazel_dep(name = "rules_pkg", version = "1.2.0")
```

`src/libraries/java/nv-boot-parent/MODULE.bazel`:

```python
module(name = "nv_boot_parent", version = "1.4.1")

bazel_dep(name = "nvcf_java_rules", version = "0.1.0")
local_path_override(module_name = "nvcf_java_rules", path = "../../../../rules/java")

bazel_dep(name = "rules_java", version = "9.3.0")
bazel_dep(name = "rules_jvm_external", version = "7.0")

maven = use_extension("@rules_jvm_external//:extensions.bzl", "maven")
maven.install(
    name = "maven",
    lock_file = "//:maven_install.json",
    # load() is forbidden in MODULE.bazel, so the BOM cannot reference
    # SPRING_BOOT_BOM here. The list is inlined verbatim and guarded by
    # //:bom_alignment_test, which fails if it drifts from platform.bzl.
    boms = [
        "org.springframework.boot:spring-boot-dependencies:4.0.7",
        "org.springframework.cloud:spring-cloud-dependencies:2025.1.2",
        "org.testcontainers:testcontainers-bom:2.0.5",
        "net.javacrumbs.shedlock:shedlock-bom:7.7.0",
    ],
    artifacts = [ ... shared starter coordinates ... ],
    repositories = [ ... same two public Central mirrors ... ],
)
use_repo(maven, "maven")
```

`src/control-plane-services/cloud-tasks/MODULE.bazel`:

```python
module(name = "nvct_cloud_tasks", version = "1.1.0")

bazel_dep(name = "nvcf_java_rules", version = "0.1.0")
local_path_override(module_name = "nvcf_java_rules", path = "../../../rules/java")

bazel_dep(name = "nv_boot_parent", version = "1.4.1")
local_path_override(module_name = "nv_boot_parent", path = "../../libraries/java/nv-boot-parent")

maven = use_extension("@rules_jvm_external//:extensions.bzl", "maven")
maven.install(
    name = "maven",
    lock_file = "//:maven_install.json",
    # Same inlined BOM list as every other Java module (see the note above);
    # //:bom_alignment_test keeps it aligned with platform.bzl.
    boms = [ ... the four canonical BOMs ... ],
    artifacts = [ ... only coordinates cloud-tasks uses directly ... ],
    repositories = [ ... same two public Central mirrors ... ],
)
use_repo(maven, "maven")
```

Service BUILD files then reference `@nv_boot_parent//nv-boot-registries:...`
for platform targets and `@maven//:...` for the service's own deps. The old
`@nv_third_party_deps//:...` labels are replaced by the module-local `@maven`.

## Maven strategy: one BOM, many hubs

Each module resolves its own Maven graph and writes its own
`maven_install.json`. That is what makes a module independently buildable. The
risk is version drift: two modules pinning different Spring versions put two
copies on a transitive classpath.

Rule: the Spring Boot BOM and any other shared BOMs are defined once, in
`@nvcf_java_rules//:platform.bzl`, as a loadable list:

```python
# rules/java/platform.bzl
SPRING_BOOT_VERSION = "3.5.11"
SPRING_BOOT_BOM = ["org.springframework.boot:spring-boot-dependencies:%s" % SPRING_BOOT_VERSION]
MAVEN_REPOSITORIES = [
    "https://repo1.maven.org/maven2",
    # internal mirrors appended by the private overlay, never in the OSS tree
]
```

`load()` is forbidden in `MODULE.bazel`, so a module cannot write
`boms = SPRING_BOOT_BOM` directly. Instead every Java module inlines the same
literal BOM list in its `maven.install` and lists BOM-managed coordinates
without a version. The alignment is enforced, not trusted: each module calls
`bom_alignment_test` (from `@nvcf_java_rules//:bom_alignment.bzl`), which
materializes `SPRING_BOOT_BOM` from `platform.bzl` and fails the build if any
canonical coordinate is missing from that module's `MODULE.bazel`. Bumping Spring
is then a one-line change in `platform.bzl`, the matching edit to each module's
inlined list, and a re-pin; a module left un-aligned fails its own test. Because
the BOM is identical everywhere, the independent resolutions agree.

The platform module (`nv_boot_parent`) additionally exports the common
starters as `java_library` targets. A service that depends on
`@nv_boot_parent//nv-boot-web:...` gets those starters and their transitive
Maven deps without re-declaring them. A service only lists a coordinate in its
own `maven.install` when it uses that library directly and the platform does
not already export it.

## Independent build contract

For every Java module:

- `MODULE.bazel` at the module root, with a unique `module(name=...)`.
- Committed `maven_install.json` (or `<name>_install.json`) lockfile.
- `.bazelrc` with the Java toolchain (`--java_language_version=21`,
  `--java_runtime_version=remotejdk_21`) and a `build:release` config.
- `bazel build //...` and `bazel test //...` pass from the module directory
  with no `--override_module` flags on the command line.
- The repo root lists the module path in `.bazelignore` so the root workspace
  does not recurse into it (same as `infra/cassandra`, stargate,
  function-autoscaler).

## CI: one matrix row per module

`.github/workflows/bazel.yml` derives a matrix row from each nested
`MODULE.bazel`. After migration:

- `bazel (nv-boot-parent)`, `bazel (cloud-tasks)`, and one row per future
  service, each running `bazel build //... && bazel test //...` in the module
  directory.
- Java paths are removed from `ROOT_GLOBS`. Java no longer rides the
  `bazel (root)` row.
- Add each module to the ROWS list:
  `nv-boot-parent|src/libraries/java/nv-boot-parent|false`,
  `cloud-tasks|src/control-plane-services/cloud-tasks|false`.

Isolated rows mean a failing service does not block the others, and build time
scales out instead of piling onto one root job.

## External and managed-side consumption

Because `nv_boot_parent` is a real module rooted at its own subtree, an
out-of-tree Bazel workspace consumes it without vendoring the monorepo:

```python
bazel_dep(name = "nv_boot_parent", version = "1.4.1")
git_override(
    module_name = "nv_boot_parent",
    remote = "https://github.com/NVIDIA/nvcf.git",
    commit = "<pinned sha>",
    strip_prefix = "src/libraries/java/nv-boot-parent",
)
```

`strip_prefix` requires `MODULE.bazel` at that subdirectory. The platform
library only needs public BCR modules (`rules_java`, `rules_jvm_external`) plus
its Maven hub, so it resolves for external consumers with no access to internal
rules. Services are consumable the same way when needed.

For in-monorepo builds the same modules use `local_path_override` instead of
`git_override`, so local edits are always authoritative and no network fetch of
the monorepo occurs.

## Versioning and pinning

- `module(version=...)` tracks the artifact version. `git_override` and
  `local_path_override` ignore it (they pin by commit or path), so it is
  informational for in-tree builds and meaningful only if a module is ever
  published to a registry.
- Re-pin a module after changing its `maven.install`:
  `REPIN=1 bazel run @maven//:pin` from the module directory. Commit the
  updated lockfile.
- Bumping a shared version (Spring, JUnit) is a change in
  `@nvcf_java_rules//:platform.bzl` followed by a re-pin of every Java module.
  CI catches any module left un-repinned.

## NOTICE and license

Each module generates its NOTICE from its own lockfile. The `nv-boot-parent`
notice tooling that currently reads the root `//:maven_install.json` is
repointed at the module-local lockfile. Services generate their own NOTICE the
same way. There is no root aggregate lockfile for Java to depend on.

## Adding a new Java service

1. Create the module directory with `MODULE.bazel`
   (`module(name = "nvct_<svc>", ...)`), `bazel_dep` + `local_path_override`
   for `nvcf_java_rules` and `nv_boot_parent`, and a `maven.install` loading
   `SPRING_BOOT_BOM` plus only the service's direct coordinates.
2. Add `.bazelrc` (Java toolchain + `build:release`) and a `CLAUDE.md`
   (`@AGENTS.md`) plus `AGENTS.md` for the subtree.
3. Write `BUILD.bazel` using `nvcf_java_library`, `nvcf_spring_boot_image`, and
   `java_junit5_test` from `@nvcf_java_rules//rules/java:defs.bzl`.
4. `REPIN=1 bazel run @maven//:pin`; commit `maven_install.json`.
5. Add the module path to root `.bazelignore` and to the `bazel.yml` ROWS list.
6. `cd <module> && bazel build //... && bazel test //...` must pass.

## Migration from the interim setup

Order matters: the rules module and platform must land before services can
point at them.

1. `rules/java`: add `MODULE.bazel` (`nvcf_java_rules`) and `platform.bzl`
   (`SPRING_BOOT_BOM`, `MAVEN_REPOSITORIES`, shared version constants).
2. `nv-boot-parent`: add `MODULE.bazel` (`nv_boot_parent`) and its own
   `maven.install` seeded from the shared coordinates it owns; re-pin; move its
   notice tooling to the module-local lockfile. Verify
   `cd src/libraries/java/nv-boot-parent && bazel build //...`.
3. `cloud-tasks`: add `MODULE.bazel` (`nvct_cloud_tasks`), replace
   `@nv_third_party_deps//:...` with `@maven//:...`, replace
   `//src/libraries/java/nv-boot-parent/...` source labels with
   `@nv_boot_parent//...`, re-pin. Verify standalone build and image target.
4. Remove Java coordinates from the root `MODULE.bazel` `nv_third_party_deps`
   hub and the root `//:maven_install.json`; drop Java from `ROOT_GLOBS`; add
   the two module rows to `.bazelignore` and `bazel.yml`.
5. Repeat step 3 for each remaining Java service as it onboards.
```
