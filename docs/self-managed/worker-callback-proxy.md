# Worker Callback Proxy

A function worker calls back to the `invocation-service` or `grpc-proxy` pod
that accepted the request. The worker callback proxy is an Envoy deployment
that forwards those callbacks to the right pod, so both services can run more
than one replica. With [High Availability](./high-availability.md) enabled,
both services then run 2 replicas with a PodDisruptionBudget.

The proxy applies only to single-cluster installs, where the compute plane
runs in the same cluster as the control plane.

## How callbacks reach a pod

- **HTTP functions:** invocation-service hands the worker its own address,
  `<dashed-pod-ip>.invocation.nvcf.svc.cluster.local:8080`, and the proxy
  address. The worker sends the callback through the proxy on TCP 10084. The
  proxy forwards only addresses of that form.
- **gRPC functions:** grpc-proxy hands the worker
  `https://<dashed-pod-ip>.<workerDomain>`. DNS resolves that name to the
  proxy. The worker opens an HTTP/3 connection, verifies the proxy's
  `*.<workerDomain>` certificate, and the proxy re-encrypts the connection to
  the grpc-proxy pod on UDP 10084. The proxy accepts only a grpc-proxy pod
  certificate for `*.grpc-headless.nvcf.svc.cluster.local`.

## Prerequisites

- OpenBao and cert-manager from the stack (`openbao.enabled` and
  `certManager.enabled`, both on by default). Enabling the proxy provisions the
  OpenBao service PKI and `ClusterIssuer/nvcf-openbao-pki`, which sign both
  proxy certificates. The LLM add-on is not required.
- An `nvcf-openbao-migrations` image that supports `NVCF_SERVICE_PKI_ENABLED`.
  Older images provision the PKI only with the LLM add-on enabled.
- No split-plane worker URLs: leave
  `global.workerEndpoints.invocationServiceURL`,
  `global.workerEndpoints.grpcProxyWorkerConnectURL`, and
  `grpcproxy.workerConnectBaseURL` empty.
- Access to the Envoy image. The chart pulls `docker.io/envoyproxy/envoy` by
  default. To use a mirror, set `workerCallbackProxy.image.registry` and
  `workerCallbackProxy.image.repository`.

## Enable the proxy

1. In the self-managed environment file, set:

   ```yaml
   workerCallbackProxy:
     enabled: true
     workerDomain: grpc-worker.nvcf.svc.cluster.local
   ```

   Workers send invocation callbacks to
   `http://worker-callback-proxy.nvcf.svc.cluster.local:10084`. If they reach
   the proxy through another address, set
   `global.workerEndpoints.invocationProxyURL`.

2. Add a DNS rule that resolves `*.<workerDomain>` to
   `worker-callback-proxy.nvcf.svc.cluster.local`. On k3s and AKS, add a
   CoreDNS rewrite to the `kube-system/coredns-custom` ConfigMap:

   ```text
   rewrite stop {
     name regex (.+)\.grpc-worker\.nvcf\.svc\.cluster\.local\.$ worker-callback-proxy.nvcf.svc.cluster.local.
     answer auto
   }
   ```

   On a local k3d cluster, run
   `make -C tools/ncp-local-cluster configure-worker-callback-dns WORKER_CALLBACK_DOMAIN=grpc-worker.nvcf.svc.cluster.local`.
   On other platforms, add the equivalent rule to the cluster DNS.

3. Apply the stack: `HELMFILE_ENV=<environment-name> helmfile sync`.

## Give workers the CA

Workers trust only the system roots, so they reject the proxy's certificate
until they receive the issuing CA.

1. Read the CA from the control plane after the proxy certificate is issued:

   ```bash
   kubectl -n nvcf get secret worker-callback-proxy-frontend-tls \
     -o jsonpath='{.data.ca\.crt}' | base64 -d > worker-callback-proxy-ca.pem
   ```

2. In the compute-plane environment file, set the PEM:

   ```yaml
   global:
     nvcaOperator:
       agent:
         workerCallbackProxyCaPem: |
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
kubectl -n nvcf get certificate worker-callback-proxy-frontend grpc-proxy-pod-tls
```

Invoke an HTTP function and a gRPC function several times. The proxy's access
log shows each callback and the pod it was forwarded to:

```bash
kubectl -n nvcf logs deploy/worker-callback-proxy
```
