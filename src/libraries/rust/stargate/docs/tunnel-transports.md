# Tunnel Transport Selection

> Type: Reference. Source: Stargate/pylon tunnel protocol implementation and Kubernetes routing constraints.

Stargate proxies supported OpenAI-compatible requests over an established QUIC
connection. Set the same protocol on Stargate and pylon:

```text
--tunnel-protocol=custom|http3|webtransport
```

`custom` is the default.

## Matrix

| Protocol | Shape | Use when | Load balancer |
| --- | --- | --- | --- |
| `custom` | Raw QUIC bidi streams with Stargate frames. | You own both endpoints and want the simplest L4 path. | L4 UDP passthrough or `stargate-k8s-router`. |
| `http3` | One HTTP/3 request stream per request. | Direct H3 experiments or reverse tunnels that still stay L4. | L4 UDP passthrough. |
| `webtransport` | H3 CONNECT session plus WebTransport bidi streams. | Reverse tunnels must pass through an H3/WebTransport-aware L7 hop. | L4 passthrough or WebTransport-aware L7. |

## Rules

- Gateway traffic never selects the tunnel protocol. It always calls the HTTP
  proxy.
- Direct backends advertise `quic://...`.
- Reverse-tunnel backends advertise upstream HTTP URL and set
  `reverse_tunnel=true`.
- Stargate opens one fresh request stream per proxied request.
- `--direct-quic-connections` controls direct backend connection-set size and
  defaults to `1`.

## Kubernetes

Backend-facing choices:

- Default: `custom` with `stargate-k8s-router`.
- BYO L7 proxy: use `webtransport`.
- Plain `http3` reverse tunnels are valid only on controlled L4 paths.

`stargate-k8s-router` routes backend gRPC by HTTP/2 authority and custom QUIC by
SNI. It is not an HTTP/3 or WebTransport L7 proxy.

On GKE internal LoadBalancer Services, expose backend-facing gRPC/TCP and
custom QUIC/UDP with separate single-protocol Services. The active-development
GKE overlay uses the Terraform-managed shared internal VIP
`ip-us-central1-stargate-backend` (`10.69.170.115`) with TCP `443` for gRPC
registration/watch and UDP `8080` for custom QUIC reverse tunnels.

Remote backend clusters that reach Stargate through split internal load
balancers should point pylon `--stargate-address` at the gRPC/TCP endpoint.
Stargate should set `--grpc-pylon-dial-addr` to the same gRPC/TCP endpoint so
`StargateInfo.grpc_pylon_dial_addr` tells pylon where to dial while
`advertise_addr` remains the per-pod gRPC authority/SNI routing identity.
Stargate should also set `--reverse-tunnel-pylon-dial-addr` to the QUIC/UDP
endpoint so `InferenceServerAck.reverse_tunnel_pylon_dial_addr` tells pylon
where to dial while `reverse_tunnel_target` remains the per-pod QUIC
SNI/routing identity.

The local overlay mirrors the split with ClusterIP Services whose service ports
(`443` and `8080`) differ from the router pod ports (`50071` and `50072`) so
NetworkPolicy checks can verify traffic is admitted only on the router pod
ports.

Frontend services are unaffected:

- `stargate-model-discovery`
- `stargate-proxy`

## Benchmark

Loopback transport comparison:

```bash
cargo run --release -p stargate-bench -- transport-bench \
  --requests 20000 \
  --concurrency 256 \
  --warmup-requests 1000 \
  --output-dir .bench-out/transport
```

Short runs are smoke tests. Use long, repeated runs and representative
load-balancers for performance claims.
