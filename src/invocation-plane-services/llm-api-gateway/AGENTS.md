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

CI subproject id: `llm-api-gateway`. Release wiring publishes both the main
gateway image and the rate-limit sync worker image through
`tools/ci/subproject-validations.yaml`, an internal GitLab CI config not
present in this public snapshot.

## LLM routing BDD before push

For LLM routing or request-handling changes, run the single-cluster Helmfile
suite before pushing. Also run the multi-cluster Helmfile suite when changing
cross-cluster routing or transport. Their `@llm-function-type` scenarios cover
chat streaming, Responses, classic invocation rejection, and unlisted models.
The scenarios depend on the earlier install steps; run the whole feature.

Read [BDD prerequisites](../../../tests/bdd/README.md) and follow the root
local QA safety rules before a live run. From the repository root:

```sh
cd tests/bdd
go test -short ./...
golangci-lint run --config .golangci.yml ./...
go test -run '^TestSingleClusterHelmfile$' -timeout 90m -v
go test -run '^TestMultiClusterHelmfile$' -timeout 90m -v
```

Run the live suites one at a time with an approved environment. Anchors exclude
the fake-runner wiring siblings. A wiring pass does not validate a deployment.
If a required live run fails or is blocked, stop before pushing and report the
unvalidated behavior.

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
