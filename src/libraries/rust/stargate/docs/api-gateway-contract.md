# API Gateway Contract

Canonical source for frontend model discovery and HTTP proxy contracts.

The gateway is the public API boundary. Stargate is the local backend router.

## Source Checked

This contract was checked against:

- `crates/stargate/src/http_proxy.rs`
- `crates/stargate/src/http_proxy/diagnostics.rs`
- `crates/stargate/src/control_plane.rs`
- `crates/pylon-lib/src/request_observer.rs`
- `crates/pylon-lib/src/queue_admission.rs`
- `crates/stargate/tests/suite/proxy_contract.rs`
- `crates/stargate/tests/suite/stats_discovery.rs`
- `crates/stargate/tests/suite/routing_key.rs`

## Ownership

Gateway owns:

- public authentication and authorization
- tenant/function/model lookup
- `routing_key` derivation
- public model alias to Stargate `x-model`
- allowed-model checks
- `x-request-id`
- `x-input-tokens`
- optional affinity, priority, SLO, and retry-budget headers

Stargate owns:

- local backend registration state
- `ListModels`
- load balancing for `RoutingTargetKey { routing_key, model_id }`
- QUIC tunnel transport to pylon
- opaque HTTP body forwarding
- internal retry/failover policy

Stargate does not authenticate public callers, parse proxy request bodies, or
replicate routing state between Stargate pods.

## Kubernetes Services

Use only frontend services:

| Purpose | Service | Port |
| --- | --- | --- |
| Model discovery | `stargate-model-discovery` | `50073` |
| Inference proxy | `stargate-proxy` | `8000` |

Do not target pod IPs or per-pod Stargate addresses for gateway traffic.
`ListModels` and HTTP proxy requests are local to the pod selected by
Kubernetes service balancing.

Pylons use backend-facing `WatchStargates`, registration, and QUIC tunnel
services. Those are not gateway APIs.

## Model Discovery

Call `StargateModelDiscovery/ListModels`, or use the HTTP mirror on the
`stargate-proxy` Service:

```text
GET /v1/models
```

The HTTP mirror accepts an optional repeatable `model_ids` filter:

| Query parameter | Meaning |
| --- | --- |
| `model_ids` | Optional repeatable model filter, for example `?model_ids=a&model_ids=b`. |

The HTTP response is JSON with the same shape as the gRPC response:

```json
{"model_ids":["model-a","model-b"]}
```

An empty `model_ids` value returns HTTP `400`. The HTTP path uses the same
model-id normalization and local routing-state lookup as gRPC, with an
unscoped model lookup.

Request:

- `routing_key`: optional; omitted or blank means unscoped.
- `model_ids`: optional filters; blank entries are invalid.

Response:

- `model_ids`: model ids with a current routable target generation in the
  selected pod's local state.

`ListModels` is a hint, not a reservation. If a recent positive result is
followed by proxy `404` with `x-stargate-error-code: no_eligible_candidates`,
the local route changed after discovery returned; the gateway may retry
according to its normal policy. A `503` for a registered model means no
eligible active backend, not a discovery miss.

## Operator Debug State

`GET /debug/state` on the `stargate-proxy` HTTP listener returns a JSON
snapshot for live diagnosis. It includes safe static configuration (identity,
listener addresses, tunnel selection, and reverse-tunnel enablement) plus the
serving Stargate's current active model IDs. The state is local and
best-effort: routing can change while the response is being assembled.

This endpoint intentionally exposes only explicit non-secret fields; it does
not serialize credentials, TLS material, backend URLs, or routing topology.
Restrict it to trusted operator networks; it is not a public gateway API.

## Proxy Endpoints

Supported:

```text
POST /v1/chat/completions
POST /v1/responses
POST /v1/embeddings
```

Required headers:

| Header | Owner | Meaning |
| --- | --- | --- |
| `x-request-id` | Gateway | Global request id and pylon observation key. |
| `x-model` | Gateway | Exact Stargate model id. |
| `x-input-tokens` | Gateway | Unsigned input-token estimate. |

Optional trusted headers:

| Header | Meaning |
| --- | --- |
| `x-routing-key` | Authenticated routing scope. Omit for unscoped. |
| `x-routing-method` | Request-scoped load-balancer override, only for methods allowed by Stargate config. |
| `x-cache-affinity-key` | Opaque cache/prefix identity. Required by some LB configs. |
| `x-priority` | Unsigned priority, default `0`. |
| `x-request-slo-ms` | Per-request LB latency hint. |
| `x-max-wait-ms` | Wait budget for temporarily infeasible candidates. |
| `x-stargate-max-wait-ms` | Stargate internal retry budget. |

The gateway must synthesize or validate these headers. Do not pass public
caller-supplied routing headers through blindly.

Internal header:

- `x-stargate-expected-queue-ms`: Stargate-to-pylon only. Stargate strips
  caller values; pylon strips it before upstream forwarding.

Body rules:

- Stargate treats bodies as opaque bytes.
- Pylon validates tunneled bodies.
- Use `content-type: application/json` for JSON bodies.
- `/v1/chat/completions` and `/v1/responses` must be valid JSON with
  `"stream": true`.
- `/v1/embeddings` must be valid JSON and does not need `stream`.

## Responses

Stargate forwards upstream status, body, and allowed headers, then adds:

- `x-inference-server-id`
- `x-inference-server-url`
- `x-stargate-cluster-id`

Treat them as internal unless the public API chooses to expose them.

Common errors:

| Status | Meaning | Gateway behavior |
| --- | --- | --- |
| `400` | Missing/invalid contract input. | Do not retry unchanged. |
| `404` + `no_eligible_candidates` | Unknown or unregistered local target. | Retry only when a recent discovery response may have raced a local route change. |
| `413` | Replay body too large. | Do not retry unchanged. |
| `502/503/504` | Transport, upstream, retry, or active-backend failure. | Treat as serving failure. |

Stargate strips pylon retry metadata before returning downstream:
`x-stargate-retryable`, `x-stargate-retry-reason`,
`x-stargate-retry-after-ms`.

## Retry Rules

Stargate may retry when the request body is replayable and retry budget remains.
Pylon may return retryable `429` with
`x-stargate-retry-reason: queue_estimate_mismatch` before upstream execution.
The local upstream may mark `429` or `503` retryable for pylon with
`x-stargate-upstream-retryable: true`; pylon converts that to Stargate retry
metadata and does not forward the upstream header downstream.

Gateway rules:

- Set `x-stargate-max-wait-ms` from the remaining request deadline.
- Avoid blind external retries after a streaming request may have reached an
  upstream.
- Keep the same `x-request-id` only for convergence retries that did not reach
  an upstream.

## Checklist

1. Use only `stargate-model-discovery` and `stargate-proxy`.
2. Authenticate and authorize the caller.
3. Derive trusted `routing_key`.
4. Resolve and authorize the model, then set `x-model`.
5. Set `x-request-id` and `x-input-tokens`.
6. Enforce streaming body rules for chat and Responses.
7. Treat `ListModels` as a short-lived hint.
8. Keep backend response headers internal unless intentionally exposed.
