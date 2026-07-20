# Bazel JaCoCo Coverage — Design Review

Reviewed by: Claude (claude-sonnet-4-6)
Date: 2026-07-14
Branch: feat/bazel
Scope: JaCoCo integration, MODULE.bazel/maven_install.json changes, update_maven_deps.py 5-part coordinates, BAZEL.md accuracy, Maven/Bazel coexistence risks

---

## Summary

The core approach is sound: `use_testrunner = False` with a custom `CoverageConsoleLauncher` that owns the JaCoCo lifecycle is the right pattern when Bazel's built-in Java coverage (which is tied to the Bazel test runner) is insufficient. There are four concrete issues to address, listed below in priority order.

---

## 1. `org.jacoco:org.jacoco.cli` Is Unused and Creates a Hidden Transitive Dependency

**Issue:** `org.jacoco:org.jacoco.cli:0.8.14` is declared in `MODULE.bazel` but referenced nowhere in any `.bzl` or `BUILD.bazel` file. `coverage_console_launcher` directly depends on `@nv_boot_maven//:org_jacoco_org_jacoco_core` and `@nv_boot_maven//:org_jacoco_org_jacoco_report`, but neither `org.jacoco.core` nor `org.jacoco.report` is listed in `MODULE.bazel` as an explicit artifact. They are currently resolved only as transitive dependencies of `org.jacoco.cli`.

This violates the stated principle that MODULE.bazel lists "root coordinates" for artifacts the build directly uses. If `org.jacoco.cli` is ever removed or if rules_jvm_external's transitive resolution changes, `coverage_console_launcher` loses its direct API deps silently.

**Fix:** Replace `org.jacoco:org.jacoco.cli:0.8.14` with the two artifacts that are actually used:

```python
"org.jacoco:org.jacoco.core:0.8.14",
"org.jacoco:org.jacoco.report:0.8.14",
```

`org.jacoco:org.jacoco.agent:jar:runtime:0.8.14` stays as-is; it is correctly and directly used as the `-javaagent`.

After the change, run `REPIN=1 bazel run @nv_boot_maven//:pin` to update `maven_install.json`.

---

## 2. Coverage Classfiles Heuristic in `nv_boot_library_test` Is Fragile

**Issue:** In `java.bzl:120-124`:

```starlark
coverage_classfiles = []
for dep in deps:
    if type(dep) == "string" and dep.startswith(":"):
        coverage_classfiles = [dep]
        break
```

This takes the **first** `:local_dep` from `deps` as the classfiles source for the JaCoCo analyzer. The `fail()` below only checks that *some* local dep exists — it does not verify the dep is actually the module library. Two failure modes:

1. A caller adds a utility or fixture target before the main library dep. Coverage reports against the wrong jar with no error at analysis or test time.
2. A module that for structural reasons lists its main library as a non-first dep silently generates an incorrect coverage report.

The error message says "requires the module library as a local dep so coverage reports can be generated" but cannot enforce that the *first* dep is the right one.

**Fix:** Add an explicit `coverage_library` parameter to `nv_boot_library_test` and remove the heuristic:

```starlark
def nv_boot_library_test(
        name,
        srcs,
        deps,
        coverage_library,   # required: the module library target for JaCoCo analysis
        ...):
    if not coverage_library or not coverage_library.startswith(":"):
        fail("coverage_library must be a local dep starting with ':'")
    coverage_classfiles = [coverage_library]
```

This is a breaking change to existing `nv_boot_library_test` callsites but is straightforward to migrate (all existing callers pass `:the_module_library` as the first dep, so `coverage_library = ":the_module_library"` is the mechanical migration).

---

## 3. Coverage Report Generation Failure Masks Test Results

**Issue:** In `CoverageConsoleLauncher.java:42-54`:

```java
try {
    CommandResult<?> result = ConsoleLauncher.run(...);
    exitCode = result.getExitCode();
} finally {
    generateCoverageReport(parsedArgs);  // throws if RT.getAgent() fails
}

System.exit(exitCode);
```

`generateCoverageReport()` calls `RT.getAgent().dump(false)` inside a `finally` block. If the JaCoCo agent is unavailable, misconfigured, or the file system is unwritable, `generateCoverageReport()` throws an exception that propagates through the `finally` block. Java's semantics for a `finally` throw discard the original completion state of the `try` block — meaning a test failure that set `exitCode = 1` would be suppressed and replaced by the coverage exception. The process exits via the uncaught exception path rather than `System.exit(1)`, which may produce a confusing `FAILED` status with a stack trace about coverage rather than the original test failure.

**Fix:** Wrap the report generation with error logging and preserve the test exit code:

```java
} finally {
    try {
        generateCoverageReport(parsedArgs);
    } catch (Exception e) {
        System.err.println("WARNING: JaCoCo coverage report generation failed: " + e.getMessage());
        e.printStackTrace(System.err);
        // exitCode from the try block is preserved; report generation failure
        // is non-fatal so test results are not obscured.
    }
}
```

The test result is the primary signal; coverage report failure should be a warning, not a hard failure.

---

## 4. `bazel coverage //...` LCOV Workflow Is Documented But Does Not Work With These Targets

**Issue:** BAZEL.md documents two coverage paths:

1. **JaCoCo HTML/XML** — generated automatically by `nv_boot_library_test` on every `bazel test` run. This works.
2. **Bazel native `bazel coverage //...` with LCOV** — used to produce a combined `_coverage_report.dat` for `lcov_to_sonar_generic.py`. This does not work with these targets.

Bazel's native Java coverage instrumentation requires the Bazel test runner. All `nv_boot_library_test` targets set `use_testrunner = False`, which bypasses the Bazel test runner entirely. When `bazel coverage //...` is run, Bazel will not instrument these Java tests, and the LCOV report will either be empty or contain only coverage from other test targets (the `sh_test` targets in `tools/bazel/`). The `lcov_to_sonar_generic.py` script is correct and useful — but it has no meaningful input to work with from these test targets.

**Fix for BAZEL.md:** Add a note to the Coverage section:

> **Note:** `bazel coverage //...` does not collect coverage data from `nv_boot_library_test` targets because `use_testrunner = False` bypasses Bazel's Java instrumentation. The LCOV combined report is only useful when the workspace also contains standard `java_test` targets. For `nv-boot-parent`, use the JaCoCo XML reports from `bazel-testlogs/<module>/tests/test.outputs/jacoco.xml` for Sonar coverage integration.

The `lcov_to_sonar_generic.py` script and its test should be retained — they are correct and will be needed when standard `java_test` targets exist alongside these.

---

## 5. MODULE.bazel JaCoCo Coordinate and ASM Version Changes

### 5-part coordinate `org.jacoco:org.jacoco.agent:jar:runtime:0.8.14`

The `update_maven_deps.py` extension to support 5-part `group:artifact:packaging:classifier:version` coordinates is correct. The parser discards packaging and classifier and uses only group, artifact, and version for the canonical key (`org.jacoco:org.jacoco.agent`). This is safe because the key is only used for publication property lookup, and no one would want to publish a JaCoCo agent dependency in a POM.

One edge case to be aware of: if MODULE.bazel ever had both `org.jacoco:org.jacoco.agent:jar:runtime:0.8.14` and a hypothetical `org.jacoco:org.jacoco.agent:0.8.14` (the default jar), the duplicate detection would fire with a correct but surprising error. This is the right behavior.

4-part coordinates (`group:artifact:packaging:classifier` without version) are not handled and raise `ValueError`. This is intentional — an unversioned 4-part form is not valid for rules_jvm_external explicit pins. No action needed.

### ASM version bump in `maven_install.json`

Coursier resolved `org.ow2.asm:asm`, `asm-commons`, and `asm-tree` from 5.0.3 / 9.7.1 to 9.9. This is caused by JaCoCo 0.8.14 requiring ASM 9.9 as a transitive dependency of `org.jacoco.core`. The bump is upward-compatible and acceptable:

- JaCoCo 0.8.14 ships its own shaded ASM copy at `org.jacoco.agent.rt.internal_29a6edd.asm.*` which is isolated from the external `org.ow2.asm` namespace. There is no runtime conflict.
- ASM 9.9 supports Java 25 class files, which is required for this project.
- The version override map in `maven_install.json` (`org.ow2.asm:asm:5.0.3 -> org.ow2.asm:asm:9.9`) is rules_jvm_external's standard conflict resolution output.

No action needed on the ASM versions.

---

## 6. Maven/Bazel Coexistence — No Risks Introduced

- `org.jacoco:org.jacoco.agent:jar:runtime` and `org.jacoco:org.jacoco.cli` are Bazel build tools. They are not in `maven_publish_properties.json` and will not appear in any generated publication POM.
- The parent `pom.xml` has its own JaCoCo Maven plugin configuration for the Maven build path. These are independent.
- The ASM version changes in `maven_install.json` are Bazel-only and do not affect the Maven dependency resolution used by Maven consumers.

---

## BAZEL.md Accuracy — Minor

The document is accurate except for the `bazel coverage //...` issue described in §4 above. All other commands, file paths, and workflows have been verified against the implementation.

One small inaccuracy: the single-class and single-method test filter examples use `--test_arg='--exclude-classname=...'` which is a negative filter (include everything *except* classes not matching). This is correct for the way JUnit ConsoleLauncher works here (the test runner scans the full classpath and you must exclude what you don't want). This is already noted in the doc and is not a bug — just worth keeping as-is since the alternative (`--select-class`) is explicitly prohibited by the JUnit + classpath-scanning constraint.

---

## Action Summary

| # | File | Action | Priority |
|---|------|---------|----------|
| 1 | `MODULE.bazel` | Replace `org.jacoco.cli` with explicit `org.jacoco.core` and `org.jacoco.report` pins | High |
| 2 | `tools/bazel/java.bzl` | Add explicit `coverage_library` param; remove heuristic | Medium |
| 3 | `tools/bazel/src/main/java/.../CoverageConsoleLauncher.java` | Catch coverage exceptions in `finally`, preserve test exit code | Medium |
| 4 | `BAZEL.md` | Add caveat that `bazel coverage //...` does not collect data from `nv_boot_library_test` targets | Low |
