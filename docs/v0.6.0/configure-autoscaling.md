# Configure Function Autoscaling

This page explains how to configure autoscaling on a deployed function using the NVCF API. For background on how the function autoscaler decides on instance counts, see the [Function Autoscaling Overview](./autoscaling/index.md). For the full schema of the request and response bodies referenced below, see the [NVCF OpenAPI specification](https://api.nvcf.nvidia.com/v3/openapi).

Use these two endpoints:

- `POST /v2/nvcf/deployments/functions/{functionId}/versions/{versionId}` sets the initial scaling bounds when you first deploy a function.
- `PATCH /v2/nvcf/deployments/{deploymentId}/gpu-specifications/{gpuSpecificationId}` updates the bounds and the autoscaling policy on an existing deployment.

All requests require an `NVCF_TOKEN` (JWT) with the `deploy_function` scope.

## Scope of Autoscaling

The function autoscaler operates per function version. It produces a single desired instance count for a given `(functionId, versionId)` pair; the GPU specification ID and deployment ID are not part of its decision. The `PATCH /v2/nvcf/deployments/.../gpu-specifications/{gpuSpecificationId}` endpoint is per-spec on the wire, but the resulting scaling decision applies at the function-version level. If a deployment has more than one GPU specification, set the autoscaling policy consistently across its specs.

## Set Scaling Bounds at Deploy Time

When creating a deployment, set `minInstances` and `maxInstances` on each entry in `deploymentSpecifications`. These are the only autoscaling fields accepted at create time.

```bash
curl -s -X POST "https://${GATEWAY_ADDR}/v2/nvcf/deployments/functions/${FUNCTION_ID}/versions/${FUNCTION_VERSION_ID}" \
  -H "Authorization: Bearer $NVCF_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "deploymentSpecifications": [{
      "gpu": "H100",
      "instanceType": "NCP.GPU.H100_1x",
      "minInstances": 1,
      "maxInstances": 4
    }]
  }'
```

Constraints:

- `minInstances` must be `>= 0`. Setting `minInstances: 0` allows the function to scale to zero when idle. The scale-to-zero idle timeout is set on the platform and is not configurable per function.
- `maxInstances` must be `> 0` and `>= minInstances`.

With no other configuration, the deployment uses the platform's default scaling policy. The platform adds and removes instances within the `[minInstances, maxInstances]` bounds based on observed utilization.

## Update Scaling Bounds on an Existing Deployment

To change the bounds after deploy, look up the `deploymentId` and the `gpuSpecificationId` for the GPU spec you want to change, then `PATCH` that spec.

Look up the IDs:

```bash
curl -s "https://${GATEWAY_ADDR}/v2/nvcf/deployments/functions/${FUNCTION_ID}/versions/${FUNCTION_VERSION_ID}" \
  -H "Authorization: Bearer $NVCF_TOKEN"
```

The response includes `deployment.deploymentId` and a `gpuSpecificationId` on each entry in `deployment.deploymentSpecifications`.

Update the bounds:

```bash
curl -s -X PATCH "https://${GATEWAY_ADDR}/v2/nvcf/deployments/${DEPLOYMENT_ID}/gpu-specifications/${GPU_SPEC_ID}" \
  -H "Authorization: Bearer $NVCF_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "minInstances": 0,
    "maxInstances": 8
  }'
```

The body must include at least one of `minInstances`, `maxInstances`, `autoscalingConfiguration`, or `autoscalingConfigurationPolicy`. You can't change `gpu`, `instanceType`, `backend`, `clusters`, `availabilityZones`, `preferredOrder`, or `maxRequestConcurrency` on an existing GPU spec.

## Customize the Autoscaling Policy

By default, every deployment uses the platform's autoscaling policy. To override it with per-function scale-up and scale-down behavior, send an `autoscalingConfiguration` block on the `PATCH` request.

```bash
curl -s -X PATCH "https://${GATEWAY_ADDR}/v2/nvcf/deployments/${DEPLOYMENT_ID}/gpu-specifications/${GPU_SPEC_ID}" \
  -H "Authorization: Bearer $NVCF_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "autoscalingConfigurationPolicy": "CUSTOM_CONFIGURATION",
    "autoscalingConfiguration": {
      "scaleUpDetails": {
        "factor": 1.5,
        "threshold": 75
      },
      "scaleDownDetails": {
        "factor": 0.5,
        "threshold": 25
      }
    }
  }'
```

`autoscalingConfigurationPolicy` accepts two values:

| Value | Effect |
|-------|--------|
| `CUSTOM_CONFIGURATION` | Apply the `autoscalingConfiguration` block on this request. |
| `PLATFORM_CONFIGURATION` | Discard any custom configuration on this GPU spec and revert to the platform defaults. |

`scaleUpDetails` and `scaleDownDetails` use the same shape:

| Field | Type | Notes |
|-------|------|-------|
| `factor` | number | Multiplier applied to the current instance count. Must be `> 1.0` on `scaleUpDetails` and `< 1.0` on `scaleDownDetails`. |
| `threshold` | integer | Utilization percent that triggers the scaling action. |
| `stickiness` | object | Optional. See below. |

## Stickiness

Stickiness reduces churn by requiring utilization to stay past the threshold for a minimum window before a scaling action fires. Add a `stickiness` block to `scaleUpDetails` or `scaleDownDetails`:

```json
{
  "autoscalingConfigurationPolicy": "CUSTOM_CONFIGURATION",
  "autoscalingConfiguration": {
    "scaleUpDetails": {
      "factor": 1.5,
      "threshold": 75,
      "stickiness": {
        "size": "PT10M",
        "threshold": "PT3M"
      }
    }
  }
}
```

Both fields are ISO-8601 durations:

- `size` is the length of the sliding window. Must be `<= PT1H`.
- `threshold` is the amount of time within the window that utilization must stay past the scaling threshold. Must be `< size`.

The threshold can be set to zero, but this would nullify the utilization metric. The scaling logic would effectively be collapsed into only scaling to/from zero/one instance(s).

The example above scales up only if utilization exceeds 75 percent for at least 3 minutes in any 10-minute window.

## Revert to Platform Defaults

To discard a custom configuration without setting a new one:

```bash
curl -s -X PATCH "https://${GATEWAY_ADDR}/v2/nvcf/deployments/${DEPLOYMENT_ID}/gpu-specifications/${GPU_SPEC_ID}" \
  -H "Authorization: Bearer $NVCF_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "autoscalingConfigurationPolicy": "PLATFORM_CONFIGURATION"
  }'
```

The GPU spec uses the platform's autoscaling policy on the next scaling cycle.

## See Also

- [Function Autoscaling Overview](./autoscaling/index.md) for what the function autoscaler does and what it depends on.
- [CLI](./cli.md) for `nvcf-cli function deploy create` and `nvcf-cli function deploy update`, which wrap the same API surface.
- [NVCF OpenAPI specification](https://api.nvcf.nvidia.com/v3/openapi) for the full request and response schema of the endpoints used on this page.
