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
runs `./tools/ci/github-release auto`. Each subproject registered in
[`tools/ci/github-release-subprojects.json`](tools/ci/github-release-subprojects.json)
uses exactly one of two release models. They are mutually exclusive: the
automation branches on a subproject's `dev_prerelease` flag and only ever
runs one path for it, never both.

Semantic-release model (most subprojects, for example `src/clis/nvcf-cli`):
walks the commits since the subproject's last release tag and decides
whether to cut a new version.

- Commits typed `feat`, `fix`, or `perf` (the "customer" commit types defined
  in [`CONTRIBUTING.md`](CONTRIBUTING.md#how-to-select-a-commit-type)) trigger
  a new stable release tag for the subproject they touch.
- Commits typed `docs`, `build`, `test`, `refactor`, `ci`, `chore`, `style`,
  or `revert` (the "foundational" types) do not trigger a release on their
  own.

Dev-prerelease model (only `nvca` and the three Helm stacks under
`deploy/stacks/`: `nvcf-compute-plane`, `self-managed`, and `observability`):
a push to `main` never cuts a stable release, regardless of commit type. It
only bumps a `-dev.N` prerelease tag off the stable base version recorded in
the subproject's `VERSION` file. In practice this means dev prereleases land
many times per day during active development; for example, at the time of
writing, `src/compute-plane-services/nvca` had cut five `-dev.N` prereleases
in a single day. A stable release for one of these four subprojects is cut
only when a commit lands on that subproject's release branch (see Branch
Naming), which bumps the patch version automatically. Until a release branch
exists for one of these subprojects, only dev prereleases accumulate; there
is no stable-release path on `main` for them.

Release notes are generated from commit messages (semantic-release
conventions) and attached to the GitHub Release for each tag, dev
prereleases included.

## Branch Naming

- `main`: the active development branch. All pull requests target `main`,
  except hotfixes (see [`CONTRIBUTING.md`](CONTRIBUTING.md#step-2-create-a-branch)).
- `release-<service-path>/vMAJOR.MINOR`: a maintenance branch for one
  subproject's release train, used by the dev-prerelease release model (see
  Release Cadence). Pushes to a branch matching `release-**/v*` run the same
  release automation, scoped to that subproject, so patch fixes on a
  maintenance train publish their own stable tags.

Real examples from this repository:

- `release-src/compute-plane-services/nvca/v3.1`
- `release-src/compute-plane-services/nvca/v3.2`
- `release-deploy/stacks/self-managed/v0.7`

The separator between the service id and the version can vary by how a
subproject registers its release metadata; for example
`release-nvcf-cassandra-migrations-v0.10` uses a flattened id instead of a
path segment. That branch also predates this subproject's current
configuration, which does not use the dev-prerelease model, so treat it as
historical evidence of the naming pattern rather than a live example. Check
a subproject's entry in
[`tools/ci/github-release-subprojects.json`](tools/ci/github-release-subprojects.json)
for its current release model and exact branch name.

Tags follow the matching format `<service-path>/vMAJOR.MINOR.PATCH`, for
example `src/clis/nvcf-cli/v1.15.11` (semantic-release model) and
`src/compute-plane-services/nvca/v3.3.0-dev.184` (dev-prerelease model).

## Who Can Trigger a Release

Automatic, semantic-release subprojects: any contributor whose reviewed pull
request merges to `main` has triggered a release for the subprojects their
commits touch, as long as at least one commit is a `feat`, `fix`, or `perf`
type. No separate release action is needed after merge.

Automatic, dev-prerelease subprojects (`nvca` and the three stacks under
`deploy/stacks/`): merging any pull request to `main` only produces a dev
prerelease tag, regardless of commit type. Triggering an actual stable
release for one of these subprojects requires a commit to land on that
subproject's release branch instead of `main`. See Manual below for who can
create that branch in the first place.

Manual: `.github/workflows/release-tags.yml` also accepts a
`workflow_dispatch` trigger with three operations:

- `auto`: re-run the same automatic logic on demand, optionally scoped to one
  service.
- `branch-cut`: cut a new maintenance release branch and open the follow-up
  version-bump pull request for the `nvca` service.
- `self-managed-branch-cut`: the same branch-cut flow for the
  `nvcf-self-managed-stack` service.

Both branch-cut operations only work for a dev-prerelease subproject; the
underlying `branch-cut` command refuses to run against a subproject that
does not use that release model.

`workflow_dispatch` requires GitHub write access to the repository. In this
repository that access is granted through organization team membership, not
a hand-maintained list: the `NVIDIA/nvcf-dev` team (the NVCF maintainers)
holds write access, and `NVIDIA/nvcf-admin` holds admin access. In practice
this means only NVCF maintainers can cut a release branch for `nvca` or one
of the three stacks; an external contributor with a fix for an already-cut
release branch needs a maintainer to either create the branch or merge a
pull request that targets an existing one. Branch-cut operations
additionally require the `NV_GITHUB_TOKEN` repository secret to be
configured, because the default `GITHUB_TOKEN` cannot trigger CI on the
generated version-bump pull request. Area ownership for review is defined in
[`.github/CODEOWNERS`](.github/CODEOWNERS).

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

No formal backport policy is documented in this repository today. A fix that
needs to reach an already-cut maintenance branch has to be applied there
directly (for example by cherry-picking the commit to the `release-*`
branch), following the same commit and review conventions as `main`.
