# Documentation Version Sync

This tool keeps top-of-tree documentation aligned with three released stack
inventories. It renders the cross-stack compatibility matrix and writes the
catalog snapshot that freezes one stack's exact documentation version.

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

## Documentation product trees

Documentation is split into one Fern product per stack plus a shared overview:

| Tree | Product | Versioned |
| --- | --- | --- |
| `docs/overview/` | Overview (matrix, quickstart, manifest, image mirroring) | No |
| `docs/self-managed/` | Self-Managed Stack (control plane) | Yes |
| `docs/compute-plane/` | Compute Plane Stack | Yes |
| `docs/observability/` | Observability Stack | Yes |

Generated blocks live only in these four trees. Frozen copies
(`docs/<stack>-<version>/`) are never regenerated; the catalog rejects output
paths that point at them.

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
- Generated blocks configured by the catalog under the product trees
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

## Freeze a stack documentation version

Each stack freezes documentation on its own schedule. No joint qualification
of all three stacks is required. After QA approves stack release `X.Y.Z` and
its artifacts are published:

```bash
git fetch --tags origin
go run -C tools/docs-version-sync . --target main --update-catalog
go run -C tools/docs-version-sync . --target main
./tools/scripts/cut-docs-version.sh --stack observability --version X.Y.Z
```

The cut script runs `--freeze-stack <release_set stack> --freeze-version X.Y.Z`,
which checks that the stack's current release in `release_set.stacks` is
`X.Y.Z`, warns when `publication_pending` is non-empty (the frozen
manifest keeps the pending markers), and writes
`docs/version-catalog/<stack>-X.Y.Z.yaml` with that one stack marked
`qualified`. `main.yaml` stays in development state. The script then copies
`docs/<stack>/` to `docs/<stack>-X.Y.Z/`, generates
`fern/products/<stack>/X.Y.Z.yml`, and prints the `versions:` entry to add to
`fern/docs.yml`.

Stack names for `--freeze-stack` are `control-plane`, `compute-plane`, and
`observability`. The script accepts the product slugs `self-managed`,
`compute-plane`, and `observability` and maps them. Overview documentation is
unversioned and is never cut.

Add or adjust `compatibility` entries for the new release before regenerating so
the matrix reflects the qualified combination.

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
