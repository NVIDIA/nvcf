# rules/java

Bazel build rules for NVCF Java services. Load them from `//rules/java:defs.bzl`.

```starlark
load("//rules/java:defs.bzl", "nvcf_java_library", "nvcf_spring_boot_image")
```

## nvcf_spring_boot_image

Packages a Java application's runtime classpath into a multi-arch OCI image.

```starlark
nvcf_spring_boot_image(
    name = "image",
    main_class = "com.nvidia.nvcf.example.ExampleApplication",
    deps = [":example_lib"],   # java_library targets; transitive runtime jars are packaged
    # base = "@temurin_jre",   # default JRE base
    # registry = "nvcr.io/...",# emits :image_push when set
    # jvm_flags = ["-XX:MaxRAMPercentage=75"],
)
```

It generates the same target set as the Go image macro (`:image`,
`:image_index`, `:image_load`, `:image.tar`, and `:image_push` when a registry
is given), so a Java service plugs into the existing release machinery unchanged.

Design note: the app runs from an exploded classpath (`/app/lib/*.jar`), not a
fat jar. Each dependency jar keeps its own `META-INF/spring.factories` and
`META-INF/spring/*.AutoConfiguration.imports`. A fat/singlejar would collapse
those same-path resources to a single file and silently break Spring Boot
auto-configuration.

Multi-module constraint: jars are packaged flat under `/app/lib` by basename, so
every jar on the transitive runtime classpath must have a unique filename. Maven
jars already are (`artifact-version.jar`); a Bazel `java_library` jar is named
after its target label (`lib<name>.jar`). A multi-module service must give its
library targets distinct names (do not reuse `lib` or `main` across packages) or
the compiled jars overwrite each other in the image layer. The example service
has a single library and is not affected.

## nvcf_java_library

Thin wrapper over `java_library`. Use it for NVCF Java code so services load a
single entry point and shared defaults can be added later in one place.

## Maven dependencies

Coordinates are resolved by `rules_jvm_external` and pinned in
`//:maven_install.json`. To add a dependency:

1. Add the coordinate to `maven.install(artifacts = [...])` in the root
   `MODULE.bazel`.
2. Re-pin: `bazel run @nv_third_party_deps//:pin` (or `REPIN=1 bazel run @nv_third_party_deps//:pin` when
   updating an existing set).
3. Reference it as `@nv_third_party_deps//:group_artifact` in `deps`. List every artifact your
   code imports directly, not just the aggregator starter, so the strict-deps
   header compiler resolves the symbols.

### Internal (nv-boot) dependencies

The foundation resolves from Maven Central only, so it builds anywhere including
the public GitHub mirror. A service that depends on nv-boot or other internal
artifacts adds the internal Artifactory Maven virtual repository to the
`repositories` list in `maven.install` and lists the nv-boot coordinates in
`artifacts`. Point `repositories` at your internal Artifactory virtual repo URL
(kept out of this file to preserve OSS snapshot hygiene) ahead of Central, then
re-pin. Builds that need those artifacts then run only where that repository is
reachable.

## Java toolchain

Java builds use a hermetic remotejdk 21 (`--java_language_version=21`,
`--java_runtime_version=remotejdk_21` in `.bazelrc`), independent of the host
JDK, so builds are reproducible on any developer machine and in CI.
