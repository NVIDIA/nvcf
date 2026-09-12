# AGENTS.md - Deployment Stacks

## Purpose

This subtree owns the Helmfile stacks and their release inventories. Read
[`INVENTORY.md`](INVENTORY.md) before adding, removing, or changing a chart,
container image, hook image, operator-created image, or downloadable stack
resource.

## Stack Boundaries

- `self-managed/` owns the NVCF control-plane stack.
- `nvcf-compute-plane/` owns the NVCF compute-plane stack.
- `observability/` owns shared observability infrastructure.
- Each stack owns its own `release-inventory.yaml` and release asset.
- Do not reference another stack's Helmfile state from an inventory config.
- Keep a dependency in the stack that installs or creates it.
- Follow the nearest nested `AGENTS.md` when it adds stack-specific guidance.

## Dependency Changes

For every dependency change:

1. Update the owning Helmfile, chart values, or stack configuration.
2. Give an optional release an explicit condition and inventory render profile.
3. Update the owning stack's `release-inventory.yaml`.
4. Add registry and repository overrides for images that customers must mirror.
5. Record images that do not appear in rendered Kubernetes `image` fields.
6. Update the artifact classification in `docs/version-catalog/main.yaml`.
7. Run the stack tests and the inventory and documentation checks described in
   [`INVENTORY.md`](INVENTORY.md).
8. After the stack release publishes its inventory asset, update the catalog
   and generated manifest in a documentation sync change.

A dependency is not fully distributed when the released inventory or generated
manifest omits one of its charts, images, or downloadable resources.

## Required and Optional Artifacts

- Mark an artifact `required` when the default stack installation needs it to
  become ready or perform its baseline function.
- Mark an artifact `optional` when only an optional feature, provider, add-on,
  or non-default mode needs it.
- An artifact used only by an optional release remains optional for the stack.
- List a customer-provided prerequisite as a prerequisite, not as a distributed
  stack artifact.

## Validation

Run from the repository root unless a command says otherwise:

```bash
make -C deploy/stacks/self-managed test
make -C deploy/stacks/nvcf-compute-plane test-local
make -C deploy/stacks/observability test
go test -C tools/docs-version-sync ./...
go run -C tools/docs-version-sync . --target main
./tools/ci/check-doc-version-sync
./tools/ci/check-docs
git diff --check
```

Do not hand-edit generated blocks in `docs/user/manifest.md`. The CI check
against the latest released inventory is warn-only for now. Generated-document
consistency remains blocking. Treat a release-drift warning as follow-up work
and keep the local checks clean for a dependency change.
