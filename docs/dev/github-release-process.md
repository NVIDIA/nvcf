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

Publish mode also requires the `NVCF_GITHUB_RELEASE_TOKEN` repository
secret. It must be a GitHub token that can push tags and create
releases. Tags pushed with the default `GITHUB_TOKEN` do not start the
follow-up tag workflow, so the workflow fails publish mode when this
secret is missing.

## Cutover order

1. Keep GitHub release automation dry-run-only while the repository is
   still being anchored.
2. Stop GitLab monorepo tag creation by setting
   `NVCF_GITLAB_RELEASE_TAGGING_ENABLED=false` in the GitLab source
   project. This skips the generated `semantic-release-*` tag
   creation jobs so the GitLab monorepo no longer creates release
   tags. It also disables GitLab tag pipelines for manually-created
   GitLab tags, so release publish cannot start from GitLab tags after
   the cutover gate is off.
3. Keep `nvcf/nvcf-github` mirror publish disabled by leaving
   `NVCF_GITHUB_MIRROR_RELEASE_PUBLISH_ENABLED` unset or `false` while
   recreating historical anchors that should not republish artifacts.
4. Recreate any missing GitHub anchors with path-format tags and
   `refs/notes/semantic-release` notes on the GitHub commit graph.
5. Enable the `nvcf/nvcf-github` mirror tag-publish bridge by setting
   `NVCF_GITHUB_MIRROR_RELEASE_PUBLISH_ENABLED=true` in that GitLab
   mirror project.
6. Manually create any GitHub tags that were missed while GitLab
   tagging was disabled and GitHub auto-tagging was still dry-run-only.
   This includes any `-dev.N`, `-rc.N`, or stable tags that should have
   existed during the cutover window. If a tag was pushed before mirror
   publish was enabled, retrigger the matching `nvcf/nvcf-github` tag
   pipeline.
7. Enable GitHub auto-tagging and publish by setting
   `NVCF_GITHUB_AUTO_TAGGING_ENABLED=true` and
   `NVCF_GITHUB_RELEASE_DRY_RUN=false`.

After step 7, release tags originate from GitHub. Publish work that
needs GitLab runners starts from the `nvcf/nvcf-github` mirror tag
pipeline, not from the original `nvcf/nvcf` monorepo and not from a
GitHub-to-GitLab API call.

## Service auto-tags

On `main` branch pushes, the workflow runs:

```bash
./tools/ci/github-release auto
```

The script reads `tools/ci/github-release-subprojects.json`, which is
generated from the internal `tools/ci/subproject-validations.yaml`
source of truth. The generated file intentionally contains only public
release metadata:

- service id
- service subtree path
- service tag format
- legacy service tag prefix, when a release line still needs old-tag
  compatibility
- version-file hints for services that do not use semantic-release
- generated/mechanical file basenames to ignore for release decisions

It does not contain GitLab runner tags, Vault paths, NGC registry
destinations, `nvcf-internal` trigger details, or Slack notification
configuration.

The service tag format mirrors GitLab and uses the repo-relative
service path:

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

The self-managed stack is not in this auto-tag set until it has a
monorepo version source. Its release config currently keeps default
branch release tagging disabled.

For `nvcf-compute-plane-stack`, GitHub-created
`deploy/stacks/nvcf-compute-plane/v*` tags are mirrored into
`nvcf/nvcf-github`. That mirror tag pipeline triggers `nvcf-internal`,
which owns stack build/package/publish. The original GitLab monorepo no
longer builds or publishes the stack from tag pipelines after
`NVCF_GITLAB_RELEASE_TAGGING_ENABLED=false`.

For semantic-release services, the GitHub workflow uses the same
release rules as the generated GitLab release jobs:

- `feat:` creates a minor release
- `fix:` and `perf:` create patch releases
- `chore:`, `ci:`, `docs:`, `style:`, `refactor:`, `test:`, and
  `build:` do not create releases

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

## nvcf-internal publish bridge

GitHub does not need GitLab credentials. Tag pushes only run the
GitHub release-note workflow; GitLab-side publish work starts after the
tag appears in the `nvcf/nvcf-github` mirror.

The `nvcf/nvcf-github` mirror tag pipeline covers all release lanes
that trigger `nvcf-internal`:

- services with `release.staging` build staging images, write
  `release-manifest.json`, and trigger `nvcf-internal` with
  `NVCF_RELEASE_MANIFEST_B64`
- services with `release.internal_release` trigger `nvcf-internal`
  with source repo/ref metadata
- `nvcf-compute-plane-stack` uses a root bridge job to trigger
  `nvcf-internal`, where the stack distribution, package, NGC
  resources, and nvpublish handoff are owned

The mirror project requires `NVCF_GITHUB_MIRROR_RELEASE_PUBLISH_ENABLED=true`
before tag pipelines publish. `nvcf-internal` accepts source metadata
from `NVCF_SOURCE_PROJECT_PATH=nvcf/nvcf-github` and fetches source
tags from `https://github.com/NVIDIA/nvcf/nvcf-github`.

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

If the GitHub mirror is a snapshot with different commit SHAs from
GitLab, do not copy GitLab refs verbatim. Recreate the latest service
tags and semantic-release notes on the GitHub commits that represent
the released content, then enable publish mode.

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
