# Inference-test Sample
This sample is a Helm chart that deploys the `fastapi-echo-sample` container.

## Prerequisites

### Set up a Kubernetes cluster

To deploy the Helm chart you need a local Kubernetes cluster. One convenient option is [microk8s](https://microk8s.io/docs/getting-started). For a full self-hosted NVCF control plane on k3d, see [`tools/ncp-local-cluster/`](../../../../tools/ncp-local-cluster/).

### Build and upload the container image

For container build and publishing details, see [fastapi-echo-sample](../../fastapi-echo-sample/README.md) and [examples/README.md](../../../README.md#publishing-container-images).

## Deploying locally (plain Kubernetes)

The sample Helm chart deploys a `fastapi-echo-sample` container and an `entrypoint` service for clients to reach the container.

First, create an image pull secret for whichever registry hosts your container:

```bash
microk8s kubectl create secret docker-registry <image-pull-secret-name> \
  --docker-server=<your-registry> \
  --docker-username=<user> \
  --docker-password=<pass>
```

Then install the sample Helm chart:

```bash
microk8s helm install <release-name> /path/to/inference-test --set ngcImagePullSecretName=<image-pull-secret-name>
```

## Deploying on self-managed NVCF

Package the chart and push it to an OCI registry your cluster can reach, then register pull credentials with `nvcf-cli`:

```bash
helm package inference-test
helm push inference-test-0.1.tgz oci://<your-registry>/<namespace>

nvcf-cli registry add \
  --hostname <your-registry> \
  --username <user> \
  --password <pass> \
  --artifact-type HELM \
  --artifact-type CONTAINER
```

To opt into worker-readiness-based instance health, set
`statusByWorkerReadiness: true` in `inference-test/values.yaml`. This
renders the optional `nvcf-workload-config` ConfigMap described in
[Helm Functions](../../../../docs/user/helm-functions.md#use-worker-readiness-for-function-health).

Create a function that references the chart, then deploy it:

```bash
nvcf-cli function create \
  --name inference-test \
  --helm-chart <your-registry>/<namespace>/inference-test:0.1 \
  --helm-chart-service entrypoint \
  --inference-url /echo \
  --inference-port 8000

nvcf-cli function deploy create \
  --function-id <function-id> \
  --version-id <version-id> \
  --gpu <gpu-name> \
  --instance-type <instance-type> \
  --min-instances 1 \
  --max-instances 1
```

## Invoke the sample locally

Get the cluster IP of the `entrypoint` service:

```bash
microk8s kubectl get service entrypoint
```

Invoke the deployed container directly:

```bash
curl --request POST \
  --url <entrypoint-service-ip>:8000/echo \
  --header 'Content-Type: application/json' \
  --data '{
  "message": "hello"
}'
```

## Invoke the sample on self-hosted NVCF

Resolve the cluster gateway and generate an invocation API key via `nvcf-cli`:

```bash
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
export NVCF_API_KEY=$(nvcf-cli api-key generate --description "inference-test-sample" --json | jq -r .apiKey)
```

Call the function through the gateway, routing with the `Host` header:

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/test" \
  --header "Host: <function-id>.invocation.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${NVCF_API_KEY}" \
  --header "Content-Type: application/json" \
  --data '{
  "key": "secret-key-1"
}'
```
