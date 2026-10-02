# Contributing to NVCF

If you are interested in contributing to NVCF, your contributions will fall into three categories:

1. You want to report a bug, feature idea, or documentation issue
   - File an [issue](https://github.com/NVIDIA/nvcf/issues/new/choose) describing what you encountered or what you want to see changed.
   - The NVCF team will evaluate and triage issues. Maintainers assign labels and priority. If you believe an issue needs priority attention, comment on the issue to notify the team.
2. You want to propose a new feature and implement it
   - File a [feature issue](https://github.com/NVIDIA/nvcf/issues/new/choose) describing your intended feature, and we shall discuss the design and implementation there.
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

- [good-first-issue](https://github.com/NVIDIA/nvcf/issues?q=is%3Aissue+is%3Aopen+label%3Agood-first-issue): beginner-friendly, with guidance.
- [help-wanted](https://github.com/NVIDIA/nvcf/issues?q=is%3Aissue+is%3Aopen+label%3Ahelp-wanted): community contributions welcome.

### Claiming an Issue

Before starting work, comment on the issue to signal your intent. This prevents two contributors from working on the same thing in parallel. A maintainer will assign it to you.

---

## Contribution Workflow

For all development, push your changes to a branch in your own fork of nvcf and then open a pull request when the code is ready. The steps below walk you through the full process.

### Current GitHub workflow

NVCF accepts external contributions through GitHub pull requests. Accepted
contributions can be merged directly in this repository.

### Step 1: Set Up Your Environment

See the [Local dev env setup](README.md#local-dev-env-setup) guide in the
README for setting up a development environment before you attempt to submit
your first pull request. In short:

1. Install Bazel through bazelisk. See [Building with Bazel](README.md#building-with-bazel).
2. Confirm your toolchain with `bazel build //src/clis/nvcf-cli:nvcf-cli`.

Full setup, caches, and the CI map live in [`BAZEL.md`](BAZEL.md).

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

### Step 4: Run Tests Locally

Before opening a PR, build and test the code you touched so you can
self-verify:

```bash
# Test the package or packages you changed, for example the CLI:
bazel test //src/clis/nvcf-cli/...

# Or run the full tree:
bazel test //...
```

For documentation-only changes, run `git diff --check` and `fern check` (see
[Documentation Contributions](#documentation-contributions)).

### Step 5: Commit Your Changes

Commit with a DCO sign-off and a conventional commit message (see [Commit Message Conventions](#commit-message-conventions) below).

### Step 6: Open a Pull Request

- Push your branch and open a PR targeting `main`.
- Use the [PR template](.github/PULL_REQUEST_TEMPLATE.md) when opening your pull request.
- Reference the related issue in the PR description with `Fixes #123`.
- Someone will review your PR soon!

---

## Commit Message Conventions

Commit and pull request messages must adhere to the [conventional commit v1.0.0 style](https://www.conventionalcommits.org/en/v1.0.0/).

Examples:

```text
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

```text
Signed-off-by: Your Name <your@email.com>
```

For more information, see the [Developer Certificate of Origin](https://developercertificate.org/).

---

## Documentation Contributions

Documentation lives under `docs/`. Fern publishes one docs edition menu with
Overview, Self-Managed, Compute Plane, Observability, and Release Notes tabs.
Each edition records the exact three-stack combination; artifact releases keep
their own versions.

| Path | Purpose |
| --- | --- |
| `docs/overview/` | Shared guides and generated compatibility/manifest content for the selected edition. |
| `docs/self-managed/`, `docs/compute-plane/`, `docs/observability/` | Current stack sources, published as Development from `main`. |
| `docs/<stack>-<version>/`, `docs/v*/` | Frozen historical archives. Change only for an explicitly approved historical fix. |
| `docs/dev/` | Contributor guides; published only when navigation or a navigated symlink includes them. |
| `fern/docs.yml`, `fern/editions.yml` | Canonical selector and exact protected release-branch commits. |
| `fern/navigation.yml` | Current five-tab navigation with explicit page slugs. |
| `fern/changelog/` | Native release notes with dated MDX entries. |
| `fern/products/` | Retained historical navigation. |

Edit current sources and update `fern/navigation.yml` when changing pages.
Use relative source-file links across tabs to preserve the selected edition.
Use an absolute product/version URL only for an intentional historical link.
Do not rewrite archive sources or update an existing docs release branch.

Run `./tools/ci/check-docs` before submitting. Preview the working tree with
`tools/ci/run-fern docs dev`, or validate the Development-first PR configuration:

```bash
DOCS_PREVIEW_CONFIG=fern/edition-preview.yml tools/ci/preview-docs --check
```

Stable editions use protected `docs/releases/X.Y.Z` branches, not copied
version folders or docs tags. See [the release workflow](docs/dev/docs-editions.md)
for qualification, preparation, registration, corrections, and rollback.

---

## Security

If you discover a security vulnerability, please follow the instructions in our [SECURITY.md](SECURITY.md). Do not open a public issue for security vulnerabilities.

---

## Getting Help

- GitHub Issues, for bug reports, feature ideas, and documentation issues: https://github.com/NVIDIA/nvcf/issues
- GitHub Discussions, for support and usage help: https://github.com/NVIDIA/nvcf/discussions
- GitHub Pull Requests, for code contributions: https://github.com/NVIDIA/nvcf/pulls

For support or usage uncertainty, start with GitHub Discussions. For feature ideas, file an issue.

Thank you for contributing to NVCF!
