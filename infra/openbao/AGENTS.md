# OpenBao image

This native subtree builds the OpenBao server and JWT plugin image.
Run commands from `infra/openbao`:

- Shell tests: `sh scripts/test-semver.sh && sh scripts/test-runtime-packages.sh`.
- Lint: `shellcheck scripts/test-runtime-packages.sh`.
- Build: `docker buildx build --platform linux/arm64 --load -t openbao:test .`.
- Verify packages: `RUNTIME_IMAGE=openbao:test sh scripts/test-runtime-packages.sh`.
- Preserve engine and plugin source pins for runtime-package repairs.
- Compare server and plugin executable hashes with the selected image before
  claiming an existing plugin catalog remains compatible.
- Qualify both architectures and a persistent-state rollout before release.
