# changelog-site

Generates a static, browsable per-service changelog for the umbrella monorepo
and publishes it to GitLab Pages.

The monorepo MR/commit log mixes every service together. This tool isolates
each released service by combining its path-scoped release tag, derived from
the service path unless `release.tag_format` overrides it, with its subtree
path. During the old-to-new tag transition it also reads
`release.legacy_tag_prefix`, then emits `changelog.json` that the embedded
`index.html` renders interactively: pick a service and two versions, see the
diff grouped into customer-facing (`feat`/`fix`/`perf`) and other commits,
each linked to its GitLab commit.

## Run locally

```bash
go run -C tools/changelog-site . \
  --config ../ci/subproject-validations.yaml \
  --repo ../.. \
  --out /tmp/changelog \
  --commit-base "https://<gitlab-host>/<group>/<project>/-/commit/"
# then open /tmp/changelog/index.html (served, e.g. `python3 -m http.server -d /tmp/changelog`)
```

`index.html` fetches `changelog.json`, so open it through a web server, not
`file://`.

## CI

The umbrella `pages` job runs this on every default-branch pipeline and
publishes `public/` to GitLab Pages (project-private, members-only). New
release tags appear automatically on the next merge to `main`.

## Service sources

Each released service is sourced one of two ways:

- Umbrella services (`release.service_name` set): path-scoped tags derived
  from the service path, plus any explicit `release.tag_format` override and
  `release.legacy_tag_prefix` tags, and history in this repo, path-scoped to
  the subtree. Listed even with zero releases, so not-yet-tagged `helm-*`
  charts still appear ("no releases yet").
- Upstream services: declared with a `changelog:` block in
  `subproject-validations.yaml`, for services whose release tags live in a
  separate repo (e.g. nvca's `v3.0.x` line in `egx/intelligent-infra/nvca`).
  The tool clones that repo (treeless, full history) and reads its tags.
  `umbrella_tag_prefix` optionally merges native umbrella releases, so a
  cutover service shows its frozen upstream line plus new monorepo releases on
  one timeline; each commit links to its own repo.

```yaml
  - id: nvca
    changelog:
      upstream_repo: https://<gitlab-host>/<group>/<upstream-project>.git
      tag_prefix: v               # upstream frozen line
      umbrella_tag_prefix: nvca-v # legacy native releases in the umbrella
```

The cross-repo clone authenticates with `CI_JOB_TOKEN` (CI) or `GITLAB_TOKEN`
(local). If the clone fails (e.g. the upstream does not allow this project's
job token), that service is skipped with a warning rather than failing the
build; add this project to the upstream's CI/CD job-token allowlist to enable
it. `tools/generate-subproject-ci` ignores the `changelog:` key.

## GitHub tags

Release tagging moved to the public GitHub mirror, and the GitLab to GitHub
mirror is one-way, so GitHub-created release tags never flow back into this
checkout. The tool fetches the GitHub mirror's tags into the local repo (their
commits are already present, since the commit graph is shared) so the changelog
stays complete, and it records which host carries each tag. Every release is
labeled `gitlab`, `github`, or `both`, shown as a chip in the UI and a short
`GL`/`GH`/`GH+GL` marker in the version picker, so a code audit can tell at a
glance where a release tag was cut.

The mirror defaults to `https://github.com/NVIDIA/nvcf.git`; override with
`--github-repo` or `NVCF_GITHUB_REMOTE`, or set it empty to disable the GitHub
source. `GITHUB_TOKEN` is used when set (the mirror is public, so it only lifts
rate limits). If the mirror is unreachable, the build degrades to GitLab-only
with a warning.

## Notes

- Commits are grouped by Conventional Commit type. Type-based grouping cannot
  distinguish user-visible behavior from `feat`/`fix`-typed release/CI work, so
  the "customer-facing" bucket can include plumbing.
- A release's commit set is the range `(previousTag, thisTag]` scoped to the
  service path; selecting `from`..`to` in the UI unions the releases in that
  span, matching `git log from..to -- <path>`.
