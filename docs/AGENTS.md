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
