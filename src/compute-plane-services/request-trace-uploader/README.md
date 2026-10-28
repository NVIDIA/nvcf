# request-trace-uploader

`request-trace-uploader` is the NVCF sidecar for Dynamo request tracing.
Dynamo calls the captured objects `RequestTraceRecord` values. The records that
contain input and output payloads have event type `request_payload`.

The minimum supported Dynamo version is v1.4.0. That version writes every
record type to one rolling `.jsonl.gz` segment family, so records are
classified by their `event_type` field rather than by filename. Dynamo v1.3.x
emitted a separate `AuditRecord` type to its own file family and is not
supported.

This service discovers only closed segments. The highest indexed segment
remains owned by the Dynamo writer.

## How it works

`service.Uploader` runs the pipeline against a journal kept on the spool
volume, so every step survives a restart.

1. Startup: health answers at once and reports not ready. The uploader
   takes the spool lock, waiting up to the drain timeout plus 10 seconds for
   an earlier uploader. It loads the journal, repairs what a crash left, and
   reports ready. Files another uploader left on the spool stay where they
   are. A journal entry whose source it cannot identify keeps the process
   live and not ready until an operator acts.
2. Intake: each closed segment is fingerprinted (SHA-256) and recorded
   before it is read. The highest indexed segment is never read or deleted.
3. Record handling: suppression, then redaction, then the backend's format
   writer. A backend chooses its transport format by implementing
   `format.Factory` on its client; a backend that takes gzip JSON lines on
   one destination gets the default writer.
4. Batching: prepared segments are grouped per destination within the
   backend's file and byte limits, and lanes are flushed every pass. A batch
   is recorded before the backend is called, and a returned job id is saved
   before the error is looked at.
5. Polling: one loop polls every open job on the status interval. A job with
   a saved id is polled, never submitted again. A job unfinished past the max
   job age is kept as stranded. A status the core does not know is counted
   in `unknown_status` and polled again.
6. Cleanup: after success, transports are deleted, the source is re-hashed
   if its size or modification time changed, and only then deleted. A
   backend that does not export, such as `debug` or a dry run, never deletes
   a source.
7. Shutdown: on SIGTERM the uploader lets work in progress finish, scans
   the spool one last time so a segment that closed after the last scan is
   not left behind, submits what is prepared, polls once more, and releases
   the spool, all within the drain timeout. What is left stays in
   the journal for the next start.

Faults follow `internal/retry`: auth failures and outages hold every submit
without moving a segment; a segment fault quarantines that segment after a
batch is split to find it. A backend that records its own metrics implements
`telemetry.Receiver` and gets the shared recorder. This binary exports
metrics over OTLP when an endpoint is set (see Metrics below); a distribution
can pass its own recorder in `service.Options.Metrics`.

## Metrics

Metrics are pushed over OTLP to the workload's collector. There is no scrape
endpoint. Two settings configure the export, and no other `OTEL_*` variable
changes it:

- `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`: the collector URL, `http` or
  `https`, used as given. For `http/protobuf` include the path, usually
  `/v1/metrics`. Unset turns the export off.
- `OTEL_EXPORTER_OTLP_METRICS_PROTOCOL`: `grpc`, `http`, or `http/protobuf`.
  Required when the endpoint is set.

`OTEL_EXPORTER_OTLP_ENDPOINT` is never used as a fallback. Every instrument is
named `dynamo.request_trace.<name>`, carries a `backend` attribute, and uses
only bounded attributes; counters start at zero. Metrics are pushed every 60
seconds and once more after the shutdown drain, for up to 5 seconds, so the
pod's grace period must cover the drain timeout plus 5 seconds. A failed
export never blocks an upload or changes readiness.

The `backend` package defines the export destination contract and a registry.
Submit and status are separate operations so a slow confirmation cannot block
other segments. Backends register themselves from an init function, so a build
links only the backends it imports. This binary links `debug` and
`objectstore`. A distribution that needs another backend imports it in its own
`main` and reuses the `config`, `backend`, `format`, `telemetry`, and `service`
packages from this module.

## Configuration

Required:

- `REQUEST_TRACE_UPLOADER_SOURCE_DIR`: absolute directory containing segments
- `REQUEST_TRACE_UPLOADER_BACKEND`: a backend linked into the build, such as
  `objectstore` or `debug` in this binary

Optional, with defaults:

- `REQUEST_TRACE_UPLOADER_SEGMENT_PREFIX`: default `request-trace`
- `REQUEST_TRACE_UPLOADER_SECRETS_FILE`: default `/var/secrets/secrets.json`
- `REQUEST_TRACE_UPLOADER_STATE_DIR`: default under the source directory
- `REQUEST_TRACE_UPLOADER_QUARANTINE_DIR`: default under the source directory
- `HEALTH_ADDR`: default `:8011`
- `REQUEST_TRACE_UPLOADER_SCAN_INTERVAL_SECONDS`: default `30`
- `REQUEST_TRACE_UPLOADER_ATTEMPT_TIMEOUT`: default `30s`
- `REQUEST_TRACE_UPLOADER_OPERATION_TIMEOUT`: default `90s`
- `REQUEST_TRACE_UPLOADER_MAX_RETRIES`: default `2`
- `REQUEST_TRACE_UPLOADER_RETRY_INITIAL_BACKOFF`: default `100ms`
- `REQUEST_TRACE_UPLOADER_RETRY_MAX_BACKOFF`: default `15s`
- `REQUEST_TRACE_UPLOADER_RETRY_MULTIPLIER`: default `2.0`
- `REQUEST_TRACE_UPLOADER_AUDIT_PREFIX`: a second segment family on the same
  spool; default none
- `REQUEST_TRACE_UPLOADER_REDACT_MULTIMODAL`: default `true`
- `REQUEST_TRACE_UPLOADER_DRAIN_TIMEOUT_SECONDS`: shutdown drain after
  SIGTERM; default `60`. Set it to fit the pod's grace period.
- `REQUEST_TRACE_UPLOADER_MAX_JOB_AGE_SECONDS`: how long a submitted job may
  stay unfinished before it is reported stranded; default `86400`
- `REQUEST_TRACE_UPLOADER_STATUS_INTERVAL_SECONDS`: how often every open job
  is polled; default `30`

Kratos Bulk Upload is an asynchronous job API, so it polls for terminal status.
These settings apply only to that backend:

- `REQUEST_TRACE_UPLOADER_KRATOS_STATUS_INTERVAL_SECONDS`: default `5`
- `REQUEST_TRACE_UPLOADER_KRATOS_STATUS_TIMEOUT_SECONDS`: default `1800`

Object-store submission is synchronous and has no status poll.

The binary loads configuration strictly. An invalid or incompatible value,
or an unknown name that starts with `REQUEST_TRACE_UPLOADER_`, stops startup
with a non-zero exit. Errors name the setting, never its value.
`config.Load` on its own still falls back to defaults with a warning, for
callers that want that.

The uploader reads only the names listed here. It has no aliases, so a
deployment sets these names directly.

Missing paths and unreadable secret files prevent readiness.

## Record suppression

Suppression runs before redaction on every record of a closed segment. A
rule has an operator-assigned name, a header group, match values, and an
action. The first matching rule wins.

- `REQUEST_TRACE_UPLOADER_SUPPRESS_NCA_IDS`: comma-separated NCA ids. Each id
  is trimmed, and an `nca-<id>-nca` form is reduced to `<id>`. The list
  builds one `stripPayload` rule named `nca_id` on the `nvcf-ncaid` and
  `nvcf-nca-id` headers. Unset or blank turns it off.
- `REQUEST_TRACE_UPLOADER_SUPPRESSION_RULES`: further rules as a JSON list,
  evaluated after the NCA id rule, for example
  `[{"name":"tenants","headers":["x-tenant"],"values":["t1"],"action":"dropRecord"}]`.
  A name is a short lowercase token. The action is `stripPayload` or
  `dropRecord`.
- `REQUEST_TRACE_UPLOADER_SUPPRESS_HEADERLESS`: `export` (default) or `drop`
  for records of a type that carries no headers. In Dynamo v1.4.0 only
  `request_payload` carries headers. It applies only while a rule exists.

`stripPayload` replaces the record with the stub the earlier shell exporter
wrote, byte for byte. `dropRecord` removes the record.

An unparseable rule list, an unknown action or policy, a missing or repeated
name, or a rule with no value stops startup with a non-zero exit.

Counters: `records_suppressed` (`rule`, `action`), `records_evaluated`
(`record_type`), `suppression_header_present` (`rule`), and
`records_incomplete` (`record_type`) for payload records whose
`payload_complete` is false. Header names and values never become
attributes. When suppression is on and the first 100 payload records carry
no rule header, the uploader logs one warning. It never holds or drops
records for that.
