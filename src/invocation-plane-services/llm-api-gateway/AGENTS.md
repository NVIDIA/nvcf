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
- The gateway fails closed: it exits at startup unless `NVCF_GRPC_ADDR`,
  `API_KEYS_PATH`, or `ALLOW_ANONYMOUS=true` is set. The local overlay and
  `mise run run` set `ALLOW_ANONYMOUS=true`. Auth modes, the static key file
  format, and the related env vars are documented in `README.md` under
  Authentication.
- NVCF mode parses `model` as `<routing_key>/<model>`. Static API key mode
  (`API_KEYS_PATH`) keeps the whole `model` string with an empty routing key.
  Model parsing and auth changes must keep both modes covered
  (`api/static_mode_test.go`, `server/server_test.go`).
- The caller's `Authorization` header must never reach Stargate. Only
  `STARGATE_SERVICE_TOKEN` is sent upstream.
- `GET /v1/models` and `GET /v1/registry` are built from the router's
  `GET /v1/models` (`api/model_registry_handler.go`) and skip auth when
  `PUBLIC_READ_ENDPOINTS` is true (default in static-keys and anonymous mode).
- Chart and stack changes belong in the chart or stack subtree. Keep runtime
  env/config names aligned when cross-subtree follow-up is needed.

## Naming

- Source subtree: `src/invocation-plane-services/llm-api-gateway`
- Go module suffix: `llm-gateway`
- Runtime service: `llm-api-gateway`
