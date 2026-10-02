# AGENTS.md - Documentation Guidance

Use this file when navigating or editing NVCF documentation under `docs/`.

## Ownership

This `docs/` tree is the canonical source for NVCF product documentation. Do
not route documentation changes to an external documentation repository or an
external workspace index.

## Layout

The published site has one docs edition menu and five tabs: Overview,
Self-Managed, Compute Plane, Observability, and Release Notes. An edition
records one qualified combination; stack artifact versions remain independent.

- `docs/overview/`: shared customer guides, compatibility matrix, manifest,
  images, and samples. These belong to the selected edition.
- `docs/self-managed/`, `docs/compute-plane/`, `docs/observability/`: current
  stack sources, published under Development from `main`.
- `docs/dev/`: contributor guides. Only pages reached through navigation or
  a navigated symlink are published.
- `docs/ngc-managed/`: legacy platform guides reached through Overview.
- `docs/<stack>-<version>/` and `docs/v*/`: frozen archives. Do not edit these
  trees without explicit authorization for a historical docs fix.
- `docs/version-catalog/main.yaml`: current artifact and development edition
  metadata. `docs/edition-manifest.json` is generated from it.
- `fern/docs.yml`: canonical edition selector, site settings, and redirects.
- `fern/editions.yml`: exact release-branch commits used by the canonical site.
- `fern/navigation.yml`: five-tab navigation for current and prepared editions.
- `fern/changelog/`: native, dated release-note entries.
- `fern/products/`: retained navigation for frozen historical routes.

## Navigation and links

For current pages, start with `fern/navigation.yml`. Stable editions build
from their own `docs/releases/X.Y.Z` branch; inspect that branch's navigation
and source when answering a version-specific question. Historical product
routes use the matching file under `fern/products/`.

Confirm each `path:` exists. Treat `href:` as an external destination.
Use explicit page slugs, and keep section `skip-slug` behavior intact.
When adding, moving, or removing a current page, update `fern/navigation.yml`.

Use relative source-file links between current pages, including cross-tab
links, so the selected edition is retained. Use absolute product/version
URLs only when intentionally linking to a frozen historical version. Preserve
anchors and downloads. Do not rewrite frozen sources to current destinations.

```bash
rg -n "<term>" docs/overview docs/self-managed docs/compute-plane docs/observability docs/dev
```

## Editing

Edit the owning current source tree for customer changes and `docs/dev/` for
contributor guides. Changes on `main` update Development. Released docs branches
are immutable; corrections require a new patch edition.

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

Release a docs edition only after the complete three-stack combination is
qualified. Use `edition prepare` with exact stack versions and a reviewed
source commit, then protect and register `docs/releases/X.Y.Z`. Do not cut
per-stack documentation folders or docs release tags. See
[the edition release and rollback workflow](dev/docs-editions.md).

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
  node:24-alpine \
  sh -c "apk add --no-cache bash git && tools/ci/run-fern docs dev"
```

Preview an alternate site configuration without changing the working tree:

```bash
DOCS_PREVIEW_ID=nvcf-candidate DOCS_PREVIEW_SKIP_COMMENT=1 \
  DOCS_PREVIEW_CONFIG=fern/candidate.yml tools/ci/preview-docs
```

The file must exist directly under `fern/`. The helper stages `docs/` and
`fern/` in a temporary clone with the original Git remote for branch refs.
CI and local helpers use the pin in `fern/fern.config.json`; CI uses the
Node version in `fern/.node-version`.

The edition preview also requires Go. It validates and normalizes current
cross-tab links in a temporary workspace. Keep explicit page slugs in
`fern/navigation.yml`. Never run `edition links` against frozen documentation
trees. `fern/edition-preview.yml` makes Development the default for PR previews.

Check the staged candidate without publishing a preview:

```bash
DOCS_PREVIEW_CONFIG=fern/edition-preview.yml tools/ci/preview-docs --check
```

Native release notes live in `fern/changelog/YYYY-MM-DD.mdx`. Use `##` headings
for entries and frontmatter tags for filtering. Record stack artifact versions
separately from the docs edition. A preview entry must not claim qualification.

See `tools/docs-version-sync/README.md` for edition preparation and registration.
Release branches use their own path-based default navigation, with hidden legacy
versions retained for historical links. Only the canonical registry owns the
published edition history. Hosted previews are required to test remote refs;
local Fern development previews show working-tree versions only.

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
