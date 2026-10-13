# Documentation Version Sync

This tool keeps top-of-tree documentation aligned with three released stack
inventories. It renders the cross-stack compatibility matrix and prepares
branch-based SemVer docs editions for qualified three-stack combinations.

## Release and documentation flow

```text
merge stack change to main
  -> a maintainer cuts or updates release-deploy/stacks/<stack>/vX.Y
  -> a push to that branch creates the stack tag and GitHub Release
  -> the tag workflow attaches that stack's inventory JSON
  -> a maintainer runs docs-version-sync
  -> the catalog records public locations or Publication pending
  -> generated blocks under the documentation product trees are updated
```

Stacks release only from release branches. `main` never cuts a stack version.
A push to `release-deploy/stacks/<stack>/vX.Y` runs
[`release-tags.yml`](../../.github/workflows/release-tags.yml), which invokes
`tools/ci/github-release auto` and cuts the next `X.Y.Z` tag when the push
changes something the stack ships. `deploy/stacks/<stack>/VERSION` names the
next train. See [`RELEASE.md`](../../RELEASE.md) for branch cutting.

The tag workflow renders only the states owned by the tagged stack. It attaches
the resolved chart and image inventory before publishing the GitHub Release.
Release attachment is keyed by tag, not by branch. Public artifact publishing
is a separate process and can happen later after QA.

## Documentation source trees

Documentation uses one edition selector and six tabs. Each stable edition
reads these source trees from its protected release branch:

| Tree | Tab | Versioned |
| --- | --- | --- |
| `docs/overview/` | Overview guides, Manifest, and Release Notes (matrix and upgrade guides) | Yes |
| `docs/self-managed/` | Self-Managed Stack (control plane) | Yes |
| `docs/compute-plane/` | Compute Plane Stack | Yes |
| `docs/observability/` | Observability Stack | Yes |

Generated blocks live only in these four trees. The native Release Notes tab
reads `fern/changelog/`. Existing frozen copies (`docs/<stack>-<version>/`)
remain historical archives and are never regenerated. New editions create a
branch, not a new source directory. See [the release and rollback workflow](../../docs/dev/docs-editions.md).

## Compatibility matrix

`docs/overview/compatibility-matrix.md` carries the `compatibility-matrix`
generated block. It renders the current release of each stack from
`release_set.stacks` and the declared `compatibility` entries:

```yaml
compatibility:
  - stack: observability
    version: "1.1.3"
    compatible_with:
      control-plane: "1.0.0+"
      compute-plane: "1.0.0+"
```

Each entry names one stack release and the minimum release of the other two
stacks it works with: `X.Y.Z+` means that release or later, and `X.Y.Z` means
that release only. The example renders as "Observability 1.1.3 works with
Self-managed 1.0.0 or later, Compute plane 1.0.0 or later". Stack names use the `release_set` keys
(`control-plane`, `compute-plane`, `observability`). Add an entry when a stack
release changes compatibility, and raise a minimum when a release stops working
with an older release of another stack. Then regenerate the documentation.

Each registered stack publishes its own inventory:

- `nvcf-self-managed-stack-inventory.json`
- `nvcf-compute-plane-stack-inventory.json`
- `nvcf-observability-stack-inventory.json`

No stack inventory references another stack's Helmfile state.

## Sync after an automatic stack release

Wait for the GitHub Release and its inventory asset, then run:

```bash
git fetch --tags origin
go run -C tools/docs-version-sync . --target main --update-catalog
```

The command selects the latest stable release for all three stacks and records
each stack as development documentation. It updates:

- `docs/version-catalog/main.yaml`
- Generated blocks configured by the catalog under the current source trees
- The self-managed, compute-plane, and observability bundle versions
- The exact source tag, commit, and inventory asset for all three stacks

The update retains publication records only when `name`, `type`, and `version`
still match. It also records a public upstream repository when the released
stack inventory already references one directly. Those upstream artifacts are
not added to `publication_pending`. New and changed versions that target the
NVCF publication repositories still require an exact publication record or a
`publication_pending` entry.

If the command reports an unclassified artifact, add its metadata under
`manifest.entries` as described below and rerun the command. Do not edit a
generated documentation block by hand.

## Record a public publication

The sync does not probe NVIDIA NGC or another registry. Verify publication
separately, then edit
[`docs/version-catalog/main.yaml`](../../docs/version-catalog/main.yaml):

1. Add an exact `publications` record using the artifact `name`, `type`, and
   inventory `version`.
2. Use a registry entry that resolves to a public host and namespace.
3. Remove the artifact catalog ID from `publication_pending`.
4. Regenerate the documentation without `--update-catalog`.

For example:

```yaml
publications:
  - name: example-service
    type: image
    version: 1.2.3
    registry: public-images
```

Set `chart_format: http` for a chart in the public HTTP Helm repository. Set
`published_version` when the public tag differs from the inventory version.
Add a new entry under `registries` only when the public host or namespace is not
already represented. Private registry locations are rejected.

Run:

```bash
go run -C tools/docs-version-sync . --target main
./tools/ci/check-doc-version-sync
./tools/ci/check-doc-version-current-release
./tools/ci/check-docs
```

A publication-only update does not require a new stack release.

## Historical per-stack snapshots

Existing per-stack folders and their catalogs are retained for historical URLs.
The per-stack folder-cut script and freeze flags have been removed. Use the
edition commands below to prepare a qualified three-stack combination on its
own branch.

## Add an artifact to the stack inventory

For the complete dependency workflow, including ownership, optionality, and
indirect images, see
[`deploy/stacks/INVENTORY.md`](../../deploy/stacks/INVENTORY.md).

For a chart or image deployed by any stack:

1. Add it to the owning Helmfile or chart values.
2. Confirm the inventory renderer includes the path:
   - A default release in an existing state is discovered automatically.
   - For a release behind a new optional setting, add that setting to the
     matching `fullOverrides` entry in
     owning `release-inventory.yaml`.
   - For a new Helmfile state, add a `states` entry in that file.
   - If an independently released chart must be rendered from its immutable
     GitHub tag, add it to
     owning `release-inventory.yaml`.
3. Merge the change to `main`, then land it on the owning stack's release
   branch. A push to the release branch creates the stack release.
4. Run the documentation sync after all selected stack releases have inventory
   assets.
5. Add a `manifest.entries` record for the new artifact description and source.
6. Add a verified public publication or leave the artifact pending. No
   publication record is needed when the stack inventory already references a
   supported public upstream repository.

A catalog-backed manifest entry uses `artifact_id` and describes the deployment
plane, kind, purpose, and public source links. Released inventory data supplies
stack ownership and required or optional status. The generator fails when a
discovered artifact has no classification.

## Add an independently versioned artifact

Use `supplemental_artifacts` for a tool, deployment resource, or separately
installed add-on that is not emitted by the stack inventory.

- Add the artifact to `supplemental_artifacts`.
- Add its `manifest.entries` record.
- For an independently versioned resource, update its `version_overrides` and
  supplemental version together.
- Add an exact publication record or keep it in `publication_pending`.

Supplemental charts and images must be referenced by `manifest.entries` or the
next stack refresh removes them. Supplemental resources survive a catalog
refresh automatically, but they still require a manifest entry to render.

## Remove or exclude an artifact

For a real removal, remove the artifact from the stack. After the next release
sync, remove its stale manifest metadata. Use `denylist` only when an artifact
is intentionally excluded from public documentation, and give it a public-safe
reason. Do not use a private registry as a placeholder for an unpublished
artifact.

## Prepare an umbrella product manifest

The opt-in `umbrella` commands prepare a product manifest independently of docs
editions. Existing `edition` commands, catalogs, registries, and publication
defaults keep their current behavior.

Review the exact three-stack catalog at a full source commit. Download the
inventory asset from each selected stack release into one directory, and fetch
the corresponding immutable source tags. The command validates those local tags
and inventory bytes; it does not discover latest versions or publish anything.

```bash
git fetch origin --tags
go run -C tools/docs-version-sync . umbrella prepare \
  --source <full-reviewed-catalog-commit> \
  --version <umbrella-version> --change initial \
  --helm-version <exact-3.x-version> --helmfile-version <exact-1.1.x-version> \
  --inventories /tmp/selected-stack-inventories \
  --out /tmp/umbrella-manifest.json

go run -C tools/docs-version-sync . umbrella check \
  --manifest /tmp/umbrella-manifest.json \
  --inventories /tmp/selected-stack-inventories
```

The default status is `development`. Supply `--qualification` with the public
review URL only after the complete combination is qualified. This records the
reviewer's declaration; an approval URL is not proof that runtime tests passed.

For a subsequent product version, supply `--previous` with its qualified
predecessor manifest and use `--change patch`, `minor`, or `major`. Pass the same
predecessor to `umbrella check`. Component downgrades and changes to an existing
component version's source or inventory are rejected. A component major requires
an umbrella major; a component minor requires at least an umbrella minor.

Manifest schema version 1 uses these fields:

| Field | Meaning |
| --- | --- |
| `version`, `previous_version`, `change` | Product version and deployment-contract transition |
| `status`, `qualification` | Development candidate or qualified combination with public review |
| `source_commit` | Reviewed catalog commit used to select the combination |
| `toolchain.helm`, `toolchain.helmfile` | Exact supported render tool versions |
| `stacks` | Exactly `control-plane`, `compute-plane`, and `observability` |
| `stacks.<name>.version` | Independent component stack version |
| `source_tag`, `source_commit` within each stack | Immutable component source identity |
| `inventory_asset`, `inventory_sha256` within each stack | Canonical released inventory filename and digest of its exact bytes |
| `compatible_with` within each stack | Reviewed requirements for the other two component stacks |

`control-plane` identifies the Self-Managed stack and retains the catalog's
existing key. The three component versions do not have to match.

Product manifests use `deploy/releases/X.Y.Z/manifest.json` and the reserved tag
format `nvcf/X.Y.Z`. That tag format avoids the existing component publisher's
`*-v*` and `**/v*` triggers. Preparation creates neither the tag nor a GitHub
release. If the product tag is present locally, validation requires the exact
manifest bytes recorded at that tag. Fetch release tags before validation.

Repeated preparation with identical inputs verifies the existing output. A
different output is rejected. Bundle and manifest checksums belong in a separate
release asset index, so the manifest never needs to contain its own hash or the
hash of an archive that embeds it.

Documentation corrections do not change this product manifest or its version.
This preparation command does not change the current immutable-docs-edition
policy; maintained umbrella documentation requires the separate docs workflow.

Run the command-level integration test and existing regressions with:

```bash
go test -C tools/docs-version-sync ./...
./tools/ci/check-doc-version-sync
tools/ci/preview-docs --check
```

The integration test builds the real command, prepares and checks independent
fixture releases, rejects changed source identities and released manifests, and
exercises the legacy edition commands through the same binary.

## Assemble an umbrella bundle

Build a distribution from the prepared manifest and the same inventory inputs:

```bash
go run -C tools/docs-version-sync . umbrella bundle \
  --manifest /tmp/umbrella-manifest.json \
  --inventories /tmp/selected-stack-inventories \
  --out /tmp/umbrella-release-assets
```

For the first assembly, `--out` must not already exist. Do not pre-create an
empty output directory.
An existing directory is accepted only when it contains the exact complete
asset set from an earlier identical assembly.

Supply `--previous` for a non-initial release, as with manifest validation.
Assembly reads each stack from its selected Git commit, never the working tree
or another stack's incidental checkout. It retains the sibling layout:

```text
manifest.json
inventory.json
bundle-files.json
inventories/
deploy/stacks/self-managed/
deploy/stacks/nvcf-compute-plane/
deploy/stacks/observability/
```

The archive also includes the reviewed root LICENSE and NOTICE, operator
instructions, and VERSION files. Each stack's VERSION is stamped with its own
artifact version. Where available, `Makefile.dist` becomes the distribution
Makefile. Base environments, local charts, Helmfile states, values, scripts, and
secret templates are included. Tests, local environment overrides, dotfiles,
and customer secret files are excluded. Source symlinks are rejected.

`release-assets.json` sits outside the archive. It records SHA-256 digests for
the archive, manifest, combined inventory, and three constituent inventories.
The archive's `bundle-files.json` records each payload file's digest and mode.
Identical inputs produce identical archives. Repeating assembly verifies
existing output; it does not overwrite different assets.

Verify a downloaded archive against the manifest and the archive digest from a
trusted release asset index:

```bash
go run -C tools/docs-version-sync . umbrella verify-bundle \
  --manifest /tmp/umbrella-release-assets/manifest.json \
  --bundle /tmp/umbrella-release-assets/nvcf-<version>.tar.gz \
  --sha256 sha256:<archive-digest>
```

Verification checks the archive digest, embedded manifest, file index, stack
versions, and inventories. It rejects links, path traversal, duplicate paths,
and oversized archives. It does not install or extract into an existing cluster.
Use the bundle's README for extraction-layout and per-plane render commands.
Chart/image payloads and the CLI binary remain separate downloads.

The command E2E builds twice from different working-tree states, verifies
downloaded assets, extracts the archive, and renders control-plane, compute-plane,
and enabled/disabled observability fixtures with real Helm and Helmfile:

```bash
NVCF_UMBRELLA_RENDER_E2E=1 go test -C tools/docs-version-sync \
  -run TestUmbrella -count=1 -v
```

The dedicated umbrella validation workflow installs checksum-pinned render tools
and requires this test. The ordinary Go suite skips rendering only when the
tools are absent; setting the variable makes missing tools a failure. These
isolated fixture renders do not qualify a production stack combination.

## Compare release sets

Store each release set's inventory JSON files in a directory, then run:

```bash
go run -C tools/docs-version-sync . \
  --compare-release-set-from /tmp/previous-inventories \
  --compare-release-set-to /tmp/current-inventories
```

The report detects dependency disappearance and explains additions, reference
or digest changes, ownership changes, and required or optional changes. A
historical directory can contain one legacy combined inventory. A current
directory normally contains three separated inventories.

Use the `inventory_tag` input on `release-tags.yml` to reproduce an inventory
without publishing a release. The preflight uploads the JSON as a workflow
artifact. Tags that lack a config with per-stack states use the config from the
workflow ref as a one-time bootstrap. They use an available tagged source chart
and otherwise render the published chart version. New tags carry their own
config.

## Validation

Run the focused Go checks when changing tool behavior:

```bash
go test -C tools/docs-version-sync ./...
go vet -C tools/docs-version-sync ./...
```

The operation is complete when the offline sync check, current-release check,
and full documentation check pass, and the generated diff contains only the
expected catalog and documentation updates.

## Docs edition preparation

The canonical registry in `fern/docs.yml` selects stable editions from protected
branches. `fern/editions.yml` pins their exact commits. The main catalog remains
a development candidate; each release branch holds its own qualified catalog.
Legacy catalogs without `docs_edition` retain their historical validation behavior.

A docs edition records one exact combination of all three stack releases. Its
SemVer version is independent of stack artifact versions. Declare `initial`
for the first edition, then `patch`, `minor`, or `major` against the registered
previous edition. A stack major requires an edition major; a stack feature
release requires at least an edition minor. A docs-only patch can retain all
three stack versions.

First synchronize the three explicit stack inventories with the existing
`--update-catalog` options and review that source commit. The source must
contain `fern/navigation.yml`. Then prepare a candidate outside the checkout:

```bash
go run -C tools/docs-version-sync . edition prepare \
  --source <full-reviewed-commit> --version 1.0.1 --change initial \
  --self-managed 1.0.1 --compute-plane 1.0.0 --observability 1.0.0 \
  --out /tmp/nvcf-docs-edition-1.0.1
```

Preparation verifies that the explicit stack versions match the reviewed
catalog, checks compatibility declarations and source tags, and writes a
candidate catalog, `docs/edition-manifest.json`, and a branch-local default
navigation. It stages the reviewed source in a separate Git clone. It does
not copy a version directory into the source checkout. Repeating an identical
preparation succeeds; different inputs or modified prepared content are
rejected without overwriting files.

The candidate is `development` by default. Qualification is a human decision
about the complete combination. After review, prepare with `--qualification`
pointing to the public issue or PR that records that approval. Providing a URL
is a declaration of approval, not automatic evidence that QA passed. The
reviewer must verify it. Artifact publication alone never qualifies an edition.

For a later edition, also supply `--previous-version` and the appropriate
`--change`. Preparation checks stack transitions against that registered
predecessor. A qualified catalog cannot be refreshed through the development
latest-release updater.

The `Prepare Docs Edition` workflow performs the same preparation and attaches
sources and a diff for review. It has read-only repository permissions and does
not create a published branch or activate a registry.

After reviewing the prepared content and qualification, create and commit a
`docs/releases/X.Y.Z` branch from the prepared clone. Retain its branch-local
`path` default; never replace it with the canonical site's Latest `ref`.
Protect that exact branch against updates, force pushes, and deletion before
registration. Publication administrators must verify the branch rules; commit
checks detect drift but do not replace branch protection.

Register its exact branch commit from the canonical checkout:

```bash
go run -C tools/docs-version-sync . edition register \
  --version 1.0.1 --commit <full-docs-release-branch-commit>
./tools/ci/check-doc-editions
```

Registration updates `fern/editions.yml` only. Activation of the matching Fern
version entry remains a reviewed navigation change. Identical registration is
idempotent; a published edition cannot be rebound to a different commit.

The edition check resolves remote branch heads, checks each qualified catalog
and its stack sources, validates its own default navigation, and checks its
generated content and Fern configuration in an isolated checkout. Missing or
moved refs, stale manifests, and unregistered Fern refs fail. `--local-refs` is
available for isolated local rehearsals; publishing always checks the remote.
Existing editions are validated against their recorded stack versions, so a
new artifact release does not invalidate historical documentation.

### Edition navigation and links

`fern/navigation.yml` contains six tabs and explicit page slugs. Preparation
converts current absolute product links to relative source-file links so Fern
keeps them inside the selected edition. Existing exact redirect aliases and
anchors are preserved. Explicit historical-version links retain their archive
URLs. Missing current-page targets fail preparation.

`tools/ci/preview-docs` derives a Development-first configuration from
`fern/docs.yml` in a temporary clone and applies the same link conversion.
Site settings, redirects, release refs, and archive entries come from the
canonical configuration. A prepared release branch uses its local default.
Frozen files and the source working tree are not rewritten.

```bash
tools/ci/preview-docs --check
```

Set `DOCS_PREVIEW_CONFIG=fern/docs.yml` to preview the canonical ordering, or
name another YAML file directly under `fern/` to preview an alternate config.
Overrides are used as supplied, without reordering or link normalization.
For a manually staged disposable clone, run:

```bash
go run -C tools/docs-version-sync . edition preview --repo /path/to/staged-clone
```

Use `edition links` instead of `edition preview` when only link conversion is
needed. Both commands modify staged files; do not run them on the source
working tree.

Release branches retain hidden path-based historical versions after their own
default. Fern reads only that default when composing the branch as a ref in the
canonical site. The central site retains the archive entries too, so old product
URLs can resolve to their original source pages without entering the edition
selector. Full hosted route/content checks are required before activating this
adapter.

Native release notes live in `fern/changelog/`. Dated MDX files use `##` headings
for cards and frontmatter tags for filters. Keep links relative and record the
edition and exact artifact versions in release entries. Qualification remains a
reviewed declaration; a development entry is not release approval.
