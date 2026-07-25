# Bazel consolidation: one module for the monorepo

Status: proposed. Not started.
Owner: TBD.

## Summary

The repository builds as 19 independent Bazel modules, one per service. That
layout is inherited from the pre-monorepo history, when each service was its own
repository. Now that this repository is the development location, the layout has
no remaining justification and is the direct cause of several recurring
failures.

The proposal is to build the repository as a single Bazel module, with services
as packages governed by visibility and code ownership rather than by workspace
boundaries. Release granularity stays per service and does not change.

## Why the current layout exists

Each service arrived as a separate upstream repository and was imported with its
own `MODULE.bazel`, `.bazelrc`, `.bazelversion`, and lockfile. That reproduces a
multi-repository arrangement inside one repository. Bazel's unit of modularity
is the package with its visibility rules, not the workspace, so one module per
service works against the tool rather than with it.

Two constraints that used to force this arrangement are gone:

- The release backend previously required a service subtree to be its own Bazel
  module root. It now accepts a separate module root, so a service in the root
  module can stage a release.
- Development happened in several repositories. It now happens here.

## Evidence

Measured on the current tree.

Configuration is copied, not shared:

| Item | Count | Observed state |
|---|---|---|
| Subtree `.bazelrc` | 19 | 17 differ from each other by 0 to 4 lines |
| Subtree `.bazelversion` | 15 present, 4 missing | 8.6.0 in subtrees, 9.1.1 at root, one release build resolved 9.2.0 |
| `MODULE.bazel` | 21 | |
| `MODULE.bazel.lock` | 20 | |

The isolation that separate modules provide is not used. Shared rule versions
are identical everywhere:

| Dependency | Agreement |
|---|---|
| `rules_go` | 16 of 16 on 0.60.0 |
| `rules_oci` | 19 of 19 on 2.2.7 |
| `rules_pkg` | 19 of 19 on 1.2.0 |
| `aspect_bazel_lib` | 19 of 19 on 2.19.3 |
| `gazelle` | 16 of 16 on 0.48.0 |

The isolation is also actively harmful. Across 17 `go.mod` files there are more
than 20 version conflicts that nobody chose, for example `logrus` 1.9.3 against
1.9.4, `uuid` 1.3.0 against 1.6.0, and `go-retryablehttp` 0.6.6 against 0.7.8.
Separate dependency graphs did not deliver independent pinning. They delivered
silent skew.

Base images are fetched once per module rather than once per build:

| | |
|---|---|
| Registry fetches per full matrix | 18 |
| Distinct artifacts actually needed | 3 |

One base image digest is fetched 11 times in a single matrix run. No cache,
mirror, or download proxy changes that number, because the number is set by how
many independent builds run, not by where the bytes come from. The same
duplication means a single base image security bump is an 18 file change across
18 pinned digests.

Finally, CI scheduling is a hand-maintained approximation of the dependency
graph. The workflow carries a static list of subtrees plus path-prefix matching
to decide what to build. Correctness depends on a person keeping that list
accurate.

## Target layout

1. One Bazel module at the repository root. Services are packages. Ownership is
   expressed through directory ownership and Bazel visibility.
2. Release granularity is unchanged: per-service tags with per-service path
   prefixes. Module granularity and release granularity are separate concerns,
   and conflating them is what produced the current layout.
3. CI scheduling derives from the build graph with `bazel query rdeps`, instead
   of a static subtree list and path-prefix matching.
4. Environment configuration is injected by the environment. Cache endpoints,
   credentials, and the CI image come from CI variables, never from files
   committed per subtree.
5. One Bazel version, one set of rule versions, one lockfile per language.

## What consolidation buys

1. Shared configuration exists once. The recurring drift classes seen recently
   (a missing subtree `.bazelrc`, four subtrees with no pinned Bazel version, a
   decommissioned cache host referenced in nine files, a CI image pinned in four
   places at three different versions) become impossible by construction rather
   than caught by review.
2. One base image fetch per build instead of one per module, and 3 digest pins
   instead of 18.
3. CI scheduling that cannot silently under-build, because it is computed from
   the graph.
4. Shared dependencies compile once instead of once per module.
5. Atomic cross-service changes: a library change and every consumer land in one
   commit, verified by one build.
6. A simpler contract for external consumers. They already depend on labels of
   the form `@nvcf//<path>`; a single module means one thing to depend on rather
   than choosing among 19.

## Costs and risks

1. Merging the language dependency graphs is the real work: 17 Go modules into
   one graph, the Rust crate graph, and the already root-scoped Java graph. The
   Go conflicts are minor version skew, so most resolve by taking the newer
   version, but each needs a build and test.
2. A dependency bump becomes repository wide. Breakage surfaces immediately
   rather than never, which is the desired behavior, but it changes how such
   changes are staged and reviewed.
3. The analysis graph per invocation grows. Mitigate with the remote cache and
   with query-driven target selection rather than wildcard builds.
4. This is incremental work measured in months and must not be attempted as one
   change.

## Constraint: the parallel migration window

Development is mid-migration, and an external project still builds against this
repository by Bazel label during the parallel period:

```
@nvcf//src/control-plane-services/cloud-tasks/nvct-core:nvct_core
@nvcf//src/control-plane-services/cloud-tasks/nvct-core:nvct_core_test_fixtures
```

Those labels are a public interface until that project cuts over. Do not rename
or restrict the visibility of the cloud-tasks targets before then. Nothing else
in this plan is blocked by that constraint, because those targets already build
in the root module.

## Phases

Each phase lands independently and must leave `bazel build //...` and
`bazel test //...` green at the repository root.

### Phase 0: stop the bleeding

Add a check that rejects a new subtree introducing `MODULE.bazel`, `.bazelrc`,
or `.bazelversion`. New services join the root module. This holds the line while
the rest proceeds.

### Phase 1: converge the Bazel version

A prerequisite, and real work rather than a file edit. Sampling two subtrees on
the root's version showed two different failures: `sh_test` is no longer a
built-in global and needs an explicit load plus a `rules_shell` dependency, and
a protoc version mismatch against the protobuf module. Expect per-subtree fixes.

### Phase 2: pilot with the Go workers

Move the four worker subtrees into the root module. They are nearly identical
and have a small dependency surface, so they prove the Go dependency merge and
the release path end to end at low risk.

Deliverable: the four workers build, test, and release from the root module, and
their subtree `MODULE.bazel`, `.bazelrc`, and `.bazelversion` are removed.

### Phase 3: remaining Go services

Repeat, resolving dependency version conflicts as they surface. Record each
resolution so the choice is auditable.

### Phase 4: Rust

Merge the Rust subtrees into a single crate graph. This is the hardest phase and
should follow the Go work so the pattern is established.

### Phase 5: query-driven CI

Replace the static subtree list and path-prefix matching with `bazel query
rdeps` against the single graph.

### Phase 6: retire the inherited plumbing

Remove per-subtree configuration, the per-service cache probe scripts (which
exist to configure a cache that CI now injects through environment variables),
and any remaining per-subtree CI fragments.

## Adjacent cleanup, independent of consolidation

These share the same root cause and are worth doing regardless.

1. Publish the Bazel CI image from this repository. It is currently built
   elsewhere and mirrored by hand, and that indirection has already caused a
   release path to run a toolchain several versions behind what CI validates
   with. A workflow here that publishes to the container registry removes the
   manual step.
2. Add a base image freshness check. Pinned digests are correct and
   reproducible, but nothing reports when a newer base image is published, so a
   patched base can go unnoticed. This is a detection problem, not a caching
   problem.
3. Require a digest on every `oci.pull`. One pull currently uses a mutable tag,
   which is neither reproducible nor safe to cache.

## Explicit non-goals

- Do not split the Java tree into standalone modules. It is already in the
  target shape and its owners are satisfied with it. Any open tasks proposing
  that split should be closed rather than executed.
- Do not attempt a single large migration.
- Do not remove per-service release granularity. Releases stay per service.
- Do not move `nvsnap` into scope. Its base image is not publicly pullable and
  it is excluded from the public build for that reason.

## Open questions

1. Does any service genuinely need an independent dependency version today? The
   current data says no, but confirm with each owning team before removing the
   ability.
2. What is the correct single Bazel version? The root and the subtrees disagree,
   and converging is a behavior change for the subtrees.
3. Should the four worker subtrees be the pilot, or a single lower-traffic
   service? The workers are the most uniform, which is why they are proposed
   here, but they also release frequently.
