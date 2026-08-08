# Dynamo Operator on NVCA self-hosted demo

This demo adapts an
[upstream disaggregated router example](https://github.com/ai-dynamo/dynamo/blob/v1.0.2/examples/backends/vllm/deploy/disagg_router.yaml)
for an NVCF Helm function. Dynamo uses Grove and KAI Scheduler to orchestrate
and place the workload. See
[Gang Scheduling](../../../../docs/user/cluster-management/gang-scheduling.md)
and
[Topology-Aware Scheduling](../../../../docs/user/cluster-management/topology-aware-scheduling.md)
for the production compute plane configuration.

## Prerequisites

- An NVCF self-hosted Kubernetes cluster with the Dynamo, Grove, and KAI
  Scheduler add-ons installed. For local cluster testing, see the
  [local Dynamo Operator guide](../../../../tools/ncp-local-cluster/docs/dynamo-operator.md).
- A Hugging Face [token](https://huggingface.co/docs/hub/en/security-tokens)
  when using the example Helm chart's default
  [Qwen/Qwen3-0.6B](https://huggingface.co/Qwen/Qwen3-0.6B) model.

## Deploying your Dynamo chart

1. Package the chart and push it to an OCI registry your cluster can reach, then register pull credentials with `nvcf-cli`:

    Note: Keep the DGD name at 24 characters or fewer so generated Kubernetes
    object names remain valid.

    ```console
    $ helm package dynamo-operator-test
    $ helm push dynamo-operator-test-0.1.0.tgz oci://<your-registry>/<namespace>
    $ nvcf-cli registry add \
        --hostname <your-registry> \
        --username <user> \
        --password <pass> \
        --artifact-type HELM \
        --artifact-type CONTAINER
    ```

1. Create the function, using the below payload as an example, substituting in your Helm chart's values

    Note: Set `helmChartServiceName` to `<DGD name>-frontend`. This example
    names the DGD `myllm`, so the generated service is `myllm-frontend`.

    ```console
    $ cat <<EOF > function-create.json
    {
      "name": "my-dynamo-operator-function",
      "inferenceUrl": "/v1/chat/completions",
      "inferencePort": 8000,
      "helmChartServiceName": "myllm-frontend",
      "helmChart": "oci://<your-registry>/<namespace>",
      "healthProtocol": "HTTP",
      "healthUri": "/health",
      "healthPort": 8000,
      "healthTimeout": "PT10S",
      "healthExpectedStatusCode": 200
    }
    EOF
    $ nvcf-cli function create --input-file ./function-create.json
    ```

    Save the function ID and function version ID output by this step

1. Deploy the function, using the below payload as an example, substituting in the ID's from above

    ```console
    $ cat <<EOF > function-deploy.json
    {
      "functionId": "<saved-function-id>",
      "versionId": "<saved-function-version-id>",
      "deploymentSpecifications": [
        {
          "gpu": "<gpu>",
          "instanceType": "<instance-type>",
          "backend": "nvcf-default",
          "minInstances": 1,
          "maxInstances": 1,
          "configuration": {
            "hfToken": "<YOUR HUGGINGFACE TOKEN>"
          }
        }
      ]
    }
    EOF
    $ nvcf-cli function deploy create --input-file ./function-deploy.json
    ```

1. Invoke your function using the function and version ID's saved above

    ```console
    $ nvcf-cli function invoke \
      --function-id <saved-function-id> --version-id <saved-function-version-id> \
      --request-body '{
        "model": "Qwen/Qwen3-0.6B",
        "messages": [
          {
              "role": "user",
              "content": "What is the capital of France?"
          }
        ],
        "stream": false,
        "max_tokens": 30
      }'
    ...
    ```
