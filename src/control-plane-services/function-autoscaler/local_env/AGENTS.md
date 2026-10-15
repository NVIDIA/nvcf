# AGENTS.md - Function Autoscaler Local Environment

Run the autoscaler end-to-end on your laptop against the mock NVCF API,
VictoriaMetrics, Cassandra, and the smoke test. The parent
`function-autoscaler/AGENTS.md` has stale paths for this section; trust this
file for local-env workflow.

## Stack Layout

`docker-compose.yml` defines the runtime dependencies (start with
`docker compose --profile local up -d`):

- `cassandra` on `:9042` for the autoscaler's distributed locks and function
  table.
- `victoriametrics` on `:8428` for the Prometheus/Thanos-compatible TSDB the
  autoscaler queries.
- `mock-server` on `:8082` is a Python FastAPI mock that pretends to be both
  the NVCF API (for `PUT scale_function`) and the metrics source. It emits
  Prometheus series to VictoriaMetrics.
- `grafana` on `:3000` for dashboards (optional).
- `jaeger` on `:16686` + `:4317` for traces (optional, the autoscaler can
  push OTLP here).

The autoscaler itself runs locally via `cargo run` (not in the compose
stack). It binds three ports:

- `:8181` probe server (liveness, readiness, health)
- `:8083` main HTTP listener (default `:8080` is patched locally; see Port
  Conflict below)
- `:41337` Prometheus metrics exposition

## Run Command

```
TIMESERIES_DB__TIMESERIES_DB_URL=http://localhost:8428 \
TIMESERIES_DB__DISABLE_AUTH=true \
cargo run --bin server
```

Run from the autoscaler root (`function-autoscaler/`), not from `local_env/`.
The binary resolves `local_env/vault/secrets.json` relative to CWD.

Env-var overrides use `__` as the section separator (see
`crates/server/src/settings/mod.rs`). The two overrides above are required
because:

- The compiled-in default for `timeseries_db_url` is `http://localhost:10903`
  (a port nothing in the local stack listens on). Override to point at
  VictoriaMetrics.
- The compiled-in default for `disable_auth` is `false`, which sends the
  autoscaler into a TSDB credential-refresh loop against an empty
  `authn_url`. Override to skip auth entirely.

The README's bare `cargo run` does not work without these.

## Port Conflict on 8080

`crates/server/src/server.rs:328` hardcodes the main HTTP listener to
`8080` and ignores `ServerSettings.port`. On dev machines that run
`k3d-ncp-local-serverlb`, `:8080` is already taken and the autoscaler will
exit with `AddrInUse`.

Workaround used here: patched `server.rs:328` from `8080` to `8083` for
local runs. Do not commit. A proper fix is to read
`ServerSettings.ip_address` and `ServerSettings.port` from the config.

## Mock Server Function Categories

`mock_server/mock_server.py` initializes a fixed set of functions per
category and emits matching Prometheus series:

| Category                       | Worker series | CP series | Invocations |
|--------------------------------|---------------|-----------|-------------|
| `worker_with_invocations`      | yes           | no        | yes         |
| `worker_idle`                  | yes           | no        | no          |
| `cp_only_with_invocations`     | no            | yes       | yes         |
| `cp_only_idle`                 | no            | yes       | no          |
| `dual`                         | yes           | yes       | yes         |

Worker series: `nvcf_worker_service_worker_thread_count_total`,
`nvcf_worker_service_request_total`, `nvcf_worker_service_response_total`.

CP series: `function_request_latency_sum`, `nvcf_function_instances_current`,
`nvcf_function_concurrency` (labeled with `nca_id="mock-nca"`).

Discovery hook (`function_request{...}`) ticks for both worker-path and
CP-active categories so the autoscaler's discovery query picks them up
either way.

Two emission threads in `mock_server.py`:
`generate_worker_metrics_wrapper` and `generate_cp_metrics_wrapper`, each
sleeping 15s.

Function counts are `FUNCTION_COUNT` env var (default 2). All function IDs
are randomized per startup; query `/test/categories` for the live IDs.

## /test/categories

The mock exposes a test-only endpoint at `GET /test/categories` that
returns the current function IDs grouped by category, plus the `nca_id` it
uses on CP-plane series. Tests use this so they do not have to predict
randomized UUIDs.

```
{
  "nca_id": "mock-nca",
  "worker_with_invocations":      [{function_id, function_version_id}, ...],
  "worker_idle":                  [...],
  "cp_only_with_invocations":     [...],
  "cp_only_idle":                 [...],
  "dual":                         [...]
}
```

## Smoke Test

`tests/test_autoscaler_smoke.py` polls the autoscaler's `/metrics` endpoint
and checks that each function category that should have produced a scaling
decision actually did. Hard-fails on `worker_with_invocations` and `dual`;
soft-fails on `cp_only_*` until `ENFORCE_FALLBACK=1`.

```
python3 local_env/tests/test_autoscaler_smoke.py
```

Env vars:

- `MOCK_URL` (default `http://localhost:8082`)
- `AUTOSCALER_METRICS` (default `http://localhost:41337/metrics`)
- `WAIT_SECONDS` (default `120`)
- `ENFORCE_FALLBACK` (default `0`; flip to `1` once the CP fallback lands
  in the autoscaler to require those categories to pass)

Expected output before the CP fallback ships:

```
PASS (hard):
  - worker_with_invocations
  - dual

WARN (soft, ENFORCE_FALLBACK=0):
  - cp_only_with_invocations <fid>: no scaling decision emitted
```

After the CP fallback ships, re-run with `ENFORCE_FALLBACK=1` and the WARN
lines must convert to PASS.

## Discovery Cycle Timing

First scaling decisions take ~60s after boot because the autoscaler has to:

1. Acquire its node entry and bucket assignment (Cassandra writes).
2. Run a discovery cycle to populate `RecentlyInvokedFunctions`.
3. Run a scaling cycle that reads from that table.

If `/metrics` only shows process metrics (no `nvcf_autoscaler_scaling_*`)
after 90s, check the log for `No buckets assigned to this node` (transient,
should clear after the next discovery tick) or `Discovered 0 unique
recently invoked functions` (mock not emitting `function_request`).

## OTEL Trace Panic (Benign)

You will see:

```
thread 'OpenTelemetry.Traces.BatchProcessor' panicked at ...
there is no reactor running, must be called from the context of a Tokio 1.x runtime
```

This is a non-fatal Tokio/OTEL exporter race during dependency-wait
retries. The main service is unaffected. Ignore unless the process
actually exits.

## Tear Down

```
docker compose --profile local down -v
```

`-v` drops the Cassandra volume; the next `up` re-initializes the keyspace
from the autoscaler's startup migrations.
