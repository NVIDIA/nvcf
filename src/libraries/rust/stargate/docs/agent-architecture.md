# Agent Architecture Reference

> Type: Reference. Source: `crates/stargate`, `crates/pylon-lib`, Kubernetes manifests, and current behavior tests.

Read this for routing, proxying, registration, Kubernetes, pylon, and
observability changes.

## System Shape

```text
gateway -> stargate proxy -> QUIC tunnel -> pylon -> local inference server
backend/pylon -> stargate gRPC registration -> local routing state
```

Stargate is the control plane and routing entrypoint. Pylon is the backend
sidecar.

## Routing Identity

- Routing key type: `RoutingTargetKey { routing_key: Option<String>, model_id: String }`.
- `routing_key` comes from `WorkerAuthenticator`, not the registration proto.
- HTTP callers may provide trusted `x-routing-key`; omitted means `None`.
- `inference_server_id` identifies one live backend registration.
- `cluster_id` groups backend registrations that share one hardware/cache
  domain.

## Proxy Contract

The canonical frontend/proxy contract is [api-gateway-contract.md](api-gateway-contract.md).
Keep endpoint lists, required headers, error codes, and retry rules there.

Architecture invariants:

- Stargate never parses proxy request bodies.
- Request bodies are buffered only for retry replay.
- Proxy requests use an already-established tunnel to a selected pylon.
- Stargate strips caller-supplied internal queue headers.
- Pylon strips Stargate internal headers before forwarding upstream.

## Tunnel Transports

`--tunnel-protocol=custom|http3|webtransport` must match on Stargate and pylon.

- `custom`: raw QUIC bidirectional stream with Stargate framing.
- `http3`: HTTP/3 request stream.
- `webtransport`: HTTP/3 extended CONNECT session plus WebTransport streams.

Direct backends advertise `quic://...`. Reverse-tunnel backends advertise their
upstream HTTP URL and set `reverse_tunnel=true`.

Read [tunnel-transports.md](tunnel-transports.md) before changing transport
selection or load-balancer requirements.

## Routability

A backend is routable only after:

- registration is active for the model
- the QUIC path exists
- a forwarded `/health` RTT sample succeeds
- any required local calibration is complete

Backend RTT comes from the registration-scoped forwarded `/health` loop, not
QUIC transport stats.

Stargates do not share routing or calibration state. HTTP proxy and
`ListModels` requests use only local state.

## Pylon Contract

Pylon:

- registers with every concrete Stargate it should be reachable through
- validates tunneled request headers and endpoint body shape
- forwards to the local HTTP upstream
- converts local upstream retry hints into Stargate retry metadata
- observes request lifecycle and runtime stats
- runs bringup calibration and active canaries
- opens reverse tunnels when configured

Streaming chat and Responses requests must set `"stream": true`.
Embeddings requests must be valid JSON and do not need `stream`.
Local upstreams can mark retryable admission failures with
`x-stargate-upstream-retryable: true`; pylon translates that to internal
Stargate retry headers.

Request-observer terminal transitions are invariants. Terminalizing an already
terminal request is a bug.

## Calibration

Each Stargate owns only local calibration state.

- Pylons with coordinated calibration register to all discovered Stargates.
- Each Stargate assigns one local owner for each
  `(routing_key, cluster_id, model_id)`.
- The assigned pylon measures local upstream capacity and submits one result to
  the assigning Stargate.
- Calibration values are not replicated and are not sent back to pylons.
- Effective cluster input capacity is
  `max(local_calibration_floor, sum(runtime_backend_reports))`.

Read [coordinated-calibration-state-machine.md](coordinated-calibration-state-machine.md).

## Load Balancing

Built-ins:

- `power-of-two`
- `groq-multiregion`
- `round-robin`
- `random`
- `pulsar`
- `pulsar-multiregion`

`LoadBalancerRequest` carries request inputs. Do not grow trait methods with
positional arguments.

All algorithms evaluate cluster candidates. Backend selection inside a chosen
cluster is a state-owned round robin. PULSAR ranks by stable capacity and keeps
transient live load in feasibility gates.

## Kubernetes

- `stargate`: backend-facing gRPC/QUIC service.
- `stargate-headless`: peer discovery and pod identity.
- `stargate-model-discovery`: frontend `ListModels`.
- `stargate-proxy`: frontend OpenAI-compatible HTTP proxy.
- `stargate-k8s-router`: optional backend-facing router for the `custom`
  transport.

Gateway traffic uses only `stargate-model-discovery` and `stargate-proxy`.
Backend traffic uses `WatchStargates`, registration, calibration submission,
and reverse tunnels.

Raw pod IPs are not a client contract. Use advertised per-pod hostnames and
headless DNS for peer forwarding.

`ListModels` is local-only and eventually consistent.

Base mock backend manifests run pylon as a sidecar in each mock-dynamo
Deployment. Pylon forwards to the colocated inference container on loopback,
keeps the pod labeled `role=inference-engine`, and adds `pylon-sidecar=true`
for pylon metrics scraping.

The active-development GKE overlay keeps the base `stargate` ClusterIP Service
for in-cluster backend traffic and also exposes split internal L4
LoadBalancer Services: `stargate-grpc-lb` for TCP `443` registration/watch
traffic and `stargate-quic-lb` for UDP `8080` custom QUIC reverse tunnels. The
split is required because GKE internal LoadBalancer Services cannot mix TCP and
UDP ports. Both Services use the Terraform-managed shared internal VIP
`ip-us-central1-stargate-backend` (`10.69.170.115`) while LB DNS names are not
available.

Cross-cluster backend overlays seed pylon `--stargate-address` with the
backend-facing gRPC endpoint. If Stargate sets `--grpc-pylon-dial-addr`,
`WatchStargates` tells pylon to dial that gRPC endpoint while keeping each
`advertise_addr` as the per-pod HTTP/2 authority/SNI routing identity. If
Stargate sets `--reverse-tunnel-pylon-dial-addr`, ACKs tell pylon to dial that
QUIC endpoint while keeping `reverse_tunnel_target` as the per-pod SNI/routing
identity.

The base manifests currently leave NetworkPolicy enforcement to overlays or
cluster policy.

The local overlay mirrors the split backend-facing LB shape with ClusterIP
Services on `443` and `8080` that target router pod ports `50071` and `50072`.
The rendered and live K8s checks keep NetworkPolicy admission tied to the pod
ports, not the Service ports.

## Observability

- Stargate metrics: `--metrics-port`, default `9090`.
- Pylon metrics: default `9089`.
- Router metrics: router health listener.
- OTel export is opt-in with `--otel-endpoint`.
- Main proxy span: `proxy_openai_request`.
- Use `x-request-id` as the request correlation id.

## Important Files

- `crates/stargate/src/http_proxy.rs`
- `crates/stargate/src/load_balancer/`
- `crates/stargate/src/routing_state/`
  - `mod.rs`: `StargateState` facade and cross-subsystem coordination.
  - `keys.rs`: routing identities, delivery targets, and registration identity.
  - `registration.rs`: live registration index and `RunningRegistration`.
  - `clusters.rs`: target lifecycle, cluster state, active models, and metrics.
  - `snapshots.rs`: routable backend and cluster snapshot types.
  - `calibration.rs`: coordinated calibration assignment and completion state.
  - `reservations.rs`: routing reservation accounting and queue estimates.
- `crates/stargate/src/metrics.rs`
- `crates/stargate/src/telemetry.rs`
- `crates/pylon-lib/src/`
- `crates/protocol/`
- `crates/proto/`
