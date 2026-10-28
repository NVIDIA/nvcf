# Release an umbrella version

An NVCF umbrella version identifies one qualified combination of the
Self-Managed, Compute Plane, and Observability stacks. Their versions remain
independent. For example, a product release could include Observability 1.0.0,
Self-Managed 1.0.1, and Compute Plane 1.0.2.

Product manifests, tags, and release assets are immutable. Documentation can
advance to another approved commit for the same product version. A prose fix
changes neither the product version nor its artifact checksums. Development
continues to read `main`.

This workflow is opt-in. Existing immutable docs editions and component release
workflows remain supported. See [docs editions](docs-editions.md) for that
legacy workflow and [tool commands](../../tools/docs-version-sync/README.md)
for the manifest and publisher interfaces.

## Prepare the product

1. Inventory published and reserved docs/product versions. Allocate an unused
   version; never assign an existing docs URL to a different stack combination.
2. Publish each stack independently. Select exact public stable Self-Managed,
   Compute Plane, and Observability releases, download their inventory assets,
   and fetch their conventional source tags. Stack maintenance branches need not
   wait for umbrella qualification to merge or publish.
3. Choose the product SemVer change from the highest required change to the
   supported deployment contract. A prose-only correction needs no product bump.
4. Prepare a release-local manifest using all three version flags. The tool
   resolves tag commits and inventory hashes; the Development docs catalog does
   not select these pins. Choose a full reviewed source commit for provenance,
   `LICENSE`, and `NOTICE`. The candidate and final publication targets must
   descend from that source commit.
   The initial release omits `--previous`; later releases supply the preceding
   qualified manifest and use `major`, `minor`, or `patch` as appropriate.

```bash
go run -C tools/docs-version-sync . umbrella prepare \
  --repo /path/to/nvcf --source FULL_REVIEWED_SOURCE_COMMIT \
  --version X.Y.Z --change initial \
  --self-managed-version RELEASED_SELF_MANAGED_VERSION \
  --compute-plane-version RELEASED_COMPUTE_PLANE_VERSION \
  --observability-version RELEASED_OBSERVABILITY_VERSION \
  --helm-version EXACT_HELM_3_VERSION --helmfile-version EXACT_HELMFILE_1_1_VERSION \
  --inventories /path/to/exact-inventories --out /tmp/product-manifest.json

go run -C tools/docs-version-sync . umbrella bundle \
  --repo /path/to/nvcf --manifest /tmp/product-manifest.json \
  --inventories /path/to/exact-inventories --out /tmp/product-assets
```

This produces manifest schema 2. Its peer constraints lock exactly the selected
combination to qualify; they do not assert runtime compatibility before QA. The
manifest remains `development` until whole-combination qualification is recorded.
All three flags require plain `X.Y.Z` versions. No `latest`, branch, prerelease,
or build-metadata selection is accepted.

Local checks verify the fetched tags, inventory identities, ownership, hashes,
and combination consistency. Candidate CI and the final publisher additionally
check public stable release visibility and the original inventory asset bytes.
Existing schema 1 callers can omit the version flags to select the reviewed
catalog; their strict catalog validation remains unchanged.

Commit the candidate at `deploy/releases/X.Y.Z/manifest.json`. Pull requests
changing the manifest and pushes to the matching `release-nvcf/X.Y.Z` branch run
candidate CI. It validates and assembles the pins and uploads a QA artifact
named `umbrella-candidate-<sha>-<run-id>-<run-attempt>`. Pull requests use their
head commit SHA. It creates no stable tag or release.
Download that exact artifact for QA and retain its candidate SHA, manifest hash,
archive hash, and CI run with the results. The CI artifact expires after
30 days; retain required evidence in durable release storage. A pin change produces a new artifact
and requires qualification of the changed combination.

Keep implementation fixes in their owning component and stack maintenance
branches. An older candidate branch may need the reviewed candidate tooling
once; thereafter it carries the selection manifest and qualification metadata.
Do not merge the whole umbrella branch back into a stack maintenance branch.
Customer catalog publication of stack resources is not a prerequisite for
assembly: CI consumes the public stable source releases and inventories.

The archive preserves the sibling paths under `deploy/stacks/`. Each stack's
`VERSION` is its own version; root `VERSION` is the umbrella version. The archive
contains the product manifest, original inventories, combined inventory, and
an index of every packaged file. `release-assets.json` records asset digests.

Downloadable archives contain public runtime files and secret templates, never
local environments or credentials. Supply separate environment and secrets
files when testing or installing. Keep the exact packaged chart/image pins.

## Install with existing stack interfaces

Verify the downloaded archive against the released manifest and a trusted
archive digest before extracting it into a new directory:

```bash
go run -C tools/docs-version-sync . umbrella verify-bundle \
  --manifest /tmp/product-assets/manifest.json \
  --bundle /tmp/product-assets/nvcf-X.Y.Z.tar.gz \
  --sha256 sha256:TRUSTED_ARCHIVE_DIGEST

bundle_dir="$(mktemp -d)"
tar -xzf /tmp/product-assets/nvcf-X.Y.Z.tar.gz -C "$bundle_dir"
```

Use the bundle's README and existing per-stack Helmfile/Make entrypoints.
Preserve the `deploy/stacks/` layout so the self-managed stack can find its
sibling Observability stack. Select cluster contexts and environment/secrets
files explicitly, and follow the existing deployment and upgrade order.

The existing `nvcf-cli` remains unchanged. After verification and extraction,
its `--control-plane-stack` flag can select
`$bundle_dir/deploy/stacks/self-managed`, and `--compute-plane-stack` can select
`$bundle_dir/deploy/stacks/nvcf-compute-plane`. Its default sources do not select
an umbrella combination automatically. The CLI does not verify or report an
umbrella identity; retain the manifest and digest with the deployment evidence.

## Qualify and publish

Use fresh isolated environments and retain the candidate identity, toolchain,
configuration, and test results. Test fresh installation, supported upgrade
paths, repeat apply, rollback limits, single/split cluster topologies, and
supported observability profiles. Validate real workloads, original function
and deployment identities, migrations, registration, and invocation continuity.
Fixture chart tests validate the packaging machinery only. The
`Umbrella bundle E2E` workflow assembles and renders fixtures; it does not run
live single- or multi-cluster BDDs or approve the product.

Before QA sign-off, download every selected external chart through its customer
registry or retained public upstream route. Account for bundled charts and
subchart dependencies in the archive. Verify effective images after stack
values and overrides; chart `appVersion` alone is insufficient. Repeat these
checks when selected pins change. Record any engineering-registry testing
separately from completed customer-distribution checks.

Run qualification through the existing packaged Helmfile/Make entrypoints.
Any CLI-assisted step must use the existing component-source interface with
explicit paths to the verified bundle. No umbrella-aware CLI is required.

An umbrella selection does not make a staged upgrade atomic. Follow each
component's supported migration and major-version sequence. A docs rollback
does not roll back installed services or reverse database migrations.

After qualification, prepare a qualified manifest with `--qualification` set
to the public evidence review URL. This changes manifest and archive digests.
Rebuild and verify the final bytes; repeat the required installation/upgrade
checks with those final assets before publication. Commit the final manifest
and customer `notes.md` under `deploy/releases/X.Y.Z/`.

Before the first release, protect `refs/tags/nvcf/**` against updates and
deletions while permitting creation by the release workflow. Keep component tag
rules intact. The umbrella tag namespace intentionally does not match component
release triggers. Record the effective rules in the release review.

Run `Publish Umbrella Release` from `main` with `version=X.Y.Z` and
`target=FULL_REVIEWED_PRODUCT_COMMIT`. Use `mode=validate`, then `draft`.
Keep that full target SHA for retries and promotion, even if `main` advances.
The target must already belong to `main` and contain the final manifest and notes.
The workflow validates source identities and assets; it does not execute QA
or independently enforce a release-owner hold on external catalog publication.
The publisher uploads missing assets, verifies existing bytes, and leaves an
interrupted upload as a draft. Retry identical inputs. A checksum conflict
requires investigation; do not replace a published asset.

After the draft's complete asset readback and reviewed qualification, use
`mode=promote`. Verify anonymous downloads of every asset and record the
`nvcf/X.Y.Z` tag commit and manifest digest. Product publication does not change
the docs default or publish the site.

## Prepare and activate maintained docs

This section currently supports schema 1 products only. Schema 2 maintained
docs preparation and activation are deferred until selected inventories can
seed the docs catalog; see [the docs follow-up](https://github.com/NVIDIA/nvcf/issues/2798).
The commands reject schema 2 instead of copying unrelated Development pins.
Keep the existing published docs edition while that support is pending.

Protect `docs/versions/**` against deletion and force pushes. Require reviewed
updates and applicable docs checks; these branches must permit approved
corrections. Keep the stricter freeze on `docs/releases/**` unchanged.

Prepare the documentation from a reviewed source commit using the published
product identity:

```bash
go run -C tools/docs-version-sync . umbrella docs-prepare \
  --repo /path/to/nvcf --version X.Y.Z \
  --product-commit FULL_PRODUCT_TAG_COMMIT \
  --manifest-sha256 sha256:PRODUCT_MANIFEST_DIGEST \
  --commit REVIEWED_DOCS_SOURCE_COMMIT --out /tmp/nvcf-product-docs
```

Review the prose against the selected combination. The tool seeds the catalog
from the product's reviewed source and embeds the immutable product manifest.
Commit the prepared files on `docs/versions/X.Y.Z` and push that branch after
verifying its protection rules. Record the resulting full commit SHA.

From a canonical checkout, prepare the approval:

```bash
go run -C tools/docs-version-sync . umbrella docs-revise \
  --repo /path/to/nvcf --version X.Y.Z \
  --product-commit FULL_PRODUCT_TAG_COMMIT \
  --manifest-sha256 sha256:PRODUCT_MANIFEST_DIGEST \
  --commit REVIEWED_DOCS_REVISION_COMMIT
```

For initial activation, add the selector entry to `fern/docs.yml`:

```yaml
- display-name: "X.Y.Z"
  slug: "X.Y.Z"
  ref: docs/versions/X.Y.Z
```

Set `fern/editions.yml`'s `default` to the new version and put its selector
first. Retain every old version, all six tabs, Development, site settings,
redirects, anchors, and downloads. Review this small canonical activation diff
separately from the prepared sources and artifact publication.

Commit the reviewed canonical inputs before running the publisher check.
It requires clean `docs/` and `fern/` trees, including untracked and ignored
files. Preview checks also support working-tree edits.

Run the integration checks and both preview configurations:

```bash
go test -C tools/docs-version-sync ./...
bash tools/scripts/test/test-publish-docs
tools/ci/publish-docs --check
tools/ci/preview-docs --check
DOCS_PREVIEW_CONFIG=fern/docs.yml tools/ci/preview-docs --check
```

Publish isolated hosted previews from the exact reviewed canonical commit.
Compare default and explicit old/new version URLs, six-tab navigation,
Development, search, redirects, anchors, downloads, and changelog links with
the retained baseline. Check mobile, light, and dark presentation. Retain the
build receipt, preview URLs, and readback results. A successful preview job
alone does not prove navigation or search behavior.

Rehearse a same-version prose correction and a full-site rollback in staging.
Keep activation pending if a runtime or hosted gate is skipped or fails. Merge
only after review and passing gates, then verify the production URLs and source
identities after the main publisher completes.

## Correct docs without a release

Fix Development and each affected maintained version through reviewed changes.
On `docs/versions/X.Y.Z`, edit the applicable prose, regenerate changed manifest
presentation, and validate. Preserve the embedded product manifest and all
catalog artifact versions, source identities, distribution locations, and
compatibility inputs. Do not synchronize that catalog to newer stack releases.

After the maintained branch change lands, run `umbrella docs-revise` from
canonical `main` with the same version, product commit, and manifest digest,
and the newly reviewed docs commit. Review and merge the registry-only change.
The existing version URL and selector remain unchanged. No release workflow
runs. Advancing the maintained branch alone does not publish its new HEAD.

For a backport, apply only the correction appropriate to that version. Do not
copy Development's next-release catalog or unrelated feature instructions.
Legacy immutable editions continue to use their existing patch-edition process.

## Roll back docs

For a prose regression, use `umbrella docs-revise` to select an earlier approved
SHA retained in `umbrella.revisions`. Preview and merge that registry change.
The maintained branch may retain later history; approval controls publication.

Before initial activation, retain the last validated production commit and
Fern/Node/tool configuration. Prepare a rollback PR that restores the previous
canonical registry, selector, settings, redirects, and relevant tool changes.
Rehearse this complete configuration on an isolated hosted preview and retain
its URL smoke results. After an approved rollback publishes, repeat production
readback. Keep product releases, old immutable docs sources, and maintained
history intact.
