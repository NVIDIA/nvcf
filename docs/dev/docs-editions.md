# Release a docs edition

A docs edition versions one documented combination of Self-Managed, Compute
Plane, and Observability. Stack artifacts keep their own release versions.
The site has one edition selector and five tabs. Development reads current
sources; stable editions read protected `docs/releases/X.Y.Z` branches.

## Choose the version and combination

Record the exact three stack versions and approval of the complete combination
in a public release review. A compatible range or a published artifact alone
does not qualify an edition. Review current guides against the selected stack
inventories before freezing them.

Use SemVer for the documented deployment contract:

- Major: a breaking change in any selected stack or the documented contract.
- Minor: a compatible feature addition in the documented combination.
- Patch: a compatible fix or documentation-only correction.

Apply the highest required bump once. Two stack majors in one edition produce
one edition major bump. A docs-only patch can keep all three stack versions.

`main` carries development metadata for the next candidate. Update its proposed
version and change kind when the release scope changes. Never mark the main
catalog qualified; qualification belongs to the prepared release branch.

## Prepare from reviewed sources

Sync the exact inventory versions with `docs-version-sync`, review the generated
content, and commit it. Fetch the predecessor and run the edition preflight:

```bash
git fetch origin --tags
./tools/ci/check-doc-editions
```

Prepare a new directory outside the checkout. The example is a docs-only patch
after `1.0.0`; substitute the reviewed source commit and approval URL:

```bash
go run -C tools/docs-version-sync . edition prepare \
  --source <full-reviewed-source-commit> \
  --version 1.0.1 --previous-version 1.0.0 --change patch \
  --self-managed 1.0.1 --compute-plane 1.0.0 --observability 1.0.0 \
  --qualification https://github.com/NVIDIA/nvcf/pull/<release-review> \
  --out /tmp/nvcf-docs-1.0.1
```

For the first edition, use `--change initial` and omit `--previous-version`.
Omit `--qualification` while evaluating an unqualified candidate. The
`Prepare Docs Edition` workflow performs the same preparation and uploads the
files and review patch; it does not create a release branch or publish the site.

Review the prepared catalog, manifest, changelog, navigation, links, and source
diff. Preserve the preparation receipt. Repeating the command with identical
inputs verifies it without overwriting modified output.

## Freeze and register the branch

Commit the prepared content on the full-version docs branch. Do not create a
docs tag or copy a new version folder. Before pushing, verify the repository's
docs-release ruleset applies to that branch. It must prohibit updates, force
pushes, and deletion, with no ordinary bypass actors. Creation remains allowed.

```bash
git -C /tmp/nvcf-docs-1.0.1 switch -c docs/releases/1.0.1
git -C /tmp/nvcf-docs-1.0.1 add docs fern .docs-edition-preparation.json
git -C /tmp/nvcf-docs-1.0.1 commit -s
git -C /tmp/nvcf-docs-1.0.1 push origin HEAD:refs/heads/docs/releases/1.0.1
git -C /tmp/nvcf-docs-1.0.1 rev-parse HEAD
```

The release branch retains its local `path` default. It must not reference
itself through `ref`. Historical archives remain hidden path entries. Verify
the remote branch head and effective protection rules after creation.

From the canonical checkout, register the exact resulting commit:

```bash
go run -C tools/docs-version-sync . edition register \
  --version 1.0.1 --commit <full-docs-branch-commit>
```

Add the matching entry first under `versions` in `fern/docs.yml`:

```yaml
- display-name: "1.0.1"
  slug: "1.0.1"
  ref: docs/releases/1.0.1
```

Keep Development and all historical entries. Advance the development candidate
metadata separately and regenerate its manifest and summaries. The registry
pins the released commit; a moved or missing branch must fail publication.

## Validate and publish

Run both configurations and the tooling tests:

```bash
go test -C tools/docs-version-sync ./...
./tools/ci/check-docs
DOCS_PREVIEW_CONFIG=fern/edition-preview.yml tools/ci/preview-docs --check
```

Generate a hosted preview of the canonical configuration as well as the
Development-first preview. Local Fern previews cannot prove remote-ref
composition. Verify edition switching, all five tabs, version summaries,
changelog links, archives, anchors, downloads, and mobile/light/dark rendering.
Use an isolated staging instance for indexed search. Record source commits,
resolved refs, preview URLs, CI results, and the last validated production
commit in the release PR.

Merge the registration and activation PR only after review and the required
checks. The publisher on `main` validates the same checkout before publishing.
Release-branch pushes validate their content; they do not publish the canonical
registry. Smoke-test production immediately after the publisher completes.

## Correct or roll back an edition

Fix content in Development, then prepare a new patch edition. Keep the old
branch and its recorded commit intact. Per-stack folder cuts are disabled for
edition catalogs.

Before a cutover, retain the previous production commit and toolchain pins.
Rehearse restoring it on an isolated preview. If production regresses, prepare
a rollback PR from current `main` that reverts the activation or registration
commit. Include the prior source/configuration and toolchain changes required
to reproduce the validated site. Resolve intervening changes explicitly.

Validate and preview that rollback, then merge it through the normal review
process. The main publisher republishes the restored configuration. Do not move
or delete release branches, remove archive sources, or publish a release
branch's reduced registry as the entire site. Record the failure and fix it in
a new reviewed edition before retrying activation.
