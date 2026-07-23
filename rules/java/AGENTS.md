# AGENTS.md - nvcf_java_rules

Standalone Bazel module (`module(name = "nvcf_java_rules")`) holding the shared
Java build macros and platform constants for every NVCF Java module. No
application code lives here.

## What this module provides

- `//:defs.bzl` - `nvcf_java_library`, `nvcf_spring_boot_image`.
- `//:platform.bzl` - `SPRING_BOOT_BOM`, `MAVEN_REPOSITORIES`, and the Spring /
  Spring Cloud / Testcontainers / ShedLock version constants. Single source of
  truth for the platform.
- `//:bom_alignment.bzl` - `bom_alignment_test`, the guard that keeps each
  consuming module's inlined `boms` list identical to `SPRING_BOOT_BOM`.
- `//private:oci.bzl` - vendored `create_oci_image` (multi-arch), so this module
  does not depend on the root workspace's `//rules/oci`.
- `//platforms:linux_arm64`, `//platforms:linux_x86_64`.

## How modules consume it

A Java module adds, in its `MODULE.bazel`:

```python
bazel_dep(name = "nvcf_java_rules", version = "0.1.0")
local_path_override(module_name = "nvcf_java_rules", path = "<relpath>/rules/java")
```

and inlines the same literal BOM list `SPRING_BOOT_BOM` exposes (load() is
forbidden in MODULE.bazel), guarded by `bom_alignment_test`.

## BOM / version bumps

Bumping Spring, Spring Cloud, Testcontainers, or ShedLock is a one-line edit in
`//:platform.bzl`, the matching edit to every Java module's inlined `boms`, then
a re-pin (`REPIN=1 bazel run @maven//:pin`) of each module. `bom_alignment_test`
fails any module left un-aligned.

## Build

```
cd rules/java && bazel build //...
```

The module resolves only public BCR modules plus its OCI bases, so it builds on
the public mirror. It is not in the root `.bazelignore` because its BUILD files
declare no targets that reference the root workspace; the root never loads its
`.bzl` files once Java services are their own modules.
