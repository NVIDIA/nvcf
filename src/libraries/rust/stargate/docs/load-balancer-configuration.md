# Load Balancer Configuration

Stargate selects one load-balancing algorithm for each model. A request can
select another preconfigured algorithm through a trusted header.

This page defines the `lb-config.json` schema and the behavior of
`wait-and-widen`, `pulsar`, and `pulsar-wait-and-widen`. Deployment systems own
the file mount and the `--lb-config-path` argument.

## Load the configuration

Start Stargate with an optional JSON file:

```text
--lb-config-path=/config/lb-config.json
```

When the argument is absent, Stargate uses `power-of-two` for every model.
When the argument is present, Stargate reads and validates the file during
startup. A missing file, invalid JSON, unknown top-level field, unsupported
algorithm field, or invalid algorithm factory configuration prevents startup.
Stargate does not reload the file after startup.

## Schema

The top-level object has three fields:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `default` | algorithm name | `power-of-two` | Algorithm for models without an entry in `models`. |
| `request_algorithms` | object | `{}` | Algorithms that `x-routing-method` may select for every model. |
| `models` | object | `{}` | Exact model ID to algorithm configuration. |

Valid algorithm names are `power-of-two`, `wait-and-widen`, `round-robin`,
`random`, `pulsar`, and `pulsar-wait-and-widen`.

For backward compatibility, Stargate also accepts `groq-multiregion` as an
alias for `wait-and-widen` and `pulsar-multiregion` as an alias for
`pulsar-wait-and-widen` in `default`, `models`, `request_algorithms`, detailed
algorithm objects, and routing-method overrides. Use the canonical names for
new configurations.

An entry in `models` or `request_algorithms` can be an algorithm name:

```json
{
  "default": "power-of-two",
  "models": {
    "model-a": "wait-and-widen"
  }
}
```

Use a detailed object to set algorithm fields:

```json
{
  "default": "power-of-two",
  "models": {
    "model-a": {
      "algorithm": "wait-and-widen",
      "require_cache_affinity_key": true
    }
  }
}
```

Detailed objects support these common fields:

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `algorithm` | algorithm name | required | Algorithm created for this object. |
| `require_cache_affinity_key` | boolean | `false` | Reject the request with HTTP `400` when `x-cache-affinity-key` is absent or blank. |
| `require_input_tokens` | boolean | `false` | Declares that the algorithm needs input tokens. The HTTP proxy already requires `x-input-tokens` for every inference request. |
| `max_input_work_seconds` | number | unset | Reject with HTTP `503` when `(request input tokens + queued input tokens) / aggregate last mean input TPS` across algorithm-eligible candidates exceeds this value or valid capacity is unavailable. |

Stargate does not range-check `max_input_work_seconds`. Use a positive value
for a useful upper bound. A nonpositive value can reject every request with
input or queued work.

Pulsar admission excludes candidates already attempted, candidates without
valid input TPS, and candidates that fail enabled KV feasibility checks. The
KV checks require request input tokens and candidate KV capacity statistics,
and reject candidates without enough free KV tokens. Other algorithms exclude
only candidates already attempted. For those algorithms, a candidate with
invalid input TPS contributes queued work but no service rate.

Detailed objects directly under `models` also support `request_algorithms`.
These model-specific overrides replace the same algorithm from the top-level
map and inherit other top-level entries. Do not nest `request_algorithms`
inside an override entry. Stargate parses but discards that nested map when it
constructs the router.

Each `request_algorithms` key must match the algorithm in its value. For
example, the key `pulsar` can map to `"pulsar"` or to a detailed object whose
`algorithm` is `pulsar`.

Unknown top-level and detailed fields are rejected. Algorithm-specific fields
are rejected when used with another algorithm.

## Selection guidance

Choose based on the routing goal and available backend statistics:

| Goal | Algorithm | Required backend signals |
| --- | --- | --- |
| Minimize estimated time to first token across heterogeneous or remote clusters. | `wait-and-widen` | Forwarded health RTT and model statistics. Valid `last_mean_input_tps` is needed when queued or request input work is nonzero. |
| Keep the same prefix on a stable, capacity-weighted cluster. | `pulsar` | Positive finite `last_mean_input_tps` for every participating cluster. |
| Keep Pulsar affinity when possible, but escape to lower-latency capacity when the primary cannot meet queue policy. | `pulsar-wait-and-widen` | Pulsar capacity plus the RTT and queue statistics used by `wait-and-widen`. |

Use `power-of-two` when these statistics or affinity requirements are not
available. Use `round-robin` for deterministic cycling and `random` for uniform
random selection.

## `wait-and-widen`

`wait-and-widen` estimates time to first token (TTFT) as:

```text
forwarded health RTT + queue delay + request prefill time
```

The queue delay uses the backend's priority-aware queue estimate when present.
Otherwise it divides queued input tokens by `last_mean_input_tps`. Prefill time
divides `x-input-tokens` by the same capacity signal.

The algorithm groups close TTFT estimates into buckets. It samples `n`
candidates from unlocked buckets and chooses the candidate with the least
queue time, then the lowest engine utilization. A later bucket becomes
available after the request has waited for a fraction of the TTFT gap.

When `cache_affinity_backend_selection_count` is enabled and the request has
`x-cache-affinity-key`, a consistent hash ring first limits selection to a
stable subset. Normal TTFT selection runs within that subset. The seed, routing
key, model ID, affinity key, cluster ID, and virtual-node index contribute to
the hash. If the subset has no usable candidate, selection falls back to the
complete candidate set.

Minimal configuration:

```json
{
  "default": "power-of-two",
  "models": {
    "model-a": {
      "algorithm": "wait-and-widen",
      "require_cache_affinity_key": true,
      "cache_affinity_backend_selection_count": 2
    }
  }
}
```

## `pulsar`

`pulsar` creates a stable weighted rendezvous ranking from the seed, routing
key, model ID, cache affinity key, and cluster ID. The weight is the cluster's
positive finite `last_mean_input_tps`. Transient queue load does not change the
base ranking.

Retries walk the same ranking after excluding failed clusters. When
`consider_kv_free_tokens` is enabled, a ranked candidate is skipped when it
does not report KV-cache metrics or has fewer free tokens than
`x-input-tokens`.

Minimal configuration:

```json
{
  "default": "power-of-two",
  "models": {
    "model-a": {
      "algorithm": "pulsar",
      "seed": "model-a-v1",
      "require_cache_affinity_key": true
    }
  }
}
```

## `pulsar-wait-and-widen`

`pulsar-wait-and-widen` combines Pulsar ranking with the WaitAndWiden fallback.
Without queue-SLO fields, an eligible Pulsar primary wins immediately.

When queue-SLO fields are enabled or the primary is ineligible, the algorithm
checks the primary and then exponentially wider ranking bands of 2, 4, 8, and
so on. Within each band, `wait-and-widen` selects an eligible candidate.

Minimal configuration:

```json
{
  "default": "power-of-two",
  "models": {
    "model-a": {
      "algorithm": "pulsar-wait-and-widen",
      "seed": "model-a-v1",
      "require_cache_affinity_key": true,
      "max_queue_time_floor_ms": 500,
      "max_queue_time_ceil_ms": 2000
    }
  }
}
```

## Algorithm fields

`wait-and-widen` supports these cache-affinity fields:

| Field | Type | Default | Constraint and effect |
| --- | --- | --- | --- |
| `cache_affinity_virtual_nodes` | unsigned integer | `150` | Virtual nodes per cluster. `0` is normalized to `1`. |
| `cache_affinity_backend_selection_count` | unsigned integer | unset | Enables the affinity subset. `0` disables it. Values above the candidate count select all candidates. |

These fields are accepted in `pulsar-wait-and-widen` JSON but do not affect its
selection. Pulsar ranking supplies that algorithm's affinity.

`wait-and-widen` and `pulsar-wait-and-widen` support these wait-and-widen fields:

| Field | Type | Default | Constraint and effect |
| --- | --- | --- | --- |
| `seed` | string | empty | Changes WaitAndWiden affinity hashing or Pulsar wait-and-widen ranking. Keep it stable across replicas that should make the same choice. |
| `max_queue_time_floor_ms` | unsigned integer | unset | Queue-SLO lower bound. Has an effect only when `max_queue_time_ceil_ms` is also set. |
| `max_queue_time_ceil_ms` | unsigned integer | unset | Queue-SLO upper bound. Has an effect only when `max_queue_time_floor_ms` is also set. |
| `ttft_bucket_size_ms` | unsigned integer | `20` | Maximum TTFT difference within one bucket. |
| `next_bucket_unlock_factor` | number | `0.25` | Fraction of the TTFT gap to wait before the next bucket unlocks. |
| `n` | unsigned integer | `2` | Number of unlocked candidates sampled. `0` is normalized to `1`. |
| `max_queued` | unsigned integer | `0` | Additional queued requests allowed above `max_engine_concurrency`. A reported concurrency of `0` disables this capacity check. |
| `ignore_queue_time` | boolean | `false` | Removes queue delay from TTFT ranking. Queue-SLO filtering still uses the queue estimate. |
| `ignore_input_processing_time` | boolean | `false` | Removes request prefill time from TTFT ranking. |

Stargate does not range-check `next_bucket_unlock_factor`. Values from `0` to
`1` unlock a later bucket between no wait and the full TTFT gap. Values outside
that range are accepted and can cause immediate or longer waits.

When both queue bounds are set, the allowed queue time interpolates from floor
to ceiling based on elapsed request time divided by `x-request-slo-ms`. Without
a positive request SLO, the ceiling applies. Stargate does not reorder the
bounds, so set the floor less than or equal to the ceiling.

`pulsar` supports:

| Field | Type | Default | Constraint and effect |
| --- | --- | --- | --- |
| `seed` | string | empty | Changes the rendezvous ranking. Keep it stable across replicas. |
| `consider_kv_free_tokens` | boolean | `false` | Requires KV-cache values to be reported and skips candidates with fewer free tokens than the request input-token estimate. |

`pulsar-wait-and-widen` supports both wait-and-widen fields and
`consider_kv_free_tokens`.

## Request algorithm overrides

Preconfigure every algorithm that a request may select:

```json
{
  "default": "power-of-two",
  "request_algorithms": {
    "wait-and-widen": "wait-and-widen",
    "pulsar": {
      "algorithm": "pulsar",
      "seed": "request-routing-v1",
      "require_cache_affinity_key": true
    },
    "pulsar-wait-and-widen": {
      "algorithm": "pulsar-wait-and-widen",
      "seed": "request-routing-v1",
      "require_cache_affinity_key": true
    }
  }
}
```

The trusted gateway can then set:

```text
x-routing-method: pulsar
```

Header values are trimmed, converted to lowercase, and normalized from
underscores to hyphens. For example, `pulsar_wait_and_widen` selects
`pulsar-wait-and-widen`.

An absent header uses the configured model algorithm. A blank, invalid UTF-8,
unknown, or known but unconfigured value returns HTTP `400`. A model-specific
`request_algorithms` entry wins over the same top-level entry. The model's
configured algorithm is always available as an override without a
`request_algorithms` entry.

Treat routing headers as trusted internal metadata. A gateway should derive or
validate them instead of forwarding public caller values.

## Load-balancer request headers

These proxy headers affect load-balancer behavior:

| Header | Requirement | Meaning |
| --- | --- | --- |
| `x-model` | required | Exact model ID used for model configuration lookup. |
| `x-routing-key` | optional | Authenticated routing scope. It participates in affinity hashes. |
| `x-routing-method` | optional | Preconfigured request algorithm. |
| `x-cache-affinity-key` | optional or config-required | Opaque stable prefix or session identity. Blank means absent. |
| `x-input-tokens` | required `u64` | Input-token estimate used by TTFT, admission, and optional KV feasibility. |
| `x-priority` | optional `u32`, default `0` | Chooses the nearest published queue estimate at or below this priority. |
| `x-request-slo-ms` | optional `u64` | Request SLO used to interpolate queue bounds. |
| `x-max-wait-ms` | optional `u64` | Wait budget for temporarily infeasible routing, capped at 60 seconds. |

Invalid required or numeric values return HTTP `400`. `x-routing-method` is
consumed by Stargate and is not forwarded upstream. See the
[API gateway contract](api-gateway-contract.md) for the complete proxy header
contract.

## Fallback and proxy retry boundary

Algorithm fallback is part of load-balancer selection:

- WaitAndWiden later-bucket fallback depends on elapsed request time.
- Pulsar fallback walks the stable ranking after exclusions or optional KV
  filtering.
- Pulsar wait-and-widen fallback widens ranking bands and runs WaitAndWiden selection
  within each band.

Proxy retries can exclude a backend or cluster and run selection again. See
[Multi-backend cluster routing](multi-backend-clusters.md#retries) for
exclusion behavior and the [API gateway contract](api-gateway-contract.md#retry-rules)
for status, replay, and retry-budget rules.

## Metrics

Stargate records algorithm and fallback choices, proxy attempts and retries,
admission rejections, upstream latency, and active backend counts. The prefix
is configurable with `--metrics-prefix`. See the
[NVCF request-router metrics reference](../../../../../docs/user/metrics/llm-request-router/metrics.md)
for metric names, labels, and descriptions.

## Validation checklist

1. Parse the JSON before deployment.
2. Keep seeds identical across replicas that should make equivalent affinity
   choices.
3. Confirm pylons publish valid capacity, queue, and optional KV-cache fields
   required by the selected algorithm.
4. Send a request without `x-routing-method` and confirm the configured model
   algorithm in logs or `stargate_routing_selections_total`.
5. Send an allowlisted override and confirm the effective algorithm.
6. Send an unconfigured override and confirm HTTP `400`.
7. Exercise an expected fallback and inspect selection, attempt, retry, and
   exhaustion counters.

## Implementation sources

- `crates/stargate/src/load_balancer/config.rs`
- `crates/stargate/src/load_balancer/factory.rs`
- `crates/stargate/src/load_balancer/router.rs`
- `crates/stargate/src/load_balancer/wait_and_widen.rs`
- `crates/stargate/src/load_balancer/pulsar.rs`
- `crates/stargate/src/load_balancer/pulsar_wait_and_widen.rs`
- `crates/stargate/src/http_proxy/`
- `crates/stargate/src/metrics.rs`
- `benches/`
