# GitHub Release Automation

The public GitHub workflow in `.github/workflows/release-tags.yml`
prepares NVCF release automation before the GitHub cutover. It is
configured to run in dry-run mode by default, so the workflow can be
validated without creating GitHub tags or releases.

## Dry-run gate

The workflow reads these repository variables:

- `NVCF_GITHUB_AUTO_TAGGING_ENABLED`: defaults to `false`. When
  `false`, the workflow always runs in dry-run mode even if another
  variable is misconfigured.
- `NVCF_GITHUB_RELEASE_DRY_RUN`: defaults to `true`. When `true`,
  branch pushes compute proposed service tags and tag pushes validate
  release tags, but nothing is written to GitHub. Set this to `false`
  only after `NVCF_GITHUB_AUTO_TAGGING_ENABLED=true`.
- `NVCF_GITHUB_RELEASE_DRAFT`: defaults to `false`. When release
  creation is enabled, `true` creates draft GitHub releases.

Do not set `NVCF_GITHUB_AUTO_TAGGING_ENABLED=true` or
`NVCF_GITHUB_RELEASE_DRY_RUN=false` until the GitHub commit graph has
release anchors for each service being cut over.

Publish mode also requires the `NV_GITHUB_TOKEN` repository
secret. It must be a GitHub token that can push tags and create
releases. Tags pushed with the default `GITHUB_TOKEN` do not start the
follow-up tag workflow, so the workflow fails publish mode when this
secret is missing.

## Cutover order

1. Keep GitHub release automation dry-run-only while the repository is
   still being anchored.
2. Recreate any missing GitHub anchors with path-format tags and
   `refs/notes/semantic-release` notes on the GitHub commit graph.
3. Enable GitHub auto-tagging and publish by setting
   `NVCF_GITHUB_AUTO_TAGGING_ENABLED=true` and
   `NVCF_GITHUB_RELEASE_DRY_RUN=false`.

After step 3, GitHub is the sole tag and release authority. The active
release path is:

```text
NVIDIA/nvcf -> GitLab mirror -> scheduled internal release dispatcher -> image release pipeline
```

After a GitHub tag and release appear in the mirror, the scheduled
internal release dispatcher detects eligible releases and starts the
image release pipeline.

## Service auto-tags

On `main` branch pushes, the workflow runs:

```bash
./tools/ci/github-release auto
```

The script reads `tools/ci/github-release-subprojects.json`. The file
intentionally contains only public release metadata:

- service id
- service subtree path
- service tag format
- optional initial version floor for a service or chart that has no tags
  yet
- legacy service tag prefix, when a release line still needs old-tag
  compatibility
- version-file hints for services that do not use semantic-release
- generated/mechanical file basenames to ignore for release decisions

It does not contain internal runner tags, Vault paths, NGC registry
destinations, internal trigger details, or Slack notification
configuration.

The service tag format uses the repo-relative service path:

```text
<service-path>/v<X.Y.Z>
```

Examples:

```text
src/invocation-plane-services/ratelimiter/v1.15.1
src/compute-plane-services/byoo-otel-collector/v0.153.3
deploy/helm/nvca-operator/v1.11.1
```

During the transition from the old service-prefix convention, the
generated metadata also carries `legacy_tag_prefix`. The workflow
uses those old tags as version anchors but creates any new tags with
the path-scoped tag derived from the service path, unless the metadata
declares an explicit `tag_format` override.

Services that declare both `version_file` and `dev_prerelease`, such
as NVCA and `nvcf-compute-plane-stack`, do not use semantic-release
for the next version. On `main`, the GitHub workflow reads the stable
base version from the version file and creates the next path-format dev
prerelease tag:

```text
src/compute-plane-services/nvca/v<X.Y.Z>-dev.N
```

On a matching release branch, the workflow creates the next stable
patch tag for that train.

Every stable release the workflow creates from a version file comments
the version it shipped on the pull requests that release covers, which
is the note `@semantic-release/github` posts for the services it
manages:

```text
This PR is included in version 3.2.14.
```

Dev prerelease tags stay silent, and a re-run over a release that
already exists does not comment again. The commented range starts at
the closest release tag reachable from the branch rather than the
highest-sorting tag, because a release branch is cut with a synthetic
root and never contained most default-branch tags. It covers every
commit since that tag rather than only the tagged commit, because the
workflow's concurrency group cancels queued runs and a superseded push
is first tagged by the next run to finish.

The self-managed stack is not in this auto-tag set until it has a
monorepo version source. Its release config currently keeps default
branch release tagging disabled.

For `nvcf-compute-plane-stack`, GitHub-created
`deploy/stacks/nvcf-compute-plane/v*` tags are mirrored. The scheduled
release dispatcher then starts the stack image and package pipeline.

For semantic-release services, the GitHub workflow uses these release
rules:

- `feat:` creates a minor release
- `fix:` and `perf:` create patch releases
- `chore:`, `ci:`, `docs:`, `style:`, `refactor:`, `test:`, and
  `build:` do not create releases

## Java framework dependency releases

`semantic-release-monorepo` scopes a service's commits to its own
subtree path. A change to the shared Java framework under
`src/libraries/java/` lands outside every service directory, so
semantic-release sees no commits for any dependent service and releases
nothing. CI still rebuilds and tests each dependent service, but the
rebuilt artifact never leaves the CI job.

`tools/ci/github-release auto` closes that gap. It reads the
per-component `bazel-java-ci.json` descriptors, the same files
`.github/workflows/bazel.yml` reads to schedule its matrix, so the
framework-to-service edge is declared once:

- `component_kind: java-framework` marks a shared framework path.
- `component_kind: java-service` marks a component that is rebuilt when
  any framework path changes.

For a registered subproject whose path matches a `java-service`
descriptor, the script cuts a dependency-triggered release when all of
the following hold:

- semantic-release computed no version for that service on this run. If
  the same push also touched the service, semantic-release owns the
  version and nothing extra is tagged.
- The service already has a release tag to bump from.
- At least one release-worthy commit touching a framework path landed
  since that tag. Release-worthiness uses the same rules listed above: a
  framework `feat:`, `fix:`, `perf:`, or breaking `!` commit fans out; a
  framework `docs:`, `chore:`, `ci:`, `style:`, `refactor:`, `test:`, or
  `build:` commit releases nothing for the framework and so releases
  nothing for its dependents either.

The synthesized bump is always a patch, including when the framework
commit is a `feat:`. A framework feature adds no capability to a service
that has not adopted it, and the service's own changelog has nothing to
substantiate a minor. A service that does adopt a new framework API does
so in a commit under its own directory, which semantic-release turns
into the correct bump; the fan-out does not run in that case.

The release notes state that the release is dependency-triggered and
list the framework commits, so a reader of a GitHub Release with no
changes in the service directory can see why the version moved.

Dry-run mode prints the tag and notes it would create and creates
nothing, the same as every other release path in this script.

## Release notes for pushed tags

On tag pushes, the workflow validates the tag and creates lightweight
GitHub release notes when dry-run mode is disabled.

Valid path-style tags are:

```text
path/to/module/vX.Y.Z
path/to/module/vX.Y.Z-rc.N
path/to/module/vX.Y.Z-dev.N
```

Legacy service-style tags are accepted as compatibility inputs while
release metadata still declares `legacy_tag_prefix`:

```text
<service-name>-vX.Y.Z
<service-name>-vX.Y.Z-rc.N
<service-name>-vX.Y.Z-dev.N
```

Invalid tags are skipped without creating a GitHub release.

## Image publishing bridge

GitHub does not need image-publishing credentials. Tag pushes only run
the GitHub release-note workflow. Image publishing starts after the tag
and release appear in the mirror.

After a GitHub tag and release are mirrored, the scheduled release
dispatcher detects eligible releases and starts the corresponding image
release pipeline. The pipeline uses the mirrored source ref and release
metadata to build, promote, and publish artifacts. The mirror tag
pipeline does not publish artifacts.

## Package metadata

Package metadata uses SemVer without the leading `v`:

| Tag | Package version |
| --- | --- |
| `src/compute-plane-services/nvca/v3.0.0` | `3.0.0` |
| `deploy/helm/nvca-operator/v1.11.1-rc.1` | `1.11.1-rc.1` |
| `nvcf-ratelimiter-v1.15.1` | `1.15.1` |

## Release branches

Release branch names use:

```text
release-<tag without patch or rc/dev suffix>
```

Examples:

| Tag | Release branch |
| --- | --- |
| `src/compute-plane-services/nvca/v3.0.0` | `release-src/compute-plane-services/nvca/v3.0` |
| `deploy/helm/nvca-operator/v1.11.1-rc.1` | `release-deploy/helm/nvca-operator/v1.11` |
| `nvcf-ratelimiter-v1.15.1` | `release-nvcf-ratelimiter-v1.15` |

Slashes remain branch namespace separators.

## Cutover anchors

GitHub release publishing needs both the latest service tag and the
matching `refs/notes/semantic-release` entry on the GitHub commit
graph. `.oss-allowlist` mirrors files, not Git refs, tags, or notes.

If the GitHub mirror is a snapshot with different commit SHAs from the
prior release source, do not copy old refs verbatim. Recreate the
latest service tags and semantic-release notes on the GitHub commits
that represent the released content, then enable publish mode.

Use the helper below to create one path-format anchor locally. The
version may be a dev prerelease, release candidate, or stable release:

```bash
./tools/ci/github-release anchor \
  --service nvca \
  --version 3.1.0-rc.1 \
  --ref <github-commit-or-ref>
```

After reviewing the created tag and note, push them:

```bash
./tools/ci/github-release anchor \
  --service nvca \
  --version 3.1.0-rc.1 \
  --ref <github-commit-or-ref> \
  --push
```

The helper uses the generated metadata to choose the current
path-format tag, for example
`src/compute-plane-services/nvca/v3.1.0`; it does not create legacy
`<service>-v` tags. If a semantic-release note already exists on the
same commit for another service, the helper refuses to overwrite it so
the notes ref can be merged manually.

## Seeding and pinning service or chart versions

Release automation computes the next version by bumping the highest
existing release tag for a service. A new service or chart has no tags
yet, so there is nothing to bump from until you seed one. This section
explains how to seed that first tag, and how to pin a new floor on a
service or chart that already has tags.

### 1. Register the service

Add the service to the release metadata in
`tools/ci/github-release-subprojects.json`. Each entry provides:

- `id`: short service id
- `path`: repo-relative subtree path, which also drives the tag format
  `<path>/v<X.Y.Z>`
- `service_name`: release or package name
- `initial_version`: optional SemVer floor to start the line from. Omit
  it to start from a `0.0.0` floor, where the next version depends on the
  commit type: a `feat` yields `0.1.0`, a `fix` yields `0.0.1`, and
  release-neutral commits produce no release. An empty string is
  rejected; either omit the field or give a valid SemVer.

For example, the `ess-helm` chart was first seeded with an
`initial_version` floor so it continued the upstream chart version line:

```json
{
  "id": "ess-helm",
  "path": "deploy/helm/ess",
  "service_name": "helm-nvcf-ess-api",
  "initial_version": "1.7.0"
}
```

After it published `1.7.1` and its directory was renamed to
`deploy/helm/encrypted-secret-store`, the floor was replaced with a
`legacy_tag_prefix` of `deploy/helm/ess/v` so the version line carried
across the path change without a reset.

Run the registry tests:

```bash
python3 tools/ci/test-github-release.py
```

### 2. Set the version

Pick the case that matches your situation.

#### Case 1: new service or chart, seed with initial_version (automatic)

Use this for a brand-new line with no tags when you want it to start
above `0.0.0`. Set `initial_version` to the desired floor in the
registration; omit it, or use `0.0.0`, to start at the default.
`initial_version` only takes effect while the service has no tags; once
any tag exists it is ignored (see Case 3).

Then just commit the registration. On the next `main` push,
`./tools/ci/github-release auto` synthesizes the floor locally and cuts
the next bump. The floor is a local computation baseline and is not
published, so no tag for the floor version appears on the remote. The
first published tag is the next bump: for example `1.7.1` (fix) or
`1.8.0` (feat) from a `1.7.0` floor, or `0.1.0` (feat) / `0.0.1` (fix)
from a `0.0.0` floor.

`ess-helm` uses this case. It is registered with `initial_version:
1.7.0`, so the first published chart release is `1.7.1` or `1.8.0` and no
`1.7.0` tag is created.

#### Case 2: new service or chart, seed manually on the command line

Use this when the seeded version itself should exist on the remote and be
recorded as already released, for example to publish an explicit `1.7.0`
tag. The `anchor` command takes the version as an argument, so
`initial_version` is not used in this case. It writes both the
path-format tag and the `refs/notes/semantic-release` note:

```bash
# preview
./tools/ci/github-release anchor --service <id> --version <X.Y.Z> --ref <commit> --dry-run
# create and push the tag and note
./tools/ci/github-release anchor --service <id> --version <X.Y.Z> --ref <commit> --push
```

`--ref` is the commit the tag lands on and defaults to `HEAD`, usually
the commit that created or imported the service. For `ess-helm` that
would be:

```bash
./tools/ci/github-release anchor --service ess-helm --version 1.7.0 --ref <chart-import-commit> --push
```

The next release then bumps from the anchored version.

#### Case 3: service or chart already has tags, pin a new version

Use this when the line already has release tags and you want to move the
floor to a specific version, for example to match a new upstream product
version. `initial_version` is ignored once tags exist, and `anchor`
refuses to run when the target commit already carries a
`refs/notes/semantic-release` note. Pin the version by pushing a plain
floor tag. semantic-release and `latest_service_tag` derive the baseline
from tag names, so the highest tag wins while the existing note keeps the
commit marked as released:

```bash
git tag <path>/v<X.Y.Z> <commit>
git push origin refs/tags/<path>/v<X.Y.Z>
```

`ess` used this case. It already had `v0.0.0`, `v0.1.0`, and `v0.2.0` on
a commit that carried a semantic-release note, so a plain floor tag was
pushed to realign it to the upstream product version:

```bash
git tag src/control-plane-services/ess/v0.4.9 <commit>
git push origin refs/tags/src/control-plane-services/ess/v0.4.9
```

The next `ess` release computes from `0.4.9`: `0.5.0` for a `feat`,
`0.4.10` for a `fix`. The `0.3.x` to `0.4.8` gap is intended.

### 3. Verify

```bash
git ls-remote --tags origin '<path>/*'
```

Git matches this pattern on slash boundaries, so `<path>/*` lists
exactly the version tags under that service path.

Seeding tags only establishes the version floor. Nothing publishes a
GitHub Release until `NVCF_GITHUB_AUTO_TAGGING_ENABLED=true` and
`NVCF_GITHUB_RELEASE_DRY_RUN=false`, as described in the dry-run gate
above. A tag pushed with the `NV_GITHUB_TOKEN` secret, or another
workflow-capable token, starts the tag workflow, but it stays inert
while the dry-run gate is on. Tags pushed with the default
`GITHUB_TOKEN` do not trigger the follow-up workflow.
