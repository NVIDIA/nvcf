# Release Process for Monorepo-Native Services

This page covers how a service in the NVCF umbrella monorepo gets a
new release: how versions are computed, what triggers a release, what
gets published (OCI image, Helm chart), and how to add a new service
to the release pipeline.

Pre-cutover services kept their own repo with manual chart-version /
image-version bumps. Once a service moves to the umbrella, it joins
this shared auto-versioning machinery.

For the public GitHub-side dry-run workflow and release tag
conventions, see [GitHub Release Automation](github-release-process.md).

## Summary

| Step | What | Where |
| --- | --- | --- |
| 1. Author commit | Conventional Commits format. The commit `type` decides whether (and how) the next release version bumps | Local |
| 2. Open MR | Pre-merge gates run: per-service Bazel build, image-push build-only, license, sonarqube, etc. | MR pipeline |
| 3. Merge to main | Default-branch pipeline runs: `compute-next-release-version-<svc>` -> `<svc>-bazel` -> `<svc>-image-push` -> (if helm) `helm-package-<svc>` + `helm-push-<svc>-*` -> `semantic-release-<svc>` -> `<svc>-slack-notify` | Default-branch pipeline |
| 4. Tag created | `semantic-release-<svc>` creates the git tag `<service-name>-v<X.Y.Z>` and a GitLab Release | End of step 3 |
| 5. Tag pipeline | A new pipeline runs against the tag ref. Same publish jobs re-run. NGC chart pushes are NOT idempotent (helm-registry rejects republishing the same version); the cds-component swallows the failure when `ngc-duplicate: skip` is set. OCI image pushes overwrite the tag pointer if the org allows it; some orgs reject. See "Known shortcomings and follow-ups" below. | Tag pipeline |

No human `git tag` step. Steps 3-5 are automatic once the MR merges.

## Versioning

Each service has its own version line. The git tag format is:

```
<service-name>-v<X.Y.Z>
```

e.g. `nvcf-unbound-v0.7.18`, `nvcf-ratelimiter-v1.13.0`,
`nvcf-grpc-proxy-v0.4.2`.

The prefix is required because all services share one repo's tag
namespace: a bare `v0.7.18` from nvcf-unbound would collide with the
next `v0.7.18` from ratelimiter. `semantic-release-monorepo` uses
this prefix to know which commits belong to which service when
computing the next version (it scopes by subtree path).

The version itself is computed by `semantic-release` from
Conventional Commits since the previous tag. The release rules per
service (see `tools/generate-subproject-ci/main.go` for the
`.releaserc.json` template) currently bump:

- `feat:` -> minor
- `fix:` / `perf:` -> patch

`chore:`, `ci:`, `docs:`, `style:`, `refactor:`, `test:`, and
`build:` do not create a release. If you do not want a commit to
trigger a release, use one of those non-releasing types or a commit
subject that does not match a Conventional Commits type.

### Path scoping (how monorepo commits filter per service)

The umbrella holds many services in one repo, so the commit analyzer
needs to know which commits are "for" each service. That filtering
comes from `semantic-release-monorepo` (loaded via
`"extends": "semantic-release-monorepo"` in the generated
`.releaserc.json`).

Two pieces of the wiring make path scoping work:

1. The generator's `compute-next-release-version-<svc>` script runs
   `cd "$SUBTREE"` before writing `package.json` and the
   `.releaserc.json`. That places the npm package root inside the
   service subtree.
2. `semantic-release-monorepo` infers the package directory from the
   `package.json` location, then runs `git log --follow -- <path>`
   under that subtree to enumerate the commits the analyzer sees.

Commits that don't touch files under the subtree are ignored when
computing the next version. A `fix(grpc-proxy): ...` commit cannot
release nvcf-unbound, and vice versa. Cross-subtree changes (e.g. an
edit in `tools/ci/` plus an edit in `src/<svc>/`) appear in every
service whose `change_paths` overlap with the diff -- which is why
the umbrella generator config restricts most service `change_paths`
to the service's own subtree.

The version stamps every release artifact in lockstep:

- OCI image: `nvcr.io/<org>/<image>:<X.Y.Z>` (also `latest`,
  `<short-sha>`)
- Helm chart: `Chart.yaml`'s `version` and `appVersion` both stamp
  to `<X.Y.Z>` at package time (the in-tree `Chart.yaml` carries a
  placeholder; the packaged `.tgz` gets the real semver)
- Git tag and GitLab Release: `<service-name>-v<X.Y.Z>`

## Pipeline lanes per service

Every service that opts into the release pipeline emits this fixed
set of jobs in the generated YAML
(`tools/ci/generated-release-jobs.yml`):

### `compute-next-release-version-<svc>` (stage: init)

Runs `semantic-release --dry-run --no-ci` to compute the next
version. Writes a `NEXT_VERSION` dotenv artifact that later jobs
consume. Runs on:

- MR pipelines whose change paths touch the service
- Default-branch pipeline with the same change paths
- Tag pipelines (`$CI_COMMIT_TAG =~ /^<service-name>-v/`); the
  script falls back to `${CI_COMMIT_TAG#<service-name>-v}` when
  there is no new release-worthy commit

### `<svc>-bazel` (stage: Prerequisites)

`bazel test //...` plus `bazel build :image_index` (and chart
target if applicable). Runs on MR + default-branch + scheduled +
web pipelines.

### `<svc>-image-push` (stage: Prerequisites)

On MR pipelines: runs `bazel build :image_push_*`. This invokes
Bazel's analyze + assemble graph (which fetches the base image and
exercises the docker auth setup) but does NOT execute the
`oci_push` action. Nothing is published to NGC from MR pipelines.

On default-branch and tag pipelines: runs `bazel run :image_push_*`.
This executes the `oci_push` action and publishes to NGC.

The build-not-push split is what catches docker-auth-scope bugs and
base-image-availability bugs in MR review, instead of post-merge on
main (the original NVCF-10337 scenario). The `docker_auth_path`
scoping in `tools/ci/subproject-validations.yaml` keeps the push
token off the public base-image pull.

To be unambiguous: an MR opening today does NOT push an image
anywhere. Only merges to main (or tag pushes) publish.

### `helm-package-<svc>` (stage: publish)

Optional, only emitted when the service declares a `release.helm`
block. Wraps `cds/cds-components/helm-package@0.16.6` with
`chart-version: ${NEXT_VERSION}`. The packaged `.tgz` carries the
semver from `compute-next-release-version-<svc>` regardless of what
`Chart.yaml` in git says.

### `helm-push-<svc>-<target>` (stage: publish)

Optional, one job per entry in `release.helm.push_targets`. Wraps
`cds/cds-components/helm-push-ngc@0.16.6` to push the packaged chart
to the named NGC org. Default `ngc-duplicate: skip` causes the
cds-component to swallow NGC's "version already exists" rejection
on re-push, so the tag pipeline's duplicate publish exits 0 without
actually replacing the chart. See "Known shortcomings and follow-ups"
below for the registry-layer truth.

### `semantic-release-<svc>` (stage: publish)

Runs `semantic-release --no-ci` (real run, not dry). Creates the
git tag and GitLab Release if there is a release-worthy commit. The
`resource_group: semantic-release-notes` setting serializes per-
service runs across the project, avoiding the
`refs/notes/semantic-release` push race that hits when multiple
services release on one pipeline.

### `<svc>-slack-notify` (stage: Snapshot)

Optional, only when the service declares a `slack_channel`. Posts
to the configured Slack channel via backstage-helper after the tag
pipeline completes.

### `<svc>-sonarqube-analysis` (stage: Prerequisites)

Optional, only when the service declares a `sonarqube_project_key`.
Runs sonar-scanner against the subtree on MR + default-branch
pipelines.

## End-to-end flow (worked example: nvcf-unbound)

A developer opens an MR with a commit
`fix(nvcf-unbound): handle empty A record edge case`.

MR pipeline runs:

- `nvcf-unbound-bazel`: builds and tests
- `compute-next-release-version-nvcf-unbound`: dry-run says the next
  version would be `0.7.19` (patch bump from `fix:`)
- `nvcf-unbound-image-push`: `bazel build :image_push_devops` etc.
  validates the auth setup and base image fetch but does not push
- `nvcf-unbound-sonarqube-analysis`: scans the subtree

The MR gets reviewed and merged.

Default-branch pipeline (commit on main) runs:

1. `compute-next-release-version-nvcf-unbound` recomputes
   `NEXT_VERSION=0.7.19`
2. `nvcf-unbound-bazel` builds + tests
3. `nvcf-unbound-image-push` pushes
   `nvcr.io/nv-ngc-devops/nvcf-unbound:0.7.19` and
   `nvcr.io/0651155215864979/ncp-dev/nvcf-unbound:0.7.19`
4. `helm-package-nvcf-unbound` packages `nvcf-unbound-0.7.19.tgz`
5. `helm-push-nvcf-unbound-{ncp-dev,nv-ngc-devops,nvcf-internal}`
   push the chart to all three NGC orgs
6. `semantic-release-nvcf-unbound` creates the git tag
   `nvcf-unbound-v0.7.19` and a matching GitLab Release
7. `nvcf-unbound-slack-notify` posts to `#nv-nvcf-cicd`

Tag pipeline (triggered by step 6) runs jobs 1-5 again. The
re-pushes are NOT clean idempotent operations:

- Helm: NGC's helm registry rejects republishing the same chart
  version. The `helm-push-ngc` cds-component swallows that
  rejection because the umbrella sets `ngc-duplicate: skip`
  (default). Result: the second helm-push exits 0 without
  publishing. If an admin had set `ngc-duplicate: overwrite`, the
  re-push would actually overwrite; `fail` would fail the job.
- OCI image: `oci_push` will attempt to move the tag pointer to
  the new manifest. Most NGC orgs accept this overwrite; some
  reject. We rely on the org being permissive. The first push (on
  the default-branch pipeline) is the canonical one.

`semantic-release-nvcf-unbound` does not re-run on the tag pipeline
(its rule scopes to default-branch only); only the publish jobs
re-fire.

## Adding a new service to the release pipeline

To register a new service for auto-versioned releases:

### 1. Add a `release:` block in `tools/ci/subproject-validations.yaml`

Image-only example (no Helm chart):

```yaml
- id: <service-id>
  path: src/<plane>/<service-id>
  change_paths:
    - src/<plane>/<service-id>/**/*
  release:
    service_name: <service-name>
    image_push_targets:
      - name: devops
        bazel_target: //nvidia-internal:image_push_devops
        auth:
          type: ci_var
          ci_var: NGC_DEVOPS_API_KEY
        docker_auth_path: nvcr.io/nv-ngc-devops/<service-name>
      - name: ncp_dev
        bazel_target: //nvidia-internal:image_push_ncp_dev
        auth:
          type: ci_var
          ci_var: NGC_DEVOPS_API_KEY
        docker_auth_path: nvcr.io/0651155215864979/ncp-dev/<service-name>
    slack_channel: C08S6KLCEJH
    sonarqube_project_key: <project-key>   # optional
```

Image + Helm example (see nvcf-unbound for the working version):

```yaml
- id: <service-id>
  path: src/<plane>/<service-id>
  change_paths:
    - src/<plane>/<service-id>/**/*
  release:
    service_name: <service-name>
    image_push_targets:
      # ... same as image-only ...
    helm:
      chart_path: deploy           # relative to subtree root
      push_targets:
        - name: ncp-dev
          ngc_path: 0651155215864979/ncp-dev
          ngc_key_var: NGC_DEVOPS_API_KEY
        - name: nv-ngc-devops
          ngc_path: nv-ngc-devops
          ngc_key_var: NGC_DEVOPS_API_KEY
        - name: nvcf-internal
          ngc_path: "0544956542906249"
          ngc_key_var: NGC_PUSH_SERVICE_KEY_NVCF_INTERNAL
    slack_channel: C08S6KLCEJH
```

### 2. Regenerate the pipeline YAML

```bash
go run -C tools/generate-subproject-ci . \
  --config ../ci/subproject-validations.yaml \
  --release-output ../ci/generated-release-jobs.yml
```

Commit the regenerated `tools/ci/generated-release-jobs.yml` along
with the validations change. The `check-release-pipeline-generated`
CI job verifies the file is in sync.

### 3. Add a `<svc>-bazel` job in the umbrella `.gitlab-ci.yml`

Mirror an existing entry (e.g. `nats-auth-callout-bazel`). Sets
`SUBTREE` and the `changes:` filter.

### 4. Push the anchor: tag AND `refs/notes/semantic-release`

If the service had previous releases from its upstream repo, anchor
the upstream's last version into the umbrella's history so
`semantic-release` picks up from a recognized starting point.

Two refs are required, not one: the version tag AND a note on
the same commit under `refs/notes/semantic-release`. semantic-release
locates the previous release by matching the tag prefix, but it
also reads `refs/notes/semantic-release` to know which commits have
been "released" already. With only the tag (no note), the
`release-notes-generator` plugin can misbehave: empty changelogs,
duplicate release entries, or in the worst case a re-publish of
commits already shipped under the old prefix. The nvcf-unbound
1.0.0 regression in May 2026 was caused in part by a missing
anchor; we then pushed only the tag and the next bump still went
sideways for a different reason.

The full anchor procedure for a service whose last upstream
release was `<last-upstream-version>`:

```bash
# 1. The version tag (matches the umbrella's tagFormat:
#    <service-name>-v<X.Y.Z>). Push BEFORE the cutover MR merges, so
#    the post-merge default-branch pipeline sees a recognized
#    predecessor instead of defaulting to 1.0.0.
git tag -a <service-name>-v<last-upstream-version> \
  -m "anchor: <svc> cutover continuity"

# 2. Fetch the existing notes ref BEFORE adding to it. `git fetch`
#    does not pull `refs/notes/*` by default, so the local copy is
#    empty/stale; without this step the next push gets rejected as
#    non-fast-forward and the natural --force "fix" would wipe out
#    every other service's release notes. The `|| true` covers the
#    case where origin has no notes ref yet (genuinely first release
#    in the repo).
git fetch origin '+refs/notes/semantic-release:refs/notes/semantic-release' || true

# 3. The semantic-release note. The note body needs to be a JSON
#    document semantic-release wrote on the original release; an
#    abbreviated stub is enough for the lookup to succeed.
#
#    Do NOT use `git notes add -f` here. In this monorepo the anchor
#    commit can already carry a different service's note (multiple
#    services frequently anchor at the same main HEAD). `-f` would
#    silently overwrite it, which causes that other service's next
#    release-notes-generator run to lose its "already released"
#    marker -- exactly the bug this section exists to prevent.
#    Check whether a note already exists and merge by hand if so.
TAG_SHA=$(git rev-parse <service-name>-v<last-upstream-version>^{commit})
if git notes --ref=refs/notes/semantic-release show "${TAG_SHA}" \
     > /dev/null 2>&1; then
  echo "ERROR: a semantic-release note already exists on ${TAG_SHA}" >&2
  echo "(likely another service has already anchored at this commit)." >&2
  echo "Inspect with: git notes --ref=refs/notes/semantic-release show ${TAG_SHA}" >&2
  echo "Then merge manually instead of using -f." >&2
  exit 1
fi
git notes --ref=refs/notes/semantic-release add -m \
  '{"channels":[null],"name":"v<last-upstream-version>","gitHead":"'"${TAG_SHA}"'","gitTag":"<service-name>-v<last-upstream-version>"}' \
  "${TAG_SHA}"

# 4. Push both refs (ci.skip on the tag so it doesn't fire a publish
#    pipeline; the existing artifacts at that version stay
#    untouched). The notes push must be a fast-forward; do NOT
#    --force, that would clobber every other service's release notes.
git push origin <service-name>-v<last-upstream-version> -o ci.skip
git push origin refs/notes/semantic-release
```

If the service is genuinely new (no prior releases), skip the
anchor entirely. The first release will be `1.0.0`
(semantic-release default).

#### Why both refs

- The tag lets `semantic-release-monorepo`'s tag-prefix lookup find
  the predecessor version.
- The note lets `release-notes-generator` know which commits have
  already been published, so the next changelog only contains
  commits since the anchor.

Without the note, the next changelog will include every commit in
git history (because semantic-release sees no record of a prior
release), and the release object will reference all of them.

### What the first release after cutover looks like

The cutover MR's commit type decides whether the first
umbrella-managed release bumps minor or patch:

- `feat(<svc>): cut over to monorepo-native` -> `feat:` -> minor
  bump. Example: anchor `<svc>-v0.5.8` plus a `feat:` cutover
  commit produces `<svc>-v0.6.0`, not `<svc>-v0.5.9`. The patch
  line resets to 0.
- `chore(<svc>): cut over to monorepo-native` -> `chore:` -> patch
  bump. Same anchor produces `<svc>-v0.5.9`.

Both are defensible. The cutover changes how an artifact is built
and released (new pipeline, new image registry token scoping,
chart version derivation), so calling it `feat:` carries real
release-flow signal for operators reading the changelog. If you
care more about keeping the patch line continuous with the
upstream's last release, use `chore:`.

The first six native cutovers in the umbrella all used `feat:`
(grpc-proxy, ratelimiter, nats-auth-callout, function-autoscaler,
http-invocation, nvcf-unbound), so each landed on the next minor
above the anchor. Future cutovers should follow the same convention
unless you have a specific reason to prefer patch continuity.

## Troubleshooting

### Tag pipeline fails YAML validation with "X needs Y but Y is not in any previous stage"

Some release job has a hard `needs:` on a job whose rules do not
include `$CI_COMMIT_TAG`. Either add the tag rule to the depended
job's rules or mark the `needs:` as `optional: true`. Fixed for the
helm-package case in NVCF-10337 follow-up.

### Slack notification says "tag: unknown"

The `git describe` fallback used to fail on shallow CI clones. The
template now prefers `${CI_COMMIT_TAG#<service-name>-v}` first, with
`git describe` as a fallback. If you still see this, the tag pipeline
fired without a matching `<service-name>-v*` tag.

### Two slack notifications per release

Two pipelines (default-branch + tag) both fired the slack-notify
rule. The template now scopes slack-notify to `$CI_COMMIT_TAG` only
so it fires once. If you see two, the rule must have an unintended
`$CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH` entry.

### "Cannot lock ref refs/notes/semantic-release"

Multiple `semantic-release-<svc>` jobs raced on the shared notes
ref. The template sets `resource_group: semantic-release-notes` so
GitLab serializes them. Retrying the failed job usually succeeds
since the ref has settled by then.

### Image push 403 on the base image fetch

The push token's scope leaked to the public base-image pull. Make
sure `docker_auth_path` on the affected target is set to the
push destination's full path (e.g.
`nvcr.io/nv-ngc-devops/<image>`), not just `nvcr.io`. Without the
scope, `rules_oci` applies the scoped push token to every nvcr.io
URL it pulls during the build, including the public distroless
base, and 403s. Fixed in NVCF-10337 / !294.

## Known shortcomings and follow-ups

Items the current shape works around or punts; tracked here so the
next contributor can either improve them or know why we live with
them.

### Vault-fetched personal token instead of `CI_JOB_TOKEN`

`compute-next-release-version-<svc>` and `semantic-release-<svc>`
both fetch a token from `kv/gitlab/semantic-release/gl-token` via
the vault-reader template. That token has broader scope than ideal
and the secret has to live in vault.

`CI_JOB_TOKEN` is the more secure default (auto-scoped, ephemeral),
but it doesn't work with `@semantic-release/gitlab` because the
plugin needs API permissions (create release objects, push tags,
write notes) that `CI_JOB_TOKEN` lacks on protected branches
without explicit project allow-list config.

The standard workaround is to drop `@semantic-release/gitlab`
entirely: use `semantic-release` only to compute the next version,
then create the GitLab release object via the official `release-cli`
(which accepts `CI_JOB_TOKEN`) in a follow-up job. This would
eliminate the vault dependency for releases. Not done yet; happy to
take a patch.

### NGC chart pushes are not actually idempotent

NGC's helm registry rejects republishing the same chart version
(immutability). The umbrella's `helm-push-ngc` jobs default to
`ngc-duplicate: skip`, which means the cds-component silently
swallows the rejection on re-push attempts. Net behavior: the
second push exits 0 and nothing changes, which looks idempotent
from the pipeline's perspective but isn't truly idempotent at the
registry layer.

This matters when:
- A release-worthy commit gets re-published by accident (the tag
  pipeline re-fires after the default-branch one). The second push
  is a no-op via skip.
- An attempt to overwrite a published chart on the same version
  requires changing `ngc-duplicate: overwrite` on the component
  call AND manual coordination with NGC owners. Not supported by
  the current generator output.

### OCI image re-push semantics depend on the org

`oci_push` will attempt to move the tag pointer on a re-push.
Different NGC orgs configure registry immutability differently;
some accept overwrites, some reject. The umbrella relies on the
target orgs being permissive. The first push (default-branch
pipeline) is the canonical one; tag-pipeline re-push is best-effort
and we accept its outcome silently.

## Related references

- `tools/ci/subproject-validations.yaml`: source of truth for which
  services have a release block and how they are configured.
- `tools/generate-subproject-ci/main.go`: the generator that emits
  the YAML in `tools/ci/generated-release-jobs.yml`.
- `tools/ci/generated-release-jobs.yml`: generated output the
  umbrella `.gitlab-ci.yml` includes; do not hand-edit.
- `BAZEL.md` at the repo root: Bazel-build-related conventions.
- `deploy/stacks/self-managed/.gitlab-ci.yml`: the self-managed
  stack's release flow (helmfile-based bundle, not individual chart
  push). Different shape from service releases; same semantic-release
  driver.
