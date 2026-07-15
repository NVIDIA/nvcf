# Multi-Backend Cluster Routing

Source: implemented Stargate and pylon cluster routing behavior.

Stargate routes at cluster level and executes at backend level.

Use this when multiple pylon registrations front the same hardware/cache
cluster.

## Identities

- `inference_server_id`: concrete pylon/backend registration id. It remains the
  QUIC pool key, response header, and metric label.
- `cluster_id`: logical hardware/capacity domain. Empty means
  `cluster_id == inference_server_id`.
- routing target: `(routing_key, model_id)`.

Pylon supports `--cluster-id`; default is `--inference-server-id`.

## State Shape

```text
(routing_key, model_id)
  -> active target generation
     -> active backend count
     -> cluster_id
        -> exact registration-cluster generation
        -> shared immutable backend publications
           -> exact registration generation
           -> exported routing data
        -> aggregated cluster snapshot

backend_id
  -> exact running registration
  -> QUIC/reverse tunnel delivery target
  -> latest per-model backend publication
```

Target cluster membership, active-backend count, and final retirement change
as one generation. The final empty generation retires while its top-level map
entry is exclusively owned; a stale registration update retries against the
replacement target instead of publishing into detached state.

Overlapping registrations in one `(routing_key, cluster_id)` scope share one
registration-cluster generation. It retires at the true zero-registration
boundary. Routed cleanup matches exact registration and cluster-generation
identity, so delayed cleanup cannot remove a same-ID replacement or mix a
fresh cluster lifetime with retired routing state.

Proxy flow:

```text
request
  -> find active clusters for RoutingTargetKey
  -> load balancer chooses cluster_id
  -> state round-robins a shared immutable backend publication in that cluster
  -> proxy and same-backend retries retain that exact publication
```

A later heartbeat replaces the stored backend publication. In-flight requests
retain the immutable snapshot they selected, so request routing does not
deep-clone full backend stats and identity on selection or reconnect retry.

## Aggregation

Backend-summed fields:

- `output_tps`
- `queue_size`
- `queued_input_size`
- `input_processing_queries`
- `output_generation_queries`
- `stats_capabilities`
- `stats_sources`

Cluster latest-wins fields:

- `max_output_tps`
- KV capacity/used/free tokens
- `num_running_queries`
- `max_engine_concurrency`
- `total_query_input_size`
- `queue_time_estimate_ms_by_priority`

- `last_mean_input_tps` is the sum of active backend reports.

`RoutedClusterSnapshot.rtt` is the unweighted arithmetic mean of the latest
forwarded `/health` RTT publication from every current active backend in the
cluster. Each backend contributes exactly once. Inserting a backend, replacing
its publication on heartbeat, or removing it recomputes the mean before load
balancers read the snapshot. The calculation uses `Duration`'s nanosecond
precision and truncates any fractional nanosecond toward zero without summing
whole durations, so valid RTT inputs cannot overflow the aggregate.

Every Pylon bootstraps its own input TPS before registration. Shared-hardware
clusters use `--initial-input-tps` as a per-Pylon contribution; do not copy a
cluster-wide total to every Pylon. Once startup completes, the Pylon registers
the same authoritative runtime snapshot with every discovered Stargate.

Latest-wins uses Stargate receive time and only active backend snapshots. If the
source backend disappears, recompute from remaining active snapshots.

## Load Balancers

All algorithms choose from cluster snapshots.

- `power-of-two`: compares aggregated cluster load.
- `groq-multiregion`: uses aggregated stats and representative RTT.
- `round-robin`: rounds across clusters.
- `random`: picks a cluster.
- `pulsar`: hashes by `cluster_id`, not `backend_id`.

Backend selection inside a chosen cluster is independent of the algorithm and
uses per-cluster round robin. Load balancers consume the representative RTT
from the shared cluster snapshot; they do not aggregate backend RTTs
independently.

## Retries

Track two exclusion sets:

- `failed_backend_ids`: concrete attempts not to retry for this request.
- `failed_cluster_ids`: clusters not to reselect for this request.

Rules:

1. Transport-local backend failure marks the backend failed.
2. If the cluster has another active backend, try that sibling first.
3. Retryable upstream responses after upstream execution fail the cluster.
4. Pylon `queue_estimate_mismatch` before upstream execution fails only the
   rejecting backend first.
5. After cluster failure, rerun load balancing without failed clusters.

## Validation Rules

- `inference_server_id` is required.
- Empty `cluster_id` normalizes to `inference_server_id`.
- Live registration uniqueness is by `inference_server_id`.
- A registration stream cannot change backend id, cluster id, URL, or reverse
  tunnel mode after its first message.
- A backend is active for a model only when its model is active and its QUIC
  path has a forwarded `/health` RTT.
- A cluster is routable only when at least one backend is active for the model.

## Observability

Compatibility surfaces:

- `x-inference-server-id`
- `x-inference-server-url`
- metrics labelled by `inference_server_id`

Cluster surfaces:

- `x-stargate-cluster-id`
- span field `selected_cluster.id`
- existing concrete backend span field `selected_inst.id`
