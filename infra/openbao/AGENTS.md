# OpenBao image

This subtree owns the server image, plugin packaging, and lifecycle driver.
Plugin implementation guidance is in `plugins/vault-plugin-secrets-jwt/AGENTS.md`.

Run from the repository root:

```sh
python3 -B -m unittest discover -s infra/openbao/tests
bash deploy/helm/openbao/tests/jwt-plugin-lifecycle.sh
bash migrations/openbao/tests/jwt-plugin-version-test.sh
```

Build with `docker buildx build --platform linux/amd64,linux/arm64 infra/openbao`.
Builds need registry access and the pinned Go toolchain. Keep legacy plugin
bytes from the pinned image; rebuilding them changes catalog digests.
Match the bridge baseline to the published pre-bridge image shipped by the
chart. Keep the image digest and old catalog version aligned with that baseline.
Bump the target catalog version when changing its source or build inputs.
Keep chart configuration and migration defaults aligned with the image.

The real lifecycle BDD uses `tests/Dockerfile` and
`tests/bdd/features/openbao-jwt-plugin-lifecycle.feature`. See `tests/bdd/README.md`
for isolated runtime qualification. Unit tests use a fake `bao` and do not
qualify server reload, signing, or rollback behavior.

Use POSIX shell for packaging and Bash for lifecycle operations. Live upgrade
and rollback qualification must follow the root QA environment rules.
