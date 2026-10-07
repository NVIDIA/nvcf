# Run models through a shared gateway

Install the routing stack once, then deploy model recipes onto compatible hardware. Each model has its own release and cache. Applications select a model through the same gateway address and caller credential.

Recipe profiles define the hardware, precision and workload requirements. The planner selects eligible nodes from current capacity.

## Before you start

Use an existing Kubernetes cluster with a Ready node for routing and suitable GPU nodes for your selected recipes. Model nodes need the NVIDIA device plugin, a GPU RuntimeClass and persistent storage. Install Python 3.11+, kubectl and Helm on your workstation.

Create a private copy of [stack.config.example.json](stack.config.example.json). Set its Kubernetes context, namespace, routing node and application images for that node's architecture. The [image build guide](recipes/BUILDING.md) covers building and distributing the routing images. [Shared stack settings](ADVANCED.md#shared-stack-settings) explains existing CRDs and custom configuration.

Run the commands below from `deploy/helm/llm-routing`.

## 1. Install the shared stack

Choose a private directory for this installation's configuration and credentials:

```bash
export STACK_WORK="$HOME/.local/state/nvcf/llm-stack"
python3 stack.py --config /path/to/stack.json --work-dir "$STACK_WORK" install
```

The command installs the gateway, router and operator, then verifies the empty registry and authentication. It saves the connection and configuration in `STACK_WORK` for the following commands.

## 2. Choose and deploy models

Choose recipes from the [catalog](recipes/catalog.json). The current Qwen options are:

| Model | Precision | Validation |
| --- | --- | --- |
| Qwen3.8-27B | FP8 | Spark smoke tested |
| Qwen3.8-27B | NVIDIA NVFP4 | Spark smoke tested |
| Qwen3.8-Flash-Next | NVFP4 | Hardware qualification pending |

Validation applies to the listed hardware profile and tested workload. The included Qwen profiles currently target GB10. Additional hardware profiles can use the same deployment commands after their runtime and resource requirements are defined and qualified.

Choose a Qwen or GGUF deployment below. For Qwen, this example deploys both tested precision variants:

```bash
python3 recipes/recipes.py deploy \
  --stack-connection "$STACK_WORK/connection.json" \
  --work-dir "$STACK_WORK/models" \
  --model qwen3.8-27b --model qwen3.8-27b-nvfp4 \
  --storage-class local-path --runtime-class nvidia
```

Use the storage and runtime class names installed in your cluster. The command plans placement, qualifies the GPUs, downloads the pinned models, starts each release and verifies discovery, chat, streaming and authentication. Progress and log paths identify each phase. On success, the models are ready to call.

For the GGUF alternative, copy [glm.config.example.json](recipes/glm.config.example.json) into a private configuration and select its model nodes and GPU settings. Deploy it through the same shared stack:

```bash
python3 recipes/recipe.py --config /path/to/glm.json \
  --work-dir "$STACK_WORK/glm" deploy \
  --stack-connection "$STACK_WORK/connection.json"
```

This command handles attachment, preparation, runtime build, qualification, download, serving, registration and verification. [Recipe settings](ADVANCED.md#optional-independent-gguf-recipe) explains the configuration.

## 3. Send a request

Use a served model ID from the deployment output:

```bash
python3 stack.py --work-dir "$STACK_WORK" chat \
  --model qwen3.8-27b 'What is 17 multiplied by 19? Give one short sentence.'
```

Change `--model` to `qwen3.8-27b-nvfp4` to call the other precision. Add `--stream` for streaming output. Both requests use the saved gateway connection and caller key.

## 4. View monitoring

Use the context and namespace from your stack configuration:

```bash
python3 recipes/recipe.py --context YOUR_CONTEXT --namespace YOUR_NAMESPACE \
  --work-dir "$STACK_WORK/monitoring" monitoring
```

Open <http://127.0.0.1:13000/d/llm-demo> to view the dashboard. Ctrl-C closes the local tunnel and leaves monitoring running.

For configuration, separate deployment phases, adding models, recovery, upgrades and troubleshooting, see [Advanced deployment and configuration](ADVANCED.md). [Monitoring settings](recipes/MONITORING.md) covers dashboard access and monitoring verification.
