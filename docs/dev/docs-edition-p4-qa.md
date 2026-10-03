# Docs edition P4 validation

This report supports [P4](https://github.com/NVIDIA/nvcf/pull/2235) and
[the docs editions epic](https://github.com/NVIDIA/nvcf/issues/2214).
The production site still uses the existing product layout.

## Candidate identity

| Item | Value |
| --- | --- |
| P4 source | `6dffae7f654708b350171d53c47ddd8b86856295` |
| Corrected initial edition | `ddb2e65ffb822ad035884df166ea0e3679426e22` on `docs/candidates/2214-p4-initial-repair` |
| Protected release branch | `docs/releases/1.0.0` at `ddb2e65ffb822ad035884df166ea0e3679426e22` |
| Qualified combination | Self-Managed 1.0.1, Compute Plane 1.0.0, Observability 1.0.0 |
| Prior production source | `9958541083fce12a81688a5c13cf83fba6a5e75f` |
| Fern CLI | 5.144.1 |

The maintainer confirmed the complete combination is qualified. P4 records that
approval; this validation did not run deployment qualification tests.

[Corrected initial-edition preview](https://nvidia-preview-nvcf-2214-p4-initial-repair.docs.buildwithfern.com/nvcf)
was built by [run 37034682188](https://github.com/NVIDIA/nvcf/actions/runs/37034682188).
Its candidate ref has its own path-based navigation; the preview composes that
ref with current Development sources. With explicit maintainer approval, the
unpublished release branch was corrected from b91c792 to ddb2e65 using an exact
force-with-lease. Protection was restored immediately and verified: active, no
exclusions or bypass actors, and updates, deletions, and force pushes blocked.

[Isolated staging](https://nvidia-nvcf-editions-staging.docs.buildwithfern.com/nvcf)
was fully published by [run 37036516593](https://github.com/NVIDIA/nvcf/actions/runs/37036516593),
without the preview flag or production instance/custom domain. It composes the
actual protected release ref with Development. Browser checks confirm that
shared-page links render HTML and retain the selected edition.

## Results

| Check | Result |
| --- | --- |
| Strict Fern validation | Canonical, Development-first, prepared patch, and corrected initial candidate pass. |
| Go tests and vet | Pass, including regression tests for generated links, branch-local redirects, and symlink navigation. |
| P4 CI | All checks pass on final head 6dffae7, including [root Bazel and its required-check gate](https://github.com/NVIDIA/nvcf/actions/runs/37036302633). |
| Corrected candidate CI | [Fern Docs CI](https://github.com/NVIDIA/nvcf/actions/runs/37034575039) and Markdown lint pass. |
| Full corrected route sweep | 1,391 URLs return HTTP 200 with matching production/candidate headings. Six transient timeouts passed on retry. |
| Historical content | All 789 historical routes retain content after verified equivalent source-link and asset-digest normalization. Frozen source files are unchanged. |
| Navigation | One edition dropdown, five tabs, exact stack summaries, edition switching, and Development cross-tab retention pass sampled browser checks. |
| Shared-page links | Local Development, Fake GPU Operator, and load-testing links resolve to rendered pages with the selected edition retained. |
| Assets and deep links | Installation and architecture diagrams load; API page, installation anchor, and sample YAML download pass. |
| Display | Desktop light/dark and mobile checks show no horizontal overflow or browser page errors. |
| Changelog and archive | Native release-note entry and archive entry points pass browser checks. |
| Docs-only patch | Same stack versions prepare as 1.0.1; retry is idempotent. No new snapshot folders are created. |
| Indexed search | Production returns Helmfile results. The automatic PR preview and the fully published staging instance both return empty search facets and no Helmfile results. This gate remains open. |
| CodeRabbit | Completed review of all 60 changed files through 6dffae7 with no actionable findings. Its generic docstring-coverage notice is non-blocking. |

[Machine-readable route evidence](docs-edition-p4-route-evidence.json) records
the tested source, preview, routes, source files, headings, and body hashes.
Normalization uses asset content digests and verified source blobs. It does not
suppress arbitrary text changes.

The new default selects current guides instead of the old per-stack snapshots.
Content review should include the current KAI Scheduler guide, OpenBao restart
and upgrade guidance, installation and troubleshooting updates, autoscaler
links, and LLM router admission-error documentation. Current landing pages also
add the edition and exact stack versions. Historical routes retain their old
content.

## Regressions found and corrected

The rehearsals found four activation issues:

- Generated manifest links escaped the selected edition.
- Ninety-five old Development aliases would select the stable default.
- A prepared release branch inherited redirects into an absent Development
  entry, preventing a later patch from passing strict checks.
- Fern rendered symlinked shared pages but links to those pages could reach
  raw Markdown or a soft 404 with HTTP 200. Edition navigation now references
  the seven real shared files, and preparation rejects symlink aliases.

The shared-link regression was verified by actual browser content, not status
codes alone. Both the corrected patch rehearsal and exact corrected initial
candidate reach the intended rendered Local Development and Fake GPU pages.

## Rollback

[Run 37030889360](https://github.com/NVIDIA/nvcf/actions/runs/37030889360)
restored the prior production docs, Fern configuration/pin, and generator on an
isolated preview. Browser checks verified the old product/version menus and
five representative current/historical pages.

[Run 37031710430](https://github.com/NVIDIA/nvcf/actions/runs/37031710430)
restored the edition candidate. The smoke check confirmed its qualified
summary, single dropdown, and tabs. Production was unchanged throughout.

## Remaining merge gates

The qualified combination, approved unpublished branch correction, restored
protection, final registry pin, historical routes, browser content, patch
preparation, and rollback are recorded above. P4 is ready for code review.

Indexed staging search remains a merge gate. The maintainer authorized trying
the PR preview first and creating staging if necessary. Both were tested after
hydration. The staging build completed successfully using the full publish
command, but its index was empty after completion. Fern's documentation calls
for staging to verify search; a successful site build is not evidence that its
search index is working.

A [read-only indexing diagnostic](https://github.com/NVIDIA/nvcf/actions/runs/37038280611)
received HTTP 403 from Fern when reading its search-index allowlist with the
existing publishing credential. No index settings were changed. Ask a Fern
administrator to check indexing for
`nvidia-nvcf-editions-staging.docs.buildwithfern.com/nvcf`, then rerun the search
check for stable 1.0.0 and Development and follow the returned result links.
The empty index is observed; its server-side cause is not confirmed.

Production publication and a final production smoke test follow maintainer
review and merge. P5 follows successful cutover. No production deployment or
P4 merge was performed during this validation.

## Release Notes review follow-up

The maintainer requested customer-facing "Release" terminology and moving the
notes out of Overview. P4 source `21246ab38f196c9ed2c5f4722a94441b491f1480`
implements this presentation change:

- Native changelog titles and introduction use "Release".
- The Release Notes tab contains the native changelog, detailed stack notes,
  and upgrade guides. Overview retains the compatibility matrix and manifest.
- Redirects preserve the moved note URLs for default, Development, and explicit
  1.0.0 routes. Changelog and dated-entry paths stay unchanged.
- All 52 existing mapped note URLs return the expected headings. All 24
  historical note routes have identical content. Current note source files
  are unchanged; rendered links follow their new navigation destinations.
- Browser checks cover both stable and Development sidebars, selected-version
  retention, old URLs, dated changelog links, and mobile overflow.

The [final preview](https://nvidia-preview-nvcf-2214-p4-release-notes-final.docs.buildwithfern.com/nvcf/release-notes)
passed the browser checks above in both stable and Development after
[run 37068764310](https://github.com/NVIDIA/nvcf/actions/runs/37068764310)
completed successfully. All 52 note URLs passed on this final preview; two
transient timeouts passed on retry. CodeRabbit completed both follow-up
reviews through 21246ab38 with no actionable findings.

The replacement candidate is `15ae2a18275c2f3ed9c6b1a6ec617d36f0f39b85` on
`docs/candidates/2214-p4-release-notes-final`. The same three stack versions and
qualification remain in place. Relative to the protected ddb2e65 content, only
Fern navigation/changelog/redirect files and the preparation receipt change.
No documentation source or artifact inventory changes.
[Fern CI](https://github.com/NVIDIA/nvcf/actions/runs/37068679119) and Markdown
lint pass for the exact candidate commit.

The protected release branch is still ddb2e65. A new, specific approval is
needed before replacing that unpublished branch with the tested candidate;
the earlier one-time approval covered the prior correction only. Indexed
staging search remains a separate merge gate.

## Combined navigation follow-up

This supersedes the presentation candidate above. P4 source
`4e703a5fbcfb30f7eed631c4c2efcef51086ca54` implements the latest requirements:

- Six tabs, including a dedicated Manifest tab.
- Compatibility Matrix in the Release Notes sidebar.
- A native Release 1.0.1 entry dated September 22, matching the published
  Self-Managed release, with actual stack versions and upgrade guidance.
- Removal of the temporary Release preview entry and the redundant Release
  Notes Overview page from current navigation.
- Redirects for the moved manifest, matrix, overview, and preview-entry URLs.

The [combined preview](https://nvidia-preview-nvcf-2214-p4-manifest.docs.buildwithfern.com/nvcf/release-notes)
passed [run 37071049542](https://github.com/NVIDIA/nvcf/actions/runs/37071049542).
It builds candidate `d8a8a93973bcf3f01fd16d0ae2201961574a935c` alongside
Development. [Exact candidate CI](https://github.com/NVIDIA/nvcf/actions/runs/37071023704)
and Markdown lint pass. Local generated-content checks, both strict Fern
layouts, and targeted Markdown lint pass.

Browser validation passes for stable and Development: six tabs, removed pages
absent from Overview, the new changelog entry and dated permalink, manifest
links, matrix placement, upgrade links, and mobile layout without overflow.
All four links in the 1.0.1 entry resolve to the selected edition. Fifteen
moved or removed default, Development, and explicit 1.0.0 URLs redirect to
the expected destinations. Explicit 1.0.0 URLs retain that version prefix.
No browser page errors occurred.

All 71 mapped note, manifest, and matrix URLs return content. All 32 historical
routes retain identical content. Seven former shared Release Notes Overview
aliases intentionally resolve to the Releases index; remaining headings match.
Current source content, qualification, artifact inventories, and generated
manifest/matrix data are unchanged from the approved ddb2e65 candidate.
Contributor guidance now documents the six-tab layout.

CodeRabbit raised three findings on 4e703a5fb:

- [Use absolute changelog links](https://github.com/NVIDIA/nvcf/pull/2235#discussion_r4170257646):
  not applied. Hosted validation proves the relative source links resolve to
  each selected edition. Absolute paths would escape Development to the
  default edition, as demonstrated in the earlier P3 rehearsal.
- [Move Manifest back to Overview](https://github.com/NVIDIA/nvcf/pull/2235#discussion_r4170257666):
  superseded by the maintainer's explicit requirement for a Manifest tab.
- [Move Compatibility Matrix back to Overview](https://github.com/NVIDIA/nvcf/pull/2235#discussion_r4170257673):
  superseded by the explicit requirement for the Release Notes sidebar.

The epic requirements and PR description record these decisions. Completed
P4 CI passes; root Bazel is still running. The protected release branch and
registry pin remain ddb2e65 with the active ruleset unchanged. A specific
approval request covers replacement with the combined d8a8a9397 candidate;
the older 15ae2a1 request is superseded. Indexed staging search remains a merge
gate. No production publication or merge occurred.

## Consolidated release 1.0.1

The maintainer requested 1.0.1 as the initial consolidated release, complete
release pages in the native changelog, and an Upgrade Notes sidebar containing
only upgrade guides. This supersedes the unpublished 1.0.0 candidates and their
pending branch-replacement approval requests.

P4 head `1acb89c91744fa12fae6322251da494240eaba03` registers
`docs/releases/1.0.1` at `23d40a34636dbb3bdcd21e8f07e9e33cd6a36a2c`.
The new branch was created after hosted validation. Effective branch rules
block updates, deletion, and force pushes; the active ruleset has no bypass
actors or exclusions. No ruleset modification was needed. The former 1.0.0
branch remains frozen and is removed from the canonical registry.

The stable catalog, generated manifest, and dropdown use 1.0.1. Its approved
combination remains Self-Managed 1.0.1, Compute Plane 1.0.0, and Observability
1.0.0. Development is 1.0.2, a proposed docs-only patch after 1.0.1. Tooling
examples are aligned with that sequence. Actual 1.0.2 preparation and an
idempotent retry pass, as do generated-content and branch-local Fern checks.
No 1.0.2 release branch was created or qualification claimed.

The native Releases timeline contains full 1.0.1, 0.6.1, and 0.6.0 entries.
Upgrade Notes contains the three existing upgrade guides. The separate
consolidated 1.0.0 entry and temporary preview entry are absent. The full note
bodies are preserved after normalizing heading levels, link destinations,
emphasis formatting, and the 1.0.1 qualified-combination introduction.
Dates follow the September 22 GitHub 1.0.1 release, the July 31 documentation
promotion of 0.6.1 (`322da01d9`), and July 14 publication of 0.6.0 notes
(`e4552079a`).

The first hosted check caught Fern's date prefixes on changelog headings.
All 30 original section anchors now have explicit aliases. Browser checks
verify every alias in both stable and Development. The existing fixes link
and redirected links with fragments keep working.

[Review the final preview](https://nvidia-preview-nvcf-2214-p4-release101-final.docs.buildwithfern.com/nvcf/release-notes).
The [candidate build](https://github.com/NVIDIA/nvcf/actions/runs/37090536382)
and [canonical protected-ref build](https://github.com/NVIDIA/nvcf/actions/runs/37090921639)
pass. The latter uses QA source `1c39bc667` and the actual protected release
branch with Development 1.0.2. Exact release-candidate
[Fern CI](https://github.com/NVIDIA/nvcf/actions/runs/37090517717) and Markdown
lint pass.

Validation covers:

- Strict local canonical and Development checks, Go tooling tests, generated
  content, Markdown lint, and registry verification against the protected ref.
- Both edition selectors, full release entries, Upgrade Notes sidebar, matrix
  and manifest placement, selected-edition links, and all 30 section aliases.
- Twelve current and former 1.0.0 redirect cases, plus mobile rendering without
  horizontal overflow. No browser page errors occurred.
- All 71 affected note, manifest, and matrix routes return content; all 32
  historical routes retain identical content. Current note URLs intentionally
  resolve to native entries with their release heading.

Current PR CI is in progress; completed checks pass. The earlier CodeRabbit
placement findings were resolved against the explicit requirements. Indexed
staging search remains a separate merge gate. Production publication and P4
merge remain pending maintainer review.

## Overview and Legacy follow-up

P4 source `94163c1719cf9816a0016746ac14e07cd9cd49f5` removes the generated
edition/stack summary from Overview, removes the independent-release-train
introduction from Compatibility Matrix, and moves Documentation Archive under
Legacy. The archive URL remains `/nvcf/overview/documentation-archive`.
The catalog and generator no longer add the Overview summary. Existing renderer
support remains available when validating previously frozen catalogs.

The corrected unpublished 1.0.1 candidate is
`d4fa180b10723aa220d52a18a3094fb3e15f050b` on
`docs/candidates/2214-p4-overview`. Its approved stack versions and manifest are
unchanged. QA source `f3fe4864d7b71f46a0c0994bfc28833399c27d4c` composes that
candidate with Development 1.0.2.

[Review this preview](https://nvidia-preview-nvcf-2214-p4-overview.docs.buildwithfern.com/nvcf/overview/overview).
[Hosted preview and Fern checks](https://github.com/NVIDIA/nvcf/actions/runs/37092643299)
pass. Browser validation covers 29 cases: stable and Development Overview,
Legacy grouping and archive clicks, all six historical archive destinations,
Compatibility Matrix content and redirects, Manifest, current tab links, and
mobile overflow. No page errors occurred; desktop and mobile screenshots were
reviewed. The removed summary is absent in both editions.

Go tests and vet, canonical and staged-development Fern checks, generated-content
checks, candidate Fern checks, Markdown lint, and whitespace checks pass.
The regeneration regression test confirms that default outputs leave authored
Overview content unchanged while generating stack summaries and the manifest.
Current P4 checks have no failures; root Bazel is still running. CodeRabbit has
automatically paused reviews after repeated commits. Its coverage ends at
`1acb89c91`, so its passing status does not cover this follow-up.

The maintainer approved replacing the unpublished `docs/releases/1.0.1` ref
from `23d40a346` to `d4fa180b10723aa220d52a18a3094fb3e15f050b`.
An exception limited to that branch allowed one exact force-with-lease.
Protection was immediately restored and verified: active, no exclusions or
bypass actors, and effective rules blocking updates, deletion, and force pushes.
P4 head `2403bd0f7dea8c805558bb41439abd7fba0a7394` pins that exact commit.
Canonical docs validation passes against the corrected protected ref.
Final pin CI is running. Indexed staging search remains a separate merge gate.
No production publication or PR merge occurred.

The final review check identified five comments from the earlier changelog
migration review. The registry-mismatch claim uses a superseded 1.0.0 QA record;
the current release ref and pin match, verified by API. Relative upgrade links
retain the selected edition in the earlier browser checks. The pod-creation
wait, final pod-status wording, and redirect-order comments require separate
follow-up review before merge; the completed Overview change does not resolve
those findings.

## Stack introduction removal and terminology audit

P4 source `0afec05e437006b29164fb3e28f9e2d3cead7bc9` removes the documentation-set
sentence from Overview and the edition/version/qualification banners from the
three stack landing pages. The catalog and default generator outputs no longer
write those banners. The regeneration test verifies all four landing pages
remain authored while the compatibility matrix and manifest still regenerate.

The corrected unpublished 1.0.1 candidate is
`570d4a47927c148159a1ec660b3ba746ccefece8` on
`docs/candidates/2214-p4-stack-intros`. QA source
`57f9445c78b52a5b1b115a48fdb59c7d8e2ed80f` composes that candidate with
Development 1.0.2. The stack versions and generated release manifest are
unchanged from the approved release.

[Review the preview](https://nvidia-preview-nvcf-2214-p4-stack-intros.docs.buildwithfern.com/nvcf/self-managed/installation-overview).
[Run 37094235755](https://github.com/NVIDIA/nvcf/actions/runs/37094235755)
passes both Fern Check and Hosted Docs Preview. All 17 browser cases pass:
eight landing-page checks across stable and Development; retained matrix,
manifest, archive excerpts, and three mirroring warnings in both editions;
and mobile layout without overflow. No page errors occurred. Desktop and mobile
screenshots were reviewed. Go tests/vet, generated-content checks, strict
canonical/Development/candidate Fern checks, Markdown lint, and whitespace checks
pass. Current PR checks have no failures; root Bazel is still running.

[The terminology report](./docs-terminology-review.md) records six unchanged
passages across three pages after scanning all 95 current navigated pages
outside Manifest and Release Notes. It also records historical duplicates and
maintainer-only excerpts. The report is for wording review; those passages have
not been edited.

The protected release branch and canonical registry remain at `d4fa180b1`.
Applying candidate `570d4a479` awaits a new explicit approval for the temporary
single-branch exception, exact force-with-lease, and immediate protection
restoration. The previous exact-commit approval has already been completed.
The two open OpenBao instruction findings and indexed staging search remain
separate pre-merge work. No production publication or merge occurred.
