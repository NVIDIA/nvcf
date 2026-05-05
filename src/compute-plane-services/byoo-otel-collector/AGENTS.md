# AGENTS.md

## Project Overview

This is the **BYO Observability OpenTelemetry Collector** (byoo-otel-collector), a Go application that combines three main components:

- **otelconfig-generator** - Generates OpenTelemetry Collector configuration YAML using the nvcf-otelconfig library
- **secrets-extractor** - Extracts and parses secrets from ESS (Encrypted Secret Store) into individual files
- **otel-collector-contrib** - Custom-built OpenTelemetry Collector binary with healthcheck v2 extension support

The collector handles:
- Receiving OTLP telemetry (logs, metrics, traces) from applications
- Collecting platform metrics (cadvisor, Kube state metrics, GPU/DCGM, OpenTelemetry Collector)
- Processing and exporting telemetry to various backends (Grafana Cloud, Datadog, Azure Monitor, etc.)
- Support for both Kubernetes and VM deployments
- Support for both container and Helm chart-based workloads

**Key Components:**
- `cmd/byoo-otel-collector/` - Main application entry point
- `internal/cli/` - CLI command handling
- `internal/otelconfig/` - OpenTelemetry configuration generation (uses nvcf-otelconfig library)
- `internal/secrets/` - Secrets extraction and management
- `internal/otelcollector/` - OpenTelemetry Collector process management
- `internal/logger/` - Logging utilities
- `internal/metrics/` - Prometheus metrics for the collector itself
- `generator/` - Python-based generator for creating configuration templates
- `testdata/` - Test fixtures and example configurations
- `validator/` - End-to-end validation tools

## Build and Test Commands

### Build
```bash
# Build the byoo-otel-collector binary
go build -o bin/byoo-otel-collector ./cmd/byoo-otel-collector

# Build the custom otel-collector-contrib binary
go install go.opentelemetry.io/collector/cmd/builder@v0.147.0
builder --config=./otel-collector-build.yaml

# Build Docker image
docker build --build-arg OTEL_BUILDER_VERSION=v0.147.0 \
  -f ./Dockerfile -t byoo-otel-collector:latest .
```

### Test
```bash
# Run all tests
go test ./...

# Run tests with verbose output
go test ./... -v

# Run tests for a specific package
go test ./internal/otelconfig/...

# Run a specific test
go test ./internal/otelconfig/ -run TestRenderOtelConfig
```

### Update Generated Files
```bash
# Regenerate configuration templates
make update-config-template

# Regenerate examples
make update-examples

# Validate generated configurations
make validate-otelconfig
```

## Go Version

- **Required:** Go 1.23+
- **Toolchain:** Go 1.23.4
- CI uses Go 1.23.4

## Code Structure

```
byoo-otel-collector/
├── cmd/
│   └── byoo-otel-collector/    # Main application entry point
│       └── main.go
├── internal/
│   ├── cli/                     # CLI command handling
│   ├── logger/                  # Logging utilities
│   ├── metrics/                 # Prometheus metrics
│   ├── otelcollector/           # Collector process management
│   ├── otelconfig/              # Configuration generation
│   └── secrets/                 # Secrets extraction
├── generator/                   # Python-based template generator
│   ├── generator.py            # Main generator script
│   └── source-config.yaml       # Source configuration
├── testdata/                    # Test fixtures and examples
├── validator/                   # E2E validation tools
└── scripts/                     # Utility scripts
```

## Testing Instructions

### Test Organization

Tests are organized alongside their implementation files with `_test.go` suffix. Major test files include:

- `internal/otelconfig/render_test.go` - Configuration rendering tests
- `internal/otelconfig/otelconfig_test.go` - Configuration generation tests
- `internal/secrets/secrets_test.go` - Secrets extraction tests
- `internal/otelcollector/otelcollector_test.go` - Collector process management tests

### Writing Tests

1. Use `testify` for assertions (`github.com/stretchr/testify`)
2. Use table-driven tests with named test cases
3. Example pattern:

```go
func TestMyFunction(t *testing.T) {
    type spec struct {
        name     string
        input    string
        expected string
        expError string
    }
    
    cases := []spec{
        {name: "valid case", input: "foo", expected: "bar"},
        {name: "error case", input: "bad", expError: "expected error message"},
    }
    
    for _, tt := range cases {
        t.Run(tt.name, func(t *testing.T) {
            result, err := MyFunction(tt.input)
            if tt.expError != "" {
                assert.EqualError(t, err, tt.expError)
            } else {
                require.NoError(t, err)
                assert.Equal(t, tt.expected, result)
            }
        })
    }
}
```

### Test Data Management

- Test data lives in `testdata/` directory
- Example configurations are in `examples/`
- After modifying templates, regenerate examples:
  ```bash
  make update-examples
  ```
- Always verify generated examples are current before committing:
  ```bash
  git diff examples/
  ```

### CI Pipeline

The GitLab CI pipeline runs:
1. **check-version-changelog-modified** - Validates VERSION and CHANGELOG.md were modified
2. **check-generated-examples** - Validates examples are up-to-date
3. **check-generated-configs** - Validates configuration templates are up-to-date
4. **lint** - Runs golangci-lint
5. **test-unit** - Runs `go test ./...`
6. **test-otel-configs** - Validates generated configurations with otel collector

## Code Style Guidelines

### General Go Conventions
- Follow standard Go formatting: `gofmt` and `go vet`
- Use meaningful variable names; avoid single-letter variables except in short loops
- Keep functions focused and reasonably sized
- Document exported functions, types, and constants

### Linting
- Uses `golangci-lint` with configuration in `.golangci.yml`
- Run `make lint` before committing to catch style violations
- Linting runs with 10-minute timeout
- Header checking is enforced via `goheader` linter using `.goheader.tmpl`

### Project-Specific Conventions

1. **Error Handling**
   - Use `fmt.Errorf` with `%v` or `%w` for wrapping errors
   - Return descriptive error messages with context
   - Use `errors.Join()` for collecting multiple errors

2. **YAML Generation**
   - Uses `nvcf-otelconfig` library for configuration generation
   - Ensure generated YAML is valid and properly formatted
   - Test with actual OpenTelemetry Collector binary

3. **Template Management**
   - Source templates are in `internal/otelconfig/templates/`
   - Templates are embedded at build time
   - Use `make update-config-template` to regenerate after changes
   - Never manually edit generated templates

4. **Testing**
   - Use table-driven tests with `type spec struct`
   - Name test cases descriptively
   - Use `require` for fatal assertions, `assert` for non-fatal
   - Test with real telemetry backends when possible

5. **Documentation Files**
   - **DO NOT** create new markdown files (*.md) for documentation unless adding a significant new feature
   - Design decisions, implementation notes, and small changes should be documented in code comments
   - Only create documentation files for major features that require user-facing documentation
   - Update existing documentation (README.md, AGENTS.md) rather than creating new files

## Security Considerations

### Configuration Security

⚠️ **IMPORTANT**: This application handles sensitive information:

1. **Secrets** - Extracts and manages secrets from ESS (Encrypted Secret Store)
2. **Endpoint URLs** - May contain authentication tokens or API keys
3. **Telemetry Data** - May contain sensitive application data
4. **Configuration Files** - Should be stored securely (Kubernetes Secrets, ConfigMaps)

### Testing with Credentials
   - Use dummy/fake endpoints in tests
   - Never commit real API keys or tokens to testdata
   - Use environment variables for sensitive test data

### Generated Configurations
   - Validate all generated YAML before deployment
   - Use `make validate-otelconfig` to verify configurations
   - Review generated configs for sensitive data exposure

## Commit Message Guidelines

### Format

This project uses **conventional commit messages** for automatic versioning:

```
<type>(<scope>): <description>

[optional body]
```

### Commit Types & Version Bumps

- `perf(scope): description` → **Major version** bump (x.0.0)
- `feat(scope): description` → **Minor version** bump (x.y.0)
- `fix(scope): description` → **Patch version** bump (x.y.z)
- Other formats → **Patch version** bump (x.y.z)

### Examples

```bash
# Major version bump
git commit -m "perf(otelcollector): optimize collector startup by 50%"

# Minor version bump  
git commit -m "feat(otelconfig): add support for new telemetry backend"

# Patch version bump
git commit -m "fix(secrets): handle empty secret files gracefully"

# Also patch version bump
git commit -m "docs: update AGENTS.md with testing instructions"
```

### Scope Suggestions

- `otelconfig` - Configuration generation
- `secrets` - Secrets extraction
- `otelcollector` - Collector process management
- `cli` - CLI command handling
- `metrics` - Prometheus metrics
- `test` - Test infrastructure
- `docs` - Documentation

## Development Workflow

### Before Committing

```bash
# 1. Run all tests
go test ./...

# 2. Run linting
make lint

# 3. Regenerate templates if otelconfig changed
make update-config-template

# 4. Regenerate examples if needed
make update-examples

# 5. Validate generated configurations
make validate-otelconfig

# 6. Verify generated files are committed
git status
```

### Adding New Features

1. **Add tests first** (TDD approach recommended)
2. Implement the feature in the appropriate package
3. Update templates if adding new backends: `make update-config-template`
4. Regenerate examples: `make update-examples`
5. Verify all tests pass: `go test ./...`
6. Validate configurations: `make validate-otelconfig`
7. Update existing documentation (README.md, AGENTS.md) if adding new APIs
   - **Do not create new markdown files** unless the feature is significant and requires standalone documentation
   - Document design decisions in code comments, not separate files
8. Use appropriate commit message type (`feat`, `fix`, `perf`)

### Modifying Configuration Templates

If adding/changing configuration templates:

1. Update source templates in `internal/otelconfig/templates/`
2. Templates are embedded at build time
3. Regenerate examples: `make update-examples`
4. Validate: `make validate-otelconfig`
5. Test with actual OpenTelemetry Collector
6. Update documentation if adding new backends or features

### Upgrading the OpenTelemetry Collector

When upgrading to a new OpenTelemetry Collector version, use the update script so all references stay in sync:

```bash
./scripts/update-collector-version.sh v0.147.0
```

The script updates the version in: `otel-collector-build.yaml`, `AGENTS.md`, `README.md`, `Dockerfile`, `Dockerfile.nvcf-otel-collector`, and `.gitlab-ci.yml`. Run it from the repository root. You can pass the version with or without the `v` prefix (e.g. `v0.147.0` or `0.147.0`). After running, verify with `git diff` and run a build to confirm.

### Working with Telemetry Backends

When working on backend support:

1. Review `internal/otelconfig/` for configuration generation logic
2. Check `generator/source-config.yaml` for template structure
3. Test with real backend endpoints (use test credentials)
4. Validate generated YAML with otel collector binary
5. Update examples in `examples/`

### Generated Code

Some files are auto-generated:
- `internal/otelconfig/templates/*.tmpl` - Configuration templates (embedded)
- `examples/` - Generated example configurations

- Do not manually edit generated files
- Regenerate using `make update-config-template` and `make update-examples`
- Commit generated files along with source changes

## Pre-commit Hooks

This repository uses [pre-commit](https://pre-commit.com) to automatically:
- Regenerate examples when `internal/otelconfig/` or `generator/` files change
- Regenerate configuration templates
- Validate configurations

Setup:
```bash
pip install pre-commit>=4.2.0
pre-commit install --hook-type pre-push
```

## Common Gotchas

1. **Template regeneration** - Always run `make update-config-template` after modifying templates
2. **Example regeneration** - Run `make update-examples` after template changes
3. **Collector version** - When upgrading the OpenTelemetry Collector, use `./scripts/update-collector-version.sh <new_version>` so all files stay in sync; do not edit version strings by hand across multiple files
4. **YAML validation** - Use `make validate-otelconfig` to verify generated YAML is valid
5. **Embedded configs** - Templates are embedded at build time
6. **Version/CHANGELOG** - CI requires VERSION and CHANGELOG.md to be modified on MRs
7. **Python generator** - The generator uses Python with uv for dependency management
8. **Secrets handling** - Secrets are extracted to individual files, ensure proper file permissions

## Useful Commands Reference

```bash
# Run specific test file
go test ./internal/otelconfig/render_test.go -v

# Run tests matching a pattern
go test ./... -run TestRenderOtelConfig

# Check Go module dependencies
go mod tidy
go mod verify

# View test coverage
go test ./... -cover

# Generate detailed coverage report
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out

# Regenerate everything
make update-config-template
make update-examples
make validate-otelconfig

# Upgrade OpenTelemetry Collector version (updates all relevant files)
./scripts/update-collector-version.sh v0.147.0

# Find all test files
find . -name '*_test.go' -not -path './vendor/*' -not -path './validator/*' -not -path './generator/*'
```

## Additional Resources

- Go module: `github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector`
- Dependencies managed via Go modules
- CI/CD: GitLab CI (see `.gitlab-ci.yml`)
- Generator documentation: `generator/doc/README.md`
- Validator documentation: `validator/README.md`
- Deployment documentation: `docs/Deployment.md`
