# Run models through a shared gateway

Install shared routing once, then install each model recipe with Helm and a small values file. Each model has its own release and persistent cache. Applications select a model through the same gateway address and caller credential.

## Before you start

- Use Kubernetes with the NVIDIA device plugin, a GPU RuntimeClass and persistent storage. Choose available nodes from the [recipe catalog](recipes/index.json).
- Install Helm, kubectl and Python 3.11+ on your workstation.
- Select your kubeconfig context and run these commands from `deploy/helm/llm-routing` in one terminal. The examples use namespace `llm-stack`.

The committed charts and [image values](dev-images/values.yaml) use images preloaded on the shared Spark nodes. [Prepare new images and charts](dev-images/README.md) only when sources change or another cluster needs them.

## 1. Install shared infrastructure

```bash
export LLM_CONTEXT="$(kubectl config current-context)"
export LLM_WORK="$HOME/.local/state/llm-routing/$LLM_CONTEXT/llm-stack"
mkdir -p "$LLM_WORK"

helm upgrade --install llm-stack dev-images/charts/llm-shared-stack-0.1.0.tgz \
  --kube-context "$LLM_CONTEXT" --namespace llm-stack --create-namespace \
  --values dev-images/values.yaml --wait --timeout 10m
```

Helm installs the gateway, router and namespace-scoped operator with an empty model registry. A chart verification Job checks gateway TLS, model discovery, acceptance of the caller key and rejection of an invalid key before Helm reports success. The same checks run on upgrades with registered models. The chart generates the caller credential and TLS material on first installation and preserves them on upgrades. The committed values watch `llm-stack`. For another namespace, also pass `--set operator.clusterId=NAME --set 'operator.watchNamespaces[0]=NAME'`. Helm creates the InferenceEndpoint CRD when absent and reuses a compatible CRD from another installation. The release that owns the CRD updates it on upgrades.

## 2. Install a model

Use the committed [recipe values](recipes/README.md#committed-values). Replace `gpu-node-1` below with an available GB10 node. The defaults use runtime class `nvidia` and storage class `local-path`. If your cluster uses different names, add `--set runtimeClassName=NAME --set storageClassName=NAME` to the Helm command.

Install the FP8 recipe:

```bash
helm upgrade --install qwen-fp8 dev-images/charts/pylon-sglang-recipe-0.2.0.tgz \
  --kube-context "$LLM_CONTEXT" --namespace llm-stack \
  --values recipes/values/qwen3.8-27b.yaml --set 'nodes[0]=gpu-node-1' \
  --wait --timeout 120m &&
kubectl --context "$LLM_CONTEXT" -n llm-stack wait \
  --for=condition=Registered inferenceendpoint/qwen-fp8 --timeout=5m
```

Kubernetes checks placement, qualifies the GPU, prepares the pinned model cache and starts serving. First startup includes the model download. Subsequent startups validate and reuse the cache. Readiness gates healthy serving through the shared gateway. See [retained caches and recovery](ADVANCED.md#helm-cache-reuse-and-recovery) for an existing download.

For a second precision on another available node, use the NVFP4 values:

```bash
helm upgrade --install qwen-nvfp4 dev-images/charts/pylon-sglang-recipe-0.2.0.tgz \
  --kube-context "$LLM_CONTEXT" --namespace llm-stack \
  --values recipes/values/qwen3.8-27b-nvfp4.yaml \
  --set 'nodes[0]=gpu-node-2' \
  --wait --timeout 120m
```

| Recipe | Precision | Availability and validation |
| --- | --- | --- |
| [Qwen3.8-27B](recipes/values/qwen3.8-27b.yaml) | FP8 | One GB10. Cached Helm startup and inference tested. |
| [Qwen3.8-27B](recipes/values/qwen3.8-27b-nvfp4.yaml) | NVIDIA NVFP4 | One GB10. Cached Helm startup, inference and model isolation tested. |
| [GLM-5.3](recipes/values/glm-5.3.yaml) | UD-IQ2_M | Two GB10s. Combined runtime tested. [Automatic Helm startup](ADVANCED.md#helm-glm-recipe) stopped at the host-memory guard. |
| Qwen3.8-Flash-Next | NVFP4 | [NVMe values](recipes/values/qwen3.8-flash-next-nvme.yaml) or [two-node values](recipes/values/qwen3.8-flash-next-tp2.yaml). Live validation pending. [Phased deployment](ADVANCED.md#helm-flash-next-recipes). |
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

## 4. Stop or uninstall a model

Downloaded model weights are the model cache on disk. Choose what to retain:

| Action | GPU and runtime memory | Deployment configuration | Downloaded files |
| --- | --- | --- | --- |
| Stop | Released after the model pods terminate | Kept for resume | Kept |
| Uninstall, keep downloads | Released | Removed | Kept for reinstall |
| Uninstall completely | Released | Removed | Deleted through separate manual storage cleanup |

These actions affect one model release. Keep the shared gateway and operator installed so other models continue serving and endpoint cleanup can finish.

### Stop and resume

Stop the FP8 release installed above. Use the same chart version that installed the release:

```bash
helm upgrade qwen-fp8 dev-images/charts/pylon-sglang-recipe-0.2.0.tgz \
  --kube-context "$LLM_CONTEXT" --namespace llm-stack \
  --reuse-values --set suspended=true --timeout 10m &&
kubectl --context "$LLM_CONTEXT" -n llm-stack wait --for=delete pod \
  -l app.kubernetes.io/instance=qwen-fp8 --timeout=10m
```

Wait for the pods to terminate before assigning their GPUs to another model. The stop command omits Helm's readiness wait because the model is intentionally unavailable. The release keeps its node placement and cache claims.

Resume when those GPUs are available again:

```bash
helm upgrade qwen-fp8 dev-images/charts/pylon-sglang-recipe-0.2.0.tgz \
  --kube-context "$LLM_CONTEXT" --namespace llm-stack \
  --reuse-values --set suspended=false --wait --timeout 120m
```

### Uninstall, keep downloads

```bash
helm uninstall qwen-fp8 --kube-context "$LLM_CONTEXT" \
  --namespace llm-stack --wait --timeout 10m
```

This removes the model's workloads, endpoint and Helm release. Its persistent volume claims (PVCs) and downloaded files remain. Reinstall using the [retained cache and its original node](ADVANCED.md#helm-cache-reuse-and-recovery).

### Uninstall completely

Follow the [complete removal procedure](ADVANCED.md#remove-downloaded-model-files) to record cache ownership, uninstall the release, then remove its storage. There is no combined uninstall-and-delete command. Delete only this model's dedicated cache claims after checking ownership and use. External or shared claims stay untouched. Physical disk reclamation depends on the volume's reclaim policy and storage provisioner.

[Advanced deployment and configuration](ADVANCED.md) covers credentials, recovery, GLM, monitoring and the existing Python workflows. The common catalog is ready for API consumers. The [recipe API and UI integration](https://github.com/NVIDIA/nvcf/issues/2337) is tracked separately.
