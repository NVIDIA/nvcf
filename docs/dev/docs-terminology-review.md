# Release terminology review

Source: [P4 commit 61366f0e1](https://github.com/NVIDIA/nvcf/commit/61366f0e1c72fa98623764f8a68834db5a038e34).

The requested documentation-set sentence and three stack edition banners are
removed. The two Archive passages and the three Image Mirroring qualification
warnings are also removed. Image Mirroring now shares the Manifest sidebar
with Artifact Manifest. Its code examples, headings, and other operating
instructions remain unchanged.

## Current customer pages

The updated audit follows all 94 Markdown pages in current navigation outside
the Manifest and Release Notes tabs. Compatibility Matrix and upgrade guides
are under Release Notes and Image Mirroring is under Manifest, so they are
excluded regardless of their source directories. One relevant passage remains:

| Page | Remaining excerpt |
| --- | --- |
| [Overview](https://github.com/NVIDIA/nvcf/blob/61366f0e1c72fa98623764f8a68834db5a038e34/docs/overview/index.md#L15-L16) | "Stack releases that are qualified to run together." |

This passage was not included in the requested removals. The three stack tabs
have no remaining relevant matches. Hardware product names such as Blackwell
Server Edition refer to hardware and are excluded.

## Historical sources

The archive navigation references 315 unique source pages, including some shared
current pages already covered above. Outside manifests and release notes,
[0.6.0 Image Mirroring](https://github.com/NVIDIA/nvcf/blob/0afec05e437006b29164fb3e28f9e2d3cead7bc9/docs/v0.6.0/image-mirroring.md#L216-L220)
and [0.6.1 Image Mirroring](https://github.com/NVIDIA/nvcf/blob/0afec05e437006b29164fb3e28f9e2d3cead7bc9/docs/v0.6.1/image-mirroring.md#L216-L220)
each retain the control-plane and compute-plane warnings: "Use the control-plane
stack version shown in the artifact manifest. The stack and its listed artifact
versions are QA-qualified together." The compute-plane warning uses the same
wording with its stack name.

The retained self-managed 1.0.1 `image-mirroring.md` source also contains all
three warnings. Its old customer URL redirects to Image Mirroring under Manifest;
this retained file is not a separate current navigation page.
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
