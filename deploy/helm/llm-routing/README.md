# LLM routing recipes

Install the shared LLM API Gateway, request router and Pylon Operator on an existing Kubernetes cluster, then add independent model recipes. The shared stack starts with an empty registry and has no default model. Each recipe owns its runtime, cache and `InferenceEndpoint`.

For Qwen models, precision variants and capacity-based Spark placement, use [Independent Qwen recipes](recipes/MULTI_MODEL.md). For llama.cpp GGUF models, use the [independent recipe binding](#optional-independent-gguf-recipe) or the [combined recipe flow](#combined-gguf-recipe-flow). GLM is optional.

## Install shared infrastructure first

The shared installer needs Python 3.11+, Helm, kubectl, a Ready node and accessible application images. It does not require GPUs, a model runtime, a RuntimeClass or model storage. Supply images built for the selected node architecture. The [image build guide](recipes/BUILDING.md) describes the source components and distribution options.

Copy [stack.config.example.json](stack.config.example.json) outside the checkout. Set the context, a new namespace, control node, cluster ID, release names and image references. Use `installCRDs: true` for the first operator in a cluster. If a compatible InferenceEndpoint CRD already exists, set it to `false`. The installer rejects overlapping operator watches and never adopts an existing namespace or CRD from another release.

From this directory, with your private config and work paths:

```bash
python3 stack.py --config /path/to/stack.json --work-dir /path/to/stack-work render
python3 stack.py --config /path/to/stack.json --work-dir /path/to/stack-work install
python3 stack.py --config /path/to/stack.json --work-dir /path/to/stack-work verify --expect-empty
```

The empty-stack check verifies empty discovery and registry responses, a 404 for an uninstalled model, and a 401 for an invalid caller key. Installation saves a private caller key, CA certificate and `connection.json`. The connection binds the namespace, node and shared resource identities. Model recipes reuse these credentials and cannot upgrade the shared releases. Keep this work directory private and available throughout the installation's lifetime.

### Install two independent models or precisions

Follow [Independent model recipes](recipes/MULTI_MODEL.md) to select and install two recipes, such as `qwen3.8-27b` (FP8) and `qwen3.8-27b-nvfp4`. The planner selects available nodes from each profile's requirements. A busy GPU is not evicted or shared. Qualify each backend, then verify both through the shared gateway.

Verify both through the same address and caller credential:

```bash
python3 stack.py --config /path/to/stack.json --work-dir /path/to/stack-work verify \
  --model qwen3.8-27b --model qwen3.8-27b-nvfp4
python3 stack.py --config /path/to/stack.json --work-dir /path/to/stack-work chat \
  --model qwen3.8-27b 'What is 17 multiplied by 19? Give one short sentence.'
python3 stack.py --config /path/to/stack.json --work-dir /path/to/stack-work chat \
  --model qwen3.8-27b-nvfp4 'What is 17 multiplied by 19? Give one short sentence.'
```

Add `--stream` for streaming chat. The shared CLI requires an explicit model ID. The verification checks discovery, healthy registry entries, real chat, streaming with token usage and rejection of an invalid key. Then test a controlled stop or upgrade of one recipe and verify that the other keeps serving. Preserve both model caches for recovery.

### Optional independent GGUF recipe

Copy [glm.config.example.json](recipes/glm.config.example.json) to a private file. Select the recipe, release prefix, `nodes.model` placement, `gpu` sizing, runtime image, RuntimeClass and storage class. Attach it to the shared connection:

```bash
python3 recipes/recipe.py --config /path/to/glm.json --work-dir /path/to/glm-work attach-stack \
  --stack-connection /path/to/stack-work/connection.json
python3 recipes/recipe.py --work-dir /path/to/glm-work render
python3 recipes/recipe.py --work-dir /path/to/glm-work inventory
```

Attachment reads shared resources and saves the normalized config in `glm-work/config.json`. It does not reserve GPUs or mark a model deployed. Continue only when inventory confirms that the selected model nodes have idle, eligible GPUs. Placement and preflight sizing use the selected recipe and GPU configuration:

```bash
python3 recipes/recipe.py --work-dir /path/to/glm-work preflight
python3 recipes/recipe.py --work-dir /path/to/glm-work build-runtime
python3 recipes/recipe.py --work-dir /path/to/glm-work qualify
python3 recipes/recipe.py --work-dir /path/to/glm-work download
python3 recipes/recipe.py --work-dir /path/to/glm-work load
python3 recipes/recipe.py --work-dir /path/to/glm-work verify-direct
python3 recipes/recipe.py --work-dir /path/to/glm-work register
python3 recipes/recipe.py --work-dir /path/to/glm-work verify-gateway
```

In this mode, model rendering and Helm operations target only its backend and chain-check releases. The `stack`, infrastructure image build/import/update/rollback, credential mutation and existing-install adoption commands are rejected. Shared infrastructure stays under `stack.py` ownership.

## Combined GGUF recipe flow

The `recipes/recipe.py` tool coordinates the combined gateway/router chart, the Pylon Operator chart and a model backend chart. The backend builds llama.cpp, downloads and verifies the model files, serves the model on one or more GPU nodes, and creates an `InferenceEndpoint` for Pylon to register. Application images and charts use your checkout, including local edits.

GGUF recipes live in folders under `recipes/`. The available definition is `glm-5.3`: GLM-5.3 `UD-IQ2_M`, 222 GiB of GGUF weights. Each folder holds the model lock, llama.cpp server arguments and memory rules. The tool derives placement and GPU-specific settings from the cluster. The separate `recipes.py` planner handles the SGLang catalog described in [MULTI_MODEL.md](recipes/MULTI_MODEL.md).

## Placement

The model runs on one node, or is split by layer across two nodes with llama.cpp RPC. `init` picks the smallest node count that fits the model in GPU memory:

- DGX Spark (GB10): CPU and GPU share about 122 GiB, so GLM-5.3 needs two nodes.
- GB300: one GPU has about 268 GiB of its own memory, so GLM-5.3 fits on one node.

The gateway, router and operator run on a separate node when the cluster has one, and share the first model node otherwise. A cluster needs at least one GPU node per model node; two nodes are enough for either example.

## Prerequisites

- Hardware: idle ARM64 nodes with one NVIDIA GPU each, enough of them for the model (see [Placement](#placement)). Mixed GPU types are not supported.
- Kubernetes and storage: an existing ARM64 Kubernetes cluster containing these nodes, with pod networking, `cluster.local` DNS, NetworkPolicy enforcement and node-compatible `ReadWriteOnce` persistent storage. Reserve 400 GiB on the first model node and 160 GiB on each additional model node.
- GPU enablement: model nodes with a working NVIDIA driver, NVIDIA Container Toolkit configured for the container runtime, and a device plugin advertising `nvidia.com/gpu`. Use an installed `RuntimeClass` matching `runtimeClass` in the config (`nvidia` in the example).
- Access: kubeconfig for the target cluster. For a kubeconfig with multiple contexts, pass `--context <name>` to the commands below. Initial installation requires creating namespaces, custom resource definitions (CRDs), role-based access control (RBAC) resources and namespaced Helm resources.
- Workstation: Python 3.11+, Git, Helm 3.14+ or Helm 4, and kubectl. Provide access to GitHub, model downloads and container images. The [image build guide](recipes/BUILDING.md) covers build tools and distribution credentials.

## Installation

Follow these steps for the first installation of the stack and model.

### Configure

Use your existing kubeconfig.

1. From the repository root, open the recipe directory and create the configuration.

   ```bash
   cd deploy/helm/llm-routing/recipes
   python3 recipe.py init
   ```

   Review the selected nodes and generated configuration. `init` discovers idle GPUs, storage and image preload settings for K3s. It runs a short GPU probe pod on candidate nodes in a temporary namespace, then deletes the namespace. The probe uses the runtime image, so the first run can take several minutes to pull it. The configuration is saved at the printed path in a private work directory.

   To place the model yourself, edit `nodes.model` in the saved configuration. For example, list two GB300 nodes to split the model across them. `preflight` checks that the placement fits.

2. Render the manifests and inventory the cluster.

   ```bash
   python3 recipe.py render
   python3 recipe.py inventory
   ```

`render` lints the Helm charts and generates manifests. `inventory` checks node readiness, GPU availability and cluster prerequisites. Continue after both commands pass.

### Build and distribute the application images

Build and distribute `gateway`, `router`, `pylon` and `operator` using the [image build guide](recipes/BUILDING.md).

### Deploy in order

Run each command in order and continue after it succeeds.

```bash
python3 recipe.py preflight
python3 recipe.py stack
python3 recipe.py build-runtime
python3 recipe.py qualify
python3 recipe.py download
python3 recipe.py load
python3 recipe.py verify-direct
python3 recipe.py register
python3 recipe.py verify-gateway
```

1. `preflight`: Check GPU calculations, the GPU type and available memory on each model node against the configuration. The tested environments use NVIDIA driver 580 or newer and CUDA 13. Rerun `preflight` and `qualify` after changing these versions.
2. `stack`: Install the gateway, router and Pylon Operator. This generates the default caller API key and self-signed certificates. To supply your own, complete [Optional configuration](#optional-configuration) before running `stack`.
3. `build-runtime`: Build llama.cpp with CUDA and remote procedure call (RPC) support.
4. `qualify`: Test calculations on each model GPU, and data transfer between them when the model is split. (After fixing a failed qualification Job, run `python3 recipe.py qualify --retry`.)
5. `download`: Download the model files and verify their sizes and SHA256 checksums. See the [runtime](recipes/NOTICE) and [model](recipes/glm-5.3/NOTICE) licenses.
6. `load`: Load the model on its GPUs and wait for the model server.
7. `verify-direct`: Test model answers and streaming directly.
8. `register`: Register the model with Pylon and wait for readiness.
9. `verify-gateway`: Test model answers, streaming, authentication, discovery and registration through the gateway. For failures, see [Gateway check troubleshooting](#gateway-check-failures).

Pinned `glm-5.3` runtime:

- Model revision: `346b3591c7f28d1a23716f97a065ecf12ec14771`, with 238,577,585,701 bytes across six GGUF shards.
- llama.cpp revision: `f872b591121761ac7b2af18283bd99bdc092a63a`.
- Capacity: equal layer split across the model nodes, context 2048 and one request slot.

## Verification

Run these commands from `deploy/helm/llm-routing/recipes` after installation or [attachment to an existing stack](#update-an-existing-installation).

### Inspect the deployment

```bash
context="$(python3 recipe.py context)" &&
  kubectl --context "$context" get nodes -o wide &&
  kubectl --context "$context" get deployments,pods,services,inferenceendpoints --all-namespaces -o wide
```

Check the node placement, ready replicas and model endpoint status.

### Send a chat or streaming request

```bash
python3 recipe.py chat 'What is 17 multiplied by 19? Give one short sentence.'
python3 recipe.py chat 'Explain what a GPU does in two sentences.' --stream
```

Use `--model` to target another model registered with the same gateway. Omitting it selects the configured recipe's model.

```bash
python3 recipe.py chat --model GLM-5.3-UD-IQ2_M 'What is 17 multiplied by 19? Give one short sentence.'
python3 recipe.py chat --model qwen3.8-27b 'What is 17 multiplied by 19? Give one short sentence.'
```

Add `--stream` to either command for streaming output.

## Maintenance

### Update only gateway or router

If this workstation has not used the running installation before, [attach to it first](#update-an-existing-installation).

1. Edit the service in your checkout: `src/invocation-plane-services/llm-api-gateway` for gateway or `src/libraries/rust/stargate` for router.
2. [Build and distribute that component](recipes/BUILDING.md#rebuild-gateway-or-router) with a fresh tag. Run the next commands in the same terminal.
3. Update the selected image and verify gateway requests.

   ```bash
   python3 recipe.py update --component "$COMPONENT" --tag "$NEW_TAG"
   python3 recipe.py verify-gateway
   ```

4. Save the printed update record path for rollback.

The model stays loaded. Gateway updates briefly interrupt requests. Router updates reconnect Pylon transports. Image-only updates require unchanged routing charts. For chart or API changes, follow [Update the stack](#update-the-stack).

To roll back the image update:

1. Replace the example filename below with the saved update record. The previous image must remain available in the node cache or registry.
2. Restore the recorded tag and verify requests.

   ```bash
   python3 recipe.py rollback --result /path/to/saved-update.json
   python3 recipe.py verify-gateway
   ```

Use the record from the latest update when rolling back.

### Update an existing installation

Use your existing kubeconfig.

1. From the repository root, open the recipe directory.

   ```bash
   cd deploy/helm/llm-routing/recipes
   ```

2. Discover the installation and verify gateway requests.

   ```bash
   python3 recipe.py attach-existing
   python3 recipe.py verify-gateway
   ```

Add `--namespace <namespace>` to attachment when the cluster has multiple installations or your access is limited to one namespace.

Continue with [Update only gateway or router](#update-only-gateway-or-router).

### Uninstall

Run the entire block, including parentheses, from `deploy/helm/llm-routing/recipes` in the same configured terminal used for installation. The context lookup uses the recipe's normal selection. If you passed `--context`, `--config` or `--work-dir` during installation, pass the same options before `context` in the lookup below.

The namespace, release and endpoint names below are the defaults for the `glm-5.3` recipe. If you changed `namespace`, `releasePrefix` or `releases` in your saved configuration, replace these names to match. The model release is the release prefix plus the recipe's `releaseName`, and the endpoint name is the recipe's `endpointName`. The chain release, `-chain`, exists only for split models. The image-import release is the release prefix plus `-images`.

The block skips absent releases, including the optional image importer, and stops on other failures. It keeps the operator running until endpoint cleanup finishes.

```bash
(
  set -eu
  context="$(python3 recipe.py context)"
  : "${context:?Context lookup returned an empty value}"
  namespace=llm-routing-poc
  : "${namespace:?Set the namespace from your saved configuration}"

  helm --kube-context "$context" -n "$namespace" uninstall llm-poc-glm --ignore-not-found --wait --timeout 3m
  kubectl --context "$context" -n "$namespace" wait --for=delete inferenceendpoint/glm53-iq2 --timeout=60s
  kubectl --context "$context" -n "$namespace" wait --for=delete deployment/pylon-glm53-iq2 --timeout=90s
  for release in llm-poc-glm-chain llm-poc-images llm-poc-operator llm-poc-stack; do
    helm --kube-context "$context" -n "$namespace" uninstall "$release" --ignore-not-found --wait --timeout 3m
  done
)
```

The model/artifact and RPC-cache PVCs, downloaded models, namespace, InferenceEndpoint CRD, CA Secret and operator credential remain. Keep the local work directory and saved configuration for reuse.

After uninstalling the demo releases, run these commands from the recipe directory with the same configuration and context selection used for installation:

```bash
python3 recipe.py init
python3 recipe.py render
python3 recipe.py inventory
```

`init` checks that the demo is uninstalled, reuses the saved placement, image references and credentials, and archives stale progress under `before-reinit-*` in the work directory. Continue with [Deploy in order](#deploy-in-order), starting at `preflight`.

### Update the stack

Builds use the selected checkout, including local edits. Use `--source-dir /path/to/existing/checkout` to select another checkout. The recorded commit ID is for debugging. Squash and rebase merges are supported.

The stack records a SHA-256 fingerprint of its routing chart files. Image updates and rollback require matching chart contents. Older installations and rollback records without a fingerprint require a coordinated stack update first.

1. Make runtime, API and chart changes in the same checkout, including generated files and regression tests.
2. Render the manifests and run the [local regression checks](#local-validation).
3. For an existing installation, preserve its installed Helm values and credentials when upgrading the stack. Copy only `recipeChartsSha256` from the private work directory's `render/stack-values.json` into those preserved values. The remaining render values are offline test data. Review the rendered changes before applying the upgrade. Fresh installations record this automatically during `stack`.
4. Build and deploy gateway and router together when their API contract changes.
5. Rerun gateway verification, recovery and image update/rollback checks.

### Recovery and limits

This optional resilience check interrupts model service and verifies inference after recovery. A split model loses its RPC workers; a single-node model loses its model server.

1. Schedule a time when a model interruption is acceptable.
2. Run the explicit recovery check.

   ```bash
   python3 recipe.py recover --confirm-model-interruption
   ```

3. Save and review the results from the target cluster.

On two DGX Spark nodes, a cold load took about 26 minutes and recovery with cached weights about 11 minutes.

Memory and runtime limits:

- On GPUs that share memory with the CPU, such as GB10, monitor host `MemAvailable` when changing context size or adding workloads.
- The runtime stops below 1 GiB available memory or when the model process/container swaps.
- `glm-5.3` uses two-bit quantization, context 2048, one request slot and TCP/RPC between split nodes.
- `glm-5.3` canary timing is 180 seconds for the timeout and 60 seconds for the interval.

The model persistent volume claims (PVCs) remain after uninstall.

## Optional configuration

### Alternative container runtimes and external configuration

For a non-K3s cluster or custom container runtime, prepare an external copy of [config.example.json](recipes/config.example.json) before installation. Set the context, node placement, GPU, storage, runtime and image settings for your cluster. Use that file instead of `init`, and pass `--config /path/to/config.json` to each recipe command, starting with `render` and `inventory`.

The `gpu` section describes the GPU on every model node:

- `name`: the name `nvidia-smi` reports, such as `NVIDIA GB300`. The `InferenceEndpoint` GPU product is derived from it.
- `computeCapability`: such as `"10.3"`. The llama.cpp build targets it as `103a-real`.
- `memoryGiB`: GPU memory, or host memory when `unifiedMemory` is `true`.
- `unifiedMemory`: `true` when the GPU shares system memory, as on GB10.
- `cudaArchitectures`: `null`, or a CMake architecture list that replaces the derived build target.

### Runtime image mirror

To use a mirror of the pinned CUDA image, set `runtimeImage` in the saved configuration before running `preflight`.

### API keys

The gateway requires an API key for inference. Model and registry reads are public. By default, `stack` saves the caller key as `api-key` in the private work directory, and the recipe client uses it automatically.

To supply your own key, set `apiKeyFile` in the saved configuration to a file containing the key before running `stack`.

### Demo UI API key

The existing `stack` step also creates a separate `demo-ui` key. It keeps `poc-client`, adds the UI key's SHA-256 hash to `apiKeys`, and creates Secret `demo-ui-api-key` with field `api-key` in the same namespace and Helm release.

The UI backend should read that Secret and send the key with gateway requests. Keep the key on the server. The private work directory retains the key in `demo-ui-api-key`; reuse that directory for later stack runs so the key stays unchanged.

For an existing stack, preserve its installed Helm values and caller keys when upgrading. Add the `demo-ui` hash to `apiKeys` and supply the matching plaintext key as `demoUiApiKey` from a private values file. Do not commit that file. After the gateway reloads its keys, verify inference with both keys and rejection of an invalid key.

### TLS certificates

Gateway clients use HTTPS and Pylon connects to the router over verified QUIC. Gateway/router HTTP, registration gRPC and Pylon/backend HTTP use plaintext inside the cluster.

To use existing certificates, complete these steps before running `stack`:

1. Set `tls.selfSigned.enabled=false` in the external configuration.
2. Create Secrets `llm-gateway-stack-gateway-tls` and `llm-gateway-stack-router-tls` in the namespace with valid `tls.crt` and `tls.key` fields.
3. Create the configured CA ConfigMap with a `ca.crt` field. Certificates must cover the configured service names and client address.

## Add a recipe

Create a folder under `recipes/` with these files:

- `recipe.json`: the name (matching the folder), release and endpoint names, llama.cpp revision, server arguments, runtime environment, storage sizes and memory rules. Copy `glm-5.3/recipe.json` as a starting point. Do not set `--device`, `--tensor-split` or `--rpc`; the tool derives them from `nodes.model`.
- `model.lock.json`: the pinned model files with sizes and SHA256 checksums.
- `NOTICE`: the model license terms.

Select the recipe with `python3 recipe.py init --recipe <folder>`. When only one recipe exists, `init` uses it.

## Migrating from the Spark recipe

This tool replaces `deploy/helm/llm-routing/spark/spark.py`. Configuration and stored values changed without compatibility fallbacks:

- `SPARK_CONTEXT` is now `LLM_ROUTING_CONTEXT`.
- `nodes.leader` and `nodes.worker` are now the `nodes.model` list, and a `gpu` section is required.
- `releases.glm` is now `releases.model`.
- The stack records `recipeSource` and `recipeChartsSha256` instead of the `sparkRecipe` values.
- Model resources are renamed, for example `rpc-worker` to `rpc-n1`.

To move an existing installation, [uninstall](#uninstall) it with the Spark recipe's names, then run `init` and deploy again. The shared-stack installer and Qwen recipes can run in a separate namespace without migrating that existing installation.

## Troubleshooting

If a command fails, follow the next check and diagnostic log path printed by the CLI. Detailed tool output is saved in private `evidence/*.log` files inside the work directory. Use `python3 recipe.py paths` to locate that directory.

### Gateway check failures

If `verify-gateway` fails, inspect its results in `evidence/gateway.json` under the local work directory. The check uses local port 18443. Stop a previous port-forward if it occupies that port.

If the command reports incomplete key cleanup, run `python3 recipe.py cleanup-key`.

After resolving the problem, rerun the check from the recipe directory:

```bash
python3 recipe.py verify-gateway
```

## Local validation

From `deploy/helm/llm-routing`, run shared-infrastructure, recipe/client and runtime chart tests. Render checks use private work directories and do not change a live cluster.

```bash
python3 -m pip install -r recipes/tests/requirements.txt
python3 -m unittest discover -s tests -v
python3 -m unittest discover -s recipes/tests -v
python3 -m unittest discover -s recipes/charts/gguf-backend/tests -v
python3 stack.py --config stack.config.example.json --work-dir "$(mktemp -d)" render
python3 recipes/recipe.py --context llm-routing-demo --config recipes/config.example.json --work-dir "$(mktemp -d)" render
git diff --check
```
