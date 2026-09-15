# AGENTS.md - request-trace-uploader

Native Go sidecar scaffold for closed Dynamo request-trace segments. It does
not publish or delete source segments until a supported upload adapter lands.

## Layout

- `cmd/`: process entrypoint and OCI image target
- `config/`: sidecar contract and bounded policy parsing
- `segment/`: closed segment discovery
- `backend/`: export destination contract and the backend registry
- `service/`: startup, recovery scan, and HTTP server
- `internal/health/`: liveness and readiness handlers

## Build and test

```bash
bazel test //src/compute-plane-services/request-trace-uploader/...
bazel build //src/compute-plane-services/request-trace-uploader/cmd:image
```

Run `bazel run //:gazelle` after changing Go imports or Bazel metadata.

## Rules

- Target Dynamo v1.4.0. It writes one segment family, so classify records by
  `event_type` and never by filename or prefix.
- Treat the highest indexed segment as active.
- Do not add a release entry until an approved upload adapter exists.
- Do not add a Prometheus scrape endpoint. The later observability increment
  exports logs, traces, and metrics through BYOO OTLP endpoints.
- Do not log request payloads, credentials, paths, or remote upload IDs.
- `config`, `segment`, `backend`, and `service` are consumed by other modules
  that link their own backends. Treat their exported surface as a contract and
  keep implementation detail under `internal/`.
- Backends register themselves from an init function. This binary links none,
  so a build reports an unlinked backend rather than failing to compile.
