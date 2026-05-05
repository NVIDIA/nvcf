# Contributing to nvcf-nats-auth-callout-service

If you are interested in contributing to nvcf-nats-auth-callout-service, your contributions will fall
into three categories:
1. You want to report a bug, feature request, or documentation issue
    - File an [issue](https://github.com/NVIDIA/nvcf/nvcf-nats-auth-callout-service/-/issues/new)
    describing what you encountered or what you want to see changed.
    - The NVCF team will evaluate the issues and triage them, scheduling
    them for a release. If you believe the issue needs priority attention
    comment on the issue to notify the team.
1. You want to propose a new Feature and implement it
    - Post about your intended feature, and we shall discuss the design and
    implementation.
    - Once we agree that the plan looks good, go ahead and implement it, using
    the [code contributions](#code-contributions) guide below.
1. You want to implement a feature or bug-fix for an outstanding issue
    - Follow the [code contributions](#code-contributions) guide below.
    - If you need more context on a particular issue, please ask and we shall
    provide.

## Code contributions

### Your first issue

1. Read the project's [README.md](https://github.com/NVIDIA/nvcf/nvcf-nats-auth-callout-service/-/blob/main/README.md)
    to learn how to setup the development environment.
1. Find an issue to work on. The best way is to look for the [good first issue](https://github.com/NVIDIA/nvcf/nvcf-nats-auth-callout-service/-/issues/?label_name%5B%5D=good%20first%20issue)
    or [help wanted](https://github.com/NVIDIA/nvcf/nvcf-nats-auth-callout-service/-/issues/?label_name%5B%5D=help%20wanted) labels
1. Comment on the issue saying you are going to work on it.
1. Code! Make sure to update unit tests.
1. When done, [create your merge request](https://github.com/NVIDIA/nvcf/nvcf-nats-auth-callout-service/-/merge_requests/new).
1. Verify that CI passes all [status checks](https://docs.gitlab.com/ee/ci/pipelines/), or fix if needed.
1. Wait for other developers to review your code and update code as needed.
1. Once reviewed and approved, an NVCF developer will merge your merge request.

Remember, if you are unsure about anything, don't hesitate to comment on issues and ask for clarifications!

### Conventional Commits

Commit and merge request messages must adhere to the
[conventional commit v1.0.0 style](https://www.conventionalcommits.org/en/v1.0.0/).
However, only MR messages will be used by bots for release note generations.

Examples:

- fix(docs): remove dead hyperlink
- refactor(auth): simplify plugin routing logic
- perf(service): improve authentication request throughput
- feat(plugins): add support for custom JWT claims
- fix(webhook): handle timeout errors gracefully

The commit title format is "type(scope): short description".

- **type:** the kind of change, see chart below for guidance on choosing type
- **scope:** a name for the product or area your change affects (required for feat, fix, and perf types)
- **short description:** one sentence, present-tense description

The commit message (or merge request text) should also include motivation for the
change, and contrast its implementation with previous behavior. The semantic
release process depends on consistent and compliant commit messages, thus
there will be automated checks in the form of server side git hooks that
will validate the format of the message.

The footer of the commit may contain a gitlab issue reference(s), and/or a BREAKING CHANGE phrase and reason.

#### How to select a commit type?

Getting the actual commit type 100% perfect is not as important as separating
it into the right category. Namely, whether the end-user will be or should
be made aware of this change or not (i.e. release notes). If the former use
types of the **End User** category below; otherwise, use ones from **Foundational**.
When you absolutely cannot decide just use the _chore_ type.

- **End User:** feat, fix or perf

- **Foundational:** docs, build, test, refactor, ci, chore, style, or revert

### Branches and Versions

The nvcf-nats-auth-callout-service repository does trunk based development in `main`. Your changes should be pushed into a branch in your own fork of nvcf-nats-auth-callout-service and then create a merge request when the code is ready.

**Version Policy**

Version numbers follow [Semantic Versioning](https://semver.org/) conventions.

### Signing Your Work

* We require that all contributors "sign-off" on their commits. This certifies that the contribution is your original work, or you have rights to submit it under the same license, or a compatible license.

  * Any contribution which contains commits that are not Signed-Off will not be accepted.

* To sign off on a commit you simply use the `--signoff` (or `-s`) option when committing your changes:
  ```bash
  $ git commit -s -m "Add cool feature."
  ```
  This will append the following to your commit message:
  ```
  Signed-off-by: Your Name <your@email.com>
  ```

* Full text of the DCO:

  ```
    Developer Certificate of Origin
    Version 1.1
    
    Copyright (C) 2004, 2006 The Linux Foundation and its contributors.
    1 Letterman Drive
    Suite D4700
    San Francisco, CA, 94129
    
    Everyone is permitted to copy and distribute verbatim copies of this license document, but changing it is not allowed.
  ```

  ```
    Developer's Certificate of Origin 1.1
    
    By making a contribution to this project, I certify that:
    
    (a) The contribution was created in whole or in part by me and I have the right to submit it under the open source license indicated in the file; or
    
    (b) The contribution is based upon previous work that, to the best of my knowledge, is covered under an appropriate open source license and I have the right under that license to submit that work with modifications, whether created in whole or in part by me, under the same open source license (unless I am permitted to submit under a different license), as indicated in the file; or
    
    (c) The contribution is provided directly to me by some other person who certified (a), (b) or (c) and I have not modified it.
    
    (d) I understand and agree that this project and the contribution are public and that a record of the contribution (including all personal information I submit with it, including my sign-off) is maintained indefinitely and may be redistributed consistent with this project or the open source license(s) involved.
  ```

### Pre-commit Hooks

- Follow the [instruction](https://pre-commit.com/#quick-start) to install the pre-commit.
- Install the pre-commit hook
    ``` shell
    pre-commit install --hook-type commit-msg
    ```

### Development Guidelines

**New Plugins**

When adding a new authentication plugin:
1. Create plugin file in `internal/plugins/<name>/`
2. Implement the `types.Plugin` interface
3. Register in plugin manager (`internal/plugins/manager.go`)
4. Add comprehensive tests
5. Update configuration documentation

**Configuration Changes**

When adding new configuration options:
1. Add to CLI command with proper flag definition
2. Add to configuration struct with appropriate tags
3. Add to config YAML file with comments showing CLI and ENV equivalents
4. Update README documentation with the new optionn