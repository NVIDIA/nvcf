# LLM Function Enablement

Enable the LLM addon before creating or invoking functions with
`functionType: "LLM"` through the LLM invocation route. The addon deploys the
LLM API Gateway and LLM request router, creates the external LLM invocation
route, and configures worker pods to use the `pylon` sidecar for model-aware
routing.

For LLM function payload shape and invocation examples, see
[Function Creation](./function-creation.md) and
[LLM Gateway](./llm-gateway.md).

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
      loadBalancer:
        config: |
          {
            "default": "power-of-two",
            "request_algorithms": {
              "power-of-two": "power-of-two",
              "round-robin": "round-robin",
              "random": "random",
              "groq-multiregion": "groq-multiregion"
            }
          }

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

`addons.llm.requestRouter.loadBalancer.config` configures request-router
algorithm selection. Function model specs use underscored `routingMethod`
values such as `round_robin`, `power_of_two`, `groq_multiregion`, and
`random`. The request router configuration uses hyphenated algorithm IDs such
as `round-robin`, `power-of-two`, `groq-multiregion`, and `random`.

When a function can use a non-default `routingMethod`, include the matching
algorithm in `request_algorithms`. If the request router does not have a
matching algorithm entry, invocation can fail with HTTP `400` before a backend
is selected.

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

## Local Plaintext Transport

Local development clusters commonly run the LLM API Gateway to NVCF API gRPC
hop and the worker `pylon` sidecar to request-router QUIC tunnel without TLS.
In that case, add both plaintext controls.

The complete Helmfile example above includes these settings and the
request-router load balancer config. If you already have an LLM block, include
these plaintext-specific fields:

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
kubectl get deploy -n nvcf llm-api-gateway llm-request-router
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
kubectl logs -n nvcf deploy/llm-request-router --tail=100
kubectl logs -n nvcf-backend <function-pod> -c llm-worker --tail=100
```

In healthy routing, the request router logs show a reverse tunnel connection
from the worker and at least one routing candidate for the requested function.
