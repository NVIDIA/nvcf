# AGENTS.md - go worker library

Native Go library for worker-side NVCF helpers: API clients, NATS consumers,
proxy utilities, tracing, metering, auth, and test fixtures.

## Layout

- `nvcf/` and `nvct/`: service clients and worker connection helpers
- `consumer/`: NATS work request consumer
- `proxy/`: HTTP/3, reverse proxy, routing, and connection helpers
- `proto/`: generated NVCF and NVCT protobufs
- `tracing/`, `metrics/`, `metering/`: observability helpers
- `test/testutils/`: mock NVCF, NVCT, NATS, OTEL, and artifact servers

## Build and Test

```bash
go test ./...
go build ./...
```

CI subproject id: `go-worker`. The umbrella uses the `go-tool` profile in
`tools/ci/subproject-validations.yaml`, an internal GitLab CI config not
present in this public snapshot.

## Local Gotchas

- Do not hand-edit generated protobuf files under `proto/`.
- Keep test utilities fake-only; do not commit real service tokens or endpoints.
- This module depends on shared Go library patterns from `src/libraries/go/lib`.
