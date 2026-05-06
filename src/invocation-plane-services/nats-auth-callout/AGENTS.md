# AGENTS.md

## Project Overview

**nvcf-nats-auth-callout-service** is a pluggable authentication service that integrates with NATS Server's authorization callout feature. It provides a flexible, multi-plugin architecture for authenticating NATS clients using various authentication mechanisms including NKeys, OAuth2 JWT tokens, and external webhooks.

**Language:** Go 1.24+
**Architecture:** Plugin-based authentication with modular package structure
**Development Model:** Trunk-based development on `main` branch

### Key Components

- `cmd/nvcf-nats-auth-callout-service/` - Main application entry point with CLI commands
- `internal/service/` - Core NATS integration and request coordination
- `internal/plugins/` - Authentication plugins (NKey, OAuth2, Webhook)
- `internal/router/` - HTTP routing with Gin framework
- `internal/tracing/` - OpenTelemetry and Lightstep tracing integration
- `internal/config/` - Configuration management with Viper
- `internal/logger/` - Structured logging with Zap
- `deploy/` - Helm charts for Kubernetes deployment

## Build and Test Commands

### Build

```bash
# Build the binary with version information
make build

# Build for development (with race detection)
make build-dev

# Show version information
./nvcf-nats-auth-callout-service version
```

### Test

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
```

### Development

```bash
# Start with hot reloading (recommended)
make dev

# Start with debugger
make dev-debug

# Run linter
make dev-lint

# Format imports
make dev-format

# Generate swagger documentation
make dev-swagger
```

## Code Style Guidelines

### General Go Conventions

- Follow standard Go formatting: `gofmt` and `go vet`
- Use meaningful variable names that reflect their purpose
- Follow the project's existing patterns for error handling
- Use Go 1.24+ features where appropriate

### Linting

- Uses `golangci-lint` with configuration in `.golangci.yml`
- Run `make dev-lint` before committing to catch style violations
- License headers are enforced via `goheader` linter using `.goheader.tmpl`

### Project-Specific Conventions

1. **Error Handling**
   - Use `fmt.Errorf` with `%w` for wrapping errors
   - Return descriptive error messages with context
   - Use appropriate error types from `internal/plugins/types/errors.go`

2. **Configuration**
   - Never hardcode configuration values in source code
   - All configurable values must be defined in config files, environment variables, or CLI flags
   - Use Viper for all configuration management

3. **Testing**
   - Use table-driven tests with descriptive test names
   - Use `zaptest.NewLogger` for proper test isolation
   - Aim for 80%+ test coverage

4. **Documentation**
   - Do not create new markdown files unless adding significant features
   - Update existing documentation (README.md, AGENTS.md) rather than creating new files
   - Design decisions should be documented in code comments

## Testing Instructions

### Test Organization

Tests are organized alongside their implementation files with `_test.go` suffix:

- `internal/service/auth_test.go` - Authentication handler tests
- `internal/plugins/oauth/oauth_test.go` - OAuth plugin tests
- `internal/plugins/nkey/nkey_test.go` - NKey plugin tests
- `internal/plugins/webhook/webhook_test.go` - Webhook plugin tests
- `internal/router/router_test.go` - HTTP router tests
- `internal/config/config_test.go` - Configuration tests

### Writing Tests

```go
func TestMyFunction(t *testing.T) {
    tests := []struct {
        name     string
        input    string
        expected string
        wantErr  bool
    }{
        {name: "valid case", input: "foo", expected: "bar"},
        {name: "error case", input: "bad", wantErr: true},
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result, err := MyFunction(tt.input)
            if tt.wantErr {
                assert.Error(t, err)
            } else {
                require.NoError(t, err)
                assert.Equal(t, tt.expected, result)
            }
        })
    }
}
```

### CI Pipeline

The GitLab CI pipeline runs:
1. **lint** - Runs golangci-lint with license header checking
2. **test-unit** - Runs `go test ./...`
3. **docker-build** - Builds Docker image
4. **helm-package** - Packages Helm chart

## Security Considerations

### Configuration Security

- **NKey Seeds**: Must be securely stored (Kubernetes secrets, Vault)
- **JWT Tokens**: JWKS endpoints should use HTTPS
- **Webhook URLs**: Should use HTTPS with proper authentication

### Testing with Credentials

- Use dummy/fake endpoints in tests
- Never commit real API keys or tokens
- Use environment variables for sensitive test data

## Commit Message Guidelines

### Format

This project uses **conventional commit messages**:

```
<type>(<scope>): <description>

[optional body]
```

### Commit Types

- `feat` - New features
- `fix` - Bug fixes
- `perf` - Performance improvements
- `docs` - Documentation only
- `build` - Build system changes
- `test` - Test additions/changes
- `refactor` - Code restructuring
- `ci` - CI configuration
- `chore` - Maintenance tasks
- `style` - Formatting changes
- `revert` - Reverting changes

### Examples

```bash
# Feature
git commit -m "feat(auth): add support for custom JWT claims"

# Bug fix
git commit -m "fix(webhook): handle timeout errors gracefully"

# Documentation
git commit -m "docs: update configuration examples"
```

### Scope Suggestions

- `auth` - Authentication service
- `plugins` - Plugin system
- `nkey` - NKey plugin
- `webhook` - Webhook plugin
- `config` - Configuration
- `tracing` - Tracing/observability
- `metrics` - Prometheus metrics

## Development Workflow

### Before Committing

```bash
# 1. Run all tests
make test

# 2. Run linting
make dev-lint

# 3. Check coverage
make coverage-check

# 4. Verify generated files are committed
git status
```

### Adding New Features

1. Add tests first (TDD approach recommended)
2. Implement the feature
3. Verify all tests pass: `make test`
4. Run linting: `make dev-lint`
5. Update existing documentation if needed
6. Use appropriate commit message type

### Adding New Plugins

1. Create plugin file in `internal/plugins/<name>/`
2. Implement the `types.Plugin` interface
3. Register in plugin manager (`internal/plugins/manager.go`)
4. Add comprehensive tests
5. Update configuration documentation

## Common Gotchas

1. **Configuration precedence**: CLI flags > Environment variables > Config file > Embedded defaults
2. **License headers**: All Go files must have Apache 2.0 license headers (enforced by `goheader` linter)
3. **NKey seeds**: Never commit NKey seeds to version control
4. **Plugin routing**: Account + plugin name form a composite key for routing

## Useful Commands Reference

```bash
# Run specific test file
go test ./internal/plugins/oauth/ -v

# Run tests matching a pattern
go test ./... -run TestOAuthPlugin

# Check Go module dependencies
go mod tidy
go mod verify

# View test coverage
go test ./... -cover

# Generate detailed coverage report
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out

# Show current configuration
./nvcf-nats-auth-callout-service config show

# Validate configuration
./nvcf-nats-auth-callout-service config validate
```

## Additional Resources

- **[README.md](README.md)** - Project overview and quick start
- **[docs/CONFIGURATION.md](docs/CONFIGURATION.md)** - Comprehensive configuration documentation
- **[SOFTWARE_DESIGN.md](SOFTWARE_DESIGN.md)** - Architecture and design documentation
- **[CONTRIBUTING.md](CONTRIBUTING.md)** - Contribution guidelines
- **[SECURITY.md](SECURITY.md)** - Security policy and vulnerability reporting
