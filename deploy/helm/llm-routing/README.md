# Run models through a shared gateway

Install shared routing once, then install each model recipe with Helm and a small values file. Each model has its own release and persistent cache. Applications select a model through the same gateway address and caller credential.

## Before you start

Use Kubernetes with the NVIDIA device plugin, a GPU RuntimeClass and persistent storage on compatible model nodes. Install Helm, kubectl and curl on your workstation. Select available nodes using the hardware profiles in the [common recipe catalog](recipes/index.json). Profiles specify node count, GPUs per node, resource requests and workload limits.

Obtain the local chart packages and a `shared.values.yaml` with application image references for your node architecture. These examples use `/tmp/llm-chart-packages` for packages and `/tmp/llm-images` for private site values. During development, [build and preload the images and package the charts](recipes/BUILDING.md). The image preparation command generates this values file. Published image references can use the same Helm installation flow.

Run these examples from `deploy/helm/llm-routing`:

```bash
export LLM_ROUTING_CONTEXT=YOUR_CONTEXT
export LLM_CHARTS=/tmp/llm-chart-packages
```

## 1. Install shared infrastructure

```bash
helm upgrade --install llm-stack "$LLM_CHARTS/llm-shared-stack-0.1.0.tgz" \
  --kube-context "$LLM_ROUTING_CONTEXT" --namespace llm-stack --create-namespace \
  --values /tmp/llm-images/shared.values.yaml --wait --timeout 10m
```

This installs the gateway, router and namespace-scoped operator with an empty model registry. The chart generates the caller credential and TLS material on first installation and preserves them on upgrades. `operator.watchNamespaces` in the values file must contain `llm-stack`. For the first operator in a cluster, set `operator.installCRDs: true`. Other installations reuse that CRD.

## 2. Install a model

Create `/tmp/llm-images/qwen.values.yaml` using an available node and your cluster's storage and runtime class names:

```yaml
recipe: qwen3.8-27b
nodes: [gpu-node-1]
runtimeClassName: nvidia
storageClassName: local-path
sharedCAConfigMap: llm-gateway-stack-ca
```

Install the FP8 recipe:

```bash
helm upgrade --install qwen-fp8 "$LLM_CHARTS/pylon-sglang-recipe-0.2.0.tgz" \
  --kube-context "$LLM_ROUTING_CONTEXT" --namespace llm-stack \
  --values /tmp/llm-images/qwen.values.yaml --wait --timeout 120m
kubectl --context "$LLM_ROUTING_CONTEXT" -n llm-stack wait \
  --for=condition=Registered inferenceendpoint/qwen-fp8 --timeout=5m
```

Kubernetes checks placement, qualifies the GPU, prepares the pinned model cache and starts serving. First startup includes the model download. Subsequent startups validate and reuse the cache. Readiness gates healthy serving through the shared gateway. See [retained caches and recovery](ADVANCED.md#helm-cache-reuse-and-recovery) for an existing download.

For a second precision on another available node, use the same values file with two overrides:

```bash
helm upgrade --install qwen-nvfp4 "$LLM_CHARTS/pylon-sglang-recipe-0.2.0.tgz" \
  --kube-context "$LLM_ROUTING_CONTEXT" --namespace llm-stack \
  --values /tmp/llm-images/qwen.values.yaml --set recipe=qwen3.8-27b-nvfp4 --set 'nodes[0]=gpu-node-2' \
  --wait --timeout 120m
```

| Recipe | Precision | Availability and validation |
| --- | --- | --- |
| Qwen3.8-27B | FP8 | One GB10. Cached Helm startup and inference tested. |
| Qwen3.8-27B | NVIDIA NVFP4 | One GB10. Runtime smoke tested. Automatic Helm validation pending. |
| GLM-5.3 | UD-IQ2_M | Two GB10s. Combined runtime tested. [Automatic Helm startup](ADVANCED.md#helm-glm-recipe) stopped at the host-memory guard. |
| Qwen3.8-Flash-Next | NVFP4 | One-node offload and two-node profiles. Live validation pending. [Advanced phased deployment](ADVANCED.md#qwen-catalog-configuration). |
| Nemotron 5 Nano 12B | Unverified | Unavailable. Exact public model artifact not verified. |
| Nemotron 5 Super 49B | Unverified | Unavailable. Exact public model artifact not verified. |
| Qwen3.8-4B | Unverified | Unavailable. Exact public model artifact not verified. |
| DeepSeek V4 Flash | Upstream mixed FP4/FP8 | Planned. Public checkpoint and upstream four-GPU candidates recorded. Chart support and hardware qualification pending. |

The catalog covers seven model families and eight entries, including two Qwen3.8-27B precisions. Deploy entries with `availability.deployable: true`. Planned and unavailable entries have no deployment profiles. Their dated primary sources and upstream candidates are recorded in the catalog.

The listed tests used short prompts. The catalog records validation for each hardware profile and workload separately. Additional hardware profiles use the same installation interface after qualification.

## 3. Discover and call models

Forward the gateway in one terminal:

```bash
kubectl --context "$LLM_ROUTING_CONTEXT" -n llm-stack port-forward svc/llm-api-gateway 18443:8080
```

In another terminal with the same context variable, retrieve its CA and caller key, then list models:

```bash
kubectl --context "$LLM_ROUTING_CONTEXT" -n llm-stack get configmap llm-gateway-stack-ca \
  -o 'jsonpath={.data.ca\.crt}' > /tmp/llm-images/gateway-ca.crt
API_KEY=$(kubectl --context "$LLM_ROUTING_CONTEXT" -n llm-stack get secret llm-shared-caller-key \
  -o 'go-template={{index .data "api-key" | base64decode}}')
curl --fail-with-body --cacert /tmp/llm-images/gateway-ca.crt -H "Authorization: Bearer $API_KEY" \
  https://localhost:18443/v1/models
```

Send a chat request:

```bash
curl --fail-with-body --cacert /tmp/llm-images/gateway-ca.crt -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' https://localhost:18443/v1/chat/completions \
  -d '{"model":"qwen3.8-27b","messages":[{"role":"user","content":"What is 17 multiplied by 19? Give one short sentence."}]}'
```

Change `model` to `qwen3.8-27b-nvfp4` to call the other precision. Add `"stream":true` for streaming.

[Advanced deployment and configuration](ADVANCED.md) covers credentials, recovery, GLM, monitoring and the existing Python workflows. The common catalog is ready for API consumers. The [recipe API and UI integration](https://github.com/NVIDIA/nvcf/issues/2337) is tracked separately.
