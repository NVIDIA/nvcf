# Task Helm Chart BYOO Sample

Helm chart that deploys the [Task BYOO Sample](../task-byoo-sample/) container as a Kubernetes Job with BYO Observability enabled. Use it to validate that a self-hosted NVCF cluster can launch a BYOO task from a Helm artifact and route telemetry to your `otlp` collector.

## Prerequisites

Build and push the `task-byoo-sample` container to an OCI registry your self-hosted NVCF cluster can access. See [task-byoo-sample/README.md](../task-byoo-sample/README.md) for the build, push, and BYOO configuration flow.

## Package and upload the chart

```bash
helm package task-helmchart-byoo/
```

Push the resulting `task-helmchart-byoo-<version>.tgz` to a chart registry your cluster can pull from and register Helm pull credentials with `nvcf-cli registry add --artifact-type HELM`.

## Launch on self-hosted NVCF

Resolve the cluster gateway and generate an invocation API key via `nvcf-cli`:

```bash
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
export NVCF_API_KEY=$(nvcf-cli api-key generate --description "task-helmchart-byoo-sample" --json | jq -r .apiKey)
export ORG_ID=<your-org-id>
```

Submit the task with a Helm chart reference through the NVCT API. NVCF populates the `OTEL_EXPORTER_OTLP_*` environment variables from the BYOO telemetry registration attached to the deployment.

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/v2/orgs/${ORG_ID}/nvct/tasks" \
  --header "Host: api.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${NVCF_API_KEY}" \
  --header "Content-Type: application/json" \
  --data '{
    "name": "task-helmchart-byoo-sample",
    "helmChart": "<your-registry>/<namespace>/task-helmchart-byoo:<version>",
    "gpuSpecification": {
      "gpu": "T10",
      "instanceType": "g6.full",
      "backend": "GFN"
    },
    "maxRuntimeDuration": "PT1H",
    "maxQueuedDuration": "PT2H",
    "terminationGracePeriodDuration": "PT15M",
    "resultHandlingStrategy": "NONE"
  }'
```

## Local smoke test

To deploy the chart directly against a local Kubernetes cluster without NVCF:

```bash
kubectl create secret docker-registry <image-pull-secret-name> \
  --docker-server=<your-registry> \
  --docker-username=<user> \
  --docker-password=<pass>

helm install <release-name> task-helmchart-byoo/ \
  --set ngcImagePullSecretName=<image-pull-secret-name>
```
