# GitHub Release Automation

The public GitHub workflow in `.github/workflows/release-tags.yml`
prepares NVCF release automation before the GitHub cutover. It is
configured to run in dry-run mode by default, so the workflow can be
validated without creating GitHub tags or releases.

## Dry-run gate

The workflow reads these repository variables:

- `NVCF_GITHUB_RELEASE_DRY_RUN`: defaults to `true`. When `true`,
  branch pushes compute proposed service tags and tag pushes validate
  release tags, but nothing is written to GitHub.
- `NVCF_GITHUB_RELEASE_DRAFT`: defaults to `false`. When release
  creation is enabled, `true` creates draft GitHub releases.

Do not set `NVCF_GITHUB_RELEASE_DRY_RUN=false` until the GitHub
commit graph has release anchors for each service being cut over.

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
- service tag prefix
- version-file hints for services that do not use semantic-release
- generated/mechanical file basenames to ignore for release decisions

It does not contain GitLab runner tags, Vault paths, NGC registry
destinations, `nvcf-internal` trigger details, or Slack notification
configuration.

The service tag format mirrors GitLab:

```text
<service-name>-v<X.Y.Z>
```

Examples:

```text
nvcf-ratelimiter-v1.15.1
byoo-otel-collector-v0.153.3
helm-nvca-operator-v1.11.1
```

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

Valid service-style tags are:

```text
<service-name>-vX.Y.Z
<service-name>-vX.Y.Z-rc.N
<service-name>-vX.Y.Z-dev.N
```

Invalid tags are skipped without creating a GitHub release.

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
