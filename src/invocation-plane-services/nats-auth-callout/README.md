[Key Features](#key-features) | [Quick Start](#quick-start) | [Development](#development) | [Documentation](#documentation) | [Requirements](#requirements)

# NVCF NATS Auth Callout Service

A pluggable authentication service that integrates with NATS Server's authorization callout feature. It provides a flexible, multi-plugin architecture for authenticating NATS clients using various authentication mechanisms including NKeys, OAuth2 JWT tokens, and external webhooks.

## Key Features

### Plugin-Based Authentication

The service supports multiple authentication plugins that can be configured per account:

- **NKey Plugin**: Cryptographic NKey signature verification
- **OAuth plugin**: OAuth2 JWT Bearer token authentication with JWKS validation
- **Webhook Plugin**: Delegation to external HTTP authentication services

### Observability

- **Distributed Tracing**: OpenTelemetry and Lightstep support
- **Metrics Collection**: Prometheus metrics with optional ServiceMonitor
- **Health Checks**: Built-in health and readiness endpoints

### Configuration

- **Flexible Configuration**: Environment variables, CLI flags, and YAML config
- **Secrets Management**: Vault agent integration for secure credential handling
- **Container Ready**: Docker and Kubernetes deployment support

## Quick Start

### Build and Run

```bash
# Build the binary
make build

# Run the server
./nvcf-nats-auth-callout-service server

# Or run directly with Go
make run
```

### Test Endpoints

```bash
# Health check
curl http://localhost:8080/healthz

# API ping
curl http://localhost:8080/v1/ping

# View configuration
./nvcf-nats-auth-callout-service config show
```

## Development

```bash
# Run all tests
make test

# Run tests with coverage
make test-coverage

# Run linting
make dev-lint

# Start with hot reloading
make dev
```

For detailed DevSpace usage, see the [DevSpace Development Guide](local-dev/README.md).

## Building

The project uses a Makefile for building with proper version injection:

```bash
# Build binary with automatic version from git
make build

# Build with custom version
make build VERSION=2.0.0

# Build for development (with race detection)
make build-dev

# Show version information
make version

# Clean build artifacts
make clean
```

### Version Information

The application includes build-time version injection:

```bash
# Show version information
./nvcf-nats-auth-callout-service version

# Show version as JSON
./nvcf-nats-auth-callout-service version --json
```

Version information includes:

- **Version**: Git tag/commit or custom version
- **Git Commit**: Full commit hash
- **Build Date**: When the binary was built
- **Go Version**: Go compiler version used

## Local Development

### Quick Start

```bash
# Install dependencies and Air
make go-install-deps
go install github.com/air-verse/air@latest

# Start development with hot reloading
make dev
```

### Development Commands

```bash
# Hot reloading development (recommended)
make dev                    # Auto-restart on file changes

# Standard development
make run                    # Run once without hot reloading

# Debugging
make dev-debug              # Start with debugger (continues immediately)
make dev-debug-suspend      # Start with debugger (waits for connection)

# Testing and Quality
make dev-test               # Run tests in development mode
make dev-lint               # Run linter and code analysis
make dev-format             # Format imports with goimports
make dev-swagger            # Generate Swagger documentation
```

### Why Choose DevSpace Over Local Development?

DevSpace is recommended because it provides:

- **Zero Setup**: No need to install Air, linting tools, or configure external services
- **Consistency**: Exact same environment as production and other developers
- **Complete Stack**: Observability tools pre-configured
- **Multiple Environments**: Easy switching between basic, tracing, metrics, and observability profiles

```bash
# Instead of complex local setup, just run:
devspace dev

# Everything is ready - just run your preferred development command:
make dev
```

## Running the Server

### Development Mode (Recommended)

#### Hot Reloading Development

```bash
# Start with hot reloading - automatically restarts on code changes
make dev

# This will:
# - Watch for file changes in Go, YAML, JSON files
# - Automatically rebuild and restart the application
# - Show build output and application logs
```

#### Standard Development

```bash
# Run without hot reloading
make run

# Equivalent to: go run ./cmd/nvcf-nats-auth-callout-service server
```

#### Debug Mode

```bash
# Start with debugger (continues immediately)
make dev-debug

# Start with debugger (waits for debugger connection)
make dev-debug-suspend

# Connect your debugger to localhost:5005
```

### Production Mode

#### Using the Built Binary

```bash
# Build first
make build

# Run with embedded defaults
./nvcf-nats-auth-callout-service server

# Run with custom config file
./nvcf-nats-auth-callout-service server --config /path/to/config.yaml

# Run with secrets file
./nvcf-nats-auth-callout-service server --secrets-file /path/to/secrets.json

# Run with both config and secrets files
./nvcf-nats-auth-callout-service server --config /path/to/config.yaml --secrets-file /path/to/secrets.json

# Run with CLI flag overrides
./nvcf-nats-auth-callout-service server --port 9090 --service-name my-service
```

#### Using Go Run (Development Only)

```bash
# Run directly with Go (use make run instead)
go run ./cmd/nvcf-nats-auth-callout-service server --help

# Run with configuration (use make run instead)
go run ./cmd/nvcf-nats-auth-callout-service server --config ./cmd/nvcf-nats-auth-callout-service/config.yaml --port 8080

# Run with secrets file (use make run instead)
go run ./cmd/nvcf-nats-auth-callout-service server --secrets-file ./secrets.json
```

### Using Docker

```bash
# Build and run the Docker container
docker build -t nvcf-nats-auth-callout-service --build-arg TARGETPLATFORM=linux/amd64 -f build/package/Dockerfile .
docker run -p 8080:8080 nvcf-nats-auth-callout-service server
```

### API Access

Access server API at `<debug-addr:8080>`:

```bash
curl -k localhost:<debug-addr>/v1/ping
```

## Testing

The project includes comprehensive test coverage with multiple testing targets:

```bash
# Run all tests
make test

# Run tests with verbose output
make test-verbose

# Run tests with coverage
make test-coverage

# Generate HTML coverage report
make coverage-html

# Check coverage meets minimum threshold (80%)
make coverage-check

# Run tests with race detection
make coverage-race

# Run comprehensive test suite
make test-all

# Clean coverage files
make clean-coverage
```

### Test Coverage

- **Overall**: Comprehensive test coverage across all packages
- **Configuration**: Tests for all configuration methods and precedence
- **Tracing**: 98.7% coverage with tests for auto-detection, endpoints, compression, headers
- **Router**: Integration tests with tracing middleware
- **CLI**: Command validation and flag binding tests

### Running Specific Tests

```bash
# Run tests for a specific package
go test ./internal/tracing/ -v

# Run tests with coverage for a specific package
go test ./internal/tracing/ -cover -v

# Run a specific test
go test ./internal/tracing/ -run TestAutoDetectRuntimeAttributes -v
```

## Configuration

**📖 For comprehensive configuration documentation, see [Configuration Guide](docs/CONFIGURATION.md).**

### Quick Configuration

```bash
# Run with default configuration
make run

# Show current configuration
make build && ./nvcf-nats-auth-callout-service config show

# Run with environment variables
NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT=9090 NVCF_NATS_AUTH_CALLOUT_SERVICE_METRICS_ENABLED=true make dev

# Run with custom config file
make build && ./nvcf-nats-auth-callout-service server --config my-config.yaml

# Run with secrets file
make build && ./nvcf-nats-auth-callout-service server --secrets-file my-secrets.json

# Run with both config and secrets files
make build && ./nvcf-nats-auth-callout-service server --config my-config.yaml --secrets-file my-secrets.json
```

The service supports configuration via:

- **CLI Flags**: `--port 8080 --service-name my-service --config config.yaml --secrets-file secrets.json`
- **Environment Variables**: `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT=8080 NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH=secrets.json`
- **Config Files**: YAML configuration files
- **Secrets Files**: JSON secrets files for sensitive configuration
- **Embedded Defaults**: Built-in configuration

Common environment variables:

- `NVCF_NATS_AUTH_CALLOUT_SERVICE_CONFIG_PATH`: Path to configuration file
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH`: Path to secrets file
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT`: Server port (default: 8080)
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME`: Service name for identification
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NATS_URL`: NATS server URL (default: nats://localhost:4222)
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY_SEED`: NKey seed for authentication
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY_SIGNATURE`: Signing key seed for JWT signing
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_METRICS_ENABLED`: Enable Prometheus metrics
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_ENABLED`: Enable distributed tracing
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_LEVEL`: Set log level (debug, info, warn, error)

## Troubleshooting

### Development Issues

#### "air: No such file or directory"

```bash
# Install Air for hot reloading
go install github.com/air-verse/air@latest

# Make sure GOPATH/bin is in your PATH
export PATH=$PATH:$(go env GOPATH)/bin

# Or use DevSpace which has Air pre-installed
devspace dev
```

#### "make: command not found"

Install Make:

- **Ubuntu/Debian**: `sudo apt-get install make`
- **macOS**: `brew install make` or install Xcode command line tools
- **Windows**: Use WSL or install Make for Windows

### Performance Issues

```bash
# Run with race detection
make build-dev

# Run benchmarks
make bench

# Check test coverage
make test-coverage
```

### Service Not Starting

```bash
# Check configuration
make build && ./nvcf-nats-auth-callout-service config show

# Check for port conflicts
lsof -i :8080

# Use a different port
./nvcf-nats-auth-callout-service server --port 8081
```

## Deployment

### Kubernetes Deployment

```bash
# Deploy with Helm
helm install nvcf-nats-auth-callout-service ./deploy

# Deploy with DevSpace
devspace deploy

# Deploy with metrics enabled
helm install nvcf-nats-auth-callout-service ./deploy --set metrics.enabled=true

# Deploy with tracing enabled
helm install nvcf-nats-auth-callout-service ./deploy \
  --set tracing.enabled=true \
  --set tracing.otel.secretHeaders.authorization="your-token"
```

### Docker Deployment

```bash
# Build container
docker build -t nvcf-nats-auth-callout-service .

# Run container
docker run -p 8080:8080 nvcf-nats-auth-callout-service server

# Run with environment variables
docker run -p 8080:8080 \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_METRICS_ENABLED=true \
  -e NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_ENABLED=true \
  nvcf-nats-auth-callout-service server
```

### Environment Variables

Key environment variables for deployment:

- `NVCF_NATS_AUTH_CALLOUT_SERVICE_CONFIG_PATH`: Path to configuration file
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_SECRETS_FILE_PATH`: Path to secrets file
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT`: Server port (default: 8080)
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME`: Service name for tracing
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NATS_URL`: NATS server URL (default: nats://localhost:4222)
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY_SEED`: NKey seed for authentication
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NKEY_SIGNATURE`: Signing key seed for JWT signing
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_METRICS_ENABLED`: Enable metrics collection
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_TRACING_ENABLED`: Enable distributed tracing
- `NVCF_NATS_AUTH_CALLOUT_SERVICE_LOGGING_LEVEL`: Log level (debug, info, warn, error)

## API Documentation

### Swagger/OpenAPI

Access interactive API documentation:

- **Local**: `http://localhost:8080/swagger/index.html`
- **DevSpace**: `http://nvcf-nats-auth-callout-service.127-0-0-1.nip.io:8080/swagger/index.html`

### Generate Documentation

```bash
# Generate Swagger docs
make dev-swagger

# Or manually
swag init -g cmd/nvcf-nats-auth-callout-service/main.go -o api/
```

### API Endpoints

| Endpoint     | Method | Description                       |
| ------------ | ------ | --------------------------------- |
| `/healthz`   | GET    | Health check endpoint             |
| `/v1/ping`   | GET    | API ping endpoint                 |
| `/metrics`   | GET    | Prometheus metrics (when enabled) |
| `/swagger/*` | GET    | API documentation                 |

## Documentation

- **[AGENTS.md](AGENTS.md)** - Comprehensive development guide for AI coding agents
- **[CONTRIBUTING.md](CONTRIBUTING.md)** - Contribution guidelines and development workflow
- **[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)** - Code of conduct for contributors
- **[SECURITY.md](SECURITY.md)** - Security policy and vulnerability reporting
- **[docs/CONFIGURATION.md](docs/CONFIGURATION.md)** - Detailed configuration documentation
- **[SOFTWARE_DESIGN.md](SOFTWARE_DESIGN.md)** - Architecture and design documentation

## Requirements

- Go 1.24+
- Docker (for container builds)
- Helm (for Kubernetes deployment)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for detailed contribution guidelines.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the full license text.
