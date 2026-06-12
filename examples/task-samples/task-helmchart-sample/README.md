# Task Helm Chart Sample

Helm chart that deploys the [Task Simple Sample](../task-simple-sample/) container as a Kubernetes Job. Use it to validate that a self-hosted NVCF cluster can launch a task from a Helm artifact.

## Prerequisites

Build and push the `task-simple-sample` container to an OCI registry your self-hosted NVCF cluster can access. See [task-simple-sample/README.md](../task-simple-sample/README.md) for the build and push flow.

## Package and upload the chart

```bash
helm package task-helmchart-test/
```

Push the resulting `task-helmchart-test-<version>.tgz` to a chart registry your cluster can pull from (for example `nvcr.io`) and register Helm pull credentials with `nvcf-cli registry add --artifact-type HELM`.

## Launch on self-hosted NVCF

Resolve the cluster gateway and generate an invocation API key via `nvcf-cli`:

```bash
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
export NVCF_API_KEY=$(nvcf-cli api-key generate --description "task-helmchart-sample" --json | jq -r .apiKey)
export ORG_ID=<your-org-id>
```

Submit the task with a Helm chart reference through the NVCT API:

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/v2/orgs/${ORG_ID}/nvct/tasks" \
  --header "Host: api.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${NVCF_API_KEY}" \
  --header "Content-Type: application/json" \
  --data '{
    "name": "task-helmchart-sample",
    "helmChart": "<your-registry>/<namespace>/task-helmchart-test:<version>",
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

helm install <release-name> task-helmchart-test/ \
  --set ngcImagePullSecretName=<image-pull-secret-name>
```
