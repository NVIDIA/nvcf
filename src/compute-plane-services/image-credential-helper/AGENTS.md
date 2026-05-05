# AGENTS.md - Guide for AI Coding Agents

Quick reference for working with **nvcf-image-credential-helper**, a Kubernetes utility (Go) that automates container registry credential management for NVCF BYOC clusters.

## Quick Start

**Repository structure:**
- `cmd/image-credential-helper/` - Main binary entrypoint
- `credhelper/` - Core credential helper logic (ECR, Volcengine, K8s helpers)
- `examples/` - Sample Kubernetes manifests
- `docker/` - Dockerfile for local/CI use (not shipped yet)

## Build & Test Commands

**Quick check before commit:**
```bash
make test    # Unit tests
make lint    # golangci-lint (goheader, etc.)
```

**Run specific tests:**
```bash
go test ./credhelper                     # All tests in package
go test ./credhelper -run TestECRHelper  # Single test function
go test -v ./...                         # Verbose output
```

**Build:**
```bash
make build    # Build binary to _output/bin/image-credential-helper
```

`make test` runs `go test` with coverage (report at `_output/cover/coverage.html`) and `make vendor-update` runs `go mod vendor`. Optional: `make shellcheck` for scripts in `./scripts`.

## Testing Instructions

**Before committing:**
```bash
make test    # Must pass
make lint    # Must pass (golangci-lint with goheader)
```

**Test structure:**
- Tests live next to code: `foo.go` → `foo_test.go`
- Use table-driven tests for multiple scenarios
- Test naming: `TestFunctionName` or `TestStructName_MethodName`

**Example test pattern:**
```go
func TestECRHelper_Matches(t *testing.T) {
    tests := []struct {
        name     string
        url      string
        wantECR  bool
        wantPub  bool
    }{
        {name: "private ECR", url: "123456789012.dkr.ecr.us-east-1.amazonaws.com", wantECR: true, wantPub: false},
        {name: "public ECR", url: "public.ecr.aws", wantECR: true, wantPub: true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // test implementation
        })
    }
}
```

## Code Style

**Go standards:**
- All exported symbols need godoc comments
- Use `logrus` for logging
- Handle all errors explicitly
- Follow standard Go formatting (gofmt)
- License headers required on all Go files (enforced by goheader linter)

**Adding new registry support:**
1. Implement `CustomAuthHelper` interface in `credhelper/`
2. Add `Matches(serverURL *url.URL) (match, isPublic bool)` method
3. Add `Run(ctx, refURL, keyID, secretKey) (username, password, error)` method
4. Register helper in `credhelper/credhelper.go`

**Package structure:**
```
cmd/image-credential-helper/  - Binary entrypoint
credhelper/
  credhelper.go      - Core CredHelper interface and registry
  ecr.go             - AWS ECR helper
  volcengine.go      - Volcengine helper
  k8shelper.go       - Kubernetes secret update logic
  k8shelper_objects.go - K8s object builders
```

## Commit & PR Instructions

**Commit format (Conventional Commits v1.0.0 required):**
```
<type>(<scope>): <short description>

[optional body]

Closes #<issue-number>  # or use NO-REF if no ticket
```

**Types:**
- **End user** (in release notes): `feat`, `fix`, `perf` - **scope required**
- **Foundational** (not in release notes): `docs`, `build`, `test`, `refactor`, `ci`, `chore`, `style`, `revert`

**Examples:**
```
feat(credhelper): add support for Azure Container Registry
fix(ecr): handle FIPS endpoint correctly
test(volcengine): add unit tests for token refresh
```

**Before committing:**
```bash
gofmt -w $(find . -name '*.go' -not -path './vendor/*')  # Format Go files
make test    # Unit tests must pass
make lint    # golangci-lint must pass
```

**PR checklist:**
1. Fork repo, create feature branch
2. Add tests for your changes
3. Run `gofmt -w` on all modified Go files
4. Run `make test && make lint`
5. If dependencies changed, update the third-party licenses list in `NOTICE`
6. Commit with conventional format
7. Create PR to `main` branch
8. Fill in PR template
9. Wait for CI to pass
10. Address review feedback

## Security Considerations

**Critical rules:**
- NEVER commit credentials, API keys, or secrets
- Validate all inputs
- Use TLS for all external communications
- Run containers as non-root when possible

**Reporting vulnerabilities - DO NOT use GitHub issues:**
- Web: https://www.nvidia.com/object/submit-security-vulnerability.html
- Email: psirt@nvidia.com
- Include: product/version, vulnerability type, repro steps, PoC code, impact

## NOTICE File Maintenance

The `NOTICE` file contains attribution and a list of third-party licenses from vendored dependencies. The `LICENSE` file contains only the NVIDIA Apache 2.0 license for this project.

**When to update:**
- After running `make vendor-update`
- After adding/removing/updating dependencies in `go.mod`

**How to update:**
```bash
# Generate the list of third-party licenses
find vendor -name "LICENSE*" -type f | sort

# Copy the output to the third-party licenses section in NOTICE file
```

## Common Gotchas

1. **Dependency changes = update vendor + NOTICE** - run `make vendor-update` after changing go.mod, then update the third-party licenses section in `NOTICE` file
2. **ECR public vs private** - `public.ecr.aws` is treated as public (no auth needed)
3. **Volcengine region extraction** - Region is parsed from the registry hostname
4. **K8s secret naming** - Pull secrets follow pattern `workload-{id}-regcred-{index}` or `worker-{id}-regcred-{index}`
5. **Credential caching** - Credentials are cached per registry host to avoid repeated API calls

## Supported Registry Providers

| Provider | Pattern | Notes |
|----------|---------|-------|
| AWS ECR | `*.dkr.ecr.*.amazonaws.com` | Uses AWS SDK for token refresh |
| AWS ECR Public | `public.ecr.aws` | Treated as public, no auth |
| Volcengine | `*.cr.volces.com` | Uses Volcengine SDK |

## Adding New Registry Support

To add support for a new container registry:

1. Create a new file `credhelper/<provider>.go`
2. Implement the `CustomAuthHelper` interface:
```go
type myHelper struct {
    newClient func(ctx context.Context, ...) (myClient, error)
}

func (h myHelper) Matches(serverURL *url.URL) (match, isPublic bool) {
    // Return true if this helper handles the registry
}

func (h myHelper) Run(ctx context.Context, refURL *url.URL, keyID, secretKey string) (username, password string, err error) {
    // Fetch short-lived credentials from the provider
}
```
3. Register in `credhelper/credhelper.go`:
```go
var customAuthHelpers = map[string]CustomAuthHelper{
    "ecr":        ecrHelper{},
    "volcengine": volcengineHelper{},
    "myprovider": myHelper{},  // Add here
}
```
4. Add tests in `credhelper/<provider>_test.go`

## Quick Links

- [CONTRIBUTING.md](./CONTRIBUTING.md) - Contribution guidelines
- [CODE_OF_CONDUCT.md](./CODE_OF_CONDUCT.md) - Community guidelines
- [SECURITY.md](./SECURITY.md) - Security policies
- [Conventional Commits](https://www.conventionalcommits.org/)
- Maintainers: nvidia/nvca-maintainers

## Agent Expectations
- Keep changes scoped to this repo.
- Check for existing local changes before editing, and do not overwrite user work or generated artifacts without confirming the need.
