# Docs edition P4 validation

This report supports [P4](https://github.com/NVIDIA/nvcf/pull/2235) and
[the docs editions epic](https://github.com/NVIDIA/nvcf/issues/2214).
The production site still uses the existing product layout.

## Candidate identity

| Item | Value |
| --- | --- |
| P4 source | `6c56941a39497acff9cd8d92cdbb71e6b4cd9d71` |
| Corrected initial edition | `ddb2e65ffb822ad035884df166ea0e3679426e22` on `docs/candidates/2214-p4-initial-repair` |
| Existing protected branch | `docs/releases/1.0.0` at `b91c792c10cae55030f4dbd603cc6cf6a3f9ef86`, unchanged |
| Qualified combination | Self-Managed 1.0.1, Compute Plane 1.0.0, Observability 1.0.0 |
| Prior production source | `9958541083fce12a81688a5c13cf83fba6a5e75f` |
| Fern CLI | 5.144.1 |

The maintainer confirmed the complete combination is qualified. P4 records that
approval; this validation did not run deployment qualification tests.

[Corrected initial-edition preview](https://nvidia-preview-nvcf-2214-p4-initial-repair.docs.buildwithfern.com/nvcf)
was built by [run 37034682188](https://github.com/NVIDIA/nvcf/actions/runs/37034682188).
Its candidate ref has its own path-based navigation; the preview composes that
ref with current Development sources. The protected release ref has not moved.

## Results

| Check | Result |
| --- | --- |
| Strict Fern validation | Canonical, Development-first, prepared patch, and corrected initial candidate pass. |
| Go tests and vet | Pass, including regression tests for generated links, branch-local redirects, and symlink navigation. |
| P4 CI | All checks pass, including [root Bazel and its required-check gate](https://github.com/NVIDIA/nvcf/actions/runs/37033388452). |
| Corrected candidate CI | [Fern Docs CI](https://github.com/NVIDIA/nvcf/actions/runs/37034575039) and Markdown lint pass. |
| Full corrected route sweep | 1,391 URLs return HTTP 200 with matching production/candidate headings. Six transient timeouts passed on retry. |
| Historical content | All 789 historical routes retain content after verified equivalent source-link and asset-digest normalization. Frozen source files are unchanged. |
| Navigation | One edition dropdown, five tabs, exact stack summaries, edition switching, and Development cross-tab retention pass sampled browser checks. |
| Shared-page links | Local Development, Fake GPU Operator, and load-testing links resolve to rendered pages with the selected edition retained. |
| Assets and deep links | Installation and architecture diagrams load; API page, installation anchor, and sample YAML download pass. |
| Display | Desktop light/dark and mobile checks show no horizontal overflow or browser page errors. |
| Changelog and archive | Native release-note entry and archive entry points pass browser checks. |
| Docs-only patch | Same stack versions prepare as 1.0.1; retry is idempotent. No new snapshot folders are created. |
| Indexed search | Production returns Helmfile results. Isolated candidate indexing remains untested pending a staging-site decision. |
| CodeRabbit | Skips this draft; no completed P4 review is claimed. |

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

The first release branch was frozen before the shared-link issue was found.
The corrected commit above is ready for review. A decision is pending between
one explicitly approved correction to that unpublished branch, with protection
restored immediately, and retaining it unchanged while releasing a new patch
edition. No protection exception or force push has been performed.

The supplied cloud-functions/current/latest URL redirects to the live
production site. An isolated staging site is still needed for indexed-search
validation. Keep P4 in draft until the release-source decision, staging search,
final registry validation, and CodeRabbit review are complete. P5 follows a
successful reviewed cutover and production smoke test.
