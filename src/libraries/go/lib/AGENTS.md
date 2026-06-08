# AGENTS.md

Scope: everything under `src/libraries/go/lib/`.

## Project Overview

This subtree is `go-lib`, the shared Go library module for NVCF. It is maintained
inside the monorepo and uses the module path
`github.com/NVIDIA/nvcf/src/libraries/go/lib`.

The package roots under `pkg/` are:

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
- `pkg/otelconfig/` - OpenTelemetry collector config rendering and backend config templates
- `pkg/secret/` - Secret management and file fetching
- `pkg/types/` - Type definitions and configuration
- `pkg/version/` - Version utilities
- `pkg/zapotelspan/` - Helpers for adding OpenTelemetry span fields to zap logs

BYOO-facing `otelconfig` commands and fixtures live in `tools/byoo`.

## Commands

Run these from `src/libraries/go/lib`:

```bash
make vendor-update
make lint
make test
make codegen-update
make update-testdata
make check-testdata
```

Useful targeted commands:

```bash
go test ./pkg/core -run TestHTTPServiceRoutes
go test ./pkg/auth
GOWORK=off GOFLAGS=-mod=vendor go test -race -timeout 10m ./...
```

Run Bazel commands from the repository root:

```bash
bazel build //src/libraries/go/lib/...
bazel test //src/libraries/go/lib/...
bazel test //src/libraries/go/lib:golangci_lint
```

Bazel build, test, and lint targets are the CI gate for this subtree. The Bazel
lint target uses the shared runner at `rules/golangci/golangci_lint.sh` with this
module's `.golangci.yml`. `make lint` still runs the same module-scoped
`golangci-lint` command for local iteration.

## Root CI

Root CI generates the `go-lib` subproject validation pipeline from
`tools/ci/subproject-validations.yaml`.

The current subproject checks are:

- vendor: `./tools/ci/check-go-vendor src/libraries/go/lib`
- codegen: `./tools/ci/check-go-codegen src/libraries/go/lib --command 'make codegen-update'`
- Bazel build/test/lint: `go-lib-bazel-build-test`, including `//src/libraries/go/lib:golangci_lint`

Local Make targets source `.env`. Keep these values current when coverage
behavior changes:

- `GO_TEST_COVERAGE_THRESHOLD`
- `GO_TEST_COVERAGE_EXCLUDE_REGEX`

## Codegen

`make codegen-update` uses the root helper `tools/scripts/update-go-deepcopy`.
It regenerates:

- `pkg/types/nvca/config/zz_generated.deepcopy.go`
- `pkg/icms-translate/translate/common/zz_generated.deepcopy.go`
- `pkg/icms-translate/translate/function/zz_generated.deepcopy.go`
- `pkg/icms-translate/translate/task/zz_generated.deepcopy.go`

The shared generated-file header is `tools/scripts/boilerplate.go.txt`. Generated
files should carry the NVIDIA Apache header.

After changing generated inputs, run:

```bash
make codegen-update
```

If generated sources change package lists or BUILD file source lists, also run
Gazelle from the repository root:

```bash
bazel run //:gazelle
```

## Dependency and Notice Maintenance

This module uses a checked-in `vendor/` tree. Do not edit `vendor/` by hand.
Use:

```bash
make vendor-update
```

This subtree does not maintain its own `NOTICE` file. Vendored notices and
licenses are collected into the repository root `NOTICE`. When dependencies or
vendored license files change, run from the repository root:

```bash
./tools/scripts/update-license
go run ./tools/collect-dependencies
```

## Style and Testing

- Use `GOWORK=off` and `GOFLAGS=-mod=vendor` when running module-scoped Go commands that need to match this module's vendored dependency set.
- `bazel test //src/libraries/go/lib:golangci_lint` runs `golangci-lint` via `go run` with `.golangci.yml`; `make lint` remains a local equivalent.
- Keep tests next to the code they cover.
- Use `testdata/` for fixtures and refresh consolidated `icms-translate` fixtures with `make update-testdata`.
- Do not commit secrets, tokens, or generated local output such as `_output/`.

## Public Mirror

The subtree OSS mirror is controlled by `.oss-allowlist`. Keep it explicit.
Do not add internal CI, scanner, registry, or toolbox files back to the allowlist.

Root files intentionally omitted from this subtree include local `NOTICE`,
standalone `.gitlab-ci.yml`, toolbox files, Renovate config, Sonar config, and
NSPECT allowlists. Root monorepo tooling owns those concerns now.
