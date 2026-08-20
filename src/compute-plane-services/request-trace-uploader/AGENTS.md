# AGENTS.md - request-trace-uploader

Native Go sidecar scaffold for closed Dynamo request-trace segments. It does
not publish or delete source segments until a supported upload adapter lands.

## Layout

- `cmd/`: process entrypoint and OCI image target
- `internal/config/`: current sidecar contract and bounded policy parsing
- `internal/segment/`: closed trace/audit segment discovery
- `internal/health/`: liveness and readiness handlers
- `internal/metrics/`: Prometheus metrics and handler
- `internal/upload/`: future upload-client boundary
- `internal/service/`: startup, recovery scan, and HTTP server

## Build and test

```bash
bazel test //src/compute-plane-services/request-trace-uploader/...
bazel build //src/compute-plane-services/request-trace-uploader/cmd:image
```

Run `bazel run //:gazelle` after changing Go imports or Bazel metadata.

## Rules

- Preserve the existing `trace` and `audit` capture-type names.
- Treat the highest indexed segment for each prefix as active.
- Do not add a release entry until an approved upload adapter exists.
- Do not log request payloads, credentials, paths, or remote upload IDs.
