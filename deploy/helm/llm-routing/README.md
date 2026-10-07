# Run models through a shared gateway

Install the routing stack once, then deploy model recipes onto compatible hardware. Each model has its own release and cache. Applications select a model through the same gateway address and caller credential.

Recipe profiles define the hardware, precision and workload requirements. The planner selects eligible nodes from current capacity.

## Before you start

Use an existing Kubernetes cluster with a Ready node for routing and suitable GPU nodes for your selected recipes. Model nodes need the NVIDIA device plugin, a GPU RuntimeClass and persistent storage. Install Python 3.11+, kubectl, Helm and Docker with Buildx on your workstation.

Run the commands below from `deploy/helm/llm-routing`.

## 1. Install the shared stack

Select your Kubernetes context and install:

```bash
export LLM_ROUTING_CONTEXT=YOUR_CONTEXT
python3 stack.py install --build-images
```

The command starts from the bundled configuration, selects a routing node, builds the application images for its architecture and preloads them onto compatible nodes. It installs the gateway, router and operator in `llm-stack`, then verifies the empty registry and authentication. Configuration, credentials and deployment evidence are saved automatically in a private directory for this context and namespace.

`--build-images` is the temporary local build path. Once the configuration references published registry images, run `python3 stack.py install`. See [shared stack settings](ADVANCED.md#shared-stack-settings) for custom configuration and [image builds](recipes/BUILDING.md) for runtime requirements.

## 2. Choose and deploy models

Choose Qwen models from the [catalog](recipes/catalog.json), or deploy GLM-5.3 `UD-IQ2_M` through the [independent GGUF recipe](ADVANCED.md#optional-independent-gguf-recipe). Both use the shared gateway.

Available recipes:

| Model | Precision | Validation |
| --- | --- | --- |
| GLM-5.3 | UD-IQ2_M | Combined flow tested. Independent deployment testing pending. |
| Qwen3.8-27B | FP8 | Spark smoke tested |
| Qwen3.8-27B | NVIDIA NVFP4 | Spark smoke tested |
| Qwen3.8-Flash-Next | NVFP4 | Live deployment testing pending |

Validation applies to the listed hardware profile and tested workload. The included Qwen profiles currently target GB10. Additional hardware profiles can use the same deployment commands after their runtime and resource requirements are defined and qualified.

This example deploys both tested Qwen precision variants:

```bash
python3 recipes/recipes.py deploy \
  --stack-connection "$(python3 stack.py paths --field connection)" \
  --model qwen3.8-27b --model qwen3.8-27b-nvfp4 \
  --storage-class local-path --runtime-class nvidia
```

Use the storage and runtime class names installed in your cluster. The command plans placement, qualifies the GPUs, downloads the pinned models, starts each release and verifies discovery, chat, streaming and authentication. Progress and log paths identify each phase. On success, the models are ready to call.

## 3. Send a request

Use a served model ID from the deployment output:

```bash
python3 stack.py chat \
  --model qwen3.8-27b 'What is 17 multiplied by 19? Give one short sentence.'
```

Change `--model` to `qwen3.8-27b-nvfp4` to call the other precision. Add `--stream` for streaming output. Both requests use the saved gateway connection and caller key.

## 4. View monitoring

Install monitoring for the shared stack and open its dashboard:

```bash
python3 recipes/recipe.py --context "$LLM_ROUTING_CONTEXT" --namespace llm-stack \
  --work-dir "$(python3 stack.py paths --field work)/monitoring" monitoring
```

Open <http://127.0.0.1:13000/d/llm-demo> to view the dashboard. Ctrl-C closes the local tunnel and leaves monitoring running.

For configuration, separate deployment phases, adding models, recovery, upgrades and troubleshooting, see [Advanced deployment and configuration](ADVANCED.md). [Monitoring settings](recipes/MONITORING.md) covers dashboard access and monitoring verification.
