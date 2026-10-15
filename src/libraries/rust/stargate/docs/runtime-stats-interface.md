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
```

The stream reports request counters. `/kv-cache/stats` is optional machine
state.

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
- volatile generation throughput: `output_tps` and `max_output_tps`
- request phase counts and queue sizes
- optional KV capacity/used/free tokens
- source and capability labels

Every publication is derived from one per-model aggregate, so throughput,
request lifecycle load, KV state, and labels cannot diverge between publication
paths. Once a valid cumulative-counter output sample exists, it is
authoritative over live request-timing estimates until stale cleanup clears the
counter-derived output window.

If request stats go stale, volatile output TPS is cleared. Sticky input TPS
stays until a later valid sample replaces it.

Shared clusters sum backend-local live load and union labels. Effective input
capacity is:

```text
sum(active_runtime_reports)
```

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
