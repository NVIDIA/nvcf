# Multi-Backend Cluster Routing

> Type: Reference. Source: implemented Stargate and pylon cluster routing behavior.

Stargate routes at cluster level and executes at backend level.

Use this when multiple pylon registrations front the same hardware/cache
cluster.

## Identities

- `inference_server_id`: concrete pylon/backend registration id. It remains the
  QUIC pool key, response header, and compatibility metric label.
- `cluster_id`: logical hardware/capacity domain. Empty means
  `cluster_id == inference_server_id`.
- routing target: `(routing_key, model_id)`.

Pylon supports `--cluster-id`; default is `--inference-server-id`.

## State Shape

```text
(routing_key, model_id)
  -> cluster_id
     -> active backend_id set
     -> aggregated cluster snapshot

backend_id
  -> registration stream
  -> QUIC/reverse tunnel delivery target
  -> latest per-model backend snapshot
```

Proxy flow:

```text
request
  -> find active clusters for RoutingTargetKey
  -> load balancer chooses cluster_id
  -> state round-robins an active backend_id in that cluster
  -> proxy over that backend's QUIC path
```

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

Special case:

- `last_mean_input_tps = max(local_calibration_floor, sum(active runtime backend input TPS))`

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
uses per-cluster round robin.

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
