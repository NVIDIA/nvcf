# Issue triage

This guide documents how maintainers triage public GitHub issues for NVCF.

## Supported paths

- Bug reports: use the bug report template.
- Feature ideas: use `feature-request.md`.
- Documentation requests: use the documentation request template.
- Security vulnerabilities: follow `SECURITY.md`. Do not open a public issue.
- Questions: use GitHub Discussions.

## Maintainer ownership

Maintainers own labels, priority, assignment, and closure decisions.
Reporters should provide clear reproduction steps, expected behavior, affected
versions, and links to relevant logs or documentation. Maintainers may close
issues that do not include enough information after requesting follow-up.

Use lowercase dash-case for all repo labels. Do not create labels with spaces,
slashes, or mixed case.

## Priority labels

- `priority-p0`: major functionality is broken, user impact is significant,
  and no workaround exists.
- `priority-p1`: the product is usable but not working as documented, and a
  workaround exists.
- `priority-p2`: minor defect, low-impact issue, or small improvement.
- `priority-unprioritized`: priority has not been set.

Every open bug should have exactly one priority label after triage.

## Status labels

- `needs-triage`: issue or pull request is awaiting maintainer review.
- `needs-info`: more information is needed from the reporter.
- `blocked`: waiting on an external dependency or decision.
- `in-progress`: someone is actively working on this.

Remove `needs-triage` after the first maintainer triage pass.

## Stale labels

- `stale`: issue has had no activity for the configured stale window.
- `no-stale`: issue is exempt from stale automation.

Use `no-stale` for issues that should remain open even without regular
activity, such as known long-running tracking work.

## Roadmap issues

Use `roadmap` for public roadmap items and standing planning work. Roadmap
issues usually also need `no-stale` so stale automation does not close
them.
