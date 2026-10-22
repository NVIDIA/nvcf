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
| 10084 | TCP, HTTP/1 | invocation-service worker tunnels. CONNECT and forward-proxy requests, allowed only to `<dashed-pod-ip>.<serviceName>.<namespace>.svc.<clusterDomain>:<port>`. |
| 8081 | TCP | Probe listener. Serves only `/ready`; not exposed by any Service. |
| 9902 | TCP | Metrics listener. Serves only `/stats/prometheus`, through the `<fullname>-metrics` ClusterIP Service. |
| 9901 | TCP | Envoy admin, on `127.0.0.1` only. Not configurable. |

## grpc-proxy upstream TLS

Envoy only completes the upstream TLS handshake with grpc-proxy pods whose
certificate matches `grpcProxy.tlsServerName` and chains to
`grpcProxy.caSecret`.

The grpc-proxy chart issues that certificate when `grpcproxy.http3` is
enabled. The defaults match a grpc-proxy release named `grpc-proxy` in the
same namespace: Secret `grpc-proxy-worker-tls`, key `ca.crt`, and SAN
`*.grpc-headless.<namespace>.svc.<clusterDomain>`.

Envoy reads the CA through file-based SDS and watches the mounted Secret
directory, so a rotated CA is applied without a restart.

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
| `workerCallbackProxy.image.registry` | `docker.io` or a mirror |
| `workerCallbackProxy.tls.frontend.certificate.issuerRef.name` | `nvcf-worker-callback`, unless `existingSecret` is set |

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

With no custom `affinity` or `topologySpreadConstraints`, the chart renders a
soft hostname anti-affinity and a soft zone spread. Set
`defaultPodAntiAffinity` or `defaultZoneSpread` to `false` to drop them.

## Shutdown

The preStop hook asks Envoy to drain its listeners gradually over
`drainTimeSeconds`, then waits `preStopSleepSeconds` before Kubernetes sends
SIGTERM. Envoy exits on SIGTERM. Render fails unless
`drainTimeSeconds <= preStopSleepSeconds < terminationGracePeriodSeconds`.

## Usage

`make template`, `make lint`, `make validate`, and `make test` use the CI
values in `tools/ci/helm-validate-values/worker-callback-proxy.yaml` unless
`values=` is set. `make install` requires `values=`.

```bash
make test             # helm lint and render tests
make validate         # kubeconform, including cert-manager and ServiceMonitor schemas
make validate-envoy   # envoy --mode validate and CA rotation; needs Docker
make install values=my-values.yaml
```
