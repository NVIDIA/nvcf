# Multi-Node Helm Task Test

Helm chart that runs a multi-node NCCL and GPU bandwidth test as an NVCT task. Use it to validate multi-node scheduling, inter-pod networking, and SSH bootstrap on a self-hosted NVCF cluster.

Tests come from [NVIDIA/nccl-tests](https://github.com/NVIDIA/nccl-tests/tree/v2.13.9). Kubernetes 1.28 or newer is required.

## Prerequisites

Build and push the runner container, then package and upload the Helm chart. Register pull credentials for both artifacts with `nvcf-cli registry add`.

```bash
cd container
docker buildx build --platform linux/amd64,linux/arm64 -t multi-node-helm-task-test-runner .
cd ..
helm package multi-node-task-test/
```

Push the image and chart to registries your cluster can access, then register credentials:

```bash
nvcf-cli registry add --hostname <your-registry> \
  --username <user> --password <pass> --artifact-type CONTAINER
nvcf-cli registry add --hostname <your-chart-registry> \
  --username <user> --password <pass> --artifact-type HELM
```

## Launch on self-hosted NVCF

Resolve the cluster gateway and generate an invocation API key via `nvcf-cli`:

```bash
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
export NVCF_API_KEY=$(nvcf-cli api-key generate --description "multi-node-helm-task-test" --json | jq -r .apiKey)
export ORG_ID=<your-org-id>
```

The chart accepts GPU and node count via values. See [`gb200-override.yaml`](gb200-override.yaml) for a reference configuration that targets GB200 with four GPUs per node and two nodes per instance.

Submit the task with the chart reference and override values through the NVCT API:

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/v2/orgs/${ORG_ID}/nvct/tasks" \
  --header "Host: api.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${NVCF_API_KEY}" \
  --header "Content-Type: application/json" \
  --data '{
    "name": "multi-node-helm-task-test",
    "helmChart": "<your-chart-registry>/<namespace>/multi-node-task-test:0.1.0",
    "gpuSpecification": {
      "gpu": "GB200",
      "instanceType": "AWS.GPU.GB200_4x.x2",
      "backend": "AWS"
    },
    "configuration": {
      "gpusPerNode": 4,
      "nodesPerInstance": 2,
      "image": {
        "repository": "<your-registry>/<namespace>/multi-node-helm-task-test-runner",
        "pullPolicy": "Always",
        "tag": "<version>"
      }
    },
    "maxRuntimeDuration": "PT1H",
    "maxQueuedDuration": "PT1H",
    "terminationGracePeriodDuration": "PT1H",
    "resultHandlingStrategy": "NONE"
  }'
```

The script at [`test_update_and_deploy.sh`](test_update_and_deploy.sh) shows an equivalent flow that packages, pushes, and submits the task from a shell. Update the org, registry, and GPU specification at the top of the script before running.
