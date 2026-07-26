# Bazel consolidation: one root module for first-party code

Status: proposed. Not started.
Owner: TBD.

Measurements below were taken on main `1c29b4b7` (2026-07-25). They move.
Regenerate them with `tools/ci/bazel-consolidation-inventory` rather than
trusting this prose, and update the stamp above when they change. The first
draft of this document was invalidated within hours by an in-flight change that
pinned the worker Bazel versions.

## Summary

First-party code builds as 19 nested service modules plus the root. That layout
is inherited from the pre-monorepo history, when each service was its own
repository. The proposal is that the 18 publicly buildable service modules
converge on one root Bazel module, with services as packages, while releases and
ownership stay per component. `nvsnap` remains an explicit nested-module
exception until its private build dependency is resolved, so the end state is
one root module plus that documented exception, not literally zero nested
modules.

Scope boundary, and the most common misreading of this proposal: one Bazel
module does not mean one Go module, one Cargo workspace, or one lockfile per
language. It means one Bazel dependency graph. Component `go.mod`, `go.sum`, and
Cargo workspaces stay where Go and Cargo tooling, publishing, or ownership need
them. The target is one root `MODULE.bazel.lock` and one Bazel-resolved graph.

## Why this is a correctness change, not tidiness

Several services consume libraries that already live in this repository through
published Go pseudoversions mapped in their nested modules. A library and its
consumer can therefore change atomically in one commit while Bazel still tests
the consumer against the older published copy of the library. A single root
graph removes that class of false green.

The duplication is the secondary argument:

| Item | Count |
|---|---|
| Nested service modules | 19 |
| `MODULE.bazel.lock` files | 20, 6.3 MB total |
| Subtree `.bazelrc` | 19, of which 17 distinct |
| `workspace_status.sh` copies | 20, 7 variants |
| Copied `rules/oci` files | 120 files, 6944 lines |
| `oci.pull` declarations | 23 (18 from nvcr.io), 8 images, 7 digests |

The isolation that separate modules provide is largely unused: `rules_oci`,
`rules_pkg`, and `aspect_bazel_lib` are on identical versions in all 19.

Two important qualifications. The `rules/oci` and stamping copies contain real
behavioral differences, so they need a shared API with per-component
configuration, not mechanical deletion. And the 23 base-image pulls resolve to
8 distinct images; each collapses to one repository definition and one digest
pin, but not necessarily to one fetch or one compilation. That depends on how
many Bazel invocations CI runs and whether they share repository and action
caches with compatible action keys.

## Java is the proof, not the exception

`nv-boot-parent` and `cloud-tasks` already build in the root module while
retaining Maven and POM workflows, a root Maven dependency hub and lock,
Java-specific rules, component CI metadata, Testcontainers and Docker-host test
lanes, externally consumed labels, and independent releases.

The lesson is not to make Java, Go, and Rust identical. It is that one root
graph can host specialized rules, dependency hubs, test lanes, and release
metadata per component. Splitting Java into standalone modules is an explicit
non-goal.

## Primary risk: label rebasing and visibility

This is the highest-risk part of the migration and it is structural, not
mechanical. Removing a workspace root changes what its labels mean.

- Nested root-relative labels such as `//internal/...`, `//rules/oci`,
  `//platforms`, and `//:Cargo.lock` resolve differently once the module root
  moves.
- `//:__subpackages__` currently scopes to one service. Under the repository
  root it expands to the entire repository. There are 156 such declarations
  outside vendored code.
- There are 644 `//visibility:public` occurrences outside vendored code and
  zero `package_group` definitions. The expressed API surface is therefore very
  broad and uniform rather than absent: almost everything is public, and nothing
  declares a narrow boundary. Consolidation must inventory that surface before
  restricting it, because today's breadth is load-bearing for consumers.

Before any boundary is removed, define: default-private service packages,
explicit exported APIs, allowed cross-layer dependency groups, a query or aspect
check for forbidden cross-service edges, and root-run Gazelle regeneration with
an idempotence check.

## CI: extend the existing hybrid, do not rebuild it

An earlier draft of this document claimed CI scheduling is a hand-maintained
approximation of the dependency graph and proposed replacing it with
`bazel query rdeps`. That was wrong, and the correction matters because it
changes what work is left.

There are two layers:

1. Target selection inside a job already uses `rdeps`. `.github/workflows/bazel.yml`
   maps changed files to labels, queries reverse dependencies, and falls back to
   a full build for non-modify changes, global files, and file types it does not
   model. That is a deliberate hybrid and it should be kept.
2. Job selection across the matrix is static: which subtree lanes to spawn comes
   from a list in the workflow plus path-prefix matching, because 19 modules
   cannot be one query.

Consolidation does not add `rdeps`. It retires the static module-root list, but
it does not eliminate job or lane selection. Affected targets still have to be
partitioned by component metadata, target tags, and execution requirements:
Docker-host and Testcontainers lanes, Java component lanes, Cargo parity tests,
BYOO generation, release and artifact staging, and any retained `nvsnap` lane.
Root-scoped Java already demonstrates this, routing on the `component_kind` and
`ci_lane` fields of its component descriptors rather than on a module root.

The hybrid must be extended during every phase, not deferred to the end. `rdeps`
alone cannot safely handle added, deleted, renamed, generated, configuration,
container, or release files, nor anything not modeled in the graph. The
conservative fallback is a feature.

## Costs and risks

1. Merging language dependency graphs is the real work. Do not assume conflicts
   resolve by taking the newer version: Kubernetes dependency families span
   several versions with conflicting `replace` directives. Toolchains also
   differ, with first-party Go SDK versions from 1.25.0 through 1.25.11 and
   protobuf and Rust toolchains spanning major versions. The older 1.23.0 pin
   reported by a naive scan comes only from a vendored third-party module and is
   not in scope. Each decision needs an owner review and service tests.
2. A dependency bump becomes repository wide. Breakage surfaces immediately
   instead of never, which is the goal, but it changes how changes are staged.
3. The analysis graph per invocation grows. Mitigate with the remote cache and
   query-driven target selection rather than wildcard builds.
4. This is incremental work measured in months and must not be attempted as one
   change.

## Constraint: the parallel migration window

An external project builds against this repository by Bazel label during the
parallel migration period:

```
@nvcf//src/control-plane-services/cloud-tasks/nvct-core:nvct_core
@nvcf//src/control-plane-services/cloud-tasks/nvct-core:nvct_core_test_fixtures
```

Those labels are a public interface until it cuts over. Do not rename them or
restrict their visibility before then. Nothing else here is blocked by it,
because those targets already build in the root module.

## Per-component exit criteria

A component is done when all of the following hold. These are the migration's
definition of done; a phase is not complete until every component it touched
satisfies them.

- In-repository Go dependencies resolve to root-local `//src/...` labels, not to
  published `@com_github_nvidia_nvcf...` copies. This is the correctness goal, so
  it is the criterion that cannot be waived.
- The component's public labels are inventoried before visibility is restricted,
  and every external consumer of a removed label has been identified.
- Build, tests, OCI image equivalence, version stamping, and staged release
  behavior all pass.
- Tag prefixes, artifact names, and registry destinations are unchanged. A
  consolidation that silently renames a published artifact has failed, however
  green the build is.

The root-local-label criterion is scoped to components that have migrated. It is
not waived for first-party NVCF code that a component vendors. NVCA currently has
80 BUILD files referring to vendored copies of NVCF Go libraries, and that is
precisely the false-green condition this proposal exists to remove. A component
may be excepted from moving; it may not be excepted from the correctness goal
while still claiming to be done.

## Exception contract

An exception is a decision, not a deferral, and it has to be written down as one.
Every nested module retained past Phase 8 records:

- Owner, and the rationale for staying nested.
- The retained module and its CI entry point.
- How the component sources first-party dependencies, and whether it can still
  test against a stale published or vendored copy of an in-repository library.
- Residual correctness risk, stated plainly.
- A dated revisit or removal trigger.

An entry missing any of these is not an exception; it is unfinished work. The
current expected entries are `nvsnap`, a deliberate helper module, and a vendored
module. NVCA, ESS, or BYOO join only if their subphase concludes so, and the
summary count is updated when that happens.

## Phases

Each phase lands independently and must leave every declared CI lane green, not
only `bazel build //...` and `bazel test //...` at the repository root. Lanes
that route by component metadata or execution requirements, such as the
Docker-host and Java component lanes, count toward that invariant.

1. Correct the inventory, remove stale Bazel documentation, and define the
   intentional nested-module exceptions. Not every nested module is in scope:
   `nvsnap`, a deliberate helper module, and a vendored module all stay nested,
   so the Phase 2 guard needs an allowlist rather than a blanket ban.
2. Establish visibility policy, root-run Gazelle enforcement, the nested-module
   guard, and the extended hybrid affected-target CI.
3. Converge Bazel and shared toolchain versions. This is real work: sampling two
   subtrees on the root Bazel version produced two different failures, one from
   `sh_test` no longer being a built-in global and one from a protoc and
   protobuf mismatch.
4. Centralize the OCI, platform, and stamping contracts behind one API covering
   Go, Rust, multi-binary images, extra layers, environment, entrypoints,
   platforms, and registry outputs. Gate it on equivalence: compare image
   manifests and configuration before and after. Define how per-service versions
   are stamped, given that one workspace-status command runs once per Bazel
   invocation while releases stay per service.
5. Migrate the four worker services. They are the most uniform and prove the Go
   dependency merge and the release path end to end.
6. Migrate the remaining ordinary Go services. The three special cases are
   scheduled explicitly rather than deferred, because Phase 8 cannot complete
   while any of them is unresolved:
   - 6a. NVCA. 1261 vendored BUILD files and direct `//vendor/...` dependencies
     whose labels will not survive a root move. Decide between rebasing the
     vendored labels and dropping the vendored tree in favor of module
     resolution before any code moves.
   - 6b. ESS. Nested Go workspace; needs the workspace collapsed or an explicit
     exception recorded.
   - 6c. BYOO. The collector already builds through a declared Bazel genrule.
     The problem is that the action uses host Go with `local` and `no-sandbox`,
     so it is neither hermetic nor remotely cacheable. Make the action hermetic,
     retain its dedicated local lane, or record an exception.
   Each subphase carries its own owner and exit criteria. If one is judged not
   worth doing, it moves to the exception allowlist under the contract below
   rather than staying open. Moving a component there changes the end state:
   the summary's count of converging modules drops, and that component keeps its
   own lockfile and module-root CI lane.
7. Move the Rust trees into the root Bazel module while initially retaining
   their Cargo workspaces, lockfiles, and separate crate hub names. One Bazel
   module can host several crate hubs. Merging them into one Cargo graph is a
   later optimization needing its own justification. Add and retain
   `cargo test --workspace --all-targets` until Bazel declares every Rust
   integration test that Cargo currently discovers. GitHub CI has no Cargo lane
   today, so this is new work rather than something already in place.
8. Retire nested configuration and the static module-root list once graph
   coverage is complete, leaving the allowlisted exceptions in place. Lane
   routing by component metadata and execution requirements stays.

## Adjacent cleanup, independent of consolidation

1. Publish the Bazel CI image from this repository. It is currently built
   elsewhere and mirrored by hand, and that indirection already caused a release
   path to run a toolchain several versions behind what CI validates with.
2. Add a base image freshness check. Pinned digests are correct and
   reproducible, but nothing reports when a newer base is published, so a
   patched base can go unnoticed. This is detection, not caching.
3. Require a digest on every `oci.pull`. One pull uses a mutable tag, which is
   neither reproducible nor safe to cache.

## Explicit non-goals

- Splitting the Java tree into standalone modules. It is already in the target
  shape. Open tasks proposing that split should be closed rather than executed.
- One Go module, one Cargo workspace, or one lockfile per language.
- Merging the Rust crate hubs as part of this work.
- A single large migration.
- Removing per-service release granularity.
- Moving `nvsnap` into scope. Its base image is not publicly pullable and it is
  excluded from the public build for that reason.

## Open questions

1. Does any service genuinely need an independent dependency version today? The
   data says no, but confirm with each owning team before removing the ability.
2. What is the correct single Bazel version, given the subtrees and the root
   disagree and converging is a behavior change for the subtrees.
3. Should the workers be the pilot? They are the most uniform, which is why they
   are proposed, but they also release frequently.
