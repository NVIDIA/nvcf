# Release process

The pre-cutover generated GitLab pipeline described here has been retired for
native public components. Start with the
[release guide](https://github.com/NVIDIA/nvcf/blob/main/RELEASE.md).

- [GitHub release automation](github-release-process.md) owns component
  versioning, maintenance branches, tags, inventory attachment and pin proposals.
- [Umbrella releases](umbrella-releases.md) owns explicit stack selection,
  candidate assembly, QA evidence and product publication.
- [Docs editions](docs-editions.md) retains the legacy immutable docs workflow.

Images, charts, stacks and the umbrella have independent versions. Chart
`appVersion` and effective image overrides must be checked separately from the
chart package version. A GitHub Release identifies source; verify actual
artifact delivery before consumption.

The former pipeline description remains in Git history.
