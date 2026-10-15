# Tunnel Transport Selection

Source: Stargate/pylon tunnel protocol implementation and Kubernetes routing constraints.

Stargate proxies supported OpenAI-compatible requests over an established QUIC
connection. Set the same protocol on Stargate and pylon:

```text
--tunnel-protocol=raw-quic|http3|webtransport
```

`raw-quic` is the default. The legacy `custom` and `custom-quic` spellings are
rejected.

Select connection direction separately on both binaries:

```text
--backend-connectivity=direct|reverse
```

Use `direct` for Edge deployments where Stargate can reach pylon's advertised
QUIC listener. Use `reverse` where pylon must dial a Stargate reverse listener.

## Matrix

| Protocol | Shape | Use when | Load balancer |
| --- | --- | --- | --- |
| `raw-quic` | Raw QUIC bidi streams with Stargate frames. | You own both endpoints and want the simplest L4 path. | L4 UDP passthrough or `stargate-k8s-router` in Raw QUIC mode. |
| `http3` | One HTTP/3 request stream per request. | Direct H3 experiments or reverse tunnels that still stay L4. | L4 UDP passthrough. |
| `webtransport` | H3 CONNECT session plus WebTransport bidi streams. | Reverse tunnels must pass through an H3/WebTransport-aware hop. | L4 passthrough or `stargate-k8s-router` in WebTransport mode. |

## Rules

- Gateway traffic never selects the tunnel protocol. It always calls the HTTP
  proxy.
- `--backend-connectivity` must describe the same topology on Stargate and
  pylon. Stargate rejects reverse-listener options in direct mode and requires
  a reverse listener in reverse mode.
- Direct backends advertise `quic://...`.
- Reverse-tunnel backends advertise upstream HTTP URL and set
  `reverse_tunnel=true`.
- Stargate opens one fresh request stream per proxied request.
- `--direct-quic-connections` controls direct backend connection-set size and
  defaults to `1`.

## Kubernetes

Backend-facing choices:

- Edge/direct: no tunnel router is required; Stargate connects to each pylon's
  reachable `quic://` pod URL.
- Cloud/reverse default: `raw-quic` with `stargate-k8s-router`.
- WebTransport: run `stargate-k8s-router --tunnel-protocol=webtransport`.
- Plain `http3` reverse tunnels are valid only on controlled L4 paths.

`stargate-k8s-router` routes backend gRPC by HTTP/2 authority in every mode.
Its UDP listener is deliberately single-mode: `raw-quic` relays QUIC streams by
SNI, while `webtransport` terminates the downstream H3 extended CONNECT,
opens an upstream H3 extended CONNECT to the selected Stargate pod, and bridges
WebTransport bidirectional streams. It rewrites the upstream session ID in each
bridged stream's prelude to the downstream session ID. The router rejects
`--tunnel-protocol=http3`; that protocol has no router-mediated mode.

WebTransport target selection is snapshotted from the downstream SNI before the
router awaits the upstream connection. A later EndpointSlice removal prevents
new sessions from targeting that pod but does not reassign an existing session.
The router preserves CONNECT request headers and propagates a non-success
upstream CONNECT status to the downstream pylon.

The two TLS hops are independent in WebTransport mode: `--tls-cert-path` and
`--tls-key-path` identify the router to pylon, while
`--upstream-tls-cert-path` (or `STARGATE_UPSTREAM_TLS_CERT_PATH`) is the trust
anchor used to verify Stargate. `--quic-insecure` disables only upstream
verification; without it, the upstream trust anchor is required.
Raw QUIC retains its existing router TLS configuration: its certificate path is
also its upstream trust source when verification is enabled, and it rejects the
WebTransport-only `--upstream-tls-cert-path` option.

On GKE internal LoadBalancer Services, expose backend-facing gRPC/TCP and
Raw QUIC/UDP with separate single-protocol Services. The active-development
GKE overlay uses the Terraform-managed shared internal VIP
`ip-us-central1-stargate-backend` (`10.69.170.115`) with TCP `443` for gRPC
registration/watch and UDP `8080` for Raw QUIC reverse tunnels.

Remote backend clusters that reach Stargate through split internal load
balancers should point pylon `--stargate-address` at the gRPC/TCP endpoint.
Stargate should set `--grpc-pylon-dial-addr` to the same gRPC/TCP endpoint so
`StargateInfo.grpc_pylon_dial_addr` tells pylon where to dial while
`advertise_addr` remains the per-pod gRPC authority/SNI routing identity.
Stargate should also set `--reverse-tunnel-pylon-dial-addr` to the QUIC/UDP
endpoint so `InferenceServerAck.reverse_tunnel_pylon_dial_addr` tells pylon
where to dial while `reverse_tunnel_target` remains the per-pod QUIC
SNI/routing identity.

When a reverse-tunnel dial address resolves to more than one socket address,
pylon and the WebTransport router try IPv4 candidates first for compatibility,
then IPv6 candidates, preserving DNS order within each family. They retry those
candidates sequentially and bind each QUIC client endpoint in the matching
address family. This is deterministic failover, not a racing Happy Eyeballs
strategy.

The local overlay mirrors the split with ClusterIP Services whose service ports
(`443` and `8080`) differ from the router pod ports (`50071` and `50072`).

### Development-Only Built-In Peer Relay

The built-in relay that sends backend gRPC and reverse-QUIC traffic from one
Stargate pod to another is a development test path, not a production transport
choice. `--enable-dev-peer-forwarding` is CLI-only and defaults to `false`; it
requires pod identity and DNS discovery, emits a startup warning, and must not
be present in production manifests. Use `stargate-k8s-router` or the applicable
L4/L7 path above for production backend traffic.

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
