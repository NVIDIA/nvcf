# AGENTS.md - Guide for AI Coding Agents

Quick reference for working with **NVCA** (NVIDIA Cloud Functions Agent), a Kubernetes agent and operator (Go) that manages NVCF Bring-Your-Own-Compute (BYOC) clusters. This monorepo contains both the NVCA agent and the NVCA Operator (formerly a separate repository).

## Quick Start

**Use standard Go tooling by default:**
```bash
go test ./internal/miniservice/... \
    -ldflags '-X github.com/NVIDIA/k8s-dra-driver-gpu/internal/info.version=v25.8.0'
```

**Repository structure:**
- `cmd/nvca/` - Main NVCA agent binary
- `cmd/webhook-server/` - Admission webhook server
- `cmd/discover-gpus/` - GPU discovery tool
- `cmd/nvca-operator/` - NVCA Operator binary (manages agent lifecycle)
- `cmd/nvca-self-managed/` - Self-managed cluster setup binary
- `cmd/nvca-mirror/` - Image mirroring binary
- `pkg/nvca/` - Core agent logic (queue processing, workload management)
- `pkg/webhook/` - Webhook handlers
- `pkg/storage/` - Storage request controller
- `pkg/operator/` - Operator reconciliation logic (NVCFBackend lifecycle)
- `pkg/apis/` - CRD definitions (triggers codegen)
- `pkg/apis/nvcf/v1/` - NVCFBackend CRD types (operator)
- `internal/miniservice/` - MiniService controller
- `internal/gc/` - Garbage collection cleaners
- `internal/metrics/` - Prometheus metrics
- `deployments/nvca-operator/` - Operator Helm chart
- `test/` - E2E and integration tests

## Dev Environment Tips

- Use the direct `go test` pattern below, including `-ldflags`, to run a single test
- Run `make test-e2e` for full end-to-end validation
- Set `KUBEBUILDER_ASSETS` for VSCode/Cursor to run envtest tests in-editor (see README.md)

## Build & Test Commands

**Quick check before commit:**
```bash
make test           # Unit tests (excludes generated code)
make lint           # golangci-lint + helm lint
```

**Run specific tests:**
```bash
go test ./pkg/nvca -run TestBackend \
    -ldflags '-X github.com/NVIDIA/k8s-dra-driver-gpu/internal/info.version=v25.8.0'
go test -v ./pkg/storage \
    -ldflags '-X github.com/NVIDIA/k8s-dra-driver-gpu/internal/info.version=v25.8.0'
```

**Running tests in AI agents / IDEs:**

When running `go test` directly (not via `make test`), you must pass ldflags to set the DRA driver version, otherwise tests will panic with `could not parse "unknown" as version`:

```bash
go test ./internal/miniservice/... -v \
    -ldflags '-X github.com/NVIDIA/k8s-dra-driver-gpu/internal/info.version=v25.8.0'
```

This is automatically handled by `make test`, but required when using `go test` directly.

**Build artifacts:**
```bash
mkdir -p _output/bin
go build -o _output/bin/nvca ./cmd/nvca
go build -o _output/bin/nvca-operator ./cmd/nvca-operator
go build -o _output/bin/webhook-server ./cmd/webhook-server
```

**Code generation (MUST run after CRD changes):**
```bash
make codegen-update      # Generate clientsets, informers, listers, DeepCopy
make openapigen-update   # Update OpenAPI schema
make testdata-update     # Regenerate test data
make docs-update         # Regenerate docs
```

**Local E2E tests:**
```bash
make test-e2e
```

## Testing Instructions

**Before committing:**
```bash
# Format YOUR code only (DO NOT run gofmt on vendor/)
gofmt -w $(find . -name '*.go' -not -path './vendor/*')

# Run all CI checks locally
make test        # Must pass (>73% coverage target)
make lint        # Must pass (golangci-lint)

# Verify vendor check will pass (no uncommitted changes)
git diff --exit-code
```

**Quick pre-commit validation:**
```bash
# Run all checks in one go
gofmt -w $(find . -name '*.go' -not -path './vendor/*') && make test && make lint && git diff --exit-code
```

**Test structure:**
- Tests live next to code: `foo.go` → `foo_test.go`
- Use table-driven tests for multiple scenarios
- Test naming: `TestFunctionName` or `TestStructName_MethodName`
- Test data in `pkg/nvca/testdata/`, `internal/*/testdata/`
- E2E tests in `test/e2e/` (run with `make test-e2e`)

**Envtest setup (for K8s controller tests):**
```bash
# Automatically handled by make test, but for IDE:
source <(./scripts/setup_envtest)
echo $KUBEBUILDER_ASSETS
```

**Example test pattern:**
```go
func TestQueueMessageProcessing(t *testing.T) {
    tests := []struct {
        name    string
        message *QueueMessage
        wantErr bool
    }{
        {name: "valid creation", message: &QueueMessage{...}, wantErr: false},
        {name: "nil message", message: nil, wantErr: true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // test implementation
        })
    }
}
```

## Code Style

**General implementation guidance (applies to AI agents too):**
- Match the existing package structure, naming, logging, and error-handling conventions in the repo instead of imposing a new framework or style
- Prefer small, explicit changes over broad refactors
- Keep code idempotent and defensive when working on controllers, reconcilers, queue handlers, or release tooling
- Handle errors explicitly and return context-rich errors when that helps the caller or operator
- Add or update tests when behavior changes
- Avoid placeholders, speculative abstractions, and unnecessary indirection
- Comment only when the logic is non-obvious
- If a task is not HTTP/API work, do not force API patterns into the implementation

**Go standards:**
- All exported symbols need godoc comments
- Use `logrus` for app logging, `klog` for K8s logging
- Handle all errors explicitly
- Follow standard Go formatting (gofmt, goimports - enforced by CI)
- 120 char line limit

**NVCA-specific patterns:**
- Queue message handlers must be idempotent
- Use context for cancellation and timeouts
- Separate storage controller reconciliation from main agent loop
- MiniService controller handles Helm chart lifecycle

**Operator-specific patterns:**
- Reconciliation must be idempotent
- Always update status separately from spec
- Use finalizers for cleanup
- Set owner references on managed resources
- Keep webhook logic fast and deterministic

**Reconcile error handling:**
- Use `k8sutil.IsTransientK8sError(err)` (`internal/util/k8sutil/errors.go`) to classify K8s API errors before deciding how to handle them
- **Transient errors** (timeouts, 429, 503, 500, conflicts, network errors) → return `reconcile.Result{Requeue: true}, nil` (silent requeue, no error metric)
- **Non-transient errors** (Forbidden, Unauthorized, Invalid, Gone) → return `reconcile.Result{}, err` (surfaces as reconcile failure)
- **Never return both `Requeue: true` and an error** — returning an error already triggers automatic requeue with exponential backoff
- Use `reconcile.TerminalError()` for truly unrecoverable errors to prevent retries
- For batch error collections (cleanup paths), use `k8sutil.AnyNonTransientK8sError(errs)` to check if any error is non-transient
- Log transient errors at `log.V(1).Info()`, non-transient at `log.Error()`
- Keep NotFound handling scoped to resource discovery and Kubernetes API errors that can recover on retry.

**Package structure:**
```
cmd/                    - Binary entry points (nvca, webhook-server, nvca-operator, nvca-self-managed, nvca-mirror)
pkg/apis/               - CRD definitions (triggers codegen)
pkg/apis/nvcf/v1/       - NVCFBackend CRD types
pkg/nvca/               - Core agent logic
pkg/storage/            - StorageRequest controller
pkg/webhook/            - Admission webhooks
pkg/queue/              - SQS/NATS queue clients
pkg/featureflag/        - Feature flag management
pkg/operator/           - Operator reconciliation logic
pkg/operator/types/     - Operator-specific types
internal/               - Private packages
  miniservice/          - MiniService controller
  gc/                   - Garbage collection
  metrics/              - Prometheus metrics
  util/                 - Shared utilities
deployments/nvca-operator/ - Operator Helm chart
```

## Commit & PR Instructions

**General PR authoring guidance (applies to AI agents too):**
- Use the local PR template shape with `Customer Summary`, `TL;DR`, `Additional Details`, `For the Reviewer`, `For QA`, and `Tickets`
- For `feat`, `fix`, and `perf` PRs, make `Customer Summary` customer-facing because it is used in release notes
- In `For QA`, list the exact checks or commands you ran and state whether QA is needed
- Reference the relevant ticket when one exists; use `NO-REF` only when that is acceptable for the change

**Commit format (Conventional Commits v1.0.0 required):**
```
<type>(<scope>): <short description>

[optional body]

Closes NVCFCLUST-XXXX  # or use NO-REF if no ticket
```

**Types:**
- **Customer** (in release notes): `feat`, `fix`, `perf` - **scope required**
- **Foundational** (not in release notes): `docs`, `build`, `test`, `refactor`, `ci`, `chore`, `style`, `revert`

**Examples:**
```
feat(queue): add support for batch message processing
fix(storage): handle PVC deletion race condition
test(miniservice): add unit tests for chart reconciliation
```

**Breaking changes - add to footer:**
```
feat(api): change MiniService status structure

BREAKING CHANGE: The status.conditions field has been restructured.

Closes NVCFCLUST-1234
```

**Before committing (CRITICAL - prevents CI failures):**
```bash
# 1. Format YOUR Go code only (NOT vendor/)
gofmt -w $(find . -name '*.go' -not -path './vendor/*')

# 2. Run tests
make test           # All unit tests must pass

# 3. Run linters
make lint           # All linters must pass

# 4. Run codegen if needed
make codegen-update # If you changed CRDs in pkg/apis/

# 5. Verify no uncommitted changes (vendor check)
git diff --exit-code

# Quick one-liner to run all checks:
gofmt -w $(find . -name '*.go' -not -path './vendor/*') && make test && make lint && git diff --exit-code
```

**IMPORTANT: Never modify vendor files**
- All fixes and changes must be made in non-vendor files only
- Vendor files are managed by `go mod vendor` and should not be edited directly
- If you encounter issues in vendor code, update dependencies in `go.mod` and run `go mod vendor`

**PR checklist:**
1. Fork repo, create feature branch
2. Add tests for your changes (aim for >73% coverage)
3. **Format YOUR code only**: `gofmt -w $(find . -name '*.go' -not -path './vendor/*')`
4. **Run all checks**: `make test && make lint`
5. Run `make codegen-update` if you touched `pkg/apis/`
6. **Verify clean working directory**: `git diff --exit-code`
7. Commit with conventional format
8. Create PR to `main` branch
9. Fill in PR template (Customer Summary for feat/fix/perf, TL;DR, ticket number)
10. Wait for CI to pass
11. Address review feedback

**Branch strategy:**
- `main` - development branch, target for new features
- `release-x.y` - release branches, bugfixes only

## Security Considerations

**Critical rules:**
- NEVER commit credentials, API keys, or secrets (use K8s secrets, inject at runtime)
- Validate all webhook inputs, use TLS, implement timeouts
- Check `go.mod` replace directives for security patches
- Run containers as non-root when possible

**Reporting vulnerabilities - DO NOT use public issue trackers:**
- Web: https://www.nvidia.com/object/submit-security-vulnerability.html
- Email: psirt@nvidia.com
- Include: product/version, vulnerability type, repro steps, PoC code, impact

## Common Gotchas

1. **Use standard Go tooling for GitHub-facing work** - only use internal dev shells when explicitly available
2. **CRD changes = run codegen** - after touching `pkg/apis/`, MUST run `make codegen-update` and `make openapigen-update`
3. **Dependency changes = update vendor + LICENSE** - run `go mod tidy` and `go mod vendor` after changing `go.mod`, then update the THIRD-PARTY LICENSES section in `LICENSE` file
4. **Envtest required** - some tests need `KUBEBUILDER_ASSETS` set (handled by `make test`)
5. **Local vs CI** - local tests may pass but CI may have additional checks
6. **Queue message idempotency** - handlers may receive duplicate messages
7. **Storage controller timing** - PVC operations are async, handle races carefully
8. **MiniService chartcache key must include namespace** - The `chartcache.ChartCacheInput` struct (in `internal/miniservice/chartcache/`) is used to generate cache keys for rendered Helm charts. Any field that affects the Helm template output (e.g., `.Release.Namespace`) MUST be included in this struct. If namespace is missing from the cache key, cached output from namespace A can be incorrectly returned for namespace B. When adding new fields to `HelmReValRenderInput` that affect rendering, also add them to `ChartCacheInput` and update `getCacheKey()` in `reconcile.go`.

## Code Generation Triggers

**Modified CRD types in `pkg/apis/`?** Run:
```bash
make codegen-update     # Generates clientsets, informers, listers, DeepCopy
make openapigen-update  # Updates OpenAPI schema
```

**Modified test data templates?** Run:
```bash
make testdata-update
```

**Changed dependencies (go.mod)?** Run:
```bash
go mod tidy
go mod vendor

# Then update LICENSE file with new third-party licenses:
find vendor -name "LICENSE*" -type f | sort
# Copy the output to the THIRD-PARTY LICENSES section in LICENSE
```

**Update all generated code:**
```bash
make codegen-update
make openapigen-update
make testdata-update
make docs-update
```

## LICENSE File Maintenance

The `LICENSE` file contains the NVIDIA Apache 2.0 license and a complete list of third-party licenses from vendored dependencies.

**When to update:**
- After running `go mod vendor`
- After adding/removing/updating dependencies in `go.mod`

**How to update:**
```bash
# Generate the list of third-party licenses
find vendor -name "LICENSE*" -type f | sort

# Copy the output to the THIRD-PARTY LICENSES section in LICENSE file
```

**Why this matters:**
- Legal compliance requires accurate attribution of third-party code
- The LICENSE file must reflect current vendored dependencies

## Metrics Initialization

Counter metrics are pre-initialized to zero in `internal/metrics/metrics.go` so they appear on the first Prometheus scrape. **When adding new label values, update the initialization code.**

**Files to update:**
- `internal/metrics/metrics.go` - Add new values to the initialization loops in `NewDefaultMetrics()`
- `internal/metrics/metrics_test.go` - Add new values to `TestMetricsInitializedToZero`

**Currently initialized metrics:**

| Metric | Label | Values to maintain |
|--------|-------|-------------------|
| `K8sAPISuccessTotal` / `K8sAPIFailureTotal` | `resource` | K8s resource types used in `TrackK8sAPICall()` calls |
| `MiniServiceEventErrorTotal` | `event_kind` | Event constants in `internal/miniservice/` |
| `OrphanedResourceCleanupTotal` | `resource_type` | Constants in `internal/metrics/gctypes/types.go` |
| `OrphanedResourceCleanupTotal` | `status` | `success`, `failure` |
| `GCCleanerRunTotal` | `cleaner_name` | `Name()` methods in `internal/gc/*/cleaner.go` |
| `GCCleanerRunTotal` | `status` | `success`, `failure` |
| `KataRuntimeIsolationEnabled` | _(default labels only)_ | Initialized to 0; set via `SetKataRuntimeIsolationEnabled()` in `pkg/nvca/agent.go` |
| `WorkloadResultTotal` | `workload_type` | `container`, `helm` (defined in `AllWorkloadTypes`) |
| `WorkloadResultTotal` | `workload_kind` | Constants in `internal/metrics/workloadtypes/types.go` (`AllWorkloadKinds`) |
| `WorkloadResultTotal` | `workload_status` | `success`, `failure` |
| `WorkloadResultTotal` | `failure_category` | `FailureCategory` enum in `internal/metrics/workloadtypes/types.go` (`AllFailureCategories`) |
| `UpstreamRequestTotal` | `operation` | `UpstreamOperationHeartbeat`, `UpstreamOperationRegister`, `UpstreamOperationCredentials` in `internal/metrics/metrics.go` (`AllUpstreamOperations`) |
| `SchedulerWorkloadCount` | `scheduler_name` | `default-scheduler`, `kai-scheduler` (constants in `internal/metrics/metrics.go`) |
| `SchedulerWorkloadCount` | `workload_kind` | Constants in `internal/metrics/workloadtypes/types.go` (`AllWorkloadKinds`) |

**When to update:**
- Adding a new K8s resource type to `TrackK8sAPICall()` calls
- Adding a new GC cleaner in `internal/gc/`
- Adding a new resource type constant in `internal/metrics/gctypes/`
- Adding a new MiniService event error type
- Adding a new failure category or workload type/kind in `internal/metrics/workloadtypes/`
- Adding a new upstream operation constant to `AllUpstreamOperations` in `internal/metrics/metrics.go`

**Why this matters:**
- Prometheus `absent()` alerts fail on non-existent metrics
- `rate()` calculations give unexpected results if metrics appear mid-scrape
- Dashboards show gaps instead of zeros

## Key Environment Variables

Useful local test variables:
- `NVCF_GOMAXPROCS` - max parallel test execution (default: 4)
- `KUBEBUILDER_ASSETS` - envtest binaries location

## Versioning & Tag Formats

This project uses [Semantic Versioning](https://semver.org/) with a `v` prefix for git tags.

**Supported tag formats:**

| Format | Description | Example | Audience |
|--------|-------------|---------|----------|
| `vMAJOR.MINOR.PATCH` | Release version | `v1.20.0` | QA / Production |
| `vMAJOR.MINOR.PATCH-dev.N` | Dev/prerelease build | `v1.20.0-dev.0` | Dev |
| `vMAJOR.MINOR.PATCH-rc.N` | Release candidate (stage) | `v1.20.0-rc.1` | QA |

**Version precedence (lowest to highest):**
- `v1.20.0-dev.0` < `v1.20.0-dev.1` < `v1.20.0-rc.1` < `v1.20.0`

**Creating tags:**
```bash
# Release tag
git tag v1.20.0

# Dev build tag
git tag v1.20.0-dev.0
```

**CI behavior:**
- Tags should trigger release validation in the hosting environment.
- Keep GitHub-facing documentation free of internal CI pipeline commands and generated pipeline artifacts.

## Observability

- **Metrics**: Prometheus metrics in `internal/metrics/` - see [METRICS.md](internal/metrics/METRICS.md) for full documentation
- **Tracing**: OpenTelemetry via `internal/otel/`
- **Logging**: Structured logging with logrus
- **Profiling**: pprof endpoints available - use `./scripts/users/nvca-pprof.sh heap`

## Debugging NVCA

```bash
# Profile NVCA heap
./scripts/users/nvca-pprof.sh heap

# Profile CPU
./scripts/users/nvca-pprof.sh profile

# View goroutines
./scripts/users/nvca-pprof.sh goroutine
```

## Feature Flags & Attributes

For cluster-wide attributes and feature flags documentation, see:
- [Feature Flags Doc](./docs/users/byoc/featureflags.md)

## Quick Links

- [CONTRIBUTING.md](./CONTRIBUTING.md)
- [Metrics Documentation](./internal/metrics/METRICS.md)
- [Feature Flags](./docs/users/byoc/featureflags.md)
- [NVIDIA Cloud Functions Docs](https://docs.nvidia.com/cloud-functions/user-guide/latest/cloud-function/cluster-management.html)
- [Conventional Commits](https://www.conventionalcommits.org/)
- Maintainers: nvidia/nvca-dev

## NVCF Workspace Routing
- Team: `@NVIDIA/nvcf-dev`.
- This repo is part of the `self-hosted-nvcf` workspace in `nvcf-agentic-dev`.
- Manifest tier: `image-source`.
- Default owner: `@NVIDIA/nvcf-dev`.
- Manifest description: NVCA worker agent application source (owned by EGX intelligent-infra team).
- Before making cross-repo assumptions, consult `nvcf-agentic-dev` first.
- Preferred local sibling path: `../nvcf-agentic-dev` if it exists.
- Resolve `nvcf-agentic-dev` in this order:
  1. local sibling checkout `../nvcf-agentic-dev`
  2. workspace checkout named `nvcf-agentic-dev`
  3. the current `origin` URL of a local `nvcf-agentic-dev` clone
- Do not assume a specific forge host.
- Start with `workspaces/self-hosted-nvcf/repos.yaml` to confirm repo ownership and nearby repos.
- When topology or rollout ordering matters, also read `workspaces/self-hosted-nvcf/docs/deployment-sequence.md` and `workspaces/self-hosted-nvcf/docs/deployment-dependencies-with-links.yaml`.
- Treat `nvcf-agentic-dev` as the routing layer: use it to identify the owning repo and dependencies, then do the implementation in the correct repo.

## Repo-Specific Routing Guidance
- This repo owns NVCA worker/agent logic. Deployment packaging usually lives in `nvca-operator-deploy`; SBOM consumers may involve `sbom-templates` and `manifests`.
- Closely related repos: `nvca-operator`, `nvca-operator-deploy`, `sbom-templates`, `manifests`.

## Agent Expectations for Workspace Routing
- Summarize the route you chose before substantial edits: why this repo is the owner, what sibling repos are relevant, and whether `nvcf-agentic-dev` docs influenced the plan.
- Keep changes scoped to this repo unless the task explicitly requires coordinated edits elsewhere.
- If the work really belongs in another repo, say so clearly and move there instead of forcing the change here.
- Check for existing local changes before editing, and do not overwrite user work or generated artifacts without confirming the need.
