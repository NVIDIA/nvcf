# OpenBao image

This native subtree builds the OpenBao server and JWT plugin image, packages
the legacy plugin, and owns the JWT lifecycle driver. See
`plugins/vault-plugin-secrets-jwt/AGENTS.md` for plugin implementation guidance.

Run from the repository root:

```sh
sh infra/openbao/scripts/test-semver.sh
sh infra/openbao/scripts/test-runtime-packages.sh
sh infra/openbao/tests/verify-runtime-ca-test.sh
python3 -B -m unittest discover -s infra/openbao/tests
bash deploy/helm/openbao/tests/jwt-plugin-lifecycle.sh
bash migrations/openbao/tests/jwt-plugin-version-test.sh
shellcheck infra/openbao/scripts/test-runtime-packages.sh
```

Build from `infra/openbao` with
`docker buildx build --platform linux/arm64 --load -t openbao:test .`.
Run `RUNTIME_IMAGE=openbao:test sh scripts/test-runtime-packages.sh` there to
verify the built image. Check both architectures before release.

Keep the legacy image digest and old catalog version aligned with the
published pre-bridge image shipped by the chart. Preserve its plugin bytes;
rebuilding the old source changes the stored catalog digest. Compare the
legacy plugin executable hash with the published image before claiming catalog
compatibility. Keep chart configuration and migration defaults aligned with
the image. Bump the target catalog version when changing its source or build
inputs.

The live lifecycle BDD uses `tests/Dockerfile` and
`tests/bdd/features/openbao-jwt-plugin-lifecycle.feature`. See
`tests/bdd/README.md` for isolated runtime qualification. Unit tests use a
fake `bao` and do not qualify server reload, signing, or rollback behavior.
Qualify a persistent-state rollout before release. Follow the root QA rules
before running live tests.

Use POSIX shell for packaging and Bash for lifecycle operations.
