# AGENTS.md - llm-api-gateway

Native Go image-source subtree for the LLM API gateway and rate-limit sync
worker.

## Layout

- Go module: `github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway`
- Main gateway: `cmd/llm-api-gateway/main.go`
- Rate-limit sync worker: `cmd/llm-api-gateway-rate-limit-sync-worker/main.go`
- Runtime config and handlers live under `internal/`

## Build and Test

```bash
go build ./...
go test ./...
gofmt -w <changed-go-files>
kustomize build kustomize/overlays/local
```

Bazel is the CI build path:

```bash
bazel test //... --flaky_test_attempts=3
```

Follow the [release workflow](../../../docs/dev/github-release-process.md) for source versions and
producer handoffs. Account for both the main gateway and rate-limit sync worker images.

## LLM routing BDD

The `@llm-function-type` scenarios in the single- and multi-cluster Helmfile
features cover LLM routing and request handling. See `tests/bdd/README.md`
to run them.

## Local Gotchas

- `github.com/olric-data/olric` is replaced by
  `github.com/max007-008/olric` on branch `cas`. The fork keeps the upstream
  module path and adds the CompareAndSwap primitive used by rate limiting.
- To pick up a new fork commit, run:

  ```bash
  go get github.com/max007-008/olric@cas
  go mod tidy
  ```

- If the CAS work is upstreamed, remove the `replace` directive and bump the
  upstream requirement.
- Do not hand-edit generated protobufs or mocks. Regenerate them from the
  owning source.
- Chart and stack changes belong in the chart or stack subtree. Keep runtime
  env/config names aligned when cross-subtree follow-up is needed.

## Naming

- Source subtree: `src/invocation-plane-services/llm-api-gateway`
- Go module suffix: `llm-gateway`
- Runtime service: `llm-api-gateway`
