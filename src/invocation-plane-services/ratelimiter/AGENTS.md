# AGENTS.md - ratelimiter

Native Go image-source subtree for the NVCF rate limiter service.

## Layout

- Go module: `ratelimiter`
- Entrypoint: `cmd/main.go`
- Protos: `pb/` and `nvcf/pb/`

## Build and Test

```bash
go build ./...
go test ./...
gofmt -w <changed-go-files>
```

Bazel is the CI build path:

```bash
bazel test //... --flaky_test_attempts=3
```

CI subproject id: `ratelimiter`. Native Bazel validation and release wiring
live in `tools/ci/subproject-validations.yaml`.

## Proto and Test Gotchas

- Do not hand-edit generated `*.pb.go` or `*_grpc.pb.go` files.
- Change `.proto` files, then run `go generate` from both `pb/` and
  `nvcf/pb/` when those packages are affected.
- `protoc` is required for proto regeneration.
- Package tests under `ratelimiter/cmd` bind fixed local ports such as
  `127.0.0.1:3320`; address conflicts are usually environmental.
- New hand-authored Go files must use the existing SPDX Apache-2.0
  header. `check-license` enforces this at CI time.
