# OpenBao migrations

This native subtree packages migration scripts and independently built helper binaries.

Run these commands from `migrations/openbao`:

- Shell tests: `for test in tests/*test.sh; do sh "$test" || exit; done`.
- Build a local image: `docker buildx build --platform linux/arm64 --build-arg GIT_SHA="$(git rev-parse HEAD)" --load -t openbao-migrations:test .`.
- Check shell scripts with `shellcheck scripts/*.sh`.
- Use POSIX shell and checksum-pinned source archives and builder images.
- Verify embedded Go and module versions, both image architectures, numeric users,
  source labels, and absence of Helm before release.
- Keep the kubectl minor compatible with the supported Kubernetes versions.
- Record dependency/license changes in the PR. Runtime qualification also needs
  the consuming chart and optional migration hooks.
