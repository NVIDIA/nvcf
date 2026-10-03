# Release terminology review

Source: [P4 commit 0afec05e4](https://github.com/NVIDIA/nvcf/commit/0afec05e437006b29164fb3e28f9e2d3cead7bc9).

The requested documentation-set sentence and the three generated stack edition
banners are removed. The generator leaves all four landing pages authored.
The remaining excerpts below are unchanged and await wording review.

## Current customer pages

The audit follows all 95 Markdown pages in current navigation outside the
Manifest and Release Notes tabs. Compatibility Matrix and upgrade guides are
under Release Notes, so they are excluded regardless of their source directory.
The search covers docs/documentation edition, edition selector, qualification,
and qualified versions. Hardware product names such as Blackwell Server Edition
are unrelated and excluded. Six relevant passages remain in three pages.

| Page | Remaining excerpt |
| --- | --- |
| [Overview](https://github.com/NVIDIA/nvcf/blob/0afec05e437006b29164fb3e28f9e2d3cead7bc9/docs/overview/index.md#L15-L16) | "Stack releases that are qualified to run together." |
| [Documentation Archive: introduction](https://github.com/NVIDIA/nvcf/blob/0afec05e437006b29164fb3e28f9e2d3cead7bc9/docs/overview/documentation-archive.md#L3-L5) | "Earlier documentation uses stack versions. These archives preserve the original guides and their URLs. They are separate documentation snapshots, not docs editions or new combinations of stack releases." |
| [Documentation Archive: selector](https://github.com/NVIDIA/nvcf/blob/0afec05e437006b29164fb3e28f9e2d3cead7bc9/docs/overview/documentation-archive.md#L16-L18) | "Use the compatibility matrix to check the documented stack combinations. The edition selector applies to the current tabbed guides; archive links open the selected historical snapshot." |
| [Image Mirroring: control plane](https://github.com/NVIDIA/nvcf/blob/0afec05e437006b29164fb3e28f9e2d3cead7bc9/docs/overview/image-mirroring.md#L222-L223) | "Use the control-plane stack version shown in the artifact manifest. The stack and its listed artifact versions are QA-qualified together." |
| [Image Mirroring: compute plane](https://github.com/NVIDIA/nvcf/blob/0afec05e437006b29164fb3e28f9e2d3cead7bc9/docs/overview/image-mirroring.md#L253-L254) | "Use the compute-plane stack version shown in the artifact manifest. The stack and its listed artifact versions are QA-qualified together." |
| [Image Mirroring: observability](https://github.com/NVIDIA/nvcf/blob/0afec05e437006b29164fb3e28f9e2d3cead7bc9/docs/overview/image-mirroring.md#L287-L288) | "Use the observability stack version shown in the artifact manifest. The stack and its listed artifact versions are QA-qualified together." |

The current three stack tabs have no remaining relevant matches.

## Historical sources

The archive navigation references 315 unique source pages, including some shared
current pages already covered above. Outside manifests and release notes,
[0.6.0 Image Mirroring](https://github.com/NVIDIA/nvcf/blob/0afec05e437006b29164fb3e28f9e2d3cead7bc9/docs/v0.6.0/image-mirroring.md#L216-L220)
and [0.6.1 Image Mirroring](https://github.com/NVIDIA/nvcf/blob/0afec05e437006b29164fb3e28f9e2d3cead7bc9/docs/v0.6.1/image-mirroring.md#L216-L220)
each repeat the control-plane and compute-plane warnings quoted above.

The retained self-managed 1.0.1 `image-mirroring.md` source also contains all
three warnings. Its old customer URL redirects to the shared current Image
Mirroring page; this retained file is not a separate current navigation page.
No archived sources were edited.

## Maintainer documentation

These files are outside customer site navigation and retain workflow terminology:

- [Docs edition workflow](https://github.com/NVIDIA/nvcf/blob/0afec05e437006b29164fb3e28f9e2d3cead7bc9/docs/dev/docs-editions.md#L3-L6):
  "A docs edition versions one documented combination of Self-Managed, Compute
  Plane, and Observability. Stack artifacts keep their own release versions.
  The site has one edition selector and six tabs. Development reads current
  sources; stable editions read protected `docs/releases/X.Y.Z` branches."
- [GitHub release process](https://github.com/NVIDIA/nvcf/blob/0afec05e437006b29164fb3e28f9e2d3cead7bc9/docs/dev/github-release-process.md#L423-L425):
  "Documentation has one SemVer docs edition for the qualified combination of all
  three stacks. Stack artifacts keep their independent releases. A stack release
  does not qualify or publish a docs edition automatically."

The storage-cache design document discusses qualified CSI access modes and
encryption capabilities, rather than documentation or release versions.
Agent guidance, catalog fields, and tooling implementation are outside this
customer wording review.
