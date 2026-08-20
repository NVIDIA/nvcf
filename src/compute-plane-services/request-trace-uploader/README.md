# request-trace-uploader

`request-trace-uploader` is the NVCF sidecar for Dynamo request tracing.
Dynamo calls the captured objects `RequestTraceRecord` values. The records that
contain input and output payloads have event type `request_payload`.

The current deployment writes request-trace segments to rotating `.jsonl.gz`
files. It uses two capture types, `trace` and `audit`. This service discovers
only closed segments: the highest indexed segment for each prefix remains owned
by the Dynamo writer.

## Initial scaffold

This initial implementation validates the sidecar configuration, verifies its
secret-file mount, creates state and quarantine directories, exposes health and
Prometheus metrics, and discovers the current backlog. It intentionally does
not transform records, submit uploads, poll remote status, delete source files,
or publish a release image.

The `internal/upload` package defines the future upload-client boundary. The
real adapter and durable journal are separate follow-up work.

## Configuration

The scaffold retains the current file contract:

- `TRACE_DIR`: absolute directory containing trace and audit segments
- `TRACE_FILE_PREFIX`: trace segment prefix
- `AUDIT_FILE_PREFIX`: audit segment prefix
- `REQUEST_TRACE_UPLOADER_DROP_NCA_IDS`: optional CSV NCA ID drop list for audit
  payloads. Bare IDs and `nca-<id>-nca` are equivalent. The future
  transform retains correlation metadata and the normalized NCA ID, but removes
  request and response payloads plus non-NCA headers before upload.
- `KRATOS_SECRETS_FILE`: readable mounted secret file; default
  `/var/secrets/secrets.json`

It also accepts these bounded operational settings:

- `METRICS_ADDR`: default `:8011`
- `UPLOAD_INTERVAL_SECONDS`: default `30`
- `STATUS_INTERVAL_SECONDS`: default `5`
- `STATUS_TIMEOUT_SECONDS`: default `900`
- `REQUEST_TRACE_UPLOADER_ATTEMPT_TIMEOUT`: default `30s`
- `REQUEST_TRACE_UPLOADER_OPERATION_TIMEOUT`: default `90s`
- `REQUEST_TRACE_UPLOADER_MAX_RETRIES`: default `2`
- `REQUEST_TRACE_UPLOADER_RETRY_INITIAL_BACKOFF`: default `100ms`
- `REQUEST_TRACE_UPLOADER_RETRY_MAX_BACKOFF`: default `15s`
- `REQUEST_TRACE_UPLOADER_RETRY_MULTIPLIER`: default `2.0`

Invalid policy values fall back to defaults and produce a safe startup warning.
Missing paths, unreadable secret files, and incompatible required values prevent
readiness.
