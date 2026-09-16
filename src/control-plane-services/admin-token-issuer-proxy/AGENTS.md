# AGENTS.md - Admin Token Issuer Proxy

Native Go service that mints admin-scoped JWTs through Vault/OpenBao for the
self-managed NVCF reference architecture.

## Layout

- `cmd/admin-issuer-proxy/`: HTTP server and startup coordination
- `internal/servicecache/`: API Keys metadata initialization and retry policy
- `internal/handlers/`: liveness, readiness, and token issuance handlers
- `internal/platform/vault/`: Vault/OpenBao signing client
- `../../../deploy/helm/admin-token-issuer-proxy/`: deployment chart

## Build and test

Run from the monorepo root:

```bash
go test ./src/control-plane-services/admin-token-issuer-proxy/... -count=1
go vet ./src/control-plane-services/admin-token-issuer-proxy/...
bazel test //src/control-plane-services/admin-token-issuer-proxy/...
bazel build //src/control-plane-services/admin-token-issuer-proxy/cmd/admin-issuer-proxy:image_index
```

CI and release wiring is owned by the monorepo. Do not add a subtree
`.gitlab-ci.yml`, nested Bazel module, Docker release pipeline, or private
GitLab import path.

## Startup contract

- `/healthz` reports process liveness and must not depend on API Keys.
- `/readyz` remains unavailable until service metadata is cached.
- `POST /v1/admin/keys` must not call Vault before metadata is ready.
- Retry only transient API Keys failures. Permanent configuration,
  authentication, TLS, response, and metadata errors remain fatal.
