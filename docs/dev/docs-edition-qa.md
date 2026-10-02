# Docs edition migration validation

Review evidence for [NVIDIA/nvcf#2214](https://github.com/NVIDIA/nvcf/issues/2214),
collected October 1, 2026. This file and its JSON data live on the disposable
integration branch. They are not release qualification records.

## Review stack

- [Phase 1: CI foundation](https://github.com/NVIDIA/nvcf/pull/2224)
- [Phase 2: edition tooling](https://github.com/NVIDIA/nvcf/pull/2225)
- [Phase 3: complete edition preview](https://github.com/NVIDIA/nvcf/pull/2226)

Production still uses the existing product configuration. The phase PRs keep
strict validation. Diagnostic previews allow four known historical broken links
so that routing and rendering can be inspected; their successful preview jobs
do not satisfy the merge gate. The independent strict job fails those links.

## Historical route comparison

[Machine-readable route evidence](./docs-edition-route-evidence.json) records
all 1,391 URLs from navigation and redirects at baseline commit
`64363f438e6374ab964d53b285c6716437d197e7`. This includes all 156 URLs from the
published sitemap. Production was compared with the
[complete diagnostic preview](https://nvidia-preview-nvcf-36951646142.docs.buildwithfern.com/nvcf)
built from `62037f8db26392bda4aa4911e0e0ececde76c3c4` in
[run 36951646142](https://github.com/NVIDIA/nvcf/actions/runs/36951646142).

| Check | Result |
| --- | --- |
| HTTP response and expected heading | 1,391 of 1,391 match |
| Historical content identity | 789 of 789 match |
| Sitemap routes absent from baseline | 0 |
| Current/default differences requiring review | 70 routes |
| Shared-page differences requiring review | 38 routes |
| Development differences requiring review | 27 routes |

Historical comparison preserves text and anchors. It accounts for equivalent
internal link destinations using the baseline's source Git blob identities,
and for Fern asset delivery URL changes using the assets' content hashes.
It removes only the Fern-generated host-specific Markdown preamble otherwise.

The 135 remaining current/shared/development differences are not asserted to be
identical. Examples include the candidate selecting current authored sources
instead of frozen default stack pages, generated summary spacing, corrected
current links, and changed development-route prefixes. Review these against the
chosen source commit before qualifying the first edition.

This is a Markdown-export comparison plus representative browser inspection.
It does not prove every anchor, download, or API reference. Recheck the final
activation commit after review changes.

## Hosted UI checks

The [focused remote-ref proof](https://nvidia-preview-nvcf-36950465741.docs.buildwithfern.com/nvcf)
verified distinct branch content and assets, per-edition native changelogs,
edition switching, and cross-tab links that retain a non-default edition.
An explicit root-relative link probe escaped to the default edition; phase 3
therefore converts current cross-tab links to relative file links in staged
edition content.

The complete diagnostic layout passed representative historical page checks,
desktop light and dark mode, a 390-pixel mobile menu with one visible selector,
changelog filtering, and dated entry permalinks. No horizontal overflow was
observed in the tested viewports. The archive page makes historical snapshots
discoverable without adding them to the visible docs edition dropdown.

Search opens and accepts queries, but the preview returned no indexed results.
[Fern recommends a staging publish](https://buildwithfern.com/learn/docs/preview-publish/overview)
for search validation. An existing staging instance is still needed for that
cutover check.

## First-edition ref rehearsal

[Rehearsal evidence](./docs-edition-rehearsal-evidence.json) records the complete
`1.0.0` development candidate with Self-Managed `1.0.1`, Compute Plane `1.0.0`,
and Observability `1.0.0`. This is a proposed combination for review, not a new
qualification approval.

Changing development Overview content and republishing updated only the
development page. The sampled branch-based Overview and all three stack landing
pages retained identical normalized content hashes. The default alias also
retained the same edition content. See
[the republish run](https://github.com/NVIDIA/nvcf/actions/runs/36954963898).

Browser checks confirmed all four summaries, the actual three stack versions,
cross-tab navigation, the native changelog, and the archive page. Fern omits the
version prefix for the selected default edition; its selector and content still
identify `1.0.0`. Non-default preservation is covered by the focused ref proof.
No browser page errors were recorded in this rehearsal.

## Preview rollback

[Run 36955692574](https://github.com/NVIDIA/nvcf/actions/runs/36955692574)
restored docs and Fern configuration from the exact pre-migration source
`64363f438e6374ab964d53b285c6716437d197e7`, including Fern `5.38.0`, on the
isolated preview. Browser checks verified the old product/version menus,
Overview, each current stack, and the oldest 0.5 install guide. Candidate
summaries and the development marker were absent. All sampled pages returned
HTTP 200, with no browser page errors. The production site was not involved.

Existing repository branch rules protect `main` and `release-*` patterns.
They do not cover `docs/releases/**`. P4 must add protection that freezes
published docs branches against ordinary updates, force pushes, and deletion,
then verify it before registration. The current rules were inspected read-only.

## Remaining activation gates

- Authorize the four-line historical link correction required by
  `docs/AGENTS.md`, then obtain clean strict legacy and candidate checks.
- Review the exact first-edition source and complete stack combination; record
  actual qualification approval before registering a stable edition.
- Protect `docs/releases/X.Y.Z` against changes and deletion, and validate its
  recorded commit identity. Candidate branches are development test data.
- After phase 1 merges, exercise the actual default-branch `workflow_run`
  preview flow; opening a stacked PR does not update that workflow.
- Validate indexed search on an existing staging instance.
- Review current-content differences and final anchors, downloads, samples, and
  API references at the activation commit.
- Restore and smoke-check the edition candidate after the preview rollback.

The launch target remains October 2. Cleanup of old source copies follows a
successful cutover and proof that archive routes and rollback no longer depend
on them. No production merge or publish is part of these diagnostic rehearsals.
