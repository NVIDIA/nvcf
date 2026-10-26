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
Dynamo KV DC Relay (gRPC) ---------> pylon -> registration -> stargate routing
```

The stream reports request counters. `/kv-cache/stats` is optional machine
state. With a Dynamo upstream, the Relay can replace both; see
[Dynamo Relay Mode](#dynamo-relay-mode).

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

Each dispatched request adds a local reservation for the selected backend
when the backend is selected. The reservation's TTL is the selected backend's
health-check RTT (not the cluster mean), bounded by
`--routing-reservation-ttl-min-ms` (default `1`) and
`--routing-reservation-ttl-max-ms` (default `1000`). The maximum only limits
the effect of a bad RTT sample; it does not end the reservation. The soft
expiry is one TTL after Stargate opens the tunnel stream and sends the request
head. Until then, for example while the stream open waits for tunnel capacity,
no update can end the reservation. An attempt that ends before sending starts
the soft expiry when it ends.

The reservation counts until Stargate receives a registration update from the
selected backend at or after the soft expiry. Receipt time is the settlement
threshold; it does not show when the update was generated or whether it includes
the request. The request tunnel and registration stream are independent, so an
update generated before the backend receives a request can arrive after the
threshold, while an update that includes the request can arrive before it. This
can briefly leave the request counted in both reported stats and the
reservation, or settle the reservation before the request appears in reported
stats. Updates from other backends in the same cluster never settle it. A queue
estimate mismatch rejection releases the reservation early, and removing or
deregistering the backend drops it.

Waiting for the backend's own update covers two cases:

- A backend that hangs but keeps its connection stops sending updates. Its last
  reported stats still look idle. With a time-based expiry or cap, its
  reservations would all end together and those idle stats would attract a new
  burst of requests each time. Held reservations keep steering traffic away
  until the backend reports again or deregisters.
- Pylon coalesces change-driven updates (`--stats-update-coalesce-ms`, default
  `10`). The reservation uses the selected backend's soft-expiry threshold to
  decide when its own updates can settle it. Because the streams are
  independent, receipt time is a heuristic and does not prove that an update
  includes the request. A larger minimum can lengthen the period in which a
  request is counted in both reported stats and its reservation.

The trade-off is that a reservation on a backend that stops reporting stays
counted, in routing and in `stargate_routing_reservations_active`, until that
backend reports or its registration ends. Only registration liveness bounds
those reservations, so setting `--registration-update-idle-timeout-ms` to `0`
leaves them unbounded for a backend that keeps serving requests without sending
updates.

Pylons that send only heartbeats keep each reservation until the first
heartbeat received at or after its soft expiry. Receipt time does not prove
that the heartbeat includes the request. The reservation cannot expire while
waiting for that heartbeat, although the reported stats may still lag the
request.
Deploying Pylons with
change-driven stats updates before this Stargate version is recommended for the
most current view but is not required.

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

Fallback input throughput uses each request's interval from submission to first
output. The newest 8 requests define the busy time, which is the union of their
intervals. The rate is the input tokens of every request whose first output
falls in that busy time, divided by its length. This includes older requests,
so the rate stays correct when more requests are in prefill at once than the
window holds. The window size sets smoothing, not a concurrency limit. A request
whose interval is longer than the busy time counts only the tokens its own rate
would process in that time. This stops one long prefill from inflating a short
window.

Fallback input throughput counts only uncached prompt tokens when usage reports
`cached_tokens` (Chat `prompt_tokens_details` or Responses
`input_tokens_details`), and total prompt tokens otherwise. Chat Completions
usage is available only when the client sets `stream_options.include_usage=true`
or the deployment sets `--force-chat-completions-include-usage`. vLLM reports
Chat cached tokens only when started with `--enable-prompt-tokens-details`;
vLLM Responses usage and Dynamo vLLM workers report them by default. Engines
that report cached tokens may omit Chat details on a cache miss, so once a model
generation's streamed Chat usage reports cached-token details, later Chat usage
without them counts as a miss. A request that expects usage holds newer
requests out of the rate until its usage arrives, for at most 120 seconds after
newer requests reach first output. Cache hits that leave fewer than 256 uncached
prompt tokens are left out, because their time to first output is mostly fixed
overhead.

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

## Dynamo Relay Mode

```text
--dynamo-relay-grpc-url=http://relay:50051
```

With the Dynamo upstream backend, Pylon can take model stats from the Dynamo
KV DC Relay (`dynamo.kvrelay.v1.KvEventRelay`) instead of the runtime:
`SubscribeServingLoad` for model load and readiness, `SubscribeKvPoolLoad` for
KV usage, and `WatchKvPoolCatalog` to join both to model names. Pylon then
starts neither the stats stream nor KV polling, and the flag is a startup error
next to `--kv-cache-stats-path`, `--engine-stats-stream`, or
`--engine-stats-stream-path`. Every Dynamo frontend and router behind the Relay
must set `DYN_SERVING_LOAD_PUBLISH`: when only some publish, the Relay reports
a partial sum as complete.

- Readiness: a model is ready while the Relay reports a ready frontend and a
  serving pool for it, whatever the window's coverage. Pylon admits no requests
  for a model before its first window or after the Relay is lost.
- Load: the queue, running, and input-size gauges, the engine concurrency
  limit, and the throughput come from a window in which the model's load is
  complete. An incomplete window clears them to zero, and local request
  observations never replace them. Rates use the Relay clock within one
  `relay_incarnation`, so the first window of an incarnation has no rate.
- KV usage: capacity and used tokens count only pools whose ranks all
  reported. A model with incomplete usage reports zero capacity, which Stargate
  reads as no KV view: a cluster takes KV usage from its freshest backend that
  reports capacity.
- Shared clusters: every Pylon reading one Relay reports the Relay's whole
  load, so Stargate takes a cluster's load from its freshest complete Relay view
  (source `dynamo_relay_load`) instead of summing its backends.
- Loss: when any Relay stream ends or sends an invalid message, or a load
  stream is silent for max(3 s, 3 × the window), Pylon clears the Relay's stats
  and readiness and reconnects with backoff. It never falls back to OpenAI
  stats.
- Capacity: the Relay reports load, not capacity, and relay mode turns off the
  OpenAI fallback, so a model's mean input capacity comes only from
  `--do-calibration` or `--initial-input-tps`. Calibration still measures
  Pylon's own calibration requests.

A ready model whose load is incomplete reports zero load until a complete
window arrives, because registration stats cannot say "unknown".

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
The maximum is the greatest max-eligible `last_mean_input_tps` published for the
current model generation. A configured `--initial-input-tps`, engine stats
stream counters, embeddings samples, and calibration can raise it. An OpenAI
fallback mean raises it only when every request it counts reported uncached
prompt tokens under the Fallback rules above. Other fallback means update
`last_mean_input_tps` but not the maximum, because cached input may be counted
as prefill. The maximum follows the smoothed mean rather than individual
samples, so the mean dilutes one outlier sample. Lower means do not reduce it,
and generation replacement clears it. It is absent until the first max-eligible
mean. A backend whose fallback usage never reports cached-token details, such
as Chat traffic without `include_usage`, or whose traffic is only near-full
cache hits, never gets a maximum this way. Use `--do-calibration` or `--initial-input-tps` for such
backends, because default Pulsar ranking requires a maximum (see below).

Pylon publishes `pylon_model_max_input_tps` when the maximum is known and removes
the series when the model is removed or replaced with unknown maximum state.
The additive protobuf field preserves older messages as an absent maximum.

Shared clusters sum backend-local live load and union labels. A complete Dynamo
Relay load view is shared engine state instead: the cluster takes the freshest
one (see [Dynamo Relay Mode](#dynamo-relay-mode)). Effective input capacity is:

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
used at runtime. Each calibration request starts with a unique prompt prefix, so
it cannot hit the prefix cache. Duplicate engine events for calibration IDs are
ignored. Pylon logs the current stats at timeout without reinjecting them.
Later valid runtime samples continue updating either unpinned distribution.

Labels:

- capabilities: `request.output.chunk_usage`,
  `machine.kv_cache.http`, `model.throughput.engine_stream`,
  `model.load.dynamo_relay`, `machine.kv_cache.dynamo_relay`
- sources: `chunk_usage`, `kv_cache_stats`, `engine_stats_stream`,
  `dynamo_relay_load`, `dynamo_relay_kv_usage`

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

It also serves a mock KV DC Relay over gRPC on the same port, for
`pylon --dynamo-relay-grpc-url=http://<mock-dynamo address>`: one aggregated
pool serving `--model-name`, with load from mock-dynamo's own requests and
windows every second. `PUT /test-control/relay` with `{"enabled": false}` ends
its streams and refuses new ones, as a lost Relay would.

With `--profile h100-llama-3.1-8b`, `mock-dynamo` uses the batched engine model
from the `mock-engine` crate by default. The model represents one Dynamo
deployment:

- `--num-gpu-workers N` sets the number of inference workers. Each worker has
  its own scheduler and KV cache.
- Each worker runs iteration-level steps. A step decodes one token for every
  running sequence and spends the rest of `--max-batched-tokens` on chunked
  prefill, so concurrent prompts share prefill compute and slow decode.
- Requests go to the worker caching the most tokens for their
  `x-cache-affinity-key`, even when that worker is busier than the others.
  Requests without a cached key go to the least-loaded worker. A key is
  cached only after its first prefill finishes, so concurrent first requests
  for one key can land on different workers.
- A completed request's cache entry covers its prompt and its output. A later
  request with the same key reuses up to that many tokens. Matching is per
  key, not per token block.
- `/kv-cache/stats` reports deployment totals for capacity and used tokens.
  Used tokens include cached prefixes and the memory reserved by running
  requests. A request larger than a worker's capacity still runs alone, so
  used can exceed capacity. Entry, hit, miss, and eviction counters report
  zero for this model.
- Stats stream pings advertise `max_engine_concurrency` as
  `num_gpu_workers * max_num_seqs`. When Pylon does not read the stats stream,
  set `--max-engine-concurrency` to the same value.

`--explain-profile h100-llama-3.1-8b` prints the step costs. They are estimates
until calibrated against a real engine. `--engine-model legacy` restores the
earlier model, where each request has fixed, independent prefill and decode
delays.
