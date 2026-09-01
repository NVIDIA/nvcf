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

## Initial scaffold

This initial implementation validates the sidecar configuration, verifies its
secret-file mount, creates state and quarantine directories, exposes health,
and verifies local segment discovery. It intentionally does not export logs,
traces, or metrics, parse or transform records, submit uploads, poll remote
status, delete source files, or publish a release image.

The `internal/upload` package defines the future upload-client boundary.
Submit and status are separate operations so a slow confirmation cannot block
other segments. The real adapters and durable journal are separate follow-up
work.

## Configuration

Required:

- `REQUEST_TRACE_UPLOADER_SOURCE_DIR`: absolute directory containing segments
- `REQUEST_TRACE_UPLOADER_BACKEND`: `objectstore` or `kratos`

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

Kratos Bulk Upload is an asynchronous job API, so it polls for terminal status.
These settings apply only to that backend:

- `REQUEST_TRACE_UPLOADER_KRATOS_STATUS_INTERVAL_SECONDS`: default `5`
- `REQUEST_TRACE_UPLOADER_KRATOS_STATUS_TIMEOUT_SECONDS`: default `1800`

Object-store submission is synchronous and has no status poll.

Invalid policy values fall back to defaults and produce a safe startup warning.
Missing paths, unreadable secret files, an unknown backend, and incompatible
required values prevent readiness.

Record suppression is configured as rules with a header group, match values,
and an action. It is not part of this scaffold.
