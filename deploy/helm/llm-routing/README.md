# Run models through a shared gateway

Install shared routing once, then install each model recipe with Helm and a small values file. Each model has its own release and persistent cache. Applications select a model through the same gateway address and caller credential.

## Before you start

Use Kubernetes with the NVIDIA device plugin, a GPU RuntimeClass and persistent storage on compatible model nodes. Install Helm and kubectl on your workstation, plus Python 3.11+ for the model access commands. Select available nodes using the hardware profiles in the [common recipe catalog](recipes/index.json). Profiles specify node count, GPUs per node, resource requests and workload limits.

Until application images are published to a registry, each installer builds them from this checkout and preloads the cluster nodes. Start Docker with Buildx support and access to base images and build dependencies. The image helper supports ARM64 and AMD64 nodes with containerd. Its import Jobs need permission to mount the nodes' containerd sockets.

Run the following from `deploy/helm/llm-routing`, in the same terminal. Select your target kubeconfig context first. This example creates a new stack in `llm-stack`; that namespace must not already exist when building images. On a shared cluster, coordinate an unused namespace and use it consistently in the build, Helm and client commands.

## 0. Build images and package charts

```bash
export LLM_CONTEXT="$(kubectl config current-context)"
export LLM_WORK="$HOME/.local/state/llm-routing/$LLM_CONTEXT/llm-stack"
export LLM_IMAGES="$LLM_WORK/images"
export LLM_CHARTS="$LLM_WORK/charts"

python3 build-shared-images.py \
  --context "$LLM_CONTEXT" --namespace llm-stack \
  --output-dir "$LLM_IMAGES" --allow-containerd-import
bash package-charts.sh --output-dir "$LLM_CHARTS"
```

Continue only after both commands succeed. The image helper builds gateway, router, operator and Pylon, preloads every compatible node, and generates `$LLM_IMAGES/shared.values.yaml` with matching image tags, architecture and operator settings. No manual image-tag edits or another developer's saved files are needed. Keep this persistent directory outside the checkout; `/tmp` is not required.

Retries before installation reuse the saved image configuration. Chart packaging requires a fresh output directory; reuse existing successful packages instead of packaging over them. For registry images or component rebuilds, see [building and distributing images](recipes/BUILDING.md).

## 1. Install shared infrastructure

```bash
helm upgrade --install llm-stack "$LLM_CHARTS/llm-shared-stack-0.1.0.tgz" \
  --kube-context "$LLM_CONTEXT" --namespace llm-stack --create-namespace \
  --values "$LLM_IMAGES/shared.values.yaml" --wait --timeout 10m
```

Helm installs the gateway, router and namespace-scoped operator with an empty model registry. A chart verification Job checks gateway TLS, model discovery, acceptance of the caller key and rejection of an invalid key before Helm reports success. The same checks run on upgrades with registered models. The chart generates the caller credential and TLS material on first installation and preserves them on upgrades. `operator.watchNamespaces` in the values file must contain `llm-stack`. For the first operator in a cluster, set `operator.installCRDs: true`. Other installations reuse that CRD.

## 2. Install a model

Create `$LLM_IMAGES/qwen.values.yaml` using an available node and your cluster's storage and runtime class names:

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
  --kube-context "$LLM_CONTEXT" --namespace llm-stack \
  --values "$LLM_IMAGES/qwen.values.yaml" --wait --timeout 120m
kubectl --context "$LLM_CONTEXT" -n llm-stack wait \
  --for=condition=Registered inferenceendpoint/qwen-fp8 --timeout=5m
```

Kubernetes checks placement, qualifies the GPU, prepares the pinned model cache and starts serving. First startup includes the model download. Subsequent startups validate and reuse the cache. Readiness gates healthy serving through the shared gateway. See [retained caches and recovery](ADVANCED.md#helm-cache-reuse-and-recovery) for an existing download.

For a second precision on another available node, use the same values file with two overrides:

```bash
helm upgrade --install qwen-nvfp4 "$LLM_CHARTS/pylon-sglang-recipe-0.2.0.tgz" \
  --kube-context "$LLM_CONTEXT" --namespace llm-stack \
  --values "$LLM_IMAGES/qwen.values.yaml" --set recipe=qwen3.8-27b-nvfp4 --set 'nodes[0]=gpu-node-2' \
  --wait --timeout 120m
```

| Recipe | Precision | Availability and validation |
| --- | --- | --- |
| Qwen3.8-27B | FP8 | One GB10. Cached Helm startup and inference tested. |
| Qwen3.8-27B | NVIDIA NVFP4 | One GB10. Cached Helm startup, inference and model isolation tested. |
| GLM-5.3 | UD-IQ2_M | Two GB10s. Combined runtime tested. [Automatic Helm startup](ADVANCED.md#helm-glm-recipe) stopped at the host-memory guard. |
| Qwen3.8-Flash-Next | NVFP4 | One-node offload and two-node profiles. Live validation pending. [Advanced phased deployment](ADVANCED.md#qwen-catalog-configuration). |
| Nemotron 5 Nano 12B | Unverified | Unavailable. Exact public model artifact not verified. |
| Nemotron 5 Super 49B | Unverified | Unavailable. Exact public model artifact not verified. |
| Qwen3.8-4B | Unverified | Unavailable. Exact public model artifact not verified. |
| DeepSeek V4 Flash | Upstream mixed FP4/FP8 | Planned. Public checkpoint and upstream four-GPU candidates recorded. Chart support and hardware qualification pending. |

The catalog covers seven model families and eight entries, including two Qwen3.8-27B precisions. Deploy entries with `availability.deployable: true`. Planned and unavailable entries have no deployment profiles. Their dated primary sources and upstream candidates are recorded in the catalog.

The listed tests used short prompts. The catalog records validation for each hardware profile and workload separately. Additional hardware profiles use the same installation interface after qualification.

## 3. Discover and call models

List the registered models and call each precision:

```bash
python3 llm.py models
python3 llm.py chat --model qwen3.8-27b \
  'What is 17 multiplied by 19? Give one short sentence.'
python3 llm.py chat --model qwen3.8-27b-nvfp4 --stream \
  'Explain what a GPU does in two sentences.'
```

Each command retrieves the gateway CA and caller key, opens a temporary local connection, and cleans up its local connection files when it finishes. Use [connection overrides](ADVANCED.md#shared-cli-connection-options) for another context, namespace or an older installation.

[Advanced deployment and configuration](ADVANCED.md) covers credentials, recovery, GLM, monitoring and the existing Python workflows. The common catalog is ready for API consumers. The [recipe API and UI integration](https://github.com/NVIDIA/nvcf/issues/2337) is tracked separately.
