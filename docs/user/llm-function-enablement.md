# LLM Function Enablement

Enable the LLM addon before creating or invoking functions with
`functionType: "LLM"` through the LLM invocation route. The addon deploys the
LLM API Gateway and LLM request router, creates the external LLM invocation
route, and configures worker pods to use the `pylon` sidecar for model-aware
routing.

For LLM function payload shape and invocation examples, see
[Function Creation](./function-creation.md) and
[LLM Gateway](./llm-gateway.md).
For request-router deployment, trusted headers, and rollout validation, see
[LLM Request Router Load Balancing](./llm-request-router-load-balancing.md).

## When to Enable

Enable the LLM addon when NVCF should route OpenAI-compatible requests by
function and model through `llm.invocation.<domain>`. The gateway extracts the
function ID from the OpenAI `model` field, applies LLM-specific validation and
rate limits, and sends the request through the LLM request router.

Standard HTTP, gRPC, and LLS functions do not require this addon, even when a
container exposes paths such as `/v1/chat/completions`, `/v1/responses`, or
`/v1/embeddings`.

When enabled, the stack creates:

- `llm-api-gateway` in the `nvcf` namespace.
- `llm-request-router` in the `nvcf` namespace.
- The `llm.invocation.<domain>` HTTPRoute when Gateway API ingress is enabled.
- LLM worker pods with a `pylon` sidecar that forwards requests to the function
  container on the configured `inferencePort`.

## Helmfile Configuration

Add the LLM addon and `agentConfig` block to your Helmfile environment file
before applying the stack:

```yaml
addons:
  llm:
    enabled: true
    gateway:
      replicaCount: 1
      auth:
        grpcInsecure: true
      metrics:
        serviceMonitor:
          enabled: false
    requestRouter:
      replicaCount: 1
      metrics:
        serviceMonitor:
          enabled: false

agentConfig:
  mergeConfig: |
    cluster:
      validationPolicy:
        name: Unrestricted
    workload:
      stargateQUICInsecure: true
```

Use `replicaCount: 1` for local or single-node test clusters. Increase
replica counts for shared or production environments.

The request router uses `power-of-two` when no load-balancer configuration is
set. Before a function selects a different routing method, configure the
effective model algorithm or a request override.

If you mirror images to a registry that does not use the stack's default
`global.image.registry` and `global.image.repository`, override the
`pylon` sidecar image passed to generated LLM workers:

```yaml
api:
  env:
    NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE: <registry>/<repository>/pylon:0.2.1
```

The LLM API Gateway and request router images are resolved from the same stack
artifact registry settings as the other control plane services.

## Request Router Worker Address

LLM worker pods learn where the request router is from the NVCF API, which sends
the address down as the `LLM_REQUEST_ROUTER_ADDRESS` worker environment
variable. The API reads it from the `nvcf.llm-request-router.worker-address`
remote-config field. NVCA carries no built-in default, so an empty field fails
launch-spec translation and no worker pod is created:

```text
terminal error: LLM request router address is not set
(LLM_REQUEST_ROUTER_ADDRESS env or STARGATE_ADDRESS legacy env)
```

Setting `addons.llm.enabled: true` points the field at the in-cluster request
router service, `llm-request-router.nvcf.svc.cluster.local:50071`.
Single-cluster installs need no further configuration.

Check the value the stack rendered after applying:

```bash
kubectl get cm nvcf-api-remote-config -n nvcf \
  -o jsonpath='{.data.nvcf-api\.yaml}' | grep worker-address
```

## External Compute Planes

Compute planes outside the control plane cluster cannot resolve
`llm-request-router.nvcf.svc.cluster.local`. Exposing the router takes three
pieces, and all three are needed.

Workers use two channels. Pylon opens a gRPC control channel on the router's
TCP port, registers, and receives a tunnel target. It then opens a QUIC reverse
tunnel on the router's UDP port. Both channels have to reach the router.

### 1. Routes

The gRPC hop is a GRPCRoute on the shared Gateway, matched by hostname, so it
needs no new listener. The QUIC hop is a UDPRoute and does need a UDP listener
on a Gateway, which the stack does not create.

```yaml
ingress:
  gatewayApi:
    gateways:
      llmRequestRouterQuic:
        name: <gateway-name>
        namespace: <gateway-namespace>
        listenerName: llm-router-quic
    routes:
      llmRequestRouter:
        enabled: true
        hostnames:
          - "llm-router.<external-domain>"
```

This renders a GRPCRoute to router port 50071, a UDPRoute to router port 50072,
and a ReferenceGrant for the cross-namespace Service. The routes stay off unless
`addons.llm.enabled` is also true, because without the addon there is no router
Service to reference.

`hostnames` defaults to `llm-router.<domain>`. It must match what the router
advertises in step 2, because pylon sends the advertised host as the request
authority and the GRPCRoute matches on it.

### 2. Router dial and identity settings

Tell the router which addresses to hand to pylon:

```yaml
addons:
  llm:
    requestRouter:
      external:
        grpcDialAddress: "<gateway-host>:<tcp-port>"
        quicDialAddress: "<gateway-host>:<udp-port>"
        advertisedHostnameTemplate: "{pod_name}.<external-domain>"
```

`grpcDialAddress` and `quicDialAddress` are what pylon connects to.
`advertisedHostnameTemplate` is a separate thing: it is the per-pod identity the
router advertises as the gRPC authority and the QUIC SNI. It supports
`{pod_name}` and `{namespace}`.

### 3. Worker address

Point workers at the same gRPC address the Gateway exposes:

```yaml
global:
  workerEndpoints:
    llmRequestRouterAddress: "<gateway-host>:<tcp-port>"
```

### Replica count

At `replicaCount: 1` a plain L4 forward is correct, because every connection
reaches the only router pod.

Above 1, the router hands each pylon a per-pod identity and expects the return
path to honor it. The GRPCRoute can carry that for the control channel, since it
matches on the authority pylon sends: give each pod its own hostname through
`advertisedHostnameTemplate` and add a route per hostname pointing at a per-pod
Service.

The QUIC hop has no equivalent. A UDPRoute forwards on port alone, so the
reverse tunnel can land on a router pod that does not own the tunnel target and
will not establish. Multi-replica external therefore needs a front end that
routes QUIC on SNI. Keep external installs at one replica otherwise.

## Local Plaintext Transport

Local development clusters commonly run the LLM API Gateway to NVCF API gRPC
hop and the worker `pylon` sidecar to request-router QUIC tunnel without TLS.
In that case, add both plaintext controls.

The complete Helmfile example above includes these settings. If you already
have an LLM block, include these plaintext-specific fields:

```yaml
addons:
  llm:
    enabled: true
    gateway:
      replicaCount: 1
      auth:
        grpcInsecure: true
    requestRouter:
      replicaCount: 1

agentConfig:
  mergeConfig: |
    workload:
      stargateQUICInsecure: true
```

`addons.llm.gateway.auth.grpcInsecure: true` configures the LLM API Gateway to
talk to the NVCF API over plaintext gRPC.

`workload.stargateQUICInsecure: true` configures generated LLM workers to pass
the plaintext QUIC setting to the `pylon` sidecar.

<Warning>
Use these plaintext settings only for local or isolated test clusters.
Production environments should use TLS-capable service configuration instead.

</Warning>

## Apply and Verify

Apply the updated control plane environment before creating LLM functions:

```bash
cd path/to/nvcf-self-managed-stack
make apply HELMFILE_ENV=<environment-name>
```

Apply or refresh the compute-plane stack for each registered GPU cluster so the
NVCA operator receives `agentConfig.mergeConfig`:

```bash
make -C deploy/stacks/nvcf-compute-plane install \
  HELMFILE_ENV=<environment-name> \
  CLUSTER_NAME=<cluster-name>
```

Existing LLM function pods keep their current sidecar arguments. Recreate or
redeploy those functions after refreshing the compute plane so new pods get the
updated worker transport settings.

Verify the LLM control plane components:

```bash
kubectl get deployment -n nvcf llm-api-gateway
kubectl get statefulset -n nvcf llm-request-router
kubectl get pods -n nvcf | grep -E 'llm-api-gateway|llm-request-router'
kubectl get httproute -A | grep llm
```

After deploying an LLM function, verify the worker sidecar:

```bash
kubectl get pods -n nvcf-backend -L FUNCTION_ID
kubectl -n nvcf-backend get pod <function-pod> \
  -o jsonpath='{range .spec.containers[*]}{.name}{"\t"}{.image}{"\n"}{end}'
kubectl -n nvcf-backend get pod <function-pod> \
  -o jsonpath='{range .spec.containers[?(@.name=="llm-worker")].args[*]}{.}{"\n"}{end}'
```

The function pod should include an `llm-worker` container using `pylon`. For
local plaintext clusters, the `llm-worker` args should include
`--quic-insecure`.

## Troubleshooting

A deploy that reports `ERROR` with no worker pod in `nvcf-backend` usually means
launch-spec translation failed. Check the NVCA agent log and the `ICMSRequest`
custom resource:

```bash
kubectl logs -n nvca-system deploy/nvca --tail=100 | grep -i "terminal error"
kubectl get icmsrequest -n nvcf-backend \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.requestStatus}{"\t"}{.status.lastReconcileError}{"\n"}{end}'
```

`LLM request router address is not set` means the API sent no router address to
the worker. See [Request Router Worker Address](#request-router-worker-address).

`404 no_eligible_candidates` from `llm.invocation.<domain>` means the request
reached the LLM Gateway, but the requested function or model was unknown or was
not registered on the selected request router. Similar `503` candidate errors
mean the router knows the target but has no active eligible backend. Check:

- The LLM function is deployed and its pod is `Running`.
- The request `model` value uses `<function-id>/<model-name>`.
- The function's `models[].name` matches the model suffix in the request.
- `models[].llmConfig.uris` includes the invoked path.
- `addons.llm.requestRouter.loadBalancer.config` includes the algorithm selected
  by the function's `models[].llmConfig.routingMethod`.
- The `llm-worker` sidecar connected to `llm-request-router`.
- Local clusters using plaintext transport include both `grpcInsecure` and
  `stargateQUICInsecure`.

Useful logs:

```bash
kubectl logs -n nvcf deploy/llm-api-gateway --tail=100
kubectl logs -n nvcf statefulset/llm-request-router \
  --all-pods=true --tail=100
kubectl logs -n nvcf-backend <function-pod> -c llm-worker --tail=100
```

In healthy routing, the request router logs show a reverse tunnel connection
from the worker and at least one routing candidate for the requested function.
