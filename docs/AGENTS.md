# AGENTS.md - Documentation Guidance

Use this file when navigating or editing NVCF documentation under `docs/`.

## Layout

- `docs/user/`: current customer-facing documentation for main.
- `docs/v*/`: frozen versioned documentation. Do not edit these trees unless the user explicitly asks for a historical docs fix.
- `docs/dev/`: developer and local workflow documentation.
- `docs/version-catalog/main.yaml`: source of truth for generated artifact versions in current docs.
- `fern/versions/main.yml`: source of truth for current docs navigation.
- `fern/versions/<version>.yml`: source of truth for versioned docs navigation.

## Navigation

Prefer the Fern navigation files and the filesystem over static route tables.

1. For current docs, start with `fern/versions/main.yml`.
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

Use `docs/user/` for current customer docs and `docs/dev/` for developer workflows.

### SVG Assets

Documentation SVGs must work in both light and dark mode. Add SVG-local CSS with `color-scheme: light dark`, a `prefers-color-scheme: dark` media query, and shared variables for background, panel, muted panel, border, text, muted text, connector line, NVIDIA green, blue, red, and amber accents.

Replace visible hard-coded fills and strokes with CSS variables. Keep transparent shapes, `fill="none"`, invisible strokes such as `stroke-opacity="0"`, embedded image data, dimensions, text, paths, and file references unchanged unless the user asks for a redraw.

NVIDIA Cloud Functions glyphs inside green icon boxes must stay white in both modes. Use a stable icon foreground token, for example `--svg-icon-on-accent: #fff`, instead of tying those glyphs to `--svg-panel`.

Before finishing SVG changes, render light and dark previews for every changed SVG and compare them together for consistent background tone, panel contrast, connector contrast, text readability, and accent brightness.

When changing artifact versions, registry paths, manifest entries, image mirroring snippets, or generated install examples, update the catalog instead of hand-editing generated blocks:

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
