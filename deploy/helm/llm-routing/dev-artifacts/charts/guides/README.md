# Run models through a shared gateway

Install the shared routing release `llm-stack` in namespace `llm-stack`, then install each model recipe with Helm. Each model has its own release and persistent cache. Applications select a model through the same gateway address and caller credential.

## Before you start

- Use Kubernetes with the NVIDIA device plugin, a GPU RuntimeClass and persistent storage. Choose available nodes from the [recipe catalog](recipes/index.json).
- Install Helm, kubectl and Python 3.11+ on your workstation.
- Run these commands from `deploy/helm/llm-routing` in one terminal. The supported release and namespace are both `llm-stack`.

The committed charts and [image values](dev-artifacts/values.yaml) use images preloaded on the shared Spark nodes. [Prepare new images and charts](dev-artifacts/README.md) only when sources change or another cluster needs them.

## 1. Install shared infrastructure

Replace `YOUR_CONTEXT` with your kubeconfig context and select it once below. All remaining commands use the selected context.

```bash
kubectl config use-context YOUR_CONTEXT

helm upgrade --install llm-stack dev-artifacts/charts/llm-shared-stack-0.1.0.tgz \
  --namespace llm-stack --create-namespace \
  --values dev-artifacts/values.yaml --wait --timeout 10m
```

Helm installs the gateway, router and namespace-scoped operator with an empty model registry. A chart verification Job checks gateway TLS, model discovery, acceptance of the caller key and rejection of an invalid key before Helm reports success. The same checks run on upgrades with registered models. The chart generates the caller credential and TLS material on first installation and preserves them on upgrades. The operator watches `llm-stack`. The operator chart defaults `installCRDs` to `true`, so the shared release owns the templated InferenceEndpoint CRD and updates its schema on upgrades. For an externally managed CRD, set `operator.installCRDs=false`. The CRD is retained on uninstall.

## 2. Install a model

List known recipe IDs, precisions, hardware profiles and validation status:

```bash
python3 llm.py recipes
```

This reads the local catalog, including recipes that are not installed. Use `--json` for the complete catalog. Choose a recipe and profile from this table before checking capacity.

Check current GPU allocation and downloaded model caches. Add `--json` for JSON output.

```bash
python3 llm.py plan --model qwen3.8-27b
```

Install and register Qwen FP8 below, replacing `NODE_FROM_PLANNER` with your chosen node. To reuse retained downloads, keep the original release name, namespace and cache node. Completed model files are verified and reused automatically; missing files are downloaded. No cache flag or values file is required. [Model values examples](recipes/README.md#committed-values) are available for optional overrides and other recipes.

```bash
helm upgrade --install qwen-fp8 dev-artifacts/charts/pylon-sglang-recipe-0.2.0.tgz \
  --namespace llm-stack \
  --set recipe=qwen3.8-27b --set 'nodes[0]=NODE_FROM_PLANNER' \
  --wait --timeout 120m &&
kubectl --namespace llm-stack wait \
  --for=condition=Registered inferenceendpoint/qwen-fp8 --timeout=5m &&
echo 'Model installed and registered.'
```

Run the entire block. It installs the model and waits for registration. Each step runs only after the previous step succeeds. Send requests separately using the chat commands below.

### Add a second model

Keep the shared stack and FP8 model installed. Check capacity for the NVFP4 recipe:

```bash
python3 llm.py plan --model qwen3.8-27b-nvfp4
```

Each recipe reserves its own GPU. Running both precisions on one-GPU nodes requires two distinct available nodes before installation. For the second model, replace `SECOND_NODE_FROM_PLANNER` with an available node different from the first model's node. If no GPU is free, [stop another model](#stop-and-resume) before installing.

```bash
helm upgrade --install qwen-nvfp4 dev-artifacts/charts/pylon-sglang-recipe-0.2.0.tgz \
  --namespace llm-stack \
  --set recipe=qwen3.8-27b-nvfp4 --set 'nodes[0]=SECOND_NODE_FROM_PLANNER' \
  --wait --timeout 120m &&
kubectl --namespace llm-stack wait \
  --for=condition=Registered inferenceendpoint/qwen-nvfp4 --timeout=5m &&
echo 'Second model installed and registered.'
```

Both models use the same gateway and caller credential. Each keeps its own Helm release and cache. Select either model in the chat commands below.

### Model choices

| Recipe | Precision | Availability and validation |
| --- | --- | --- |
| [Qwen3.8-27B](recipes/values/qwen3.8-27b.yaml) | FP8 | One GB10. Cached Helm startup and inference tested. |
| [Qwen3.8-27B](recipes/values/qwen3.8-27b-nvfp4.yaml) | NVIDIA NVFP4 | One GB10. Cached Helm startup, inference and model isolation tested. |
| [GLM-5.3](recipes/values/glm-5.3.yaml) | UD-IQ2_M | Two GB10s. Combined runtime tested. [Automatic Helm startup](ADVANCED.md#helm-glm-recipe) stopped at the host-memory guard. |
| Qwen3.8-Flash-Next | NVFP4 | Automatic [one-node NVMe](recipes/values/qwen3.8-flash-next-nvme.yaml) and [two-node tensor parallel](recipes/values/qwen3.8-flash-next-tp2.yaml) startup and short-prompt gateway checks passed at configured context 8,192 and concurrency 1. [Installation](ADVANCED.md#helm-flash-next-recipes). |
| Nemotron 5 Nano 12B | Unverified | Unavailable. Exact public model artifact not verified. |
| Nemotron 5 Super 49B | Unverified | Unavailable. Exact public model artifact not verified. |
| Qwen3.8-4B | Unverified | Unavailable. Exact public model artifact not verified. |
| DeepSeek V4 Flash | Upstream mixed FP4/FP8 | Planned. Public checkpoint and upstream four-GPU candidates recorded. Chart support and hardware qualification pending. |

The catalog covers seven model families and eight entries, including two Qwen3.8-27B precisions. Deploy entries with `availability.deployable: true`. Planned and unavailable entries have no deployment profiles. Their dated primary sources and upstream candidates are recorded in the catalog.

The listed tests used short prompts. The catalog records validation for each hardware profile and workload separately. Additional hardware profiles use the same installation interface after qualification.

Flash-Next uses recipe `qwen3.8-flash-next`. Select `spark-nvfp4-nvme` for one node with local NVMe offload or `spark-nvfp4-tp2` for two nodes with a suitable interconnect. Its [installation guide](ADVANCED.md#helm-flash-next-recipes) shows how to supply verified node capabilities and print the Helm command for that placement.

## 3. Discover and call models

List the available models in a table and call each precision. Use `python3 llm.py models --json` for the complete gateway response in scripts:

```bash
python3 llm.py models
python3 llm.py chat --model qwen3.8-27b \
  'What is 17 multiplied by 19? Give one short sentence.'
python3 llm.py chat --model qwen3.8-27b-nvfp4 --stream \
  'Explain what a GPU does in two sentences.'
```

The `models` and `chat` commands retrieve the gateway CA and caller key, open a temporary local connection, and clean up their local connection files when they finish. Use [connection overrides](ADVANCED.md#shared-cli-connection-options) for another context or an existing caller credential.

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
helm upgrade qwen-fp8 dev-artifacts/charts/pylon-sglang-recipe-0.2.0.tgz \
  --namespace llm-stack \
  --reuse-values --set suspended=true --timeout 10m &&
kubectl -n llm-stack wait --for=delete pod \
  -l app.kubernetes.io/instance=qwen-fp8 --timeout=10m
```

Wait for the pods to terminate before assigning their GPUs to another model. The stop command omits Helm's readiness wait because the model is intentionally unavailable. The release keeps its node placement and cache claims.

Resume when those GPUs are available again:

```bash
helm upgrade qwen-fp8 dev-artifacts/charts/pylon-sglang-recipe-0.2.0.tgz \
  --namespace llm-stack \
  --reuse-values --set suspended=false --wait --timeout 120m
```

### Uninstall, keep downloads

```bash
helm uninstall qwen-fp8 --namespace llm-stack --wait --timeout 10m
```

This removes the model's workloads, endpoint and Helm release. Its persistent volume claims (PVCs) and downloaded files remain. Reinstall using the [retained cache and its original node](ADVANCED.md#helm-cache-reuse-and-recovery).

### Uninstall completely

Follow the [complete removal procedure](ADVANCED.md#remove-downloaded-model-files) to record cache ownership, uninstall the release, then remove its storage. There is no combined uninstall-and-delete command. Delete only this model's dedicated cache claims after checking ownership and use. External or shared claims stay untouched. Physical disk reclamation depends on the volume's reclaim policy and storage provisioner.

[Advanced deployment and configuration](ADVANCED.md) covers shared-stack upgrades and uninstall, credentials, model recovery, GLM and monitoring.
