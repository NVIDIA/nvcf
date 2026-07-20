# Phase 3 Bazel Migration — Design Review

Reviewed by: Claude (claude-sonnet-4-6)
Date: 2026-07-14
Scope: Maven-to-Bazel coexistence design for `nv-boot-parent`

---

## 1. MODULE.bazel as Version Source of Truth

**Verdict: Correct choice under Bzlmod, but with one architectural tension.**

Under Bzlmod, MODULE.bazel is already the authoritative input to Coursier resolution. A parallel version properties file would create a drift surface — you'd have to remember to keep both in sync on every bump. The design correctly eliminates that risk.

The one tension: MODULE.bazel serves two audiences with different requirements. Bazel needs every transitive dep that appears in `BUILD` files; the publication layer only needs root-level published coordinates. Right now these audiences share the same list, which means publication metadata is potentially polluted by build-only deps (test tools, `junit-platform-console-standalone`, `wiremock-standalone`, etc.). This doesn't cause a correctness bug today because `maven_publish_properties.json` is the whitelist that controls what actually gets publication properties — but the coupling makes the design more fragile to intent drift.

**Improvement for next project:** Add a comment partition or a second `publication_artifacts` array in MODULE.bazel so the publication-relevant entries are explicitly called out, rather than being an implicit subset.

---

## 2. Hidden Risks in Parsing MODULE.bazel from Python

**There is a real, latent bug: the parser does not handle comments.**

In `update_maven_deps.py`, `extract_module_array` (line 44-62) does not skip lines starting with `#`. The current MODULE.bazel contains commented-out example entries inside the `artifacts = [` array:

```python
# "org.apache.tomcat.embed:tomcat-embed-core:<fixed-version>",
# "org.apache.tomcat.embed:tomcat-embed-el:<fixed-version>",
# "org.apache.tomcat.embed:tomcat-embed-websocket:<fixed-version>",
```

`COORDINATE_PATTERN = re.compile(r'"([^"]+)"')` matches quoted strings anywhere on the line, including after `#`. Each of those three lines would be extracted as a coordinate with version `<fixed-version>`. Currently harmless because none appear in `maven_publish_properties.json`, but:

- If anyone adds a real `tomcat-embed-core` pin while the comment block remains, duplicate detection fires with a confusing error referencing a phantom coordinate.
- If a future commented example uses a coordinate that *does* appear in publish properties, `generated_bzl()` would silently emit wrong versions.

**Fix:** In `extract_module_array`, skip lines where the stripped form starts with `#`:

```python
if stripped.startswith("#"):
    continue
```

**Second risk: exact-match entry point for the array.** The parser checks `stripped == f"{array_name} = ["` exactly (line 49). Any of these break it silently (producing empty results then a ValueError):

- Trailing space or comment on that line
- Bazel auto-formatter emitting the opening bracket differently
- Future Bzlmod syntax changes

The `if not entries: raise ValueError` guard catches total failure, but not partial reads. Consider `stripped.startswith(f"{array_name} = [")` to at minimum tolerate trailing comments on the opening line.

**Third risk:** The `<<'EOF'` heredoc approach in `nv_boot_publish_pom` bakes the entire POM XML into the genrule `cmd` string (`maven.bzl:167-174`). Shell limits are rarely hit for POMs, but the full content becomes part of Bazel's action cache key. More importantly, property references like `${json-patch.version}` must be double-escaped via `_genrule_cmd_escape`. This is applied correctly (`maven.bzl:168`), but it's easy to miss on future edits — any new string interpolation into the POM template must go through that escape.

---

## 3. Is the managed\_dep / pinned\_dep Split Reasonable?

**Yes — the design is sound and the enforcement is correct.**

- `managed_dep()` emits `group:artifact` (no version), relying on the parent BOM chain for resolution. Correct for the Spring ecosystem.
- `pinned_dep()` emits `group:artifact:${prop.version}` and fails at load time if the coordinate is not in `MAVEN_PUBLISH_ARTIFACTS`. This `fail()` at load time (`publication_deps.bzl:23`) is the right enforcement point — it prevents silent version omission.

One subtle point: `pinned_dep()` causes the generated POM to include a `<properties>` block with the concrete version hardcoded (via `_properties_xml`). That version comes from the generated `.bzl` file. So version updates for pinned deps require: update MODULE.bazel → rerun `update_maven_deps.py --write` → publish new POM. This is correct but the three-step nature should be documented in a contributor checklist.

**One gap:** `_coordinate()` in `publication_deps.bzl` produces a colon-delimited string with `optional` as the fifth field. The parsing side in `_dependency_xml` (`maven.bzl:14-34`) handles up to 5 parts. `managed_dep(optional=True)` produces `group:artifact::compile:true` (empty version field). The parser handles this with `scope = parts[3] if len(parts) >= 4 and parts[3] else "compile"` — the `and parts[3]` guard correctly handles the empty version slot. This is correct but fragile; the contract is implicit. Document this invariant or cover it with a test.

---

## 4. Maven Consumer Compatibility

**Structurally sound; two content errors to fix before publishing.**

The POM structure is correct for consumer compatibility:

- `<parent>` pointing to `nv-boot-parent` enables BOM version inheritance — consumers don't need explicit versions for Spring-managed deps
- `<properties>` block correctly materializes pinned versions where needed
- Absence of `<dependencyManagement>` in module POMs is intentional and correct — parent chain provides it
- `<relativePath />` prevents spurious local parent lookups

**Content bugs in `nv_boot_publish_pom` (`maven.bzl:125-141`):**

```xml
<url>https://spring.io/projects/spring-boot/nv-boot-parent/{artifact_id}</url>
<developers>
  <developer>
    <name>Spring</name>
    <email>ask@spring.io</email>
    <organization>VMware, Inc.</organization>
```

These are Spring's metadata, not NVIDIA's. They won't break Maven resolution, but Maven Central validation would reject them, and they expose the copy-paste origin if this goes public. These fields should be parameterized in `nv_boot_publish_pom` or replaced with NVIDIA values.

**Non-standard file in jar:** `nv_boot_maven_artifact` writes `maven.properties` at the jar root (`maven.bzl:305-311`) in addition to the standard `META-INF/maven/.../pom.properties`. Maven's own tooling only reads `META-INF/maven/...`; the root-level `maven.properties` is not a Maven convention. This is harmless but unexpected — remove it unless there is a known consumer dependency on it.

---

## 5. What to Change Before Applying to Another Maven Parent Project

In priority order:

### P0 — Correctness

**Fix the comment parser bug** (`update_maven_deps.py:44-62`): add `if stripped.startswith("#"): continue` before the regex match. This is a correctness fix, not polish. The current Tomcat security override comment block inside `artifacts = [` already exercises this bug path; it is silently tolerated only because no commented coordinate appears in `maven_publish_properties.json`.

### P1 — CI Enforcement

**Add a `--check` Bazel test target** that runs `update_maven_deps.py --check` as part of `bazel test //...`. Without CI enforcement, the generated file can silently drift when MODULE.bazel is updated. The check mode exists but is only run manually.

### P2 — Reusability

**Parameterize POM metadata** in `nv_boot_publish_pom`: `<url>`, `<developers>`, `<organization>`, and `<scm>` should be arguments (with defaults), not hardcoded Spring/NVIDIA values. A second project will have different SCM URLs and developer metadata. Making them parameters is what converts this macro from a project-specific helper into a reusable library.

### P3 — Robustness

**Make the array header match more tolerant**: change `stripped == f"{array_name} = ["` to `stripped.startswith(f"{array_name} = [")` in `extract_module_array`. This tolerates trailing comments or whitespace on the opening bracket without silently producing empty output.

### P4 — Documentation

**Document the three-step pinned-dep update workflow** in a contributor guide or CLAUDE.md: `MODULE.bazel` → `update_maven_deps.py --write` → publish. Without documentation, contributors will update MODULE.bazel and not regenerate, and the error from `--check` mode will be cryptic.

### P5 — Cleanliness

**Remove `maven.properties` from the jar root** (`maven.bzl:305-311`) unless there is a specific consumer that reads it. It adds noise to every published jar and is not a Maven convention.
