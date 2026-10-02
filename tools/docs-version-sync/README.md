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

Documentation uses one edition selector and five tabs. Each stable edition
reads these source trees from its protected release branch:

| Tree | Tab | Versioned |
| --- | --- | --- |
| `docs/overview/` | Overview (matrix, quickstart, manifest, image mirroring) | Yes |
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
Do not cut new folders with `cut-docs-version.sh` or `--freeze-stack`; edition
catalogs reject that workflow. Use the edition commands below to prepare a
qualified three-stack combination on its own branch.

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
  --source <full-reviewed-commit> --version 1.0.0 --change initial \
  --self-managed 1.0.1 --compute-plane 1.0.0 --observability 1.0.0 \
  --out /tmp/nvcf-docs-edition-1.0.0
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
  --version 1.0.0 --commit <full-docs-release-branch-commit>
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

`fern/navigation.yml` contains five tabs and explicit page slugs. Preparation
converts current absolute product links to relative source-file links so Fern
keeps them inside the selected edition. Existing exact redirect aliases and
anchors are preserved. Explicit historical-version links retain their archive
URLs. Missing current-page targets fail preparation.

The `fern/edition-preview.yml` configuration makes Development the default
for PR previews. It uses the same conversion in the helper's temporary clone.
Frozen files are not rewritten. For a manually staged clone, run:

```bash
go run -C tools/docs-version-sync . edition links --repo /path/to/staged-clone
```

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
