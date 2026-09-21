# AGENTS.md - Documentation Guidance

Use this file when navigating or editing NVCF documentation under `docs/`.

## Ownership

This `docs/` tree is the canonical source for NVCF product documentation. Do
not route documentation changes to an external documentation repository or an
external workspace index.

## Layout

The published site is one Fern site with four products. Each stack product
has its own version menu. Overview is unversioned.

- `docs/overview/`: unversioned shared documentation: compatibility matrix, quickstart, manifest, image mirroring, multi-tenancy, function usage (API, CLI, function and task creation, invocation, LLM gateway), load testing, release-notes index, local development, and shared assets under `images/` and `samples/`.
- `docs/self-managed/`: top-of-tree Self-Managed Stack (control plane) documentation published as `dev`.
- `docs/compute-plane/`: top-of-tree Compute Plane Stack documentation published as `dev`.
- `docs/observability/`: top-of-tree Observability Stack documentation published as `dev`.
- `docs/<stack>-<train>/`: frozen per-stack documentation for a release train, for example `docs/observability-1.3/`. Do not edit these trees unless the user explicitly asks for a historical docs fix. `docs/self-managed-1.0/` is the full pre-split tree frozen at the 1.0.0 retag and contains compute-plane and observability pages as well.
- `docs/v*/`: frozen legacy full-tree documentation from before the per-stack split. Same rule: do not edit.
- `docs/ngc-managed/`: legacy NGC-managed (BYOC) platform documentation, published under Overview.
- `docs/dev/`: developer and local workflow documentation.
- `docs/version-catalog/main.yaml`: source of truth for generated artifact versions in top-of-tree docs.
- `fern/docs.yml`: product and version registry.
- `fern/products/overview.yml`: Overview navigation.
- `fern/products/<stack>/dev.yml`: top-of-tree navigation for one stack.
- `fern/products/<stack>/<version>.yml`: frozen navigation for one stack version. Legacy full-tree versions live under `fern/products/self-managed/`.

A page belongs to exactly one product. Links inside a product stay relative.
Links to a page in another product use an absolute site path such as
`/nvcf/self-managed/installation-overview` or `/nvcf/overview/quickstart`,
because Fern resolves relative links inside the rendering product.

## Navigation

Prefer the Fern navigation files and the filesystem over static route tables.

1. For top-of-tree docs, start with `fern/products/overview.yml` or `fern/products/<stack>/dev.yml`.
2. For pinned release docs, use the matching version file under `fern/products/<stack>/`.
3. Confirm the mapped `path:` exists before answering.
4. If a nav item uses `href:`, treat it as an external page. Do not invent a local file.
5. If Fern nav does not answer the question, search with `rg`:

```bash
rg -n "<term>" docs/overview docs/self-managed docs/compute-plane docs/observability docs/dev
```

Useful file listing commands:

```bash
rg --files docs/overview docs/self-managed docs/compute-plane docs/observability docs/dev
rg --files docs/*-[0-9]* docs/v*
```

## Editing

Use the product tree that owns the page for top-of-tree customer docs and `docs/dev/` for developer workflows. Each stack product's default route points to that stack's latest frozen version once one exists, otherwise to `dev`.

### Artifact manifest

For the maintainer workflow, including automatic stack releases, public
publication updates, and new artifact registration, see
[`tools/docs-version-sync/README.md`](../tools/docs-version-sync/README.md).

The generated tables in `docs/overview/manifest.md` use catalog artifacts and
`manifest.entries` from `docs/version-catalog/main.yaml`. For each entry, set
its deployment plane, kind, requirement, public-safe description, and public
GitHub or upstream source links. Use `artifact_id` for catalog artifacts and
static fields only for prerequisites that are not in the catalog.

Do not hand-edit the generated manifest block. Regenerate and test it with:

```bash
go run -C tools/docs-version-sync . --target main
go test -C tools/docs-version-sync ./...
```

The generator must reject unclassified artifacts, unexpected EA-only entries,
and `load_tester_supreme`.

### SVG Assets

Documentation SVGs must work in both light and dark mode. Add SVG-local CSS with `color-scheme: light dark`, a `prefers-color-scheme: dark` media query, and shared variables for background, panel, muted panel, border, text, muted text, connector line, NVIDIA green, blue, red, and amber accents.

Replace visible hard-coded fills and strokes with CSS variables. Keep transparent shapes, `fill="none"`, invisible strokes such as `stroke-opacity="0"`, embedded image data, dimensions, text, paths, and file references unchanged unless the user asks for a redraw.

NVIDIA Cloud Functions glyphs inside green icon boxes must stay white in both modes. Use a stable icon foreground token, for example `--svg-icon-on-accent: #fff`, instead of tying those glyphs to `--svg-panel`.

Before finishing SVG changes, render light and dark previews for every changed SVG and compare them together for consistent background tone, panel contrast, connector contrast, text readability, and accent brightness.

Use `--update-catalog` only when synchronizing artifact versions and registry
paths from the latest stable releases of all three stacks. Presentation-only changes to
`manifest.entries` use the regeneration command above.

To synchronize the development catalog and generated blocks from the latest
stable releases of all three stacks:

```bash
go run -C tools/docs-version-sync . --target main --update-catalog
./tools/ci/check-doc-version-sync
```

The first command reads the inventories attached to the latest stable GitHub
stack releases and writes a development release set. The second command is an
offline consistency check. CI runs the offline check before this separate
current-release check:

```bash
./tools/ci/check-doc-version-current-release
```

The current-release check does not discover public NGC availability. Keep
exact public locations in `publications` and mark unavailable versions in
`publication_pending`. Identify publication records by `name`, `type`, and
`version` so charts, images, and resources with the same name remain distinct.
Version overrides also require `name` and `type`.

Stacks qualify and freeze documentation independently. When a stack's release
train is qualified, run `tools/scripts/cut-docs-version.sh` for that stack and
train. It copies only that stack's tree, adds the version to that product's
menu in `fern/docs.yml`, and snapshots the catalog. The compatibility matrix in
`docs/overview/compatibility-matrix.md` is generated and stays current across
all stacks; it is not frozen.

Generated blocks are marked with comments such as:

```mdx
{/* docs-version-sync:BEGIN marker-name */}
{/* docs-version-sync:END marker-name */}
```

Keep the marker comments intact.

## Local preview

Render the docs locally with Fern in a Docker container. Serves on `http://localhost:3000`:

```bash
docker run --rm -it \
  -v "$(pwd):/workspace" \
  -w /workspace \
  -p 3000:3000 \
  node:20-alpine \
  sh -c "npm install -g fern-api && fern docs dev"
```

## Validation

Run the narrow version check after catalog or generated block changes:

```bash
./tools/ci/check-doc-version-sync
```

Run full docs validation before finishing docs changes:

```bash
./tools/ci/check-docs
```

For pure routing or AGENTS.md-only changes, `git diff --check` plus targeted `rg` checks are usually sufficient.
