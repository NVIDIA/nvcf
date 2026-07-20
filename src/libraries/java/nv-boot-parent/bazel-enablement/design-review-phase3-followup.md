# Phase 3 Bazel Migration — Follow-up Review

Reviewed by: Claude (claude-sonnet-4-6)
Date: 2026-07-14
Scope: Correctness of follow-up changes addressing design-review-phase3.md feedback

---

## Summary

All five feedback items are correctly addressed. No new correctness bugs are introduced. Two minor observations below (neither is a blocker).

---

## Item-by-Item Assessment

### P0 — Comment parser bug in `update_maven_deps.py`

**Fix: correct.**

```python
if stripped.startswith("#"):
    continue
```

Added at `update_maven_deps.py:54-55`, after the `in_array` gate and before the closing-bracket check. Placement is correct: comment lines inside the array are silently skipped rather than being fed to `COORDINATE_PATTERN`. The commented-out Tomcat example block in MODULE.bazel no longer produces phantom coordinates. The `if not entries: raise ValueError` guard below still fires if the array is genuinely empty.

### P3 — Fragile array header match in `update_maven_deps.py`

**Fix: correct.**

```python
if not in_array and stripped.startswith(f"{array_name} = ["):
```

Changed from `==` to `startswith` at `update_maven_deps.py:49`. Now tolerates trailing characters (comments, whitespace) on the opening bracket line without silently producing an empty result. No regression: the loop still only enters `in_array` mode once, and the closing `]` breaks correctly.

### P1 — CI enforcement via `bazel test //...`

**Fix: correct for the enforcement goal. One non-blocking structural note.**

Two targets are added in `tools/bazel/BUILD.bazel`:

- `maven_deps_check` (genrule): runs `update_maven_deps.py --check --workspace .` and produces a sentinel `.ok` file. In Bazel's sandboxed execution, the exec root lays out all declared `srcs` at their workspace-relative paths, so `--workspace .` correctly resolves `MODULE.bazel` and `tools/bazel/generated_maven_deps.bzl` in the sandbox. The `&& touch $@` idiom means the sentinel is only written on success, so a stale generated file causes the build to fail. Validated working.

- `maven_deps_check_test` (sh_test): the actual `bazel test //...` enforcement. Calls `update_maven_deps.py --check --workspace <workspace>` via `find_workspace` heuristics.

**Structural note:** The `maven_deps_check` genrule is not depended upon by any other target, so `bazel build //...` does not build it. Only `bazel test //...` (via the `sh_test`) enforces the invariant automatically. This is acceptable — the sh_test is the right enforcement hook — but the genrule without a dependent may confuse a future maintainer wondering why it exists. A one-line comment on the genrule (`# Buildable staleness check; test coverage is via maven_deps_check_test`) would close the question.

`rules_shell` is now declared as a direct `bazel_dep` in MODULE.bazel, which is correct Bzlmod hygiene for a directly-used rule.

**`find_workspace` in `maven_deps_check_test.sh`:** The fallback chain is reasonable. Under `bazel test`, `BUILD_WORKSPACE_DIRECTORY` is not set, so the script falls through to `script_dir/../..`. When `$0` resolves inside the Bzlmod runfiles tree at `<runfiles>/_main/tools/bazel/maven_deps_check_test.sh`, `script_dir/../..` = `<runfiles>/_main`, which contains `MODULE.bazel` via the `//:MODULE.bazel` data dep. This works in the validated environment. The `_main` and `nv_boot_parent` explicit candidates provide fallback coverage. No issue.

### P2 — Spring/VMware metadata in generated POMs

**Fix: correct.**

`_developers_xml()` is extracted as a private Starlark helper (`maven.bzl:74-90`). `nv_boot_publish_pom` now takes optional parameters:

```python
project_base_url = "https://gitlab-master.nvidia.com/nvcf/nvcf-libraries/java/nv-boot-parent"
developer_name = "NVIDIA"
developer_email = ""
developer_organization = "NVIDIA Corporation"
developer_organization_url = "https://www.nvidia.com"
```

The defaults are NVIDIA values. `<url>` and `<scm><url>` both use `project_base_url`, which makes them identical — acceptable for an internal artifact. If a caller ever needs to distinguish the project homepage from the SCM URL, an additional `scm_url` parameter would be needed, but there is no requirement for that now.

The `developers_xml` string is interpolated into `pom_xml_suffix` before `_genrule_cmd_escape` is applied to `pom_xml_suffix` as a whole (`maven.bzl:187-189`). The escape is therefore applied to the developer content as well. The current NVIDIA defaults contain no `$` characters, but the chain is correct for any caller-supplied values that might.

**`<email>` omission:** `developer_email = ""` causes `<email>` to be absent from the generated `<developer>` block. This is intentional and correct — NVIDIA's published POMs don't require a developer email, and Maven does not require it.

### P5 — Root-level `maven.properties` in jars

**Intentionally retained. No review needed.**

Noted: this exists for Maven build parity with `properties-maven-plugin`. The prior review flagged it as unexpected but non-breaking; retaining it with an explicit rationale is the correct resolution.

---

## No New Issues

The `_genrule_cmd_escape` escape chain is preserved correctly across both `pom_xml_prefix` and `pom_xml_suffix`. Property references like `${json-patch.version}` in dependency XML survive the escape into the POM correctly: Bazel converts `$$` → `$` in cmd strings, and `<<'EOF'` heredoc does not expand variables, leaving the literal `${json-patch.version}` in the output file.

No issues with the Starlark `.format()` calls: `{dependency_xml}` is a substitution placeholder in the format template; `${json-patch.version}` appears only in the substituted value, not in the template, so Starlark does not attempt to resolve it as a placeholder.
