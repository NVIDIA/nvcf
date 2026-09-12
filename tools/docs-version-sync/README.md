# Documentation Version Sync

This tool keeps the artifact versions in the top-of-tree documentation aligned
with the latest stable self-managed stack release. Top-of-tree documentation is
under `docs/user/`, but its version catalog follows a tagged stack release, not
an untagged `main` commit.

## Release and documentation flow

```text
merge release-worthy stack change to main
  -> release automation creates the self-managed stack tag and GitHub Release
  -> the tag workflow attaches nvcf-self-managed-stack-inventory.json
  -> a maintainer runs docs-version-sync
  -> the catalog records public locations or Publication pending
  -> generated blocks under docs/user/ are updated in a Pull Request
```

The stack release is automatic. A merge to `main` runs
[`release-tags.yml`](../../.github/workflows/release-tags.yml), which invokes
`tools/ci/github-release auto` for every registered subproject. A `feat:`,
`fix:`, or `perf:` commit that changes `deploy/stacks/self-managed/` creates the
next `deploy/stacks/self-managed/vX.Y.Z` tag and GitHub Release. Chart pin and
Helmfile changes in that subtree should use a release-worthy commit type. No
maintainer normally creates the stack tag by hand.

The tag workflow renders the states owned by the self-managed distributable.
That currently includes the control plane and the shared observability state
used by its observability profile. It excludes the independently released
compute-plane stack. The workflow attaches the resolved chart and image
inventory before publishing the GitHub Release. Public artifact publishing is
a separate process and can happen later after QA.

Changes limited to `deploy/stacks/nvcf-compute-plane/` or the standalone
`deploy/stacks/observability/` stack create releases for those registered
subprojects. Their independently owned inventories and aggregate documentation
sync are tracked separately from this self-managed flow.

## Sync after an automatic stack release

Wait for the GitHub Release and its inventory asset, then run:

```bash
git fetch --tags origin
go run -C tools/docs-version-sync . --target main --update-catalog
```

The command selects the latest stable self-managed stack release unless
`--stack-version X.Y.Z` is supplied. It updates:

- `docs/version-catalog/main.yaml`
- Generated blocks configured by the catalog under `docs/user/`
- The self-managed bundle version

The update retains publication records only when `name`, `type`, and `version`
still match. New and changed versions without a matching record are added to
`publication_pending`.

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

## Add an artifact to the stack inventory

For the complete dependency workflow, including ownership, optionality, and
indirect images, see
[`deploy/stacks/INVENTORY.md`](../../deploy/stacks/INVENTORY.md).

For a chart or image deployed by the self-managed stack:

1. Add it to the owning Helmfile or chart values.
2. Confirm the inventory renderer includes the path:
   - A default release in an existing state is discovered automatically.
   - For a release behind a new optional setting, add that setting to the
     matching `fullOverrides` entry in
     [`release-inventory.yaml`](../../deploy/stacks/self-managed/release-inventory.yaml).
   - For a new Helmfile state, add a `states` entry in that file.
   - If an independently released chart must be rendered from its immutable
     GitHub tag, add it to
     [`release-inventory.yaml`](../../deploy/stacks/self-managed/release-inventory.yaml).
3. Merge the release-worthy change to `main`. Release automation creates the
   self-managed stack release.
4. Run the documentation sync after a self-managed stack release containing
   the change appears with its inventory asset.
5. Add a `manifest.entries` record for the new artifact.
6. Add a verified public publication or leave the artifact pending.

A catalog-backed manifest entry uses `artifact_id` and describes the deployment
plane, kind, requirement, purpose, and public source links. The generator fails
when a discovered artifact has no classification.

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

## Validation

Run the focused Go checks when changing tool behavior:

```bash
go test -C tools/docs-version-sync ./...
go vet -C tools/docs-version-sync ./...
```

The operation is complete when the offline sync check, current-release check,
and full documentation check pass, and the generated diff contains only the
expected catalog and documentation updates.
