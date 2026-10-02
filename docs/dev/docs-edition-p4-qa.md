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
