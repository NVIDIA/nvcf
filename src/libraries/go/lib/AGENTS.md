# AGENTS.md

## Project Overview

**nvcf-go** is a repository hosting common NVCF (NVIDIA Cloud Functions) components shared by both `egx/cloud` and `egx/edge`. The repository includes:

- API definitions
- Authentication components (OAuth, JWT, token fetching)
- Core HTTP services and event streaming
- Kubernetes client utilities
- OpenTelemetry integration
- Secret management

**Language:** Go  
**Architecture:** Modular package structure under `pkg/`  
**Development Model:** Trunk-based development on `main` branch

## Build and Test Commands

### Development Workflow

```bash
# Update vendored dependencies
make vendor-update

# After vendor update, regenerate NOTICE third-party list:
make license-update

# Verify NOTICE is in sync:
make check-license

# Run linting
make lint

# Run all unit tests
make test

# Run individual unit test
go test ./pkg/core -run TestHTTPServiceRoutes

# Update generated K8s client code
make codegen-update
```

### Test Coverage

To view coverage results locally:
1. `cd _output/cover && python3 -m http.server 8000`
2. Open browser: `http://<machine_ip>:8000/coverage.html`

## Code Style Guidelines

- **CGO:** Disabled by default (`CGO_ENABLED=0`)
- **Build Flags:** Static linking with vendored dependencies (`-mod=vendor`)
- **Linting:** Uses `golangci-lint` with configuration in `.golangci.yml`
- **Timeout:** Linting runs with 10-minute timeout
- Always run `make lint` before committing to catch style violations

### Go-Specific Guidelines

- Use vendored dependencies (never modify `vendor/` manually)
- Update boilerplate with `scripts/boilerplate.go.txt`
- Follow standard Go formatting (gofmt)
- Write tests alongside new code (test files: `*_test.go`)

## Testing Instructions

### Running Tests

```bash
# Run all tests
make test

# Run tests for specific package
go test ./pkg/auth

# Run specific test by name
go test ./pkg/core -run TestHTTPServiceRoutes

# Run all packages with parallel execution
go test -p $(nproc) -timeout 10m ./...
```

### Test Requirements

- All code changes must include or update unit tests
- Tests must pass before creating a pull request
- Test timeout is set to 10 minutes
- Fix any test failures before requesting review
- Add tests even if not explicitly requested in the issue

### Package Testing Structure

- Tests are co-located with source code
- Test files: `pkg/<package>/*_test.go`
- Test data: `pkg/<package>/test/` directories
- Mock data and fixtures should be in `test/` subdirectories

## Commit Message Guidelines

### Conventional Commits

All commits and PRs must follow [Conventional Commits v1.0.0](https://www.conventionalcommits.org/en/v1.0.0/).

**Format:** `type(scope): short description`

- **type:** Kind of change (see below)
- **scope:** Product/area affected (required for feat, fix, perf)
- **short description:** One sentence, present tense

### Commit Types

**End User (release notes):**
- `feat` - New features
- `fix` - Bug fixes
- `perf` - Performance improvements

**Foundational (no release notes):**
- `docs` - Documentation only
- `build` - Build system changes
- `test` - Test additions/changes
- `refactor` - Code restructuring
- `ci` - CI configuration
- `chore` - Maintenance tasks
- `style` - Formatting changes
- `revert` - Reverting changes

### Examples

```text
fix(docs): remove dead hyperlink
refactor(docs): use java 8 streams
perf(workspace): improve workspace mount speed
feat(workspace): enable workspaces in staging env
fix(formatter): handle unicode chars with csv formatted output
```

### Commit Message Body

- Include motivation for the change
- Contrast implementation with previous behavior
- May include GitHub issue references in footer
- Use `BREAKING CHANGE:` in footer if applicable

## Pull Request Instructions

### PR Checklist

1. **Before Creating PR:**
   - Run `make lint`
   - Run `make test`
   - Verify all tests pass
   - Update unit tests for changed code
   - If dependencies changed, run `make vendor-update`, then `make license-update` and verify with `make check-license`

2. **PR Title Format:** Use conventional commit format

   ```text
   type(scope): short description
   ```

3. **PR Description:**
   - Explain motivation for changes
   - Link to related issues
   - Note any breaking changes
   - Describe testing performed

4. **Review Process:**
   - Wait for CI status checks to pass
   - Address reviewer feedback
   - NVCA developer will merge when approved

### Branch Strategy

- Fork the repository
- Create feature branch in your fork
- Push changes to your fork
- Create PR from your fork to `main`
- Trunk-based development: all changes merge to `main`

## Security Considerations

### Secret Management

- **Never commit secrets** to the repository
- Secret fetcher components: `pkg/secret/`
- Use secure credential storage mechanisms appropriate for your environment

### Authentication

- OAuth/JWT handling: `pkg/oauth/`
- Token management: `pkg/auth/`, `pkg/ngc/`
- Always validate tokens before use
- JWKS caching implemented in `pkg/oauth/jwtcache.go`

### Private Dependencies

If using private dependencies, configure access appropriately:
- Set `GOPRIVATE` environment variable for private module domains
- Use secure credential storage for authentication
- Ensure tokens have minimum required permissions

## LICENSE File Maintenance

The `LICENSE` file contains only the NVIDIA Apache 2.0 project license (so [pkg.go.dev](https://pkg.go.dev) can detect it). The `NOTICE` file contains the list of third-party licenses from vendored dependencies, following [Apache NOTICE convention](https://www.apache.org/dev/licensing-howto.html).

**When to update:**
- After running `make vendor-update`
- After adding/removing/updating dependencies in `go.mod`

**How to update:**
```bash
# Update NOTICE with current vendor license list
make license-update

# Verify NOTICE is in sync (also runs in CI)
make check-license
```

**CI enforcement:**
- The `check-license` CI job verifies the NOTICE file is in sync with vendor licenses
- If out of sync, run `make license-update` to fix

**Why this matters:**
- Legal compliance requires accurate attribution of third-party code
- NOTICE must reflect current vendored dependencies

## Common Troubleshooting

### Build and Test Issues

- Ensure Go 1.24.0+ is installed
- Verify `vendor/` directory is up to date: run `make vendor-update`
- Check that all dependencies are available
- Run `make lint` to catch style violations

## Additional Resources

- [CONTRIBUTING.md](CONTRIBUTING.md) - Detailed contribution guidelines
- [README.md](README.md) - Project overview and setup
- [SECURITY.md](SECURITY.md) - Security policies
- [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) - Community guidelines
- [Issues](https://github.com/NVIDIA/nvcf-go/issues) - GitHub issue tracker
