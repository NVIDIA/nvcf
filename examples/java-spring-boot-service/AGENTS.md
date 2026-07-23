# AGENTS.md - nvcf_java_example

Reference Spring Boot service, as a standalone Bazel module
(`module(name = "nvcf_java_example")`). The copy-me template for a new NVCF Java
service module.

## Build and test (from this directory)

```
cd examples/java-spring-boot-service
bazel build //...
bazel test //...
```

Depends only on `nvcf_java_rules` (macros + multi-arch image helper) via
`bazel_dep` + `local_path_override`, plus its own `@maven` hub and committed
`maven_install.json`. A real service additionally `bazel_dep`s `nv_boot_parent`
and adds the internal Maven overlay. Re-pin after editing `maven.install`:

```
REPIN=1 bazel run @maven//:pin
```

The `boms` list must stay identical to `SPRING_BOOT_BOM` in
`@nvcf_java_rules//:platform.bzl`; `//:bom_alignment_test` enforces it.
