# Release Process

NVCF is a monorepo. Each subproject under `src/`, `deploy/stacks/`, and
`migrations/` (services, CLIs, Helm stacks, migration sets) is versioned and
released independently. There is no single repo-wide release; releases happen
per subproject, driven by commits that touch that subproject's path.

The automation that implements this lives in
[`.github/workflows/release-tags.yml`](.github/workflows/release-tags.yml) and
[`tools/ci/github-release`](tools/ci/github-release). Read those files for the
authoritative behavior; this document summarizes it for contributors.

## Release Cadence

Releases are commit-triggered, not calendar-triggered. There is no fixed
weekly or monthly cadence. On every push to `main`, the `service-release` job
runs `./tools/ci/github-release auto`. Almost every subproject registered in
[`tools/ci/github-release-subprojects.json`](tools/ci/github-release-subprojects.json)
uses the same model: semantic-release walks the commits since the
subproject's last release tag and decides whether to cut a new version. The
three `deploy/stacks/` subprojects are the exception and are covered below.

- Commits typed `feat`, `fix`, or `perf` (the "customer" commit types defined
  in [`CONTRIBUTING.md`](CONTRIBUTING.md#how-to-select-a-commit-type)) trigger
  a new stable release tag for the subproject they touch.
- Commits typed `docs`, `build`, `test`, `refactor`, `ci`, `chore`, `style`,
  or `revert` (the "foundational" types) do not trigger a release on their
  own.
- A commit whose type carries a `!` breaking marker triggers a release
  regardless of type.

Only commits that touch a subproject's own path count toward its version.
`semantic-release-monorepo` scopes the commit analysis to the subtree, so a
`fix(grpc-proxy):` commit cannot release `nvcf-cli` and vice versa.

Release notes are generated from commit messages (semantic-release
conventions) and attached to the GitHub Release for each tag.

### The Helm stacks release from a branch

The three Helm stacks under `deploy/stacks/` are the exception. A stack is a
pinned composition of charts, so its version is a claim about a set somebody
qualified together, not about the last commit that touched the tree. They are
registered with `version_file` and `release_branch_only`. A merge to `main`
releases no stack version at all.

- A `VERSION` file in the stack directory names the train that the next branch
  cut will open, for example `deploy/stacks/self-managed/VERSION`.
- Cutting that train is a manual `workflow_dispatch` on `release-tags` with
  `operation=branch-cut` and the stack's service id. It creates
  `release-deploy/stacks/<stack>/vMAJOR.MINOR` holding the default branch's
  content, and opens a pull request advancing `VERSION` to the next minor on
  `main`.
- Every push to that branch then releases: `MAJOR.MINOR.0` first, and the next
  patch for each push after it. Backporting a fix is what ships a stack patch.
- To build an unreleased tree, whether a pull request or `main` between branch
  cuts, dispatch `operation=release-candidate` with the stack's service id,
  selecting the branch you want built. It cuts `MAJOR.MINOR.PATCH-rc.N` at that ref. The
  internal publish lanes accept `-rc.N`, so the stack is packaged and published
  without a version landing on its stable line.

`nvca` used the same model until the 3.3 line and now releases from `main`
like every other subproject. Nothing publishes a `-dev.N` any more, and none
of the ones published under the older form of this model are still in the
repository; every remaining stack and nvca tag is a stable version.

## Branch Naming

- `main`: the active development branch. All pull requests target `main`,
  except hotfixes (see [`CONTRIBUTING.md`](CONTRIBUTING.md#step-2-create-a-branch)).
- `release-<service-path>/vMAJOR.MINOR`: a maintenance branch for one
  subproject's release train. These branches always run the build, test, lint,
  and scan workflows. Whether a push to one also cuts a tag depends on the
  subproject: for the `deploy/stacks/` subprojects it does, because that is
  where they release from; for everything else release automation runs on
  `main` only and a maintenance tag is cut by hand.

Real examples from this repository:

- `release-src/compute-plane-services/nvca/v3.7`
- `release-deploy/stacks/self-managed/v0.7`

The separator between the service id and the version can vary by how a
subproject registers its release metadata; for example
`release-nvcf-cassandra-migrations-v0.10` uses a flattened id instead of a
path segment. Check a subproject's entry in
[`tools/ci/github-release-subprojects.json`](tools/ci/github-release-subprojects.json)
for its exact tag prefix and branch name.

Tags follow the matching format `<service-path>/vMAJOR.MINOR.PATCH`, for
example `src/clis/nvcf-cli/v1.15.11` and
`src/compute-plane-services/nvca/v3.4.0`.

## Who Can Trigger a Release

Any contributor whose reviewed pull request merges to `main` has triggered a
release for the subprojects their commits touch, as long as at least one
commit is a `feat`, `fix`, or `perf` type. No separate release action is
needed after merge.

Manual: `.github/workflows/release-tags.yml` also accepts a
`workflow_dispatch` trigger, whose `operation` input selects what it does.

- `auto` (the default) re-runs the same automatic logic on demand, optionally
  scoped to a single service through the `service` input. This is a recovery
  path for a run that failed or was cancelled, not a way to force a version
  that the commits do not justify.
- `branch-cut` opens the next release train for a `deploy/stacks/` subproject,
  named by the required `service` input.
- `release-candidate` cuts an `-rc.N` for a `deploy/stacks/` subproject at the
  branch the run is dispatched from, named by the required `service` input.

`workflow_dispatch` requires GitHub write access to the repository. In this
repository that access is granted through organization team membership:
maintainers (`NVIDIA/nvcf-dev` and `NVIDIA/nvcf-admin`) can run it. Area
ownership for review is defined in
[`.github/CODEOWNERS`](.github/CODEOWNERS).

Publishing additionally requires the `NV_GITHUB_TOKEN` repository secret,
because a tag pushed with the default `GITHUB_TOKEN` does not start the
follow-up tag workflow that creates release notes.

## Artifact Destinations

GitHub tags and GitHub Releases are the primary release artifact, one per
subproject version. Publishing is gated by two repository variables read in
`release-tags.yml`: `NVCF_GITHUB_AUTO_TAGGING_ENABLED` and
`NVCF_GITHUB_RELEASE_DRY_RUN`. These are configured in repository settings,
not in source, so check their current values in the repository if you need
to confirm whether tag and release publishing is live or running in dry-run
preview mode at a given point in time.

Container images are handled separately from the tag and release flow.
[`.github/workflows/image-push-manual.yml`](.github/workflows/image-push-manual.yml)
builds and pushes a multi-arch image for one service subtree to an internal
NGC registry, either via manual `workflow_dispatch` or automatically for a
pull request carrying the `deploy-to-stg` label. This path exists for
pre-merge and staging testing; it is not an automatic per-release publish
step tied to a version tag.

## Backport Policy

A fix lands on `main` first, where it releases normally for every subproject
it touches.

Maintenance branches are the exception, not the default. Most subprojects do
not have one: a fix ships by merging to `main` like any other change. Open a
maintenance branch only when a released train needs a fix that cannot wait
for, or must not carry, the current state of `main`.

Support window: where a subproject does maintain release branches, only the
latest minor release train and the one before it (N and N-1) are maintained.
A release branch older than N-1 is effectively end of life and does not
receive further backports.

Mechanism: cherry-pick the commit from `main` onto the `release-*` branch,
following the same commit and review conventions as `main`. There is no
automation that backports a commit for you.

For a `deploy/stacks/` subproject that is all of it: the push to the release
branch cuts the next patch on that train.

For every other subproject, release automation runs on `main` only, so a
maintainer creates the patch tag by hand:

```sh
git tag <service-path>/vMAJOR.MINOR.PATCH <commit-on-the-release-branch>
git push origin <service-path>/vMAJOR.MINOR.PATCH
```

Push the tag with a token that can start workflows, so the tag workflow
creates the matching GitHub Release.
