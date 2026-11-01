# AGENTS.md - nats-auth-callout

Native Go service that integrates with NATS Server authorization callouts.
Authentication is plugin-based and currently covers NKey, OAuth2 JWT, and
external webhook flows.

## Layout

- `cmd/nvcf-nats-auth-callout-service/`: binary entrypoint and CLI commands
- `internal/service/`: NATS callout integration and request coordination
- `internal/plugins/`: auth plugins and plugin manager
- `internal/router/`: Gin HTTP routing
- `internal/config/`: Viper configuration
- `internal/tracing/`: OpenTelemetry and Lightstep tracing
- `../../../deploy/helm/nats-auth-callout/`: Helm chart assets

## Build and Test

```bash
make build
make test
make test-coverage
make coverage-check
make dev-lint
```

For local iteration:

```bash
make dev
make dev-debug
make dev-format
make dev-swagger
```

Follow the [release workflow](../../../docs/dev/github-release-process.md) for source versions and
producer handoffs.

## Plugin Work

When adding a plugin:

1. Add the implementation under `internal/plugins/<name>/`.
2. Implement the plugin interface from `internal/plugins/types`.
3. Register it in `internal/plugins/manager.go`.
4. Add focused tests and configuration docs.

Configuration precedence is CLI flags, then environment variables, then config
file, then embedded defaults. Plugin routing uses account plus plugin name as
the composite key.

## Local Gotchas

- Keep NKey seeds, JWTs, webhook credentials, and API keys out of the repo.
- Use `zaptest.NewLogger` in tests that need logger isolation.
- `make coverage-check` enforces the local coverage threshold.
- License headers are enforced by the goheader linter.
