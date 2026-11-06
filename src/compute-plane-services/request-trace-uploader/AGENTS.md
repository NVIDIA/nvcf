# AGENTS.md - request-trace-uploader

Native Go sidecar that exports closed Dynamo request-trace segments through a
backend and deletes each source only after the backend confirmed it. See
`README.md` for the pipeline and settings.

## Layout

- `cmd/`: process entrypoint and OCI image target
- `runner/`: shared startup, drain and final export for every binary
- `config/`: settings and strict loading (`LoadStrict`); no env aliases
- `service/`: startup, the pipeline (`Uploader`), and the HTTP server
- `segment/`: closed segment discovery
- `record/`, `transform/`: reading records, suppression, redaction
- `format/`: the backend's transport format writers
- `batch/`, `journal/`: batching and the durable state machine
- `backend/`: export destination contract and the backend registry
- `telemetry/`, `credentials/`: hooks other modules use
- `telemetry/otlp/`: the OTLP metrics exporter, kept out of `telemetry` so the
  interface package does not pull in the OpenTelemetry SDK
- `internal/`: journal store, cleanup, disk guard, fault policy, health
- `uploadertest/`, `backend/backendtest/`: fixtures, crash helper, fake backend

## Build and test

```bash
go build ./... && go vet ./... && go test ./...
bazel test //src/compute-plane-services/request-trace-uploader/...
bazel build //src/compute-plane-services/request-trace-uploader/cmd:image
```

Run `bazel run //:gazelle -- src/compute-plane-services/request-trace-uploader`
after changing Go imports or Bazel metadata.

## Rules

- Target Dynamo v1.4.0. It writes one segment family, so classify records by
  `event_type` and never by filename or prefix.
- Treat the highest indexed segment as active: never read or delete it.
- Record every step in the journal before acting on it. Record a batch before
  calling the backend, and save a returned job id before looking at the
  error. Never submit a job that has a saved id again.
- A pipeline step that a crash can interrupt gets a named point in
  `service` and a case in `service/crash_test.go`.
- Do not add a Prometheus scrape endpoint. Metrics go through `telemetry`.
- Do not log request payloads, credentials, paths, or remote upload IDs. Log
  backend faults by class, scope and reason, not by their error text.
- New settings fail startup on a bad value; never add a silent fallback.
- `config`, `segment`, `backend`, `format`, `telemetry`, `telemetry/otlp`,
  `runner`, and `service` are consumed by other modules that link their own backends.
  Treat their exported surface as a contract and keep implementation detail
  under `internal/`.
- Backends register themselves from an init function, so a build links only
  the backends it imports.
- Keep backend names, env names, and file patterns of private backends out of
  this module. They register through the hooks above.
