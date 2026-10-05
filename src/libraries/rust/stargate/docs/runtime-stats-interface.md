# Runtime Stats Interface

Source: pylon stats collector, request observer, and mock backend contracts.

Pylon gets request-throughput stats from the inference runtime through:

```text
GET /pylon/v1/stats/stream
Accept: application/x-ndjson
```

Do not put private stats in OpenAI response bodies. Pylon uses the runtime stats
stream when available, and OpenAI chunk metadata only as fallback.

## Flow

```text
client -> stargate -> QUIC tunnel -> pylon -> runtime
runtime -> /pylon/v1/stats/stream -> pylon -> registration -> stargate routing
runtime -> /kv-cache/stats ---------> pylon -> registration -> stargate routing
CLI concurrency fallback ----------> pylon -> registration -> stargate routing
```

The stream reports request counters. `/kv-cache/stats` is optional machine
state.

## Registration Updates

Pylon publishes its registration, including model stats, to every Stargate in
two cases:

- When advertised stats or status change, for example when a request is
  admitted, produces its first output, or completes. Updates are coalesced so
  each registration stream sends at most one change-driven update per
  `--stats-update-coalesce-ms` (default `10`).
- As a heartbeat when nothing has been sent for `--min-update-interval-ms`
  (default `1000`). Stargate uses this interval for registration liveness.

Stargate routes on the most recent update plus its own pending reservations.
Change-driven updates keep that view within the coalescing window and network
delay of the backend's actual load, instead of up to one heartbeat behind.

## Stream Events

Each non-empty line is JSON with `v: 1` and `type`.

Stats event:

```json
{
  "v": 1,
  "type": "stats",
  "request_id": "req-123",
  "model": "llama",
  "tokens_processed": 128,
  "tokens_generated": 17,
  "finished": false
}
```

Rules:

- `request_id` and `model` are required.
- Token counters are cumulative unsigned integers.
- At least one counter is required unless `finished` is true.
- Pylon computes deltas and ignores duplicate or regressing counters.
- `finished: true` closes the request; later events for it are ignored.
- Malformed events are counted and dropped. They do not close the stream.

Ping:

```json
{"v":1,"type":"ping"}
```

An engine can report its maximum concurrent requests for a model in a ping:

```json
{"v":1,"type":"ping","model":"llama","max_engine_concurrency":25}
```

`model` is required when `max_engine_concurrency` is present. A positive limit
overrides the configured fallback for that model. Zero withdraws the engine
limit and restores the fallback. Omitting the field leaves the current limit
unchanged. Stream disconnects also retain the last reported limit.

## Concurrency Fallback

For an engine without a stats endpoint, set Pylon's fallback concurrency:

```text
pylon --upstream-http-base-url=http://127.0.0.1:8090 --model-name=llama --max-engine-concurrency=25
```

`--max-engine-concurrency N` accepts a positive integer and has no default.
Pylon uses it for each model until that model reports an engine limit, including
models discovered after startup. It is available in every stats source mode.
Pylon publishes the effective limit from the first registration and uses the
same value for local queue admission. If neither source supplies a limit, Pylon
publishes zero, meaning unknown.

Set this value to the engine's actual request capacity. It informs queue
estimates and routing capacity checks; it does not configure the engine's
scheduler or create a Pylon request semaphore. `--calibration-max-concurrency`
only controls calibration traffic and does not supply this fallback.

## Source Modes

```text
--engine-stats-stream=auto|required|off
--engine-stats-stream-path=/pylon/v1/stats/stream
```

- `auto`: use the stream. If it returns `404`, `405`, or `501` before a valid event, use OpenAI fallback.
- `required`: keep retrying the stream and never fall back.
- `off`: skip the stream and use OpenAI fallback.

Transient errors, malformed events, and EOF do not switch `auto` to fallback.

Fallback reads streamed OpenAI usage fields such as `usage.completion_tokens` or
`output_tokens_so_far`. Text peeking is last resort.

## Optional KV Stats

`/kv-cache/stats` may report runtime machine state:

```json
{
  "model": "model-a",
  "kv_cache_capacity_tokens": 1000,
  "kv_cache_used_tokens": 400,
  "kv_cache_free_tokens": 600
}
```

Use it only when the runtime has reliable KV state.

## Aggregation

Pylon publishes:

- sticky completed-request input throughput: `last_mean_input_tps`
- generation maximum input throughput: optional `max_input_tps`
- volatile generation throughput: `output_tps` and `max_output_tps`
- request phase counts and queue sizes
- effective maximum engine concurrency
- optional KV capacity/used/free tokens
- source and capability labels

Every publication is derived from one per-model aggregate, so throughput,
request lifecycle load, KV state, and labels cannot diverge between publication
paths. Once a valid cumulative-counter output sample exists, it is
authoritative over live request-timing estimates until stale cleanup clears the
counter-derived output window.

If request stats go stale, volatile output TPS is cleared. Sticky input TPS
stays until a later valid sample replaces it.
The maximum is the greatest `last_mean_input_tps` published for the current
model generation, including a configured initial TPS. It follows the smoothed
mean rather than individual samples, so the mean dilutes one outlier sample.
Lower means do not reduce it, and generation replacement clears it. It is
absent until the first mean is published.

Pylon publishes `pylon_model_max_input_tps` when the maximum is known and removes
the series when the model is removed or replaced with unknown maximum state.
The additive protobuf field preserves older messages as an absent maximum.

Shared clusters sum backend-local live load and union labels. Effective input
capacity is:

```text
mean input capacity = sum(active runtime mean reports)
maximum input capacity = max(active generation peak reports)
```

Maximum capacity is available only when every active backend in the shared
engine cluster reports a valid maximum. Historical per-observer peaks are not
additive. Pulsar uses maximum capacity by default; queue estimates still use
mean capacity.

Before registration, Pylon initializes each model generation from exactly one
source. `--initial-input-tps` installs the configured value. Local calibration
installs nothing: it runs an increasing request ramp until a load-step timeout,
and those requests' exact-generation observer events build the same distribution
used at runtime. Duplicate engine events for calibration IDs are ignored. Pylon
logs the current stats at timeout without reinjecting them.
Later valid runtime samples continue updating either unpinned distribution.

Labels:

- capabilities: `request.output.chunk_usage`,
  `machine.kv_cache.http`, `model.throughput.engine_stream`
- sources: `chunk_usage`, `kv_cache_stats`, `engine_stats_stream`

## Metrics

Runtime-stats metrics use the `pylon_engine_stats_*` prefix for stream events,
invalid events, reconnects, connection state, live requests, model states,
stale cleanup, dirty snapshots, and source transitions.

`pylon_engine_stats_model_states` counts models admitted into aggregate
counter or stream-observation state. Lifecycle-only and KV-only model state
does not inflate that gauge.

## Mock Defaults

`mock-dynamo` serves the stats stream, OpenAI-compatible
chat/Responses/embeddings endpoints, and `/kv-cache/stats`. Use
`pylon --engine-stats-stream=off` when a test intentionally exercises OpenAI
fallback.

With `--profile h100-llama-3.1-8b`, `mock-dynamo` uses the batched engine model
from the `mock-engine` crate by default. The model represents one Dynamo
deployment:

- `--num-gpu-workers N` sets the number of inference workers. Each worker has
  its own scheduler and KV cache.
- Each worker runs iteration-level steps. A step decodes one token for every
  running sequence and spends the rest of `--max-batched-tokens` on chunked
  prefill, so concurrent prompts share prefill compute and slow decode.
- Requests go to the worker caching the most tokens for their
  `x-cache-affinity-key`, otherwise to the least-loaded worker.
- Stats stream pings advertise `max_engine_concurrency` as
  `num_gpu_workers * max_num_seqs`. When Pylon does not read the stats stream,
  set `--max-engine-concurrency` to the same value.

`--explain-profile h100-llama-3.1-8b` prints the step costs. They are estimates
until calibrated against a real engine. `--engine-model legacy` restores the
earlier model, where each request has fixed, independent prefill and decode
delays.
