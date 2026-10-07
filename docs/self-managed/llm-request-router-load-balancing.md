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
              "round-robin": "round-robin"
            },
            "models": {
              "model-a": {
                "algorithm": "wait-and-widen",
                "cache_affinity_backend_selection_count": 2,
                "cache_affinity_wait_ms": 200
              }
            }
          }
```

The example uses these settings in the `model-a` configuration:

| WaitAndWiden model field | Default | Effect |
| --- | --- | --- |
| `cache_affinity_backend_selection_count` | unset | Number of clusters in the affine group. A positive value enables the group. |
| `cache_affinity_wait_ms` | `0` | Minimum time in milliseconds from request arrival before global buckets become eligible. |

Replace `model-a` with the exact routed model name. For requests with an
affinity key, this example keeps global buckets closed for 200 ms. Stargate can
select an available affine candidate immediately. Set `cache_affinity_wait_ms`
in the model configuration.

After the wait, global buckets include all backends, including the affinity
group. Global selection uses full prefill cost for every backend and retains
the queue-admission, capacity, and retry-exclusion checks. Stargate still tries
affinity selection first on each routing attempt.

When an engine reports a positive concurrency limit and has a free request
slot, Stargate estimates zero queue delay. It counts prefill, decode, and
pending assignments against that limit. The request's own prefill time still
contributes to estimated TTFT. Pylon uses the same rule for its local queue
admission check. Engines with an unknown limit retain the existing queue-delay
estimate.

The self-managed stack passes
`addons.llm.requestRouter.loadBalancer` to the request-router chart as
`llmRequestRouter.loadBalancer`.

The chart supports two configuration sources:

| Value | Owner and behavior |
| --- | --- |
| `loadBalancer.config` | The chart creates the `llm-request-router-lb` ConfigMap, stores the JSON under `lb-config.json`, mounts it read-only at `/etc/llm-request-router`, and passes `--lb-config-path=/etc/llm-request-router/lb-config.json`. |
| `loadBalancer.configPath` | The chart only passes `--lb-config-path=<path>`. The operator must add and maintain the file mount by another mechanism. |

Inline `config` takes precedence when both values are set. When neither value
is set, Stargate uses its built-in `power-of-two` default and accepts a
routing-method override when it is in the allowlist of built-in algorithms.

Stargate reads and validates the file only during process startup. The
request-router workload does not include a load-balancer ConfigMap checksum in
its pod template. New installations default to a Deployment; existing
installations can pin a StatefulSet. After a ConfigMap-only update, restart the
selected workload so every replica loads the same configuration.

Existing StatefulSet installations must set
`addons.llm.requestRouter.workload.kind=StatefulSet` before upgrading. Changing
the workload kind is a controlled migration, not an in-place Kubernetes kind
mutation. A plain Helm upgrade across workload kinds can temporarily run both
the Deployment and StatefulSet; use the chart migration procedure to remove or
rename the old workload and verify that only the selected kind owns the router
Pods before scaling it.

## Distinguish router algorithms from nvcf-cli routing methods

Algorithm availability is enforced at separate layers:

| Layer | Input contract |
| --- | --- |
| `lb-config.json` | Canonical Stargate algorithm names: `power-of-two`, `wait-and-widen`, `round-robin`, `random`, `pulsar`, and `pulsar-wait-and-widen`. Legacy `groq-multiregion` and `pulsar-multiregion` aliases remain accepted for existing deployments. |
| Function model `llmConfig.routingMethod` | The same algorithm names, with underscores accepted in place of hyphens, optionally followed by tuning parameters. Legacy aliases remain accepted for existing functions. The function API checks only the format. |
| LLM API Gateway | Removes any caller-supplied `x-routing-method`, then forwards the nonblank routing method from authenticated model metadata, trimmed, without algorithm validation. |
| Stargate `x-routing-method` | Case-insensitive algorithm name with hyphens or underscores, optionally followed by tuning parameters. The algorithm must match the effective algorithm or a model or top-level `request_algorithms` entry. Otherwise, Stargate returns HTTP `400`. |

For example, when a configuration is set and `power-of-two` is the effective
algorithm, `wait_and_widen` requires a `wait-and-widen` entry in
`request_algorithms`. The legacy `groq_multiregion` value remains accepted
and resolves to the same algorithm.

Use `wait-and-widen` and `pulsar-wait-and-widen` in new function metadata,
`lb-config.json` files, request-algorithm maps, and deployment manifests.
Existing `groq-multiregion` and `pulsar-multiregion` values continue to work
through the Stargate and control-plane compatibility aliases.

## Tune a model with a routing expression

A function owner can tune the selected algorithm for one model. Set
`llmConfig.routingMethod` to the algorithm name followed by `;name=value`
parameters:

```text
pulsar;seed=stable-a;consider_kv_free_tokens=true
wait-and-widen;n=3;max_queue_time_floor_ms=200;max_queue_time_ceil_ms=2000
```

The parameters override the router configuration of that algorithm for that
model only. Omitted parameters keep the `lb-config.json` values: the model
entry when its algorithm matches, else the `request_algorithms` entry, else
the default. The algorithm must be available to the model, as for a value
without parameters. A value without parameters, such as `pulsar`, routes as
before.

With `nvcf-cli`, put the whole model spec in single quotes. Without quotes,
the shell ends the command at the first `;` and the CLI sends the algorithm
name without its parameters:

```sh
nvcf-cli function update --function-id=<function-id> --version-id=<version-id> \
  --llm-model-update='name=my-model,routingMethod=pulsar;seed=stable-a'
```

In an update JSON file, set the same value under `modelUpdates[].llmConfig`:

```json
{
  "modelUpdates": [
    {
      "modelName": "my-model",
      "llmConfig": { "routingMethod": "pulsar;seed=stable-a" }
    }
  ]
}
```

### Format

The value is an RFC 8941 Item with Parameters, with these rules:

- At most 1024 bytes and 32 parameters.
- No commas anywhere.
- Each parameter is `name=value`. A name starts with a lowercase letter, uses
  lowercase letters, digits, and underscores, and appears once.
- A space is allowed after `;`, not around `=`.
- A value is an integer, a decimal with at most three fractional digits, a
  token, or a quoted string. Write booleans as `true` or `false`, not `?1` or
  `?0`. A semicolon is not allowed inside a quoted string.
- A quoted string is read as the parameter type. Quote a number that needs more
  than three fractional digits, such as `next_bucket_unlock_factor="0.0625"`.
  A quoted number must be a plain decimal number, without an exponent, `inf`,
  or `NaN`.

The function API checks this format on create and update and returns HTTP
`400` for a malformed value. It stores an accepted value as written, apart from
leading and trailing spaces. The router checks parameter names and values on
each request.

### Parameters

Each parameter overrides the `lb-config.json` field with the same name. For
field behavior and built-in defaults, see the
[Stargate load balancer configuration](https://github.com/NVIDIA/nvcf/blob/main/src/libraries/rust/stargate/docs/load-balancer-configuration.md).

| Parameter | Type | Algorithms | Accepted values |
| --- | --- | --- | --- |
| `require_cache_affinity_key` | boolean | all | `true`, `false` |
| `require_input_tokens` | boolean | all | `true`, `false` |
| `max_input_work_seconds` | number | all | Greater than 0 |
| `sample_count` | integer | `power-of-two` | 1 to 64 |
| `comparator` | token | `power-of-two`, `wait-and-widen` | `ttft`, `queue-time`, `input-work-seconds`, `utilization`, `num-requests-queued` |
| `seed` | token or string | `pulsar`, `wait-and-widen`, `pulsar-wait-and-widen` | Any |
| `consider_kv_free_tokens` | boolean | `pulsar`, `pulsar-wait-and-widen` | `true`, `false` |
| `n` | integer | `wait-and-widen`, `pulsar-wait-and-widen` | Greater than 0 |
| `max_queued` | integer | `wait-and-widen`, `pulsar-wait-and-widen` | 0 or greater |
| `max_queue_time_floor_ms` | integer | `wait-and-widen`, `pulsar-wait-and-widen` | 0 or greater. The effective configuration must also set `max_queue_time_ceil_ms`. |
| `max_queue_time_ceil_ms` | integer | `wait-and-widen`, `pulsar-wait-and-widen` | 0 or greater. The effective configuration must also set `max_queue_time_floor_ms`. |
| `ttft_bucket_size_ms` | integer | `wait-and-widen`, `pulsar-wait-and-widen` | Greater than 0 |
| `next_bucket_unlock_factor` | number | `wait-and-widen`, `pulsar-wait-and-widen` | Any finite number |
| `ignore_queue_time` | boolean | `wait-and-widen`, `pulsar-wait-and-widen` | `true`, `false` |
| `ignore_input_processing_time` | boolean | `wait-and-widen`, `pulsar-wait-and-widen` | `true`, `false` |
| `cache_affinity_virtual_nodes` | integer | `wait-and-widen` | 1 to 1024 |
| `cache_affinity_backend_selection_count` | integer | `wait-and-widen`, `pulsar-wait-and-widen` | Greater than 0 |
| `cache_affinity_input_tokens_scale` | number | `wait-and-widen`, `pulsar-wait-and-widen` | 0.0 to 1.0 |
| `cache_affinity_wait_ms` | integer | `wait-and-widen`, `pulsar-wait-and-widen` | 0 or greater |
| `band_widen_interval_ms` | integer | `pulsar-wait-and-widen` | 0 or greater |
| `fallback_max_queued` | integer | `pulsar-wait-and-widen` | 0 or greater |

`round-robin` and `random` accept only the parameters for all algorithms.

### Rejections

Stargate rejects an invalid value with HTTP `400` before backend selection. The
response has the body `{"error":"<message>","code":"<code>"}` and the header
`x-stargate-error-code: <code>`. The message names the rejected parameter or
value.

| Code | Cause |
| --- | --- |
| `malformed_expression` | The value breaks a format rule. |
| `unknown_method` | The algorithm name is not a Stargate algorithm, or the value is blank. |
| `unavailable` | The algorithm is not configured for the model. |
| `unknown_parameter` | The parameter name is not a tuning field. |
| `not_applicable` | The parameter does not apply to the selected algorithm. |
| `invalid_value` | The value has the wrong type or is out of range. |
| `inert_value` | The value would have no effect, such as `n=0`. |
| `inert_combination` | Only one of `max_queue_time_floor_ms` and `max_queue_time_ceil_ms` is set after the override. |

Values without parameters use the same response for `unknown_method` and
`unavailable`.

### How updates apply

- An update needs no router restart or ConfigMap change.
- The LLM API Gateway caches function metadata for up to 60 seconds. An update
  reaches the router within that time, and until then requests can carry the
  old or the new value.
- For each routing key and model, each router keeps the configurations of the
  two most recent values, compared byte for byte. Both values reuse their
  configuration and load-balancer state while an update spreads. A third value
  replaces the older one.
- A new value starts with new load-balancer state, such as Pulsar rankings or
  the round-robin position.
- An entry expires after 15 idle minutes. The router removes expired entries
  during later cache operations. Each router keeps at most 16384 entries.

### Logs and traces

- The span field `routing.requested_algorithm` carries the header value as
  received on every request.
- When the router builds or replaces a configuration, it writes the info log
  `routing expression configuration built` with the algorithm, outcome, and
  value.
- Each rejected request writes the warn log `invalid routing algorithm header`
  with the code and message.

### Routers without expression support

A request router release without routing expression support reads the whole
value as an algorithm name. It returns HTTP `400` for every value with
parameters, and values without parameters keep working. The function API
accepts parameters regardless of the router release. In that case, a model with
parameters gets HTTP `400` on every request until the router is upgraded or
the parameters are removed.

## Keep router headers trusted

The gateway can send the following headers to Stargate. Derive or validate
their values from authenticated function metadata and the request.

| Header | Gateway contract |
| --- | --- |
| `x-request-id` | Gateway request context. |
| `x-routing-key` | Authorized function routing key. |
| `x-model` | Routed model name normalized from the request. |
| `x-routing-method` | Function model `llmConfig.routingMethod`. |
| `x-input-tokens` | Gateway input-token estimate. |
| `x-cache-affinity-key` | Gateway session-affinity derivation. |
| `x-priority` | Gateway-owned priority when one is resolved. |
| `x-request-slo-ms` | Optional request SLO in milliseconds. Stargate uses it to interpolate queue-admission limits; it does not shorten the affinity wait. |
| `x-max-wait-ms` | Optional routing wait limit in milliseconds from request arrival, capped at 60000. It can expire before global buckets open. |
| `x-stargate-max-wait-ms` | Optional proxy retry budget in milliseconds. |

An explicit `x-max-wait-ms` shorter than the configured affinity wait can end
routing with HTTP `503` before global buckets become eligible. Set the
affinity wait within the latency budget for the workload.

<Warning>
The NVCF LLM invocation HTTPRoute does not strip the other router-facing
headers. Do not expose the route to untrusted callers until a managed ingress
policy removes them before the request reaches the LLM API Gateway.
</Warning>

For a separately managed Gateway API route, add this filter to the rule that
forwards to `llm-api-gateway`:

```yaml
filters:
  - type: RequestHeaderModifier
    requestHeaderModifier:
      remove:
        - x-request-id
        - x-routing-key
        - x-model
        - x-routing-method
        - x-input-tokens
        - x-cache-affinity-key
        - x-priority
        - x-request-slo-ms
        - x-max-wait-ms
        - x-stargate-max-wait-ms
```

The gateway-routes chart does not expose a value for this filter. Use an
equivalent policy at an external edge or maintain a route override. Preserve
`x-multi-turn-session-id`; clients can use it for session affinity. Chat
Completions and Responses request bodies can also supply `prompt_cache_key`.
See the
[Gateway API header modifier guide](https://gateway-api.sigs.k8s.io/guides/user-guides/http-header-modifier/)
for filter semantics.

For affinity routing, the gateway can supply `x-cache-affinity-key` as a stable
session or prefix identifier. When using `prompt_cache_key`, place its
SHA-256-derived value in the header. The request body can retain the raw value
for the model backend. Enable `require_cache_affinity_key` only when the
gateway supplies a key for every endpoint served by the model.

Stargate returns HTTP `400` for a blank, unknown, or configured-but-unavailable
`x-routing-method`, and for an invalid routing expression. The response carries
the code described in [Rejections](#rejections). It also returns HTTP `400`
when a required router header is missing or a numeric header is invalid.

## Apply and roll out

Render the chart before applying it:

```bash
helm template llm-request-router \
  deploy/helm/llm-request-router/llm-request-router \
  --values <request-router-values.yaml>
```

Confirm that the rendered request-router workload (a Deployment by default) has
the expected `--lb-config-path` argument and that inline JSON creates one
ConfigMap with the `lb-config.json` key.

If the LLM route accepts untrusted traffic, inspect the rendered or live
HTTPRoute and confirm that the trusted-header filter is present:

```bash
kubectl get httproute -n <gateway-namespace> llm-invocation -o yaml
```

Apply the self-managed environment from the unpacked stack directory:

```bash
cd path/to/nvcf-self-managed-stack
make apply HELMFILE_ENV=<environment-name>
```

For an inline configuration, inspect the live file and start argument:

```bash
kubectl get configmap -n nvcf llm-request-router-lb \
  -o jsonpath='{.data.lb-config\.json}'
kubectl get deployment -n nvcf llm-request-router \
  -o jsonpath='{.spec.template.spec.containers[0].args}'
```

Replace `deployment` with `statefulset` in these commands when
`addons.llm.requestRouter.workload.kind` is pinned to `StatefulSet`.

Restart after a ConfigMap-only change, then wait for all replicas:

```bash
kubectl rollout restart deployment/llm-request-router -n nvcf
kubectl rollout status deployment/llm-request-router -n nvcf
kubectl get pods -n nvcf -l app.kubernetes.io/name=llm-request-router
```

Use `statefulset/llm-request-router` instead when the StatefulSet workload is
explicitly selected.

Confirm every listed pod was recreated after the ConfigMap update. For each
pod, check for the `load balancer config loaded` startup log and compare its
reported default and model-override count:

```bash
kubectl logs -n nvcf <request-router-pod> \
  | grep 'load balancer config loaded'
```

Do not continue if a pod predates the update, lacks a successful load log, or
reports a different configuration summary. Stable affinity requires the same
mounted configuration, seed, and candidate view across replicas.

## Validate routing

Use a deployed function whose `routingMethod` is the effective configured
algorithm or is present in `request_algorithms`.

1. Invoke without changing the function routing method and confirm success.
2. Update the function to a method present in `request_algorithms` and confirm
   success.
3. Update the function to a method that is neither the configured algorithm
   nor present in `request_algorithms`. Invoke it and confirm that Stargate
   returns HTTP `400`.
4. Update the function to a method with a parameter that applies to it, such as
   `pulsar;seed=test` for `pulsar`. Invoke it and confirm success. Then update it
   with an unknown parameter, such as `pulsar;widen=2`. The update succeeds.
   Invoke it and confirm HTTP `400` with the `unknown_parameter` code.
5. For an affinity-aware method, repeat a supported multi-turn request with the
   same `prompt_cache_key` or the returned `x-multi-turn-session-id`.
6. Exercise a failed or saturated backend and confirm selection and retry
   counters change.

## Observe the request router

The chart exposes `llm-request-router:9090/metrics` when request-router metrics
are enabled. The current chart passes `--metrics-port` and uses Stargate's
default `stargate_` metric prefix.

See [LLM Request Router Metrics](../observability/metrics/llm-request-router/metrics.md) for
metric names, labels, and scrape configuration.

## Troubleshoot

| Symptom | Check |
| --- | --- |
| Pod does not start after a configuration change | Inspect request-router logs for file read, JSON parse, unknown field, or algorithm factory errors. |
| HTTP `400` before backend selection | Read the `code` in the response body or the `x-stargate-error-code` header; see [Rejections](#rejections). Compare the function `routingMethod` with the configured algorithm and `request_algorithms`. Check required and numeric gateway headers. |
| HTTP `400` for affinity-aware routing | Confirm the gateway generated a nonblank affinity key when `require_cache_affinity_key` is enabled. |
| HTTP `503` with no eligible candidates | Confirm pylons are registered and publish the capacity, queue, and optional KV-cache statistics required by the algorithm. |
| New ConfigMap value has no effect | Confirm the pod creation time. Restart the selected Deployment or StatefulSet because Stargate does not reload the file. |
| Unexpected fallback or retries | Compare routing selection, proxy attempt, retry, and retry-exhaustion metrics. Check pylon retry reasons. |
| Affinity differs between replicas | Compare the ConfigMap, pod creation times, startup summaries, seed values, and registered candidate set. |

Use these logs together:

```bash
kubectl logs -n nvcf deployment/llm-request-router \
  --all-pods=true --tail=100
kubectl logs -n nvcf deploy/llm-api-gateway --tail=100
kubectl logs -n nvcf-backend <function-pod> -c llm-worker --tail=100
```

Use `statefulset/llm-request-router` for the first command when the StatefulSet
workload is explicitly selected.
