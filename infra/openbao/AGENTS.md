# OpenBao server image

Run commands from `infra/openbao`:

- Build: `docker buildx build --platform linux/arm64 --load -t openbao:test .`.
- Check runtime packages: build with `--target runtime` and inspect `apk info -v`.
- Shell tests: `sh scripts/test-semver.sh`.
- Lint: `shellcheck -x -P ../.. scripts/build-openbao.sh scripts/verify-openbao.sh`.
- Build server binaries: `scripts/build-openbao.sh`.
- Verify server binaries: `scripts/verify-openbao.sh`.
- Verify JWT plugin binaries: `scripts/verify-jwt-plugin.sh`.

Use POSIX shell and checksum-pinned source archives and builder images.
Verify embedded Go versions and runtime package floors on amd64 and arm64.
Preserve the runtime user and entrypoint. Keep migration helper changes aligned
with `migrations/openbao`. Read the plugin's nested guidance before editing it.
