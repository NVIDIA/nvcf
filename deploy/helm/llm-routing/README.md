# Run models through a shared gateway

Install the shared routing release `llm-stack` in namespace `llm-stack`, then install each model recipe with Helm. Each model has its own release and persistent cache. Applications select a model through the same gateway address and caller credential.

## Prerequisites

### Cluster

- Kubernetes that you reach with kubectl and Helm, using a context that can create CustomResourceDefinitions and namespaces.
- Linux ARM64 GPU nodes whose GPU matches a recipe profile. The [recipe catalog](recipes/index.json) includes GB10 (Spark) and GB300 (Station) profiles. `python3 llm.py recipes` lists them.
- One whole GPU per node for each model: no MIG, time-slicing or MPS, and no `NoSchedule` or `NoExecute` taints on model nodes.
- The NVIDIA driver and container toolkit on each GPU node, and a RuntimeClass named `nvidia`.
- The [NVIDIA device plugin](https://github.com/NVIDIA/k8s-device-plugin) advertising `nvidia.com/gpu`. Use v0.17.4 or later on GB10.
- The node label `nvidia.com/gpu.product` on each GPU node. [GPU Feature Discovery](https://github.com/NVIDIA/k8s-device-plugin/tree/main/docs/gpu-feature-discovery) sets it, or apply it on Spark with `kubectl label node NODE nvidia.com/gpu.product=NVIDIA-GB10`. Use `NVIDIA-GB300` for Station. Capacity planning and the in-chart placement check match it against the recipe profile.
- A StorageClass named `local-path`, or another class passed to `llm.py plan --storage-class`, with `WaitForFirstConsumer` binding and unrestricted topology. Each model cache is node-local disk on its model node.
- The shared stack images on every eligible node. No published images exist yet. The committed [image values](dev-artifacts/values.yaml) reference development images with `Never` pull policy, so [prepare and preload them](dev-artifacts/README.md) on your nodes first. The verification Job also needs `python:3.12-alpine`, pulled or preloaded.
- Outbound access from the nodes to Hugging Face for model weights, Docker Hub for `lmsysorg/sglang`, nvcr.io, and GitHub for the llama.cpp source used by GGUF recipes.

k3s provides the `nvidia` RuntimeClass when the container toolkit is installed before k3s starts. It also installs Traefik ingress and the `local-path` StorageClass by default.

### Workstation

- kubectl, Helm 3.10 or newer, and Python 3.11 or newer.
- A checkout of this repository. Run all commands from `deploy/helm/llm-routing` in one terminal. The supported release and namespace are both `llm-stack`.

### Check the cluster

Replace `YOUR_CONTEXT` with your kubeconfig context, then check and load any missing shared images:

```bash
kubectl config use-context YOUR_CONTEXT
python3 dev-artifacts/build.py --load-only --allow-containerd-import
```

Then run preflight to check tools, cluster access, GPU compatibility, runtime, storage configuration and image availability:

```bash
python3 llm.py preflight --values dev-artifacts/values.yaml
```

## 1. Install shared infrastructure


```bash
helm upgrade --install llm-stack dev-artifacts/charts/llm-shared-stack-0.1.0.tgz \
  --namespace llm-stack --create-namespace \
  --values dev-artifacts/values.yaml --wait --timeout 10m
```

Helm installs the gateway, router and namespace-scoped operator with an empty model registry. A chart verification Job checks gateway TLS, model discovery, acceptance of the caller key and rejection of an invalid key before Helm reports success. The same checks run on upgrades with registered models. The chart generates the caller credential and TLS material on first installation and preserves them on upgrades. The operator watches `llm-stack`. The operator chart defaults `installCRDs` to `true`, so the shared release owns the templated InferenceEndpoint CRD and updates its schema on upgrades. For an externally managed CRD, set `operator.installCRDs=false`. The CRD is retained on uninstall.

### Add monitoring (optional)

To collect gateway and model metrics in VictoriaMetrics and view them in Grafana, install the independent monitoring release described in [Optional monitoring](recipes/MONITORING.md). It runs in namespace `llm-stack` and installs and uninstalls separately from the models.

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

Install and register Qwen FP8 below, replacing `PROFILE_FROM_PLANNER` and `NODE_FROM_PLANNER` with your chosen profile and node. To reuse retained downloads, keep the original release name, namespace and cache node. Completed model files are verified and reused automatically; missing files are downloaded. No cache flag or values file is required. [Model values examples](recipes/README.md#committed-values) are available for optional overrides and other recipes.

```bash
helm upgrade --install qwen-fp8 dev-artifacts/charts/pylon-sglang-recipe-0.2.0.tgz \
  --namespace llm-stack \
  --set recipe=qwen3.8-27b --set profileName=PROFILE_FROM_PLANNER \
  --set 'nodes[0]=NODE_FROM_PLANNER' \
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

Each recipe reserves its own GPU. Running both precisions on one-GPU nodes requires two distinct available nodes before installation. For the second model, replace `PROFILE_FROM_PLANNER` with its profile and `SECOND_NODE_FROM_PLANNER` with an available node different from the first model's node. If no GPU is free, [stop another model](#stop-and-resume) before installing.

```bash
helm upgrade --install qwen-nvfp4 dev-artifacts/charts/pylon-sglang-recipe-0.2.0.tgz \
  --namespace llm-stack \
  --set recipe=qwen3.8-27b-nvfp4 --set profileName=PROFILE_FROM_PLANNER \
  --set 'nodes[0]=SECOND_NODE_FROM_PLANNER' \
  --wait --timeout 120m &&
kubectl --namespace llm-stack wait \
  --for=condition=Registered inferenceendpoint/qwen-nvfp4 --timeout=5m &&
echo 'Second model installed and registered.'
```

Both models use the same gateway and caller credential. Each keeps its own Helm release and cache. Select either model in the chat commands below.

### Model choices

| Recipe | Precision | Hardware |
| --- | --- | --- |
| [Qwen3.8-27B](#2-install-a-model) | FP8 | [1 GB10 (Spark)](recipes/values/qwen3.8-27b.yaml) or [1 GB300 (Station)](recipes/values/qwen3.8-27b-station.yaml) |
| [Qwen3.8-27B](#add-a-second-model) | NVIDIA NVFP4 | [1 GB10 (Spark)](recipes/values/qwen3.8-27b-nvfp4.yaml) |
| [GLM-5.3](ADVANCED.md#helm-glm-recipe) | UD-IQ2_M | [2 GB10s (Spark)](recipes/values/glm-5.3.yaml) |
| [Qwen3.8-Flash-Next](ADVANCED.md#helm-flash-next-recipes) | NVFP4 | [1 GB10 (Spark) with local NVMe](recipes/values/qwen3.8-flash-next-nvme.yaml) or [2 GB10s (Spark) with a 200 Gbps link](recipes/values/qwen3.8-flash-next-tp2.yaml) |
| [DeepSeek V4 Flash](recipes/planned.json) | Upstream mixed FP4/FP8 | No recipe profile yet |

Planned recipes have no installable profile. See the [catalog](recipes/index.json) for profile details and the [Flash-Next guide](ADVANCED.md#helm-flash-next-recipes) for NVMe and two-node setup.

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

### Connect a coding agent

Coding agents on your workstation reach the models through the same gateway. Keep the connection open in its own terminal:

```bash
python3 llm.py connect
```

It forwards the gateway to `https://127.0.0.1:18443/v1` and prints `export` lines for the gateway URL, CA file and caller key. Run them in the terminal where you start the agent, then configure [Pi](ADVANCED.md#pi) or [Codex](ADVANCED.md#codex). Press Ctrl-C to close the connection and remove the credential files. Agents need a recipe tuned for a large context. See [Connect a coding agent](ADVANCED.md#connect-a-coding-agent).

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

Follow the [complete removal procedure](ADVANCED.md#remove-downloaded-model-files) to check cache ownership and use, uninstall the release, and remove its dedicated storage. Disk reclamation depends on the volume's reclaim policy and storage provisioner.

[Advanced deployment and configuration](ADVANCED.md) covers shared-stack upgrades and uninstall, credentials, model recovery, GLM and monitoring.
