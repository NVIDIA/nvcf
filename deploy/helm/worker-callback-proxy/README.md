# worker-callback-proxy

Helm chart `helm-nvcf-worker-callback-proxy` deploys Envoy in front of
grpc-proxy and invocation-service so that both can run more than one replica.

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
| 8081 | TCP | Envoy admin. Serves `/ready` for probes; not exposed by the Service. |

Envoy only completes the upstream TLS handshake with grpc-proxy pods whose
certificate matches `grpcProxy.tlsServerName` and chains to
`grpcProxy.caSecret`.

## Certificates

With the defaults the chart creates two cert-manager `Certificate` resources
from `ClusterIssuer/nvcf-openbao-pki`:

- `<fullname>-frontend-tls` for `*.<workerDomain>`, served to workers.
- `grpc-proxy-pod-tls` for `*.grpc-headless.<namespace>.svc.<clusterDomain>`,
  mounted by grpc-proxy for its HTTP/3 listener.

Set `tls.frontend.existingSecret` to use a pre-created certificate instead.
Workers must trust the CA that signed the frontend certificate.

## Required values

| Value | Example |
| --- | --- |
| `workerCallbackProxy.workerDomain` | `grpc-worker.example.com` |
| `workerCallbackProxy.image.registry` | `docker.io` or a mirror |

Wildcard DNS for `*.<workerDomain>` must resolve to the Service.

## Shutdown

The preStop hook asks Envoy to drain its listeners, then waits
`preStopSleepSeconds` before Kubernetes sends SIGTERM. Envoy keeps serving
open connections for `drainTimeSeconds`. Keep
`terminationGracePeriodSeconds` larger than the sum of both.

## Usage

```bash
make lint values=../../../tools/ci/helm-validate-values/worker-callback-proxy.yaml
make template values=../../../tools/ci/helm-validate-values/worker-callback-proxy.yaml
make validate values=../../../tools/ci/helm-validate-values/worker-callback-proxy.yaml
```
