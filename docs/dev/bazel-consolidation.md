# Bazel consolidation: one root module for first-party code

Status: proposed. Not started.
Owner: TBD.

Measurements below were taken on main `1f8ea711` (2026-07-26). They move.
Re-derive them before relying on them, and update the stamp above when they
change. The first draft of this document was invalidated within hours by an
in-flight change that pinned the worker Bazel versions, and again during review
when the notary import landed, so treat any number here as a snapshot.

All commands below are run from the stamped commit, not from a branch. Note
which ones exclude vendored trees and which do not: the module total is
deliberately inclusive, because the point of that row is the total number of
Bazel modules in the repository.

```sh
# 22 modules total, vendored included; 19 first-party service modules
git ls-files | grep -E '(^|/)MODULE\.bazel$' | wc -l
git ls-files | grep -v /vendor/ | grep -E '^src/.*/MODULE\.bazel$' | wc -l

# 20 lockfiles; 19 subtree .bazelrc of which 17 distinct
git ls-files | grep -E '(^|/)MODULE\.bazel\.lock$' | wc -l
git ls-files | grep -v /vendor/ | grep -E '^src/.*/\.bazelrc$' | wc -l
git ls-files | grep -v /vendor/ | grep -E '^src/.*/\.bazelrc$' \
  | xargs md5sum | awk '{print $1}' | sort -u | wc -l

# 15 on 8.6.0, 4 on 9.1.1
git ls-files | grep -v /vendor/ | grep -E '^src/.*/\.bazelversion$' \
  | xargs cat | sort | uniq -c

# 20 workspace_status.sh in 7 variants; 120 copied rules/oci files
git ls-files | grep -E '(^|/)workspace_status\.sh$' | wc -l
git ls-files | grep -E '(^|/)workspace_status\.sh$' \
  | xargs md5sum | awk '{print $1}' | sort -u | wc -l
git ls-files | grep -v /vendor/ | grep -E '(^|/)rules/oci/' | wc -l

# 647 public, 156 __subpackages__, 1 package_group: first-party BUILD files
for pat in '//visibility:public' '//:__subpackages__' 'package_group('; do
  git ls-files | grep -v /vendor/ | grep -E '(^|/)BUILD\.bazel$' \
    | xargs grep -c -F "$pat" | awk -F: -v p="$pat" '{n+=$2} END {print p, n}'
done

# 1261 vendored BUILD files under nvca
git ls-files | grep -E '^src/compute-plane-services/nvca/vendor/.*/BUILD\.bazel$' | wc -l
```

Two figures are not reduced to a command here, because they need the contents of
`MODULE.bazel` to be parsed rather than matched: the `oci.pull` breakdown and the
Go SDK version range. Both were derived by reading the `oci.pull` and
`go_sdk.download` declarations across the tracked `MODULE.bazel` files, and
should be treated as reported rather than reproduced. Check them by hand before
relying on either.

## Summary

First-party code builds as 19 nested service modules plus the root. That layout
is inherited from the pre-monorepo history, when each service was its own
repository. The proposal is that all 19 first-party service modules converge on
one root Bazel module, with services as packages, while releases and ownership
stay per component.

An earlier draft carried `nvsnap` as a standing exception on the grounds that its
base image is not publicly pullable. That rationale is stale. `.github/workflows/bazel.yml`
still records it, but its current Bazel targets disagree: `nvsnap` pins public
`gcr.io/distroless/static`, `docker.io/library/ubuntu`, and `docker.io/library/alpine`
bases by digest, builds both `go_oci_image` targets on `@distroless_static`, and
documents a public-input-only source build. Private NGC is the publish destination
of those targets, not their base.

The qualification matters: this is a statement about the Bazel targets, not about
every build path in the directory. The legacy `docker/agent/Dockerfile.app`
defaults to a private `nvsnap-agent-base` image and to private uvloop, libuv, and
libzmq builder images. The criuv2 variant is not the offender; it documents public
inputs. Those Dockerfile paths are not what consolidation moves, but they are the
likely origin of the original claim and have to be retired or excluded explicitly
rather than quietly. Phase 1 therefore has to
resolve `nvsnap` rather than assume it: migrate it with the rest, or record a real
reason under the exception contract. Its absence from the public CI matrix is a
row decision, not an architectural one.

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

`rules_oci`, `rules_pkg`, and `aspect_bazel_lib` are on identical versions in all
19 modules, so for those three the isolation a separate module provides is not
being used. That is a narrow observation and should not be read as a general one:
the language dependency graphs do diverge, as the costs section below records for
Kubernetes families, protobuf, and Rust toolchains. The claim is that nothing is
gained by isolating these particular rule sets, not that isolation is unused.

Two important qualifications. The `rules/oci` and stamping copies contain real
behavioral differences, so they need a shared API with per-component
configuration, not mechanical deletion. And the 23 base-image pulls resolve to
8 distinct images; each collapses to one repository definition and one digest
pin, but not necessarily to one fetch or one compilation. That depends on how
many Bazel invocations CI runs and whether they share repository and action
caches with compatible action keys.

## Java is the proof, not the exception

`nv-boot-parent` and `cloud-tasks` already build in the root module. Being
specific about what is shared and what is not, because this is the concrete model
the rest of the plan generalizes:

Shared, at the root: one `rules_jvm_external` hub and one pinned
`//:maven_install.json` for the Bazel build, plus Java-specific rules and the
root Bazel graph.

Retained, per component: separate Maven reactors, each with its own multi-module
`pom.xml` tree and BOM, and their own Maven-side validation, publication, and
release cadence. `nv-boot-parent` publishes a BOM and starters that
`cloud-tasks` consumes as ordinary Maven artifacts.

Also retained per component: CI metadata, Testcontainers and Docker-host test
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
- There are 647 `//visibility:public` occurrences outside vendored code and
  one `package_group` definition. The expressed API surface is therefore very
  broad and nearly uniform: almost everything is public, and exactly one place
  declares a narrow boundary. Consolidation must inventory that surface before
  restricting it, because today's breadth is load-bearing for consumers.

  That single `package_group` arrived with the notary import, which replaced two
  root-relative `__subpackages__` visibility entries with a named group. It is
  the pattern this phase generalizes, and it is worth noting that it appeared
  without this plan: the need is being felt independently.

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

1. Target selection already uses `rdeps`, but only for the root row on a pull
   request. `.github/workflows/bazel.yml` maps changed files to labels, queries
   reverse dependencies, and falls back to a full build for non-modify changes,
   global files, and file types it does not model. Every other row, and every
   non-pull-request trigger, builds its full declared scope by design. For a
   nested module that is its whole workspace; a root-scoped Java row already
   shares the root graph and builds that component's subtree of it. Sharing the
   graph is therefore not by itself what enables narrowing: only the root row
   performs narrowing today, and extending it to the other rows is work that
   consolidation makes possible rather than work it does automatically.
   That is a deliberate hybrid and it should be kept.

   This is also an argument for consolidation rather than against it. Narrowing
   needs a graph that spans the changed files, and today only the root module
   provides one for the nested services. Root-scoped Java components already sit
   in that graph, which is why extending narrowing to them is a workflow change
   rather than a restructuring; the nested services have to move first.
2. Job selection across the matrix is static: which subtree lanes to spawn comes
   from a list in the workflow plus path-prefix matching, because 19 modules
   cannot be one query.

Consolidation does not add `rdeps`. It retires the static module-root list, but
it does not eliminate job or lane selection. Affected targets still have to be
partitioned by component metadata, target tags, and execution requirements:
Docker-host and Testcontainers lanes, Java component lanes, Cargo parity tests,
BYOO generation, and release and artifact staging.
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
   protobuf spanning major versions and Rust toolchains spanning releases from
   1.91.1 to 1.97.0. The older 1.23.0 pin
   reported by a naive scan comes only from a vendored third-party module and is
   not in scope. Each decision needs an owner review and service tests.
2. A dependency bump becomes repository-wide. Breakage surfaces immediately
   instead of never, which is the goal, but it changes how changes are staged.
3. The analysis graph per invocation grows. Query-driven target selection is the
   mitigation; remote caching is not. Caching avoids re-executing actions, but
   the analysis phase still loads and analyzes whatever the invocation asks for,
   so the fix is asking for less rather than caching more.
4. This is incremental work measured in months and must not be attempted as one
   change.

## Constraint: the parallel migration window

An external project builds against this repository by Bazel label during the
parallel migration period:

```text
@nvcf//src/control-plane-services/cloud-tasks/nvct-core:nvct_core
@nvcf//src/control-plane-services/cloud-tasks/nvct-core:nvct_core_test_fixtures
```

Those labels are a public interface until it cuts over. Do not rename them or
restrict their visibility before then. Nothing else here is blocked by it,
because those targets already build in the root module.

## Per-component exit criteria

A migrated component is done when all of the following hold. These are the
migration criteria, not the only way to close a phase: a phase is complete when
every component it touched satisfies either these criteria or the exception
contract. The two closure paths are set out below.

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

### Two closure paths

A component closes its subphase one of two ways, and the two must not be
conflated. Saying every touched component satisfies the migration criteria, while
also permitting a component to finish by staying nested, is a contradiction.

| | Migrated component | Retained service exception |
|---|---|---|
| Satisfies | The criteria above | The exception contract below |
| Bazel module | Root | Its own, retained |
| CI | Root graph, routed by lane | Its own module-root lane |
| Counted in | The migrated total | Explicitly outside it |
| Records | Nothing extra | Residual correctness risk, in writing |

A retained exception is a legitimate outcome. It is not a quiet one: it lowers the
migrated count, keeps a lockfile and a module-root lane alive, and leaves a stated
correctness risk on the record. The final phase completes when every component has taken
exactly one of these two paths.

### The non-waivable invariant

The two closure paths above resolve process, not correctness, and stating them
without this leaves a contradiction: a retained exception cannot both close the
initiative and be exempt from the reason the initiative exists.

So one invariant is not waivable by either path:

> Every first-party dependency resolves from the current checkout.

A component satisfies this when the in-repository libraries it builds against are
the ones in the tree being built, rather than a published or vendored copy of an
earlier state. That is the entire correctness argument: it is what stops a
library and its consumer changing together in one commit while Bazel tests the
consumer against the older copy.

Joining the root module is the ordinary way to satisfy it, and is what makes it
automatic. It is not the only way. A retained exception may satisfy it by other
means, for example a path-based or workspace-local override that resolves those
dependencies to the working tree, and if it does so it is a legitimate end state
rather than a deferral. What it may not do is record the risk and move on.

Concretely, this is the criterion that a retained exception's contract has to
demonstrate, not merely acknowledge. NVCA has dozens of BUILD files referring to
vendored copies of NVCF Go libraries; that is the condition to remove, whether by
migrating or by resolving those labels to the checkout.

The root-local-label criterion, by contrast, is scoped to components that have
migrated: it is how a migrated component satisfies the invariant, not an
independent requirement. NVCA has dozens of BUILD
files referring to vendored copies of NVCF Go libraries, and that is precisely
the false-green condition this proposal exists to remove. The exact figure is
deliberately not quoted here: plausible definitions of the metric disagree, and
it depends on choices a reader cannot see, so a number here would be
unverifiable in a document whose point is that quoted numbers must be
reproducible. A component
may be excepted from moving; it may not be excepted from the correctness goal
while still claiming to be done.

## Exception contract

An exception is a decision, not a deferral, and it has to be written down as one.
Every retained first-party service module records:

- Owner, and the rationale for staying nested.
- The retained module and its CI entry point.
- How the component sources first-party dependencies, and whether it can still
  test against a stale published or vendored copy of an in-repository library.
- Residual correctness risk, stated plainly.
- A dated revisit or removal trigger.

An entry missing any of these is not an exception; it is unfinished work.

### The guard needs three mechanisms, not one allowlist

Requiring every allowlist entry to carry a permanent exception contract from
Phase 2 onward is incompatible with incremental migration: during the migration
most nested modules are simply not migrated yet, which is a schedule fact rather
than an architectural decision. The guard therefore distinguishes three things:

1. Vendored-path exclusions. Exact paths, not patterns. A vendored third-party
   module is outside the guard's remit entirely and needs no justification.
2. A migration and retirement ledger. Every nested module that still exists but
   is scheduled to go away, with its target phase and whether it goes by
   migration or by removal. `rules/oci-destinations` is a retirement entry, not a
   migration one: what disappears is its standalone module boundary, not its
   contents. Its package becomes root-owned unless the macro is rehomed into the
   shared OCI API from the OCI and stamping phase, which is the preferable
   outcome and should be decided there rather than by default. The ledger must shrink monotonically; a
   phase that adds an entry, or leaves one without a target phase, fails. This is
   the mechanism that makes incremental migration expressible without pretending
   every unmigrated module is a considered exception.
3. Permanent service exceptions. Only these carry the exception contract above,
   and only these survive the final phase.

Conflating 2 and 3 is what makes an allowlist rot: it cannot distinguish "not yet"
from "never", so nothing ever forces the first to resolve.

The 22 tracked `MODULE.bazel` files are three different things, and conflating
them obscures the end state:

| Category | Count | End state |
|---|---|---|
| First-party service modules | 19 | Converge on the root |
| Root module | 1 | The destination |
| Migration scaffolding (`rules/oci-destinations`) | 1 | Removable once its five consumers migrate |
| Vendored third-party (`cel.dev/expr`) | 1 | Never in scope; excluded from the guard |

Only the first category can hold an architectural exception. `rules/oci-destinations`
is scaffolding that five nested modules currently depend on, so it is retired by
the final phase rather than excepted. A vendored third-party module is a guard exclusion,
not a decision anyone has to justify.

So: migrated services satisfy the root-label criteria; approved service exceptions
satisfy the exception contract above; vendored third-party modules are excluded
from the guard entirely. NVCA, ESS, or BYOO join the second group only if their
subphase concludes so, and the summary is updated when that happens.

## Phases

Each phase lands independently and must leave every declared CI lane green, not
only `bazel build //...` and `bazel test //...` at the repository root. Lanes
that route by component metadata or execution requirements, such as the
Docker-host and Java component lanes, count toward that invariant.

1. Correct the inventory, remove stale Bazel documentation, and settle the
   exception question. Three of the 22 tracked modules are not first-party services
   and are handled by category rather than by exception: `rules/oci-destinations`
   is migration scaffolding retired once its five consumers migrate, and the
   vendored `cel.dev/expr` module is excluded from the guard outright. `nvsnap`
   is the only open question, and its recorded rationale contradicts its code.
   This phase must classify it, and classification is not the same as resolving
   it: `nvsnap` cannot migrate before the toolchain convergence and the shared
   OCI and stamping API exist, because it builds two `go_oci_image` targets. So
   Phase 1 puts it in one of exactly two states, and neither is "undecided":

   Phase 1 resolved this: `nvsnap` migrates, and it goes first. Its Bazel targets
   pin public bases by digest, so the stale private-base rationale does not hold,
   and nothing in the repository consumes it, so it is the safest place to
   exercise the migration mechanics. It is neither a permanent exception nor a
   deferred ledger entry; see Phase 5a. The Phase 2 guard is not a
   single allowlist: it separates vendored-path exclusions, a migration and retirement
   ledger of modules not yet resolved, and permanent service exceptions. Only the third
   category carries the exception contract. Requiring one for every entry would
   make the guard unusable during the migration itself.
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
5. Pilot in two steps, chosen on evidence gathered in Phase 1 rather than on
   uniformity.

   - 5a. `nvsnap`, to prove the mechanics. Nothing in the repository consumes it:
     the only references outside its own directory are the exclusion comment in
     the Bazel workflow and a NOTICE attribution. It has no release wiring and is
     not a CI matrix row. A mistake therefore costs nothing, and it can be
     reshaped to fit rather than accommodated. This is where label rebasing, the
     root-module move, and the OCI and stamping contracts get exercised for the
     first time.
   - 5b. `image-credential-helper`, to prove the invariant. It is the live case:
     it pins a May pseudoversion of `src/libraries/go/lib`, 33 library commits
     behind, and also vendors the library with five source files now differing
     from the tree. It builds green against library source that no longer exists
     here. Migrating it is what demonstrates the invariant is enforceable on a
     real service rather than on a convenient one.

   The four worker services follow. They were the original pilot because they are
   uniform, but they are only 8 commits stale, so they would have proved the easy
   case first.
6. Migrate the remaining ordinary Go services. The three special cases are
   scheduled explicitly rather than deferred, because the final phase cannot complete
   while any of them is unresolved:
   - 6a. NVCA. 1261 vendored BUILD files and direct `//vendor/...` dependencies
     whose labels will not survive a root move. Decide between rebasing the
     vendored labels and dropping the vendored tree in favor of module
     resolution before any code moves.
   - 6b. ESS. Retains its Go workspace while the root Bazel graph ingests its
     modules, consistent with the scope boundary above: one Bazel module does not
     mean one Go workspace. The work is teaching the root graph to build those
     modules, not collapsing the workspace.
   - 6c. BYOO. The collector already builds through a declared Bazel genrule.
     The problem is that the action uses host Go with `local` and `no-sandbox`,
     so it is neither hermetic nor remotely cacheable. Make the action hermetic,
     retain its dedicated local lane, or record an exception.
   Each subphase carries its own owner and exit criteria. If one is judged not
   worth doing, it moves to the exception list under the contract above
   rather than staying open. Moving a component there changes the end state:
   the summary's count of converging modules drops, and that component keeps its
   own lockfile and module-root CI lane.
7. Establish source-of-truth ownership before moving the Rust trees. Any subtree
   still synchronized from an upstream repository can have a later import restore
   its nested module and its old labels, silently undoing the migration.
   `stargate` is in this position today. Each affected subtree needs either a
   native ownership cutover, or compatible Bazel changes landed upstream and then
   synchronized, before its labels are rebased. This is a prerequisite, not a
   cleanup step.
8. Move the Rust trees into the root Bazel module while initially retaining
   their Cargo workspaces, lockfiles, and separate crate hub names. One Bazel
   module can host several crate hubs. Merging them into one Cargo graph is a
   later optimization needing its own justification. Add and retain
   `cargo test --workspace --all-targets` until Bazel declares every Rust
   integration test that Cargo currently discovers. GitHub CI has no Cargo lane
   today, so this is new work rather than something already in place.
9. Retire nested configuration and the static module-root list once graph
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
- Keeping `nvsnap` permanently out of scope on the current stated rationale.
  That rationale no longer matches the code and Phase 1 must re-decide it.

## Open questions

1. Does any service genuinely need an independent dependency version today? The
   evidence gathered so far does not show one, but that evidence is thin: it
   covers three rule sets on identical versions, not the language dependency
   graphs, which do diverge. Confirm with each owning team before removing the
   ability rather than treating the absence of a known case as an answer.
2. What is the correct single Bazel version, given the subtrees and the root
   disagree and converging is a behavior change for the subtrees.
3. Should the workers be the pilot? They are the most uniform, which is why they
   are proposed, but they also release frequently.
