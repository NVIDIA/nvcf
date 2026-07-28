# LLM Request Router Load Balancing

Use this guide to deploy and validate Stargate load-balancer configuration in
self-managed NVCF. For the complete `lb-config.json` schema, algorithm
behavior, defaults, and tuning fields, see the
[Stargate load balancer configuration](https://github.com/NVIDIA/nvcf/blob/main/src/libraries/rust/stargate/docs/load-balancer-configuration.md).

This guide covers NVCF deployment ownership and trusted request metadata. It
does not redefine the Stargate schema.

## Configure the self-managed stack

Set the request-router configuration in the Helmfile environment:

```yaml
addons:
  llm:
    enabled: true
    requestRouter:
      loadBalancer:
        config: |
          {
            "default": "power-of-two",
            "request_algorithms": {
              "power-of-two": "power-of-two",
              "round-robin": "round-robin",
              "random": "random",
              "groq-multiregion": "groq-multiregion",
              "pulsar": "pulsar"
            }
          }
```

The self-managed stack passes
`addons.llm.requestRouter.loadBalancer` to the request-router chart as
`llmRequestRouter.loadBalancer`.

The chart supports two configuration sources:

| Value | Owner and behavior |
| --- | --- |
| `loadBalancer.config` | The chart creates the `llm-request-router-lb` ConfigMap, stores the JSON under `lb-config.json`, mounts it read-only at `/etc/llm-request-router`, and passes `--lb-config-path=/etc/llm-request-router/lb-config.json`. |
| `loadBalancer.configPath` | The chart only passes `--lb-config-path=<path>`. The operator must add and maintain the file mount by another mechanism. |

Inline `config` takes precedence when both values are set. When neither value
is set, Stargate uses its built-in `power-of-two` default.

Stargate reads and validates the file only during process startup. The
StatefulSet does not include a load-balancer ConfigMap checksum in its pod
template. After a ConfigMap-only update, restart the StatefulSet so every
replica loads the same configuration.

## Match function routing methods

The LLM API Gateway gets the routing method from the authenticated function
model specification and sends it to Stargate as `x-routing-method`. The
current public `llmConfig.routingMethod` values are:

- `round_robin`
- `power_of_two`
- `groq_multiregion`
- `pulsar`
- `random`

Stargate normalizes underscores to hyphens. Each non-default method that a
function can select must therefore have a matching entry in
`request_algorithms`. For example, `groq_multiregion` requires the
`groq-multiregion` entry.

Stargate also implements `pulsar-multiregion`, but the current public function
model API does not accept `pulsar_multiregion` as `llmConfig.routingMethod`.
Use it only as the static `default` or a model-specific algorithm until the
public model API adds that request override.

## Keep router headers trusted

The LLM API Gateway resolves router-facing headers from authenticated function
metadata and the parsed request:

| Header | NVCF source |
| --- | --- |
| `x-request-id` | Gateway request context. |
| `x-routing-key` | Authorized function routing key. |
| `x-model` | Authorized model name. |
| `x-routing-method` | Function model `llmConfig.routingMethod`. |
| `x-input-tokens` | Gateway input-token estimate. |
| `x-cache-affinity-key` | Gateway session-affinity derivation. |
| `x-priority` | Gateway-owned priority when one is resolved. |

The chat-completions provider creates a new outbound request from gateway
context. Native proxy paths clone inbound headers and overwrite the routing
headers for which the gateway resolved a value. They always remove
caller-supplied `x-priority`, but do not unconditionally remove every other
router-facing header.

Strip all router-facing headers in the trusted ingress or gateway boundary
before accepting traffic from public callers. Clients that need session
stickiness should send only `x-multi-turn-session-id`. The gateway derives
`x-cache-affinity-key` from supported session inputs when affinity applies.

Stargate returns HTTP `400` for a blank, unknown, or configured-but-unavailable
`x-routing-method`. It also returns HTTP `400` when a required router header is
missing or a numeric header is invalid.

## Apply and roll out

Render the chart before applying it:

```bash
helm template llm-request-router \
  deploy/helm/llm-request-router/llm-request-router \
  --values <request-router-values.yaml>
```

Confirm that the rendered StatefulSet has the expected
`--lb-config-path` argument and that inline JSON creates one ConfigMap with the
`lb-config.json` key.

Apply the self-managed environment:

```bash
make apply HELMFILE_ENV=<environment-name>
```

For an inline configuration, inspect the live file and start argument:

```bash
kubectl get configmap -n nvcf llm-request-router-lb \
  -o jsonpath='{.data.lb-config\.json}'
kubectl get statefulset -n nvcf llm-request-router \
  -o jsonpath='{.spec.template.spec.containers[0].args}'
```

Restart after a ConfigMap-only change, then wait for all replicas:

```bash
kubectl rollout restart statefulset/llm-request-router -n nvcf
kubectl rollout status statefulset/llm-request-router -n nvcf
kubectl get pods -n nvcf -l app.kubernetes.io/name=llm-request-router
```

Do not continue if replicas use different seeds or configuration revisions.
Stable affinity requires the same seed and candidate view across replicas.

## Validate routing

Use a deployed function whose `routingMethod` is allowlisted in
`request_algorithms`.

1. Invoke without changing the function routing method and confirm success.
2. Update the function to another allowlisted method and confirm success.
3. Try a method that is valid in the function API but absent from
   `request_algorithms`; confirm that Stargate returns HTTP `400`.
4. For an affinity-aware method, repeat a supported multi-turn request with
   the returned `x-multi-turn-session-id`.
5. Exercise a failed or saturated backend and confirm selection and retry
   counters change.

The native Stargate Bazel test parses and constructs every JSON example in its
configuration guide:

```bash
cd src/libraries/rust/stargate
bazel test //crates/stargate:stargate_test \
  --test_filter=published_load_balancer_configuration_examples_parse
```

## Observe the request router

The chart exposes `llm-request-router:9090/metrics` when request-router metrics
are enabled. The current chart passes `--metrics-port` and uses Stargate's
default `stargate_` metric prefix.

Start with these counters and histograms:

- `stargate_routing_selections_total`
- `stargate_routing_kv_free_token_fallback_selections_total`
- `stargate_proxy_attempts_total`
- `stargate_proxy_retries_total`
- `stargate_proxy_retry_exhausted_total`
- `stargate_admission_rejections_total`
- `stargate_proxy_duration_seconds`
- `stargate_active_inference_servers`

See [LLM Request Router Metrics](./metrics/llm-request-router/metrics.md) for
labels and scrape configuration.

## Troubleshoot

| Symptom | Check |
| --- | --- |
| Pod does not start after a configuration change | Inspect request-router logs for file read, JSON parse, unknown field, or algorithm factory errors. |
| HTTP `400` before backend selection | Compare the function `routingMethod` with `request_algorithms`. Check required and numeric gateway headers. |
| HTTP `400` for affinity-aware routing | Confirm the gateway generated a nonblank affinity key when `require_cache_affinity_key` is enabled. |
| HTTP `503` with no eligible candidates | Confirm pylons are registered and publish the capacity, queue, and optional KV-cache statistics required by the algorithm. |
| New ConfigMap value has no effect | Confirm the pod creation time. Restart the StatefulSet because Stargate does not reload the file. |
| Unexpected fallback or retries | Compare routing selection, proxy attempt, retry, and retry-exhaustion metrics. Check pylon retry reasons. |
| Affinity differs between replicas | Compare the mounted JSON, seed values, and registered candidate set on every replica. |

Use these logs together:

```bash
kubectl logs -n nvcf statefulset/llm-request-router --tail=100
kubectl logs -n nvcf deploy/llm-api-gateway --tail=100
kubectl logs -n nvcf-backend <function-pod> -c llm-worker --tail=100
```
