# gRPC Invocation Enablement

The self-managed Helmfile stack deploys grpc-proxy as a core service. Client
gRPC traffic reaches grpc-proxy through the Gateway TCP listener on port
`10081`.

Split or multi-cluster deployments need one additional route. During
invocation, a worker opens an HTTP/1 CONNECT callback channel to grpc-proxy on
port `10086`. Enable this path when worker pods cannot route to grpc-proxy pod
IPs, such as when compute clusters and the control-plane cluster use separate
pod networks.

Single-cluster deployments usually do not need this worker callback route
because workers can reach grpc-proxy pod IPs directly.

For a complete Amazon EKS installation example that compares single-cluster and
multi-cluster topologies, see the
[CSP End-to-End Example](https://docs.nvidia.com/nvcf/v0.6.0-rc/csp-end-to-end-example).

<Warning>
Split or multi-cluster gRPC invocation is beta in 0.6.0. It supports one
grpc-proxy replica in the control plane. Set `grpcproxy.replicaCount: 1` and
keep the grpc-proxy Horizontal Pod Autoscaler (HPA) disabled. Multiple
grpc-proxy replicas are not supported because the 0.6.0 worker callback route is
shared TCP routing and can send a callback to the wrong pod.

</Warning>

## Network Paths

gRPC invocation uses two network legs:

| Leg | Protocol | Default port | Purpose |
| --- | --- | --- | --- |
| Client to grpc-proxy | HTTP/2 gRPC | `10081` | Sends client gRPC requests through the Gateway TCP listener. |
| Worker to grpc-proxy | HTTP/1 CONNECT | `10086` | Opens the callback channel that attaches the worker session to grpc-proxy. |

The worker callback listener does not add HTTP/1 client invocation support.
Client gRPC traffic still uses HTTP/2. Self-hosted HTTP/3 callback routing is
out of scope for 0.6.0.

## Add the Gateway Listener

Add a TCP listener for the worker callback path only when enabling split or
multi-cluster gRPC invocation. The listener name must match
`ingress.gatewayApi.routes.grpcWorker.listenerName`.

```yaml
- name: worker-tcp
  protocol: TCP
  port: 10086
  allowedRoutes:
    namespaces:
      from: Selector
      selector:
        matchLabels:
          nvcf/platform: "true"
```

See [Gateway Routing](./gateway-routing.md#grpc-worker-callback-listener) for
where this listener fits in the Gateway quickstart.

## Set Helmfile Values

Add these values to your environment file before deploying or syncing the
control plane:

```yaml
grpcproxy:
  replicaCount: 1
  deployment:
    autoscaling:
      enabled: false
  workerConnectBaseURL: "http://grpc.<domain-or-service>:10086"

ingress:
  gatewayApi:
    routes:
      grpcWorker:
        enabled: true
        listenerName: worker-tcp
```

Set `grpcproxy.workerConnectBaseURL` to the base URL that workers can use to
reach the grpc-proxy callback listener. The stack passes this value to the
grpc-proxy chart, which renders the grpc-proxy container configuration. Do not
set the raw container environment variable directly.

For local multi-cluster testing with ncp-local, use:

```yaml
grpcproxy:
  workerConnectBaseURL: "http://grpc.nvcf.svc.cluster.local:10086"
```

After updating the environment file, continue with the normal
[Helmfile Installation](./helmfile-installation.md) flow.
