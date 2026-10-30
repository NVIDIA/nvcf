# worker-callback-proxy

Helm chart `helm-nvcf-worker-callback-proxy` deploys Envoy in front of
grpc-proxy and invocation-service for multi-cluster worker callbacks. A
single cluster does not need it: workers call the pod directly.

A worker that picks up a request has to call back into the exact pod that
holds the client connection. The callback hostname carries that pod's IP as
dashes, for example `10-42-0-5.grpc-worker.example.com`. Envoy turns the
hostname back into the pod IP and forwards the callback there.

The chart uses the upstream `envoyproxy/envoy` image. There is no service
source in `src/`; the Envoy configuration is rendered by the chart.

## Listeners

| Port | Protocol | Purpose |
| --- | --- | --- |
| 10443 | UDP, HTTP/3 | grpc-proxy worker tunnels. TLS for `*.<workerDomain>` terminates here and is re-encrypted to the grpc-proxy pod on its HTTP/3 port. |
| 10084 | TCP, HTTP/1 over TLS | invocation-service worker tunnels. CONNECT and forward-proxy requests, allowed only to `<dashed-pod-ip>.<serviceName>.<namespace>.svc.<clusterDomain>:<port>`. TLS for `*.<workerDomain>` unless `invocation.tls.enabled` is false. |
| 8081 | TCP | Probe listener. Serves only `/ready`; not exposed by any Service. |
| 9902 | TCP | Metrics listener. Serves only `/stats/prometheus`, through the `<fullname>-metrics` ClusterIP Service. |
| 9901 | TCP | Envoy admin, on `127.0.0.1` only. Not configurable. |

## grpc-proxy upstream TLS

Envoy only completes the upstream TLS handshake with grpc-proxy pods whose
certificate matches `grpcProxy.tlsServerName` and chains to
`grpcProxy.caSecret`.

The grpc-proxy chart issues that certificate when `grpcproxy.http3.enabled`
and `grpcproxy.http3.tls.certificate.enabled` are both set; otherwise
`grpcproxy.http3.tls.secretName` must name an existing Secret. The defaults match a grpc-proxy release named `grpc-proxy` in the
same namespace: Secret `grpc-proxy-worker-tls`, key `ca.crt`, and SAN
`*.grpc-headless.<namespace>.svc.<clusterDomain>`.

Envoy reads the CA through file-based SDS and watches the mounted Secret
directory, so a rotated CA is applied without a restart.

## invocation-service TLS

An invocation callback has two TLS layers. The worker opens TLS to Envoy on
10084 with the `*.<workerDomain>` frontend certificate, so the CONNECT request
and its target are encrypted. Inside the tunnel the worker runs a second TLS
session to the invocation-service pod on `invocation.port`, 8443 by default,
which is the pod's worker TLS listener (`invocation.workerTls` in the
http-invocation chart). Envoy does not terminate that inner session.

Workers use `https://<name>.<workerDomain>:10084` as the proxy address. They
check the frontend certificate and the pod certificate separately, so they must
trust the CA of each one; the two CAs can differ. Set `invocation.tls.enabled: false`
and `invocation.port: 8080` only for a plaintext test setup.

## Destination trust boundary

Envoy checks the identity of the upstream, not its address. On the grpc-proxy
path the Lua filter turns `<dashed-ip>.<workerDomain>` into that IP, and Envoy
dials it on `grpcProxy.port` without checking that the IP is a grpc-proxy
endpoint. The upstream TLS handshake fails unless the peer presents the
grpc-proxy certificate, so callback data never reaches another host, but
Envoy still starts a QUIC handshake with any IP a caller names. The
invocation path only accepts
`<dashed-pod-ip>.<serviceName>.<namespace>.svc.<clusterDomain>:<port>`,
resolved through cluster DNS. The worker's inner TLS session checks the pod
certificate, not Envoy.

Where the cluster's CNI enforces NetworkPolicy, add an egress policy that
limits the proxy to the backend pods and callback ports, plus cluster DNS.
Enforcement depends on the CNI: a cluster without NetworkPolicy support
accepts the policy and ignores it. The example matches the self-managed stack,
with grpc-proxy and invocation-service releases of those names in `nvcf`.
Adjust the selectors to your pod labels, drop the rule for a disabled tunnel,
and add one for the collector when `tracing.endpoint` is set.

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: worker-callback-proxy-egress
  namespace: nvcf
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: worker-callback-proxy
      app.kubernetes.io/instance: worker-callback-proxy
  policyTypes: [Egress]
  egress:
    # grpc-proxy HTTP/3 (grpcProxy.port)
    - to:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: helm-nvcf-grpc-proxy
              app.kubernetes.io/instance: grpc-proxy
      ports:
        - { protocol: UDP, port: 10084 }
    # invocation-service (invocation.port)
    - to:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: helm-nvcf-invocation-service
              app.kubernetes.io/instance: invocation-service
      ports:
        - { protocol: TCP, port: 8443 }
    # Cluster DNS, for the invocation hostnames
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
          podSelector:
            matchLabels:
              k8s-app: kube-dns
      ports:
        - { protocol: UDP, port: 53 }
        - { protocol: TCP, port: 53 }
```

## Timeouts

`timeouts.grpcStreamIdle` and `timeouts.invocationStreamIdle` close a stream
that carries no data in either direction for that long. Without them Envoy
closes quiet streams after 5 minutes, and a route timeout of `0s` does not
lift that limit. `0s` disables the limit. The defaults match the connection
idle timeouts:

- `invocationStreamIdle: 3605s` is invocation-service's request timeout: one
  hour, the longest request and poll it serves, plus 5s so it can still
  respond at the hour. Envoy should not close a quiet request before the
  service does. Change it only if that limit changes.
- `grpcStreamIdle: 1800s` is the gRPC connection idle timeout from the hosted
  grpc-proxy Envoy (HTTP/3 listener); grpc-proxy itself has no matching
  limit. Set it to the longest quiet gap expected in a gRPC stream: lower to
  free quiet streams sooner, higher for functions whose streams stay quiet
  longer.

Hosted Envoy does not set `stream_idle_timeout`, so it uses the 5m default.

## Frontend certificate

With the defaults the chart creates a cert-manager `Certificate`,
`<fullname>-frontend-tls`, for `*.<workerDomain>`. Set
`tls.frontend.certificate.issuerRef.name` to the issuer, or set
`tls.frontend.existingSecret` to use a pre-created certificate. Workers must
trust the CA that signed the frontend certificate.

## Required values

| Value | Example |
| --- | --- |
| `workerCallbackProxy.workerDomain` | `grpc-worker.example.com` |
| `workerCallbackProxy.tls.frontend.certificate.issuerRef.name` | `nvcf-worker-callback`, unless `existingSecret` is set |

`workerCallbackProxy.image.registry` is optional. Set it only to pull Envoy
from a mirror; when empty the image is `repository:tag`.

Wildcard DNS for `*.<workerDomain>` must resolve to the Service.
`values.schema.json` rejects unknown keys, and render fails on inconsistent
ports, Service settings, or shutdown timings.

## Observability

- Application and access logs are JSON lines on stdout. Access logs carry
  `x_request_id`, `trace_id`, and `connection_id`.
- `metrics.serviceMonitor.enabled` adds a Prometheus Operator
  `ServiceMonitor` for the metrics Service.
- `tracing.endpoint` sends OpenTelemetry traces to an OTLP/gRPC collector.

## Placement

The chart renders `affinity` and `topologySpreadConstraints` as given and
renders neither when empty, like the other NVCF charts. The self-managed stack
supplies hostname anti-affinity and zone spread from `highAvailability.mode`.
Standalone installs that run more than one replica should set them.

## Shutdown

The preStop hook asks Envoy to drain its listeners gradually over
`drainTimeSeconds`, then waits `preStopSleepSeconds` before Kubernetes sends
SIGTERM. Envoy exits on SIGTERM. Render fails unless
`drainTimeSeconds <= preStopSleepSeconds < terminationGracePeriodSeconds`.

## Usage

`make template`, `make lint`, and `make validate` use the CI values in
`tools/ci/helm-validate-values/worker-callback-proxy.yaml` unless `values=` is
set. `make test` lints with `values=` but its render tests always use the CI
values, since they assert exact output. `make install` requires `values=`.

```bash
make test             # helm lint and render tests
make validate         # kubeconform, including cert-manager and ServiceMonitor schemas
make validate-envoy   # envoy --mode validate and CA rotation; needs Docker
make install values=my-values.yaml
```
