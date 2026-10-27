# Worker Callback Proxy

A function worker calls back to the `invocation-service` or `grpc-proxy` pod
that accepted the request. Workers in the same cluster as the control plane
reach that pod directly, so they do not need this proxy, and
[High Availability](./high-availability.md) scales both services without it.

Workers in another cluster cannot reach control-plane pod IPs. The worker
callback proxy is an Envoy deployment that accepts those callbacks and
forwards each one to the pod that holds the request.

## How callbacks reach a pod

- **HTTP functions:** invocation-service hands the worker its own address,
  `<dashed-pod-ip>.invocation.nvcf.svc.cluster.local:8080`, and the proxy
  address. The worker sends the callback through the proxy on TCP 10084. The
  proxy forwards only addresses of that form.
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
  proxy and grpc-proxy certificates. The LLM add-on is not required.
- No split-plane worker URLs: leave
  `global.workerEndpoints.invocationServiceURL`,
  `global.workerEndpoints.grpcProxyWorkerConnectURL`, and
  `grpcproxy.workerConnectBaseURL` empty.
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
   global:
     workerEndpoints:
       invocationProxyURL: http://<proxy-address>:10084
   ```

   `invocationProxyURL` is the address the compute cluster uses to reach the
   proxy Service on TCP 10084. It defaults to
   `http://worker-callback-proxy.nvcf.svc.cluster.local:10084`, which resolves
   only inside the control-plane cluster.

2. Make `*.<workerDomain>` resolve to the proxy Service from the compute
   cluster, for example with a wildcard DNS record for the load balancer
   address. Workers resolve every `<dashed-pod-ip>.<workerDomain>` name, not
   only the base name.

   On a local k3d cluster, run
   `make -C tools/ncp-local-cluster configure-worker-callback-dns WORKER_CALLBACK_DOMAIN=grpc-worker.example.com`.
   It adds a CoreDNS rewrite to `kube-system/coredns-custom`.

3. Apply the stack: `HELMFILE_ENV=<environment-name> helmfile sync`.

## Give workers the CA

Workers trust only the system roots, so they reject the proxy's certificate
until they receive the issuing CA.

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
