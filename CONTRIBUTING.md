# Contributing to NVCF

If you are interested in contributing to NVCF, your contributions will fall into three categories:

1. You want to report a bug, feature idea, or documentation issue
   - File an [issue](https://github.com/nvidia/nvcf/issues/new/choose) describing what you encountered or what you want to see changed.
   - The NVCF team will evaluate and triage issues. Maintainers assign labels and priority. If you believe an issue needs priority attention, comment on the issue to notify the team.
2. You want to propose a new feature and implement it
   - File a [feature issue](https://github.com/nvidia/nvcf/issues/new/choose) describing your intended feature, and we shall discuss the design and implementation there.
   - Once we agree that the plan looks good, go ahead and implement it, using the code contributions guide below.
3. You want to implement a feature or bug fix for an outstanding issue
   - Follow the code contributions guide below.
   - If you need more context on a particular issue, start a discussion on the issue.

Use [GitHub Discussions](https://github.com/NVIDIA/nvcf/discussions) for support and usage help. Maintainer triage rules are documented in [`.github/ISSUE_TRIAGE.md`](.github/ISSUE_TRIAGE.md).

---

## Code of Conduct

All participants are expected to abide by our [Code of Conduct](CODE_OF_CONDUCT.md). Please read it before contributing.

---

## Getting Started

### Good First Issues

New to the project? Look for issues labelled:

- [good-first-issue](https://github.com/nvidia/nvcf/issues?q=is%3Aissue+is%3Aopen+label%3Agood-first-issue): beginner-friendly, with guidance.
- [help-wanted](https://github.com/nvidia/nvcf/issues?q=is%3Aissue+is%3Aopen+label%3Ahelp-wanted): community contributions welcome.

### Claiming an Issue

Before starting work, comment on the issue to signal your intent. This prevents two contributors from working on the same thing in parallel. A maintainer will assign it to you.

---

## Contribution Workflow

For all development, push your changes to a branch in your own fork of nvcf and then open a pull request when the code is ready. The steps below walk you through the full process.

### Current GitHub workflow

This repository is being actively improved to support a more GitHub-native contribution flow. During this transition, commits are still pushed from our GitLab-based internal workflow, and GitHub pull requests cannot be merged directly.

We do accept external contributions through GitHub pull requests. When a contribution is ready to land, the NVCF team will carry it through the current internal workflow and preserve attribution by co-authoring the resulting commit with the original contributor and the bot that pushes the commit.

Thank you for working with us during this transition while we make the project easier to contribute to directly on GitHub.

### Step 1: Set Up Your Environment

See the [Local Dev Env Setup](README.md#local-dev-env-setup) guide for setting up a development environment before you attempt to submit your first pull request.

### Step 2: Create a Branch

Name your branch using your username and a prefix that reflects the type of change:

- `yourname/feat/description`: new feature (for example, `jsmith/feat/multi-node-inference`)
- `yourname/fix/description`: bug fix (for example, `jsmith/fix/router-timeout`)
- `yourname/docs/description`: documentation change (for example, `jsmith/docs/update-quickstart`)

Branch model: `main` is the active development branch. All PRs target `main`, except hotfixes. Stable releases are cut from `release-x.y` branches.

### Step 3: Make Your Changes

- Make your changes.
- Include tests for any new functionality or bug fix.
- Update documentation if your change affects user-facing behavior.

### Step 4: Commit Your Changes

Commit with a DCO sign-off and a conventional commit message (see [Commit Message Conventions](#commit-message-conventions) below).

### Step 5: Open a Pull Request

- Push your branch and open a PR targeting `main`.
- Use the [PR template](.github/PULL_REQUEST_TEMPLATE.md) when opening your pull request.
- Reference the related issue in the PR description with `Fixes #123`.
- Someone will review your PR soon!

---

## Commit Message Conventions

Commit and pull request messages must adhere to the [conventional commit v1.0.0 style](https://www.conventionalcommits.org/en/v1.0.0/).

Examples:

```
fix(docs): remove dead hyperlink
refactor(docs): use java 8 streams
perf(workspace): improve workspace mount speed
feat(workspace): enable workspaces in staging env
fix(formatter): handle unicode chars with csv formatted output
```

The commit title format is `type(scope): short description`.

- `type`: the kind of change. Refer to the guidance below.
- `scope`: a name for the product or area your change affects (required for feat, fix, and perf types).
- `short description`: one sentence, present-tense description.

The commit message body should include motivation for the change and contrast with previous behavior. The footer may contain a GitHub issue reference and/or a `BREAKING CHANGE` phrase and reason. Automated checks will validate message format on every PR.

### How to select a commit type

Getting the actual commit type 100% perfect is not as important as separating it into the right category, namely whether the customer or end user will be or should be made aware of this change through release notes. If yes, use a customer type. If no, use a foundational type. When in doubt, use `chore`.

- Customer types (appear in release notes): `feat`, `fix`, `perf`
- Foundational types (do not appear in release notes): `docs`, `build`, `test`, `refactor`, `ci`, `chore`, `style`, `revert`

---

## Code Style

- Keep PRs focused: one concern per PR.
- Ensure clean builds: no warnings or errors, all tests must pass.
- Do not leave commented-out code in your diff.

---

## Developer Certificate of Origin (DCO)

We require that all contributors "sign-off" on their commits. This certifies that the contribution is your original work, or you have rights to submit it under the same license, or a compatible license.

- Any contribution which contains commits that are not Signed-Off will not be accepted.

To sign off on a commit you simply use the `--signoff` (or `-s`) option when committing your changes:

```bash
git commit -s -m "Add cool feature."
```

This will append the following to your commit message:

```
Signed-off-by: Your Name <your@email.com>
```

For more information, see the [Developer Certificate of Origin](https://developercertificate.org/).

---

## Documentation Contributions

Documentation content lives under `docs/`. Fern publishes that content using version-specific navigation files under `fern/versions/`, and `fern/docs.yml` declares the public version list.

| Path | Audience | Published | Versioning role |
|---|---|---|---|
| `docs/user/` | Customers | Yes | Source content for the default `main` docs version. `fern/versions/main.yml` points here and publishes at `/nvcf/`. |
| `docs/v0.5/` | Customers using release 0.5 | Yes | Frozen content for the `0.5` docs version. `fern/versions/v0.5.yml` points here and publishes at `/nvcf/v0.5/`. |
| `docs/dev/` | Contributors / internal dev | Only if symlinked | Developer-oriented source pages. These are published only when a symlink from a versioned content tree, usually `docs/user/`, is listed in that version's Fern nav. |
| `fern/docs.yml` | Docs site | Yes | Declares public versions, display names, slugs, and the nav file for each version. |
| `fern/versions/*.yml` | Docs site | Yes | Defines navigation and page order for one published version. Page paths are relative to the version file. |

Use `docs/user/` for changes that should appear in the default `main` docs. Update `docs/v0.5/` only for fixes that must also apply to the 0.5 release docs. When adding, renaming, moving, or removing a published page, update the matching `fern/versions/<version>.yml` file.

All navigation sections use `skip-slug: true`, so each page title becomes a flat URL slug within its published version. Keep page titles unique and descriptive within the version nav. Run `fern check` to validate the docs after any navigation or link change. Preview locally with `fern docs dev` from the `fern/` directory.

---

## Security

If you discover a security vulnerability, please follow the instructions in our [SECURITY.md](SECURITY.md). Do not open a public issue for security vulnerabilities.

---

## Getting Help

- GitHub Issues, for bug reports, feature ideas, and documentation issues: https://github.com/nvidia/nvcf/issues
- GitHub Discussions, for support and usage help: https://github.com/NVIDIA/nvcf/discussions
- GitHub Pull Requests, for code contributions: https://github.com/nvidia/nvcf/pulls

For support or usage uncertainty, start with GitHub Discussions. For feature ideas, file an issue.

Thank you for contributing to NVCF!
