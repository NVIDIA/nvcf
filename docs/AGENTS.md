# AGENTS.md - Documentation Guidance

Use this file when navigating or editing NVCF documentation under `docs/`.

## Ownership

This `docs/` tree is the canonical source for NVCF product documentation. Do
not route documentation changes to an external documentation repository or an
external workspace index.

## Layout

- `docs/user/`: top-of-tree customer-facing documentation published as `dev`.
- `docs/v*/`: frozen versioned documentation. Do not edit these trees unless the user explicitly asks for a historical docs fix.
- `docs/ngc-managed/`: legacy NGC-managed (BYOC) platform documentation. Separate from the self-hosted docs in `docs/user/`.
- `docs/dev/`: developer and local workflow documentation.
- `docs/version-catalog/main.yaml`: source of truth for generated artifact versions in top-of-tree docs.
- `fern/versions/dev.yml`: source of truth for top-of-tree docs navigation.
- `fern/versions/<version>.yml`: source of truth for versioned docs navigation.

## Navigation

Prefer the Fern navigation files and the filesystem over static route tables.

1. For top-of-tree docs, start with `fern/versions/dev.yml`.
2. For pinned release docs, use the matching file under `fern/versions/`.
3. Confirm the mapped `path:` exists before answering.
4. If a nav item uses `href:`, treat it as an external page. Do not invent a local file.
5. If Fern nav does not answer the question, search with `rg`:

```bash
rg -n "<term>" docs/user docs/dev docs/v*
```

Useful file listing commands:

```bash
rg --files docs/user docs/dev
rg --files docs/v*
```

## Editing

Use `docs/user/` for top-of-tree customer docs and `docs/dev/` for developer workflows. The default published docs route points to the latest stable version, not `docs/user/`.

### Artifact manifest

The generated tables in `docs/user/manifest.md` use catalog artifacts and
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
paths from the latest stack manifest. Presentation-only changes to
`manifest.entries` use the regeneration command above.

To synchronize the catalog and generated blocks:

```bash
go run -C tools/docs-version-sync . --target main --update-catalog
./tools/ci/check-doc-version-sync
```

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
