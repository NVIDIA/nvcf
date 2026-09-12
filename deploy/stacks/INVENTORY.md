# Stack Inventory Flow

Each deployable stack owns an inventory of the charts, images, and resources
that a customer must obtain. The self-managed, compute-plane, and observability
inventories are separate release assets. An inventory config must reference
only Helmfile states under its owning stack directory.

## Ownership Model

Keep each fact in one source:

- The owning Helmfile and chart values define runtime dependencies and versions.
- Each stack's `release-inventory.yaml` supplies release-time inventory
  configuration that cannot be derived from an ordinary render.
- The resolved JSON inventory records the artifacts found at an immutable stack
  tag. Release automation publishes it with the stack release.
- `docs/version-catalog/main.yaml` records public distribution locations and
  human-authored manifest classification.
- Generated blocks in `docs/user/manifest.md` present the catalog to users.

Do not maintain a second hand-written list of versions in the documentation.

## What Belongs in an Inventory

Include every artifact that the stack can cause a customer cluster to pull:

- The stack bundle.
- Helm charts installed by the stack.
- Images in containers and init containers.
- Images used by Helm hooks.
- Images created later by an operator or controller.
- Images passed through command arguments or configuration instead of a
  Kubernetes `image` field.
- Downloadable resources distributed as part of the stack.

An ACME HTTP-01 solver image is one example of an indirect image. A controller
creates its short-lived workload only when an ACME challenge is requested, so
a normal default workload list might not contain the image. The inventory
renderer also scans resolved command arguments shaped like `--*-image=<ref>`
for this reason. If a new dependency uses another indirect form, extend the
renderer and cover that form with a test.

## Required and Optional Rules

Use the default supported installation as the boundary:

- `required`: The default installation needs the artifact to become ready or
  perform its baseline function.
- `optional`: The artifact is needed only when a feature, provider, add-on, or
  non-default mode is enabled.

An image required by an optional component is still optional at the stack
level. Record the condition or feature that activates it in the artifact
description. Customer-provided cluster prerequisites are not distributed stack
artifacts. Document them as prerequisites instead.

## Contributor Flow

When adding, removing, or changing a dependency:

1. Change the owning Helmfile, values, or chart configuration.
2. Add an explicit condition for an optional release.
3. Update the state in the owning stack's `release-inventory.yaml`.
   Add the optional setting to its `fullOverrides`, or add a state entry when
   the dependency belongs to a new Helmfile state.
4. Add a `sourceCharts` entry when an independently released chart must be
   rendered from an immutable source tag. Update the renderer when an indirect
   artifact uses an input form it does not recognize.
5. Add or update tests that prove the resolved inventory contains the artifact
   and its source release.
6. Update `manifest.entries` in `docs/version-catalog/main.yaml`. Set the plane,
   kind, requirement, public description, and public source link.
7. For a new third-party dependency, verify its license against
   `.allowed-licenses.txt` and update `NOTICE` or subtree attribution files as
   required.
8. Run:

   ```bash
   make -C deploy/stacks/self-managed test
   make -C deploy/stacks/nvcf-compute-plane test-local
   make -C deploy/stacks/observability test
   go test -C tools/docs-version-sync ./...
   go vet -C tools/docs-version-sync ./...
   go run -C tools/docs-version-sync . --target main
   ./tools/ci/check-doc-version-sync
   ./tools/ci/check-docs
   ```

The renderer rejects unclassified artifacts. Do not denylist an artifact merely
because its public publication is pending.

## Release Packaging

The release workflow generates each inventory from its immutable stack tag. It
renders the configured profiles, collects charts and ordinary images,
discovers supported indirect image references, validates the result, and
attaches the matching inventory JSON to that stack's GitHub Release.

The generation command has this shape:

```bash
go run -C tools/docs-version-sync . \
  --generate-stack-inventory /tmp/stack-inventory.json \
  --inventory-config deploy/stacks/<stack>/release-inventory.yaml \
  --stack-version X.Y.Z \
  --stack-source-tag deploy/stacks/<stack>/vX.Y.Z \
  --stack-source-commit <full-commit-sha>
```

Generation requires the tagged source, Helm and Helmfile, and release registry
credentials. It normally runs in `.github/workflows/release-tags.yml`. Do not
put credentials in repository files or command examples.

The distribution path is:

```text
owning stack source and release inventory
  -> tagged per-stack inventory generation
  -> matching stack inventory JSON release asset
  -> docs-version-sync catalog update
  -> docs/version-catalog/main.yaml
  -> generated docs/user/manifest.md blocks
```

## Documentation Sync

After stable releases and inventory assets exist for all three stacks, run:

```bash
git fetch --tags origin
go run -C tools/docs-version-sync . --target main --update-catalog
go run -C tools/docs-version-sync . --target main
go test -C tools/docs-version-sync ./...
./tools/ci/check-doc-version-sync
./tools/ci/check-docs
```

By default the update selects the latest stable release of each stack. Use
`--stack-version`, `--compute-stack-version`, and
`--observability-stack-version` to select an exact compatible release set.

The catalog update retains an exact publication only when its artifact name,
type, and version still match. Leave an artifact in `publication_pending` until
its public location has been verified. Do not hand-edit generated manifest
blocks.

The CI check against the latest released inventory remains warn-only for now. A
release-drift warning does not block a merge. Generated-document consistency
remains blocking, and local validation should still return success before a
dependency change is complete.
