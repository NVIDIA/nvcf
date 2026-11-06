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
5. Polling: one loop polls every open job on the status interval. A job is
   pending, succeeded, failed or not found. A job with a saved id is polled,
   never submitted again under that id. A job still pending, or whose status
   cannot be read, past the max job age is kept as stranded and polled less
   often; a job the backend has no record of is kept as stranded at once. A
   job that failed in a way that may still have landed (class `uncertain`,
   such as an upload the destination could not confirm) has its work sent
   again once as a new job, counted in `submits_ambiguous` as a possible
   duplicate; a second such failure is kept as stranded. A status the core
   does not know is counted in `unknown_status` and polled again.
6. Cleanup: after success, transports are deleted, the source is re-hashed
   if its size or modification time changed, and only then deleted. A
   backend that does not export, such as `debug` or a dry run, never deletes
   a source.
7. Shutdown: on SIGTERM the uploader lets work in progress finish, scans
   the spool one last time so a segment that closed after the last scan is
   not left behind, submits what is prepared, polls once more, and releases
   the spool, all within the drain timeout. What is left stays in
   the journal for the next start.
8. Spool bounds: on every scan pass, quarantine entries older than 7 days
   are deleted, then the oldest entries until quarantine is under 20% of the
   spool. If the spool is still above 85% full, the oldest closed segments
   that are in no batch are deleted. A segment in a batch that may have been
   sent, and the active segment, are never deleted. Preparing pauses while
   the spool is above 85%, while free space is under 512 MiB or twice the
   next segment's size, or while 16 prepared segments wait for a batch;
   prepared work is still sent. Every deletion is counted in
   `segments_evicted` or `quarantine_evicted`.

Faults follow `internal/retry`: auth failures and outages hold every submit
without moving a segment; a segment fault quarantines that segment after a
batch is split to find it. After `REQUEST_TRACE_UPLOADER_QUARANTINE_TRIP`
segments are quarantined in a row, a breaker stops intake and counts
`quarantine_tripped`. Segments already prepared are still submitted,
polled and cleaned up. After
`REQUEST_TRACE_UPLOADER_QUARANTINE_RESUME_SECONDS` intake reads new
segments again, counts `quarantine_resumed`, and starts a fresh run, so a
fault that is still there trips the breaker again. A success ends a run
but does not close a tripped breaker. A backend that records its own metrics implements
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
only bounded attributes; counters start at zero.

Rate, errors and duration of the export path:

- `uploads` (`status`): jobs that finished, `success` or `failure`.
- `upload.duration` (`status`), seconds: one backend submit call, `success`
  when it returned a job id and no error.
- `prepare_duration`, seconds: preparing one segment, from opening the
  format writer through reading, suppression, redaction and writing the
  transport.
- `confirm_latency`, seconds: from a job's submit to its confirmation.
- `export_latency`, seconds: from a segment's discovery to the confirmation
  of its export, once per segment.
- `records_received` (`record_type`): every line read from a segment, by
  Dynamo v1.4.0 event type, or `unknown` for an unparseable line, an older
  release's record, or an event type this build does not know.

`prepare_duration` buckets run from 5 ms to 1 hour; the other durations run
from 50 ms to 24 hours. Metrics are pushed every 60
seconds and once more after the shutdown drain, for up to 5 seconds, so the
pod's grace period must cover the drain timeout plus 5 seconds. A failed
export never blocks an upload or changes readiness.

The `backend` package defines the export destination contract and a registry.
Submit and status are separate operations so a slow confirmation cannot block
other segments. Backends register themselves from an init function, so a build
links only the backends it imports. This binary links `debug` and
`objectstore`. A distribution that needs another backend imports it in its own
`main`, starts through `runner`, and reuses the `config`, `backend`, `format`,
`telemetry`, `telemetry/otlp`, and `service` packages from this module.

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
  keep reporting pending before it is reported stranded; default `21600`
  (6 hours). Planned, not built yet: the same limit for every unresolved
  job that has an id.
- `REQUEST_TRACE_UPLOADER_QUARANTINE_TRIP`: how many segments quarantined
  in a row stop intake; default `5`, from `1` to `1000`
- `REQUEST_TRACE_UPLOADER_QUARANTINE_RESUME_SECONDS`: how long intake stays
  stopped after a trip before it reads new segments again; default `3600`,
  from `60` to `86400`
- `REQUEST_TRACE_UPLOADER_STATUS_INTERVAL_SECONDS`: how often every open job
  is polled, whatever the backend; default `30`

Used only by one backend:

- `REQUEST_TRACE_UPLOADER_OBJECTSTORE_BUCKET` and
  `REQUEST_TRACE_UPLOADER_OBJECTSTORE_REGION`: required by `objectstore`
- `REQUEST_TRACE_UPLOADER_OBJECTSTORE_ENDPOINT`: an `https://` URL for an
  S3-compatible store other than AWS; default the SDK endpoint for the region
- `REQUEST_TRACE_UPLOADER_OBJECTSTORE_KEY_PREFIX`: prefix for object keys;
  default the bucket root
- `REQUEST_TRACE_UPLOADER_OBJECTSTORE_PATH_STYLE`: path-style bucket
  addressing, which most stores other than AWS need; default `false`
- `REQUEST_TRACE_UPLOADER_OBJECTSTORE_DRY_RUN`: log what would be uploaded,
  send nothing and need no credentials; default `false`
- `REQUEST_TRACE_UPLOADER_DEBUG_VERBOSITY`: `basic` (default) or `detailed`
  logging for `debug`

Object-store submission is synchronous, so its first status poll reports the
final outcome.

The binary loads configuration strictly. An invalid or incompatible value,
or an unknown name that starts with `REQUEST_TRACE_UPLOADER_`, stops startup
with a non-zero exit. Errors name the setting, never its value.
`config.Load` on its own still falls back to defaults with a warning, for
callers that want that.

The uploader reads only the names listed in this README. It has no aliases,
so a deployment sets these names directly.

Missing paths and unreadable secret files prevent readiness.

## Record suppression

Suppression runs before redaction on every record of a closed segment. A
rule has an operator-assigned name, a header group, match values, and an
action. The first matching rule wins.

- `REQUEST_TRACE_UPLOADER_SUPPRESS_NCA_IDS`: comma-separated NCA ids. Each id
  is trimmed, and an `nca-<id>-nca` form is reduced to `<id>`. The list
  builds one `stripPayload` rule named `nca_id` on the headers in
  `REQUEST_TRACE_UPLOADER_SUPPRESS_NCA_HEADERS`. Unset or blank turns it off.
- `REQUEST_TRACE_UPLOADER_SUPPRESS_NCA_HEADERS`: comma-separated header names
  that carry the NCA id, read in the order given; the first one with a value
  decides. Names are trimmed and lower-cased. The uploader has no built-in
  list, so this is required while `REQUEST_TRACE_UPLOADER_SUPPRESS_NCA_IDS`
  has an id; the deployment supplies the default, for example
  `nvcf-ncaid,nvcf-nca-id`. A blank or repeated name stops startup.
- `REQUEST_TRACE_UPLOADER_SUPPRESSION_RULES`: further rules as a JSON list,
  evaluated after the NCA id rule, for example
  `[{"name":"tenants","headers":["x-tenant"],"values":["t1"],"action":"dropRecord"}]`.
  A name is a short lowercase token. The action is `stripPayload` or
  `dropRecord`.
- `REQUEST_TRACE_UPLOADER_SUPPRESS_HEADERLESS`: `export` (default) or `drop`
  for records of a type that carries no headers. In Dynamo v1.4.0 only
  `request_payload` carries headers. It applies only while a rule exists.

`stripPayload` replaces the record with a stub that has the same structure
and values as the one the earlier shell exporter wrote; its JSON formatting
can differ. `dropRecord` removes the record.

An unparseable rule list, an unknown action or policy, a missing or repeated
name, a rule with no value, or NCA ids without NCA id headers stops startup
with a non-zero exit.

Counters: `records_suppressed` (`rule`, `action`), `records_evaluated`
(`record_type`), `suppression_header_present` (`rule`), and
`records_incomplete` (`record_type`) for payload records whose
`payload_complete` is false. Header names and values never become
attributes. When suppression is on and the first 100 payload records carry
no rule header, the uploader logs one warning. It never holds or drops
records for that.
