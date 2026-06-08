# Go Lib

This subproject hosts common NVCF components shared by both `egx/cloud` and
`egx/edge`, including:

- API definitions
- Authentication components (OAuth, JWT, token fetching)
- Core HTTP services and event streaming
- Kubernetes client utilities
- OpenTelemetry integration
- Secret management

## Package Structure

The repository is organized into modular packages under `pkg/`:

- `pkg/auth/` - Authentication token fetching
- `pkg/cobraautobind/` - Cobra command binding helpers
- `pkg/core/` - HTTP services, event streaming, Kubernetes clients, and context management
- `pkg/fnds/` - FNDS client and common types
- `pkg/http/` - HTTP utilities, including logger, retry client, and request headers
- `pkg/icms-translate/` - Queue-message translation helpers and CLI support for workload manifests
- `pkg/imagecredential/` - Image credential refresh job and Kubernetes secret helpers
- `pkg/ngc/` - NGC token fetching
- `pkg/nvkit/` - Shared service framework utilities for auth, config, logging, metrics, tracing, servers, and clients
- `pkg/oauth/` - OAuth and JWT handling with JWKS caching
- `pkg/otel/` - OpenTelemetry integration
- `pkg/otelconfig/` - OTel collector config rendering and backend config templates
- `pkg/secret/` - Secret management and file fetching
- `pkg/types/` - Type definitions and configuration
- `pkg/version/` - Version utilities
- `pkg/zapotelspan/` - Helpers for adding OpenTelemetry span fields to zap logs

## Development

Run these commands from `src/libraries/go/lib`:

```bash
# Update vendored dependencies
make vendor-update

# Run linting
make lint

# Run all unit tests
make test

# Run individual unit test
go test ./pkg/core -run TestHTTPServiceRoutes

# Update generated K8s client code
make codegen-update

# Refresh consolidated icms-translate fixtures
make update-testdata

# Verify consolidated icms-translate fixtures are current
make check-testdata
```

Run Bazel commands from the repository root:

```bash
# Build all go-lib Bazel targets
bazel build //src/libraries/go/lib/...

# Run go-lib Bazel tests
bazel test //src/libraries/go/lib/...

# Run go-lib lint through Bazel
bazel test //src/libraries/go/lib:golangci_lint
```

Bazel build, test, and lint targets are the CI gate for this subtree. The Bazel
lint target uses the shared runner at `rules/golangci/golangci_lint.sh` with this
module's `.golangci.yml`. `make lint` still runs the same module-scoped
`golangci-lint` command for local iteration.

### Test Coverage

To view coverage results locally:
1. `cd _output/cover && python3 -m http.server 8000`
2. Open browser: `http://<machine_ip>:8000/coverage.html`

## Documentation

- [AGENTS.md](AGENTS.md) - Comprehensive development guide including architecture, testing, security, and commit conventions
- [CONTRIBUTING.md](CONTRIBUTING.md) - Contribution guidelines and code contribution process
- [SECURITY.md](SECURITY.md) - Security policies and vulnerability reporting
- [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) - Community guidelines
- BYOO-facing otelconfig commands and fixtures now live in [`tools/byoo`](../../../../tools/byoo)
