# Worker Callback Proxy

A function worker calls back to the `invocation-service` or `grpc-proxy` pod
that accepted the request. Workers in the same cluster as the control plane
reach that pod directly, so they do not need this proxy, and
[High Availability](./high-availability.md) scales both services without it.

Workers in another cluster cannot reach control-plane pod IPs. The worker
callback proxy is an Envoy deployment that accepts those callbacks and
forwards each one to the pod that holds the request.

## How callbacks reach a pod

- **HTTP functions:** invocation-service hands the worker the pod target
  `http://<dashed-pod-ip>.invocation.nvcf.svc.cluster.local:8443` and the proxy
  address `https://invocation.<workerDomain>:10084`. The worker opens TLS to
  the proxy on TCP 10084 and verifies its `*.<workerDomain>` certificate. That
  session ends at the proxy. Because the target scheme is `http`, the worker
  sends a forward-proxy request through the session. The proxy then opens its
  own TLS session to the pod's 8443 listener and verifies the pod certificate
  against the stack CA and the pod name. The worker never sees the pod
  certificate. The proxy forwards only addresses of that form, and only the
  worker callback path. It can read the callback, as it can for gRPC.
- **gRPC functions:** grpc-proxy serves HTTP/3 callbacks and hands the worker
  `https://<dashed-pod-ip>.<workerDomain>`. DNS resolves that name to the
  proxy. The worker verifies the proxy's `*.<workerDomain>` certificate, and
  the proxy re-encrypts the connection to the grpc-proxy pod on UDP 10084. It
  accepts only a grpc-proxy certificate for
  `*.grpc-headless.nvcf.svc.cluster.local`.

## Prerequisites

- OpenBao and cert-manager from the stack (`openbao.enabled` and
  `certManager.enabled`, both on by default). Enabling the proxy provisions the
  OpenBao service PKI and `ClusterIssuer/nvcf-openbao-pki`, which sign the
  proxy, grpc-proxy and invocation-service certificates. The LLM add-on is
  not required.
- An invocation-service image with worker TLS support. The stack turns on its
  8443 worker TLS listener (`invocation.workerTls`) when the proxy is enabled,
  unless `workerCallbackProxy.invocation.upstreamTls.enabled` is false.
- Function workers that send `http://` callback targets as forward-proxy
  requests (worker-utils 1.3.2 or later).
- No split-plane worker URLs: leave
  `global.workerEndpoints.invocationServiceURL`,
  `global.workerEndpoints.grpcProxyWorkerConnectURL`,
  `grpcproxy.workerConnectBaseURL`, and
  `invocation.env.WORKER_STREAM_PROPERTIES__SELF_ADDRESS` empty.
- Access to the Envoy image. The chart pulls `docker.io/envoyproxy/envoy` by
  default. To use a mirror, set `workerCallbackProxy.image.registry` and
  `workerCallbackProxy.image.repository`.

## Enable the proxy

1. In the self-managed environment file, enable the proxy and expose it to the
   compute cluster:

   ```yaml
   workerCallbackProxy:
     enabled: true
     workerDomain: grpc-worker.example.com
     service:
       type: LoadBalancer
   ```

   Workers reach the invocation tunnel at
   `https://invocation.<workerDomain>:10084`, which the wildcard DNS record in
   the next step covers. Set `global.workerEndpoints.invocationProxyURL` only
   when workers use another name; it must be `https` and covered by the
   `*.<workerDomain>` certificate.

   Use a private load balancer where you can, and allow only the compute
   cluster's egress addresses through your load balancer or network controls.
   Both callback paths are TLS, but any client that reaches the Service can
   open one.

2. Make `*.<workerDomain>` resolve to the proxy Service from the compute
   cluster, for example with a wildcard DNS record for the load balancer
   address. Workers resolve every `<dashed-pod-ip>.<workerDomain>` name, not
   only the base name.

   A single cluster does not need the proxy or this step, because workers there
   call the pods directly. Leave `workerCallbackProxy.enabled` false. If you
   enable the proxy anyway, for example to test it, the stack advertises proxy
   addresses, so the names must resolve or callbacks fail.

   On a local k3d setup with separate control-plane and compute clusters, run
   `make -C tools/ncp-local-cluster configure-worker-callback-dns WORKER_CALLBACK_DOMAIN=grpc-worker.example.com`.
   It adds a CoreDNS rewrite to `kube-system/coredns-custom` in the compute
   cluster, `COMPUTE_CLUSTER_NAME` (default `ncp-local-compute-1`), where
   workers resolve DNS, and asks before it switches your kubectl context there.
   The rewrite points at `worker-callback-proxy.nvcf.svc.cluster.local`, which
   the compute cluster does not have yet, and the local control plane does not
   expose the proxy. Until [#3257](https://github.com/dsx-ai-factory/nvcf/issues/3257)
   adds both, set `WORKER_CALLBACK_PROXY_SERVICE` to a name that the compute
   cluster resolves to the proxy and that you have exposed yourself.

3. Apply the stack: `HELMFILE_ENV=<environment-name> helmfile sync`.

## Certificates and CAs

A callback uses a TLS session from the worker to the proxy. From the proxy to
the pod it uses a second, separate TLS session, unless you send plain HTTP to
the invocation-service pod (below). The two sessions never share a certificate:

| Session | Client | Server certificate | Client verifies with | Set by |
| --- | --- | --- | --- | --- |
| Worker to proxy (HTTP on TCP 10084, gRPC on UDP 443) | Worker | Proxy frontend certificate for `*.<workerDomain>` | The worker's CA bundle: the stack CA for the default issuer, its system roots for a public certificate | `workerCallbackProxy.tls.frontend` |
| Proxy to invocation-service pod (TCP 8443) | Proxy | Pod certificate for `*.invocation.nvcf.svc.cluster.local`, issued by the stack CA | `ca.crt` of Secret `invocation-service-worker-tls`, and the pod name | `workerCallbackProxy.invocation.upstreamTls.enabled` |
| Proxy to grpc-proxy pod (UDP 10084) | Proxy | Pod certificate for `*.grpc-headless.nvcf.svc.cluster.local`, issued by the stack CA | `ca.crt` of Secret `grpc-proxy-worker-tls`, and the fixed name `*.grpc-headless.nvcf.svc.cluster.local` | Always on |

The pod certificates always come from the stack CA. Only the frontend
certificate is a choice, and workers never see the pod certificates, so workers
never need their CA.

### Pod certificate Secrets

The proxy reads the CA that verifies each pod from the Secret that holds that
pod's certificate:

| Pod | Secret (default) | Key | Where the name is set |
| --- | --- | --- | --- |
| invocation-service | `invocation-service-worker-tls` | `ca.crt` | The stack names the Secret once and passes the same name to the invocation-service certificate and to the proxy (`invocation.upstream.tls.caSecret.name`), so the two cannot differ |
| grpc-proxy | `grpc-proxy-worker-tls` | `ca.crt` | The grpc-proxy chart's default name for its worker certificate. The proxy chart's `grpcProxy.caSecret` defaults to it. |

You do not set these in the stack. They matter when you use the proxy chart on
its own or want to inspect a certificate. Envoy reloads the CA when the Secret
changes, so a rotated CA needs no restart.

### Plain HTTP to the invocation-service pod

To send plain HTTP from the proxy to the invocation-service pod, set
`workerCallbackProxy.invocation.upstreamTls.enabled: false`. The stack then
uses the pod's plaintext port 8080 and does not turn on its worker TLS
listener. The HTTP callback then has one TLS session, from the worker to the
proxy. Use this only where the network between the proxy and the pod is
trusted. gRPC is not affected.

### Callback path and CONNECT

The proxy routes a forward-proxy callback only to the worker callback path,
`/v2/nvcf/worker/request-attach`, because the pod's worker port also serves the
rest of the invocation API. A request for any other path gets a 404.

The stack gives workers `http://` targets, which workers send as forward-proxy
requests, so it turns CONNECT tunnels off. A CONNECT tunnel is a raw TCP
connection to the pod that the proxy cannot read or restrict, and the worker
runs its own TLS session through it, so that worker needs the stack CA. Turn
CONNECT back on only for workers older than worker-utils 1.3.2:

```yaml
workerCallbackProxy:
  invocation:
    connect:
      enabled: true
```

## Use a public certificate on the proxy

By default the proxy frontend certificate comes from the stack's OpenBao
issuer, so workers need the stack CA. To let workers validate it with their
system roots, for example for workers that another scheduler starts and that
cannot receive a per-stack CA, give the frontend its own certificate. Use an
issuer for a public CA, such as a Let's Encrypt `ClusterIssuer` with DNS-01
validation for `*.<workerDomain>`:

```yaml
workerCallbackProxy:
  enabled: true
  workerDomain: grpc-worker.example.com
  tls:
    frontend:
      certificate:
        issuerRef:
          kind: ClusterIssuer
          name: letsencrypt
```

Or point at an existing `kubernetes.io/tls` Secret for `*.<workerDomain>`:

```yaml
workerCallbackProxy:
  tls:
    frontend:
      existingSecret: worker-public-tls
```

The frontend certificate covers both the HTTP and the gRPC callbacks. The proxy
reloads it when the Secret changes, so a renewal needs no restart. The pod
certificates stay on the stack CA, and the proxy verifies them. With a public
frontend certificate you do not need `workerCallbackCaPem`, so skip the next
section.

## Give workers the CA

Skip this section when the frontend certificate comes from a public CA.
Otherwise workers trust only the system roots, so they reject the proxy's
certificate until they receive the issuing CA.

1. Read the CA from the control plane after the proxy certificate is issued:

   ```bash
   kubectl -n nvcf get secret worker-callback-proxy-frontend-tls \
     -o jsonpath='{.data.ca\.crt}' | base64 -d > worker-callback-ca.pem
   ```

2. In the compute-plane environment file, set the PEM:

   ```yaml
   global:
     nvcaOperator:
       agent:
         workerCallbackCaPem: |
           -----BEGIN CERTIFICATE-----
           ...
           -----END CERTIFICATE-----
   ```

3. Apply the compute-plane stack. NVCA passes the CA to function pods as
   `NVCF_PROXY_CA_PEM`. Only function pods created after the change receive
   it, so redeploy existing functions.

## Verify

```bash
kubectl -n nvcf get deploy worker-callback-proxy invocation-service grpc-proxy-deployment
kubectl -n nvcf get certificate
```

Invoke an HTTP function and a gRPC function several times. The proxy's access
log shows each callback and the pod it was forwarded to:

```bash
kubectl -n nvcf logs deploy/worker-callback-proxy
```
