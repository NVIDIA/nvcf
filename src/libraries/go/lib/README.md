[Package Structure](#package-structure) | [Development](#development) | [Requirements](#requirements) | [Documentation](#documentation)

# NVCF Common

This repo hosts common NVCF components that shared by both `egx/cloud` and
`egx/edge`, including:

- API definitions
- Authentication components (OAuth, JWT, token fetching)
- Core HTTP services and event streaming
- Kubernetes client utilities
- OpenTelemetry integration
- Secret management

## Package Structure

The repository is organized into modular packages under `pkg/`:

- **`pkg/auth/`** - Authentication token fetching
- **`pkg/core/`** - HTTP services, event streaming, Kubernetes clients, and context management
- **`pkg/http/`** - HTTP utilities (logger, retry client, request headers)
- **`pkg/icms-translate/`** - Queue-message translation helpers and CLI support for workload manifests
- **`pkg/ngc/`** - NGC token fetching
- **`pkg/oauth/`** - OAuth/JWT handling with JWKS caching
- **`pkg/otel/`** - OpenTelemetry integration
- **`pkg/otelconfig/`** - OTel collector config rendering and backend config templates
- **`pkg/secret/`** - Secret management and file fetching
- **`pkg/types/`** - Type definitions and configuration
- **`pkg/version/`** - Version utilities

## Development

Common commands:

```bash
# Update vendored dependencies
make vendor-update

# After vendor update, regenerate NOTICE third-party list
make license-update

# Verify NOTICE is in sync
make check-license

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
```

### Test Coverage

To view coverage results locally:
1. `cd _output/cover && python3 -m http.server 8000`
2. Open browser: `http://<machine_ip>:8000/coverage.html`

## Requirements

- **Go:** 1.24.0+
- **Kubernetes API:** v0.34.2 compatibility

## Documentation

- **[AGENTS.md](AGENTS.md)** - Comprehensive development guide including architecture, testing, security, and commit conventions
- **[CONTRIBUTING.md](CONTRIBUTING.md)** - Contribution guidelines and code contribution process
- **[SECURITY.md](SECURITY.md)** - Security policies and vulnerability reporting
- **[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)** - Community guidelines
- BYOO-facing otelconfig commands and fixtures now live in [`tools/byoo`](../../../../tools/byoo)
