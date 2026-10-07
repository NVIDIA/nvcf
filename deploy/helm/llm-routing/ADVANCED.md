# Advanced deployment and configuration

## Helm configuration

The [main guide](README.md) uses local chart archives and an explicit values file. Build these archives with [package-charts.sh](package-charts.sh). A chart source directory can also be installed after its local dependencies are built. Helm and kubectl use your current kubeconfig context. Use their `--kube-context` and `--context` flags for an explicit override.

Use [shared-stack/values.local.example.yaml](charts/shared-stack/values.local.example.yaml) as a template for existing preloaded images. Replace the four image references with your node cache or registry references and set the operator watch namespace to the installation namespace. Set `operator.installCRDs: true` for the first CRD owner, or `false` to reuse an installed compatible CRD. Each shared stack watches its own namespace.

The shared chart generates the caller key in `llm-shared-caller-key` and the transport credential in `llm-shared-cluster-token`. Existing installations retain these Secrets and their CA during upgrades and reinstalls. For an existing caller credential, set `callerKey.existingSecret` to a Secret containing `api-key`. For an existing transport credential, set `clusterCredential.create: false` and `operator.credential.existingSecret` to a Secret containing `cluster-token`. Both Secrets must be in the release namespace. Keep generated credentials and site values in private storage.

TLS customization uses `gatewayStack.tls` and the child chart listener TLS values. See [shared-stack/values.yaml](charts/shared-stack/values.yaml) and the [gateway chart](../llm-gateway-stack/llm-gateway-stack/values.yaml). Model values reference the same CA ConfigMap through `sharedCAConfigMap`.

### Shared Helm installation and verification

Use the output variables from the [installation guide](README.md#1-install-shared-infrastructure). The shared chart runs a verification Job after installation and upgrade. It uses the installed CA to check gateway TLS and the `/v1/models` response, then checks that an unknown model returns HTTP 401 with an invalid key and HTTP 404 with the valid key. An empty registry is valid on first installation. Existing models remain valid on upgrades. A failed check makes the Helm command fail:

```bash
helm upgrade --install llm-stack dev-images/charts/llm-shared-stack-0.1.0.tgz \
  --kube-context "$LLM_CONTEXT" --namespace llm-stack --create-namespace \
  --values dev-images/values.yaml --wait --timeout 10m
```

The Job retries temporary connection and gateway availability failures for up to 120 seconds. TLS certificate and authentication failures stop verification immediately. Successful verification Jobs are removed. Failed Jobs and their logs remain available until the next install or upgrade.

The verification image must be pullable or preloaded on eligible nodes. See [verification image distribution](recipes/BUILDING.md#verification-image) for its default and offline settings.

### Shared CLI connection options

`llm.py` uses the current kubeconfig context and namespace `llm-stack`. Put global overrides before the command:

```bash
python3 llm.py --context my-cluster --namespace llm-other models
python3 llm.py --context my-cluster --namespace llm-other chat \
  --model qwen3.8-27b 'Say hello in one short sentence.'
```

The helper retrieves the installed CA and caller credential for each command. Its local connection and temporary files last only for that command. For legacy installations, `--ca-configmap NAME` and `--api-key-file FILE` select the existing CA and caller key. Image preparation remains in the [image build guide](recipes/BUILDING.md).

### Gateway access for SDKs and curl

For an SDK or raw HTTP client, keep a gateway connection open and provide its CA and caller key. Install curl for the examples below.

Forward the gateway in one terminal:

```bash
kubectl --context "$LLM_CONTEXT" -n llm-stack port-forward svc/llm-api-gateway 18443:8080
```

In another terminal, set `LLM_CONTEXT` and `LLM_WORK` as in the installation guide, then retrieve the gateway CA and caller key and list models:

```bash
kubectl --context "$LLM_CONTEXT" -n llm-stack get configmap llm-gateway-stack-ca \
  -o 'jsonpath={.data.ca\.crt}' > "$LLM_WORK/gateway-ca.crt"
API_KEY=$(kubectl --context "$LLM_CONTEXT" -n llm-stack get secret llm-shared-caller-key \
  -o 'go-template={{index .data "api-key" | base64decode}}')
curl --fail-with-body --cacert "$LLM_WORK/gateway-ca.crt" -H "Authorization: Bearer $API_KEY" \
  https://localhost:18443/v1/models
```

Send a chat request:

```bash
curl --fail-with-body --cacert "$LLM_WORK/gateway-ca.crt" -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' https://localhost:18443/v1/chat/completions \
  -d '{"model":"qwen3.8-27b","messages":[{"role":"user","content":"What is 17 multiplied by 19? Give one short sentence."}]}'
```

Change `model` to `qwen3.8-27b-nvfp4` to call the other precision. Add `"stream":true` for streaming.

### Helm cache reuse and recovery

To install a Qwen release against an existing complete cache in the same namespace, add these values:

```yaml
reuseCaches: true
cache:
  existingClaim: retained-qwen-cache
```

Select the node holding that PVC. The chart mounts it and preserves its existing Helm ownership. `reuseCaches: true` validates the pinned files locally and fails when required files are missing or corrupt. New downloads use atomic completion markers, so interrupted preparation retries from the retained cache.

Inspect startup and readiness:

```bash
kubectl --context "$LLM_CONTEXT" -n llm-stack get pods,inferenceendpoints
kubectl --context "$LLM_CONTEXT" -n llm-stack logs deployment/qwen-fp8-serve-0 -c placement-check
kubectl --context "$LLM_CONTEXT" -n llm-stack logs deployment/qwen-fp8-serve-0 -c sglang
```

Correct the values and rerun the original Helm command. Set `suspended: true` in the model values to release its GPUs while retaining cache claims. Set it back to `false` and rerun Helm to resume. Automatic recipes withdraw their endpoint while suspended and restore it on resume. The shared release and other models continue serving. `helm uninstall` removes model-owned workloads and endpoint resources while retaining model PVCs.

### Helm GLM recipe

The GLM automatic profile uses two distinct GB10 nodes. Create `$LLM_WORK/glm.values.yaml`:

```yaml
recipe: glm-5.3
profileName: gb10-x2
nodes: [gpu-node-1, gpu-node-2]
runtimeClassName: nvidia
storageClassName: local-path
sharedCAConfigMap: llm-gateway-stack-ca
```

Install it with the shared stack already running:

```bash
helm upgrade --install glm dev-images/charts/pylon-gguf-backend-0.2.0.tgz \
  --kube-context "$LLM_CONTEXT" --namespace llm-stack \
  --values "$LLM_WORK/glm.values.yaml" --wait --timeout 120m
```

Kubernetes checks placement, builds the pinned llama.cpp runtime, starts the worker RPC server and qualifies both GPUs. It then verifies or downloads the pinned GGUF cache and starts the model server. The served model ID is `GLM-5.3-UD-IQ2_M`. The common catalog lists its per-node resource and storage requirements. The current cached two-node Helm trial stopped at the host-memory guard during loading. Automatic serving validation remains open.

To reuse existing GLM downloads and runtime artifacts, set `reuseCaches: true`, `artifacts.existingClaim` and `rpc.cache.existingClaim` to the retained claims. Supply their original nodes in leader/worker order. Runtime identity and complete model checksums are validated before serving.

## Existing Python workflows

The following sections retain the original planner, combined installation and maintenance commands. Use the Helm flow above for new independent installations.

Start with the [main guide](README.md) for installation, model deployment, chat and monitoring. Use this guide for configuration details, individual phases, upgrades and recovery.

## Shared stack settings

The shared installer runs the gateway, router and operator on a Ready node using architecture-compatible application images. Model recipes supply their GPU, runtime and storage settings independently. The [image build guide](recipes/BUILDING.md) describes the source components and distribution options.

`stack.py install` starts from [stack.config.example.json](stack.config.example.json) when a saved configuration is absent. Select the context with `--context` or `LLM_ROUTING_CONTEXT`. The default namespace is `llm-stack`; use `--namespace` to select another installation. Setup chooses a Ready routing node and detects a compatible existing InferenceEndpoint CRD.

The default work directory is `${XDG_STATE_HOME:-$HOME/.local/state}/nvcf/llm-routing/stacks/<context-hash>/<namespace>`. It holds the private `config.json`, credentials, connection and evidence for that installation. Commands reuse it automatically. Print the saved locations with:

```bash
python3 stack.py paths
python3 stack.py paths --field config
python3 stack.py paths --field connection
```

For registry images, initialize and edit the generated configuration before installation:

```bash
python3 stack.py init
${EDITOR:-vi} "$(python3 stack.py paths --field config)"
python3 stack.py install
```

Set each component's image repository, immutable tag and pull policy. The bundled registry references are placeholders. During development, `install --build-images` supplies fresh local image references and builds and preloads them. It applies to a fresh shared stack. Keep image updates for an existing installation on its explicit upgrade path.

The normal `install` command includes gateway verification. Use these individual commands to inspect manifests or repeat verification:

```bash
python3 stack.py render
python3 stack.py verify --expect-empty
```

The empty-stack check verifies empty discovery and registry responses, a 404 for an uninstalled model, and a 401 for an invalid caller key. Installation saves a private caller key, CA certificate and `connection.json`. The connection binds the namespace, node and shared resource identities. Model recipes reuse these credentials. The shared installer owns gateway and operator updates. Keep this work directory private and available throughout the installation's lifetime.

### Custom configuration and state locations

Use `--config FILE` to supply an existing private configuration and `--work-dir DIR` to select a custom state directory. Put these optional overrides before the command. Installation saves the configuration for later commands. For a custom state location, continue passing the same `--work-dir DIR`.

A custom configuration specifies the context, namespace, control node, cluster ID, release names and image references. Use `installCRDs: true` for the first operator in a cluster or `false` for a compatible existing CRD. The installer checks namespace and operator ownership before applying resources.

### Install two independent models or precisions

Follow [Independent model recipes](#qwen-catalog-configuration) to select and install two recipes, such as `qwen3.8-27b` (FP8) and `qwen3.8-27b-nvfp4`. The planner selects idle nodes that satisfy each profile. The deploy command qualifies each backend and verifies both through the shared gateway.

Verify both through the same address and caller credential:

```bash
python3 stack.py verify \
  --model qwen3.8-27b --model qwen3.8-27b-nvfp4
python3 stack.py chat \
  --model qwen3.8-27b 'What is 17 multiplied by 19? Give one short sentence.'
python3 stack.py chat \
  --model qwen3.8-27b-nvfp4 'What is 17 multiplied by 19? Give one short sentence.'
```

Add `--stream` for streaming chat. The shared CLI requires an explicit model ID. The verification checks discovery, healthy registry entries, real chat, streaming with token usage and rejection of an invalid key. Then test a controlled stop or upgrade of one recipe and verify that the other keeps serving. Preserve both model caches for recovery.

### Reinstall with retained model caches

Keep the original shared-stack work directory, namespace and model PVCs. The work directory binds the reinstall to the original namespace, node and credentials. This procedure interrupts the selected installation. Use the release names, context and namespace from that installation's saved configuration.

Remove model releases first so the operator can clean up their transports, then remove the shared releases:

```bash
(
  set -eu
  : "${CONTEXT:?Set the saved context}" "${NAMESPACE:?Set the saved namespace}" \
    "${STACK_RELEASE:?Set the saved stack release}" "${OPERATOR_RELEASE:?Set the saved operator release}"
  for release in qwen3-8-27b qwen3-8-27b-nvfp4 "$STACK_RELEASE" "$OPERATOR_RELEASE"; do
    helm --kube-context "$CONTEXT" --namespace "$NAMESPACE" uninstall "$release" \
      --cascade foreground --wait --timeout 5m
  done
)
```

The model PVCs and retained credentials remain available. Use the same context and namespace as the original installation. The shared installer resolves its saved state, and model deployment creates a new attempt directory automatically:

```bash
python3 stack.py install --reinstall
python3 recipes/recipes.py deploy \
  --stack-connection "$(python3 stack.py paths --field connection)" \
  --model qwen3.8-27b --model qwen3.8-27b-nvfp4 \
  --storage-class local-path --runtime-class nvidia --reuse-caches
```

`--reinstall` verifies that the previous releases are removed and that the retained namespace, routing node and credentials match the saved installation. It archives the earlier connection evidence and records the new resource identities.

`--reuse-caches` validates each retained PVC and volume binding and places the model on its cache's node. The cache phase checks the exact pinned checkpoint entirely locally. An incomplete or mismatched cache stops deployment with a reason. Successful reuse preserves the downloaded weights and proceeds through qualification, serving and gateway verification.

### Optional independent GGUF recipe

Copy [glm.config.example.json](recipes/glm.config.example.json) to a private file. Select the recipe, release prefix, `nodes.model` placement, `gpu` sizing, runtime image, RuntimeClass and storage class. Attach it to the shared connection:

```bash
python3 recipes/recipe.py --config /path/to/glm.json --work-dir /path/to/glm-work attach-stack \
  --stack-connection "$(python3 stack.py paths --field connection)"
python3 recipes/recipe.py --work-dir /path/to/glm-work render
python3 recipes/recipe.py --work-dir /path/to/glm-work inventory
```

Attachment reads shared resources and saves the normalized config in `glm-work/config.json`. Inventory must confirm idle, eligible GPUs before deployment. Placement and preflight sizing use the selected recipe and GPU configuration:

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

In this mode, model rendering and Helm operations target its backend and chain-check releases. Use `stack.py` to manage shared infrastructure and credentials, and a separate work directory for monitoring.

Combine these phases with `python3 recipes/recipe.py --config /path/to/glm.json --work-dir /path/to/glm-work deploy --stack-connection "$(python3 stack.py paths --field connection)"`. If it stops, read the phase log and use the recovery command printed by the CLI. Continue the remaining phases above from the same work directory. The saved state records completed checkpoints.

## Combined GGUF recipe flow

The `recipes/recipe.py` tool coordinates the combined gateway/router chart, the Pylon Operator chart and a model backend chart. The backend builds llama.cpp, downloads and verifies the model files, serves the model on one or more GPU nodes, and creates an `InferenceEndpoint` for Pylon to register. Application images and charts use your checkout, including local edits.

GGUF recipes live in folders under `recipes/`. The available definition is `glm-5.3`: GLM-5.3 `UD-IQ2_M`, 222 GiB of GGUF weights. Each folder holds the model lock, llama.cpp server arguments, server tuning defaults and memory rules. The tool derives placement and GPU-specific settings from the cluster. The separate `recipes.py` planner handles the SGLang catalog described in [Qwen catalog configuration](#qwen-catalog-configuration).

## Placement

The model runs on one node, or is split by layer across two nodes with llama.cpp RPC. `init` picks the smallest node count that fits the model and its default context in GPU memory:

- DGX Spark (GB10): CPU and GPU share about 122 GiB, so GLM-5.3 needs two nodes.
- GB300: one GPU has its own memory, reported by `nvidia-smi` as about 250 GiB, with 249.75 GiB usable through CUDA. With the GB300 defaults of two 65,536-token requests, GLM-5.3 needs 242 GiB on one node: 236 GiB for its weights and KV cache, plus 6 GiB headroom. It fits on one node with about 7 GiB to spare.

The gateway, router and operator run on a separate node when the cluster has one, and share the first model node otherwise. A cluster needs at least one GPU node per model node; two nodes are enough for either example.

Tested configurations for the combined GGUF workflow:

- Two DGX Spark nodes, model split across both. See [Recovery and limits](#recovery-and-limits) for load times.
- Two GB300 workstations, model on one node (October 2026). `init` detected compute capability 10.3. `preflight` measured 249 GiB of free GPU memory. The model loaded in about 3 minutes from local disk, decoded at about 42 tokens per second at context 2048, and ran for more than 11 hours without restarts.
  - `retune` to the GB300 defaults (two 65,536-token slots) reloaded the model in 2.5 minutes, and `verify-direct` and `verify-gateway` passed. The GPU used 231.4 GiB, below the 242 GiB the memory check budgets, up from 219.3 GiB at context 2048.
  - A 33,367-token prompt took 133 seconds (about 250 tokens per second), and the answer decoded at about 32 tokens per second. A follow-up with the same prefix reused the cached prompt and started answering in 0.3 seconds.

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

   To place the model yourself, edit `nodes.model` in the saved configuration. For example, list two GB300 nodes to split the model across them. To change the context size or concurrent requests, edit `tuning` (see [Server tuning](#server-tuning)). `preflight` checks that the placement and context fit.

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
- Capacity: equal layer split across the model nodes. Context size and concurrent requests depend on the GPU. See [Server tuning](#server-tuning).

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

## Monitoring

From `deploy/helm/llm-routing/recipes`, install monitoring and view the dashboard:

```bash
python3 recipe.py monitoring
```

Open `http://127.0.0.1:13000/d/llm-demo`. Viewing requires no login. Ctrl-C closes the tunnel and leaves monitoring running. Use `--port` to change the local port.

See [advanced monitoring configuration](recipes/MONITORING.md) for settings, dashboard access, verification and uninstall.

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

If monitoring is installed, [remove it first](recipes/MONITORING.md#uninstall).

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

- On GPUs that share memory with the CPU, such as GB10, monitor host `MemAvailable` when raising `tuning.contextPerSlot` or adding workloads.
- On GPUs with their own memory, such as GB300, the model pod's cgroup memory can sit at its limit while the server process uses little memory. The difference is page cache from reading the model files, which the kernel reclaims under pressure. It is not a leak. On GB300 the pod stayed at its 64Gi limit for more than 11 hours with about 1.4 GiB resident, no swap, no memory-guard stops and no restarts.
- The runtime stops below 1 GiB available memory or when the model process/container swaps.
- `glm-5.3` uses two-bit quantization and TCP/RPC between split nodes. By default it serves one 2048-token request at a time on GB10, and two 65,536-token requests on GB300.
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

### Server tuning

`init` saves the recipe's llama.cpp tuning for the detected GPU memory type in the configuration. For `glm-5.3` on GB300:

```json
"tuning": {"contextPerSlot": 65536, "slots": 2, "batchSize": 2048, "ubatchSize": 512, "defaultMaxTokens": 8192, "threads": 8}
```

- `contextPerSlot`: tokens per request, prompt and output together. Use a multiple of 256.
- `slots`: requests served at the same time. The Pylon canary sends a short request every 60 seconds; a second slot keeps it from waiting behind a long request.
- `batchSize` and `ubatchSize`: prompt tokens processed per step. Larger values process long prompts faster and use more GPU memory. `ubatchSize` cannot exceed `batchSize`.
- `defaultMaxTokens`: output limit for requests that do not set `max_tokens`. A request can set a larger limit.
- `threads`: CPU threads for the model server.

`glm-5.3` defaults:

- GB10 and other GPUs that share memory with the CPU: one 2048-token slot, batch 128 and micro-batch 128, 512 default output tokens. These are the original Spark settings.
- GB300 and other GPUs with their own memory: two 65,536-token slots, batch 2048 and micro-batch 512, 8192 default output tokens.

Every slot reserves a KV cache for its full context. The recipe sets the cost per token in `kvCacheKiBPerToken`, 108 KiB for `glm-5.3`, so two 65,536-token slots use 13.5 GiB. `init`, `preflight` and `retune` include the KV cache in the memory check. When the context does not fit, the error reports the largest `contextPerSlot` that does.

Before `load`, edit `tuning` and run `preflight` again. After `load`, edit `tuning`, then apply it and verify:

```bash
python3 recipe.py retune
python3 recipe.py verify-direct
python3 recipe.py verify-gateway
```

`retune` restarts the model server with the saved tuning and `resources`, and reuses the built runtime and downloaded model. RPC workers restart only when their memory limits change. The endpoint is unavailable while the model reloads. A loaded model holds its GPU memory, so `retune` checks the context against the free GPU memory that `preflight` measured. If the model does not come back, check the model pod logs, lower `tuning` and run `retune` again. It reapplies whenever the last Helm upgrade did not complete.

### Pod resources

The model server and RPC worker pods get CPU and memory from the recipe's memory rules. To change them, add a `resources` section to the saved configuration before `load`, or run `retune` after `load`:

```json
"resources": {
  "model": {"requests": {"memory": "40Gi"}, "limits": {"memory": "96Gi", "cpu": "16"}},
  "rpc": {"limits": {"memory": "120Gi"}}
}
```

- `model` sets the model server pod and `rpc` sets each RPC worker pod while serving.
- Only `cpu` and `memory` can be set, as strings: a number such as `8` or `0.5` with an optional suffix (`m`, `k`, `M`, `G`, `T`, `P`, `E`, `Ki`, `Mi`, `Gi`, `Ti`, `Pi` or `Ei`). This is a subset of Kubernetes quantities; exponents such as `1e6` and the `n` and `u` suffixes are rejected. CPU must be a whole number of millicores, such as `250m` or `1.5`. Memory cannot use the `m` suffix; use `Mi` or `M`. Each pod keeps one GPU.
- Unset values keep their computed defaults. A request above its merged limit is rejected when the configuration is loaded, before any cluster command.
- The `preflight` and `load` memory checks still use the recipe's memory rules, not these values.

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

- `recipe.json`: the name (matching the folder), release and endpoint names, llama.cpp revision, server arguments, tuning defaults, KV cache cost, runtime environment, storage sizes and memory rules. Copy `glm-5.3/recipe.json` as a starting point.
  - Do not set `--device`, `--tensor-split` or `--rpc` in `serverArgs`; the tool derives them from `nodes.model`.
  - Do not set `--ctx-size`, `--parallel`, `--batch-size`, `--ubatch-size`, `--predict` or `--threads`; set `tuning.unified` and `tuning.discrete` instead.
  - Set `kvCacheKiBPerToken` to the KV cache size of one token across all layers, rounded up. For `glm-5.3`, llama.cpp caches 576 values per layer for attention and 128 for the sparse-attention indexer, at 2 bytes each over 78 layers: about 107 KiB.
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
python3 -m pip install -r recipes/tests/requirements.txt -r recipes/tests/requirements-monitoring.txt
python3 -m unittest discover -s tests -v
python3 -m unittest discover -s recipes/tests -v
python3 -m unittest discover -s recipes/charts/gguf-backend/tests -v
python3 stack.py --config stack.config.example.json --work-dir "$(mktemp -d)" render
python3 recipes/recipe.py --context llm-routing-demo --config recipes/config.example.json --work-dir "$(mktemp -d)" render
git diff --check
```

## Qwen catalog configuration

The deploy command creates a private directory for each attempt beside the stack connection, under `models/`, and prints its location. Set `--work-dir` to choose a new directory explicitly.

Choose models and workload requirements. The planner chooses supported profiles and available GPU nodes, then renders an independent Helm release for each model. The shared gateway and Pylon Operator stay installed when a model is added or removed.

The Qwen3.8-27B FP8 and NVIDIA NVFP4 profiles passed live Spark smoke tests on October 6, 2026, with context length configured to 8,192 and concurrency 1. After installing a shared stack with an empty registry, both independent precision releases passed discovery, short-prompt chat, streaming and authentication through the same gateway address and credential.

Suspending NVFP4 returned HTTP 503 while FP8 continued serving chat and streaming. The shared infrastructure and FP8 serving pods remained unchanged. NVFP4 recovered using its retained cache, and both models passed verification again. These checks did not exercise the full configured context, benchmark performance or evaluate output quality. Flash-Next profiles remain unverified on Spark.

Startup and readiness probes allow five seconds because SGLang health checks can take more than one second.

### Models and placement

| Model | Precision | Included hardware profiles |
| --- | --- | --- |
| Qwen3.8-27B | FP8 or NVIDIA NVFP4 | Independent single-GPU precision recipes, no speculation, float32 SSM state |
| Qwen3.8-Flash-Next | NVFP4 | One GPU with NVMe file offload, or two GPUs with tensor parallelism |

The default planner selects the fewest eligible nodes that satisfy every requested model's workload and capacity constraints. `--preference latency` prefers in-memory profiles over NVMe offload when enough nodes are available. This preference is a placement policy, not a measured latency guarantee.

Each profile declares operating system, image architecture, GPU compatibility, memory, storage, CPU, context and concurrency requirements. Hardware matching belongs to the profile. The planner and deployment workflow use the same commands for Spark, Station or another compatible system. The planner accounts for current pod requests, including init containers and sidecars, and places each selected model on a distinct group of idle GPUs. When capacity is unknown or insufficient, it explains which requirements failed. Changes to running workloads require an explicit lifecycle operation.

Each Qwen3.8-27B precision has its own served model ID, release, endpoint and cache. Use `qwen3.8-27b` for FP8 and `qwen3.8-27b-nvfp4` for NVIDIA NVFP4. The NVFP4 checkpoint contains FP8 attention and GDN projections with NVFP4 MLPs and language-model head. The runtime reads that mixed quantization from the pinned checkpoint. Both precision recipes reserve the same conservative GB10 profile memory envelope until separate hardware measurements justify different limits.

The initial Qwen3.8-27B envelope is at most 9,216 total context tokens and one concurrent request, matching the cookbook's tested request envelope. Flash-Next profiles allow up to 262,144 context tokens, with concurrency up to 8 for NVMe offload and 24 for two-node serving. Defaults are 8,192 total context tokens and concurrency 1. These are limits for candidate testing, not measured performance claims.

Source recipes:

- [Qwen3.8-27B](https://lmsysorg.mintlify.app/cookbook/autoregressive/Qwen/Qwen3.8-27B)
- [NVIDIA Qwen3.8-27B NVFP4 checkpoint](https://huggingface.co/nvidia/Qwen3.8-27B-NVFP4)
- [Qwen3.8-Flash-Next](https://lmsysorg.mintlify.app/cookbook/autoregressive/Qwen/Qwen3.8-Flash-Next)

The included GB10 Flash-Next profiles use the RadixArk NVFP4 checkpoint. Images are pinned to public Linux ARM64 manifest digests and checkpoints to commit revisions in [catalog.json](recipes/catalog.json). Review [model and runtime terms](recipes/NOTICE) before downloading weights.

### Shared infrastructure

For a new installation, follow the [shared infrastructure flow](README.md) and verify its empty registry before adding models. `stack.py` exports a private `connection.json` that binds recipes to the installed stack identity and credentials. You can also use an existing gateway/operator installation, including installations made directly with the [LLM Gateway Stack](../llm-gateway-stack/README.md#install) and [Pylon Operator](../pylon-operator/README.md) charts. Pass `--kube-context "$CONTEXT"` to every Helm command and `--context "$CONTEXT"` to every kubectl command in those guides.

For the first test, deploy these recipes in the namespace already watched by the operator. An existing operator may watch only its original namespace. Additional namespaces must be configured by the stack administrator before registering models. Reuse the existing gateway address, CA and caller key. Do not run GLM's `init`, `stack` or uninstall sequence to add a Qwen model.

Prerequisites:

- Python 3.11+, kubectl and Helm on the workstation.
- Ready nodes matching the selected profile, with exclusive `nvidia.com/gpu` allocation, GPU product labels, cgroup v2 and an installed NVIDIA RuntimeClass. The included Qwen profiles currently target Linux ARM64 GB10. Other hardware requires a compatible runtime image and an explicit profile with its memory and topology requirements.
- A `WaitForFirstConsumer` StorageClass for per-node retained model caches. Check physical disk capacity before installation; Kubernetes allocatable storage does not establish PVC capacity.
- For NVMe offload, the node's Kubernetes ephemeral storage must reside on local NVMe. The planner requires this explicit capability because Kubernetes does not discover it reliably.
- For two-node tensor parallelism, both nodes must share a 200GbE fabric. Supply its actual IPv4 addresses and interface names. This chart initially uses NCCL TCP over that fabric with host networking, not RDMA. Live collective qualification is required before model download. Runtime ports are generated per release and must be free on both nodes.
- The pinned images must be pullable or preloaded on selected nodes. Allow model download access during the download phase. Optional Hugging Face credentials are referenced by `hfTokenSecret` (key `token`), never embedded in a plan.

### Define a hardware profile

Each catalog profile supplies `hardware.os`, `architecture`, `gpuProducts` (Kubernetes labels), `cudaDeviceNames` (runtime identities), `gpuCount` and `memoryMode`. The current runtime assigns one GPU to each rank. Unified-memory profiles use host memory requirements. Discrete-memory profiles also declare `minDeviceMemoryGiB`, with measured per-node `cudaTotalMemoryGiB` supplied in the capability file. Memory requirements use GiB, per node. Distributed profiles declare their minimum link speed in `minFabricGbps`.

Pin a compatible runtime image and model revision, define the workload limits and resource reservations, then run qualification and inference on that hardware before recording it as validated. Existing validation records apply to their exact hardware, artifact pins and tested workload.

### Deploy command and recovery

The existing `recipes.py deploy` command combines planning, rendering, qualification, download, serving and gateway verification for fresh releases. The optional workload and capability arguments below also apply to `deploy`.

The command stops at a failed phase and keeps its releases, caches, plan, logs and rendered values in the chosen work directory. Read `failure.json` and the named log before recovery. Use the saved values with the [individual phase commands](#deploy-and-verify) to resolve and repeat the failed phase, then continue the remaining phases. Keep serving releases in the serving phase. Use [lifecycle operations](#lifecycle) for changes to running models.

Each automated deployment requires a new work directory and absent model releases. Use `--reuse-caches` for a [reinstall with retained model caches](#reinstall-with-retained-model-caches). Changes to an existing release follow the explicit phase and lifecycle procedures below.

### Plan and render

Run from `deploy/helm/llm-routing/recipes`. Store plans, capability files and evidence outside the checkout.

```bash
export CONTEXT=your-context
export NAMESPACE=your-gateway-namespace
export STACK_CONNECTION=/path/to/private/stack/connection.json
export WORK=/path/to/private/recipe-test
mkdir -p "$WORK"
```

```bash
python3 recipes.py plan \
  --stack-connection "$STACK_CONNECTION" \
  --model qwen3.8-27b \
  --model qwen3.8-27b-nvfp4 \
  --storage-class local-path --runtime-class nvidia \
  --output "$WORK/plan.json"

python3 recipes.py render \
  --plan "$WORK/plan.json" --output "$WORK/render"
```

Set `CONTEXT` and `NAMESPACE` to the context and namespace in the connection file for later Helm commands. Bound planning verifies the shared resource identities, operator watch scope, CA and caller credential, then derives its target from that connection. Explicit `--context` or `--namespace` values must match. Planning reads cluster state but changes nothing. Rendering is offline. The output explains the selected profile and nodes for each model. Inspect the plan before deploying and regenerate it if capacity changes. Use new output paths when replanning; commands do not overwrite earlier evidence.

To plan against a saved inventory:

```bash
python3 recipes.py inventory --context "$CONTEXT" --output "$WORK/inventory.json"
```

For an existing stack without a connection file, replace `--stack-connection` on `plan` with `--context "$CONTEXT" --namespace "$NAMESPACE"`. For offline planning, use `--inventory "$WORK/inventory.json" --namespace "$NAMESPACE"` instead. Offline planning does not inspect or bind to shared infrastructure and cannot be combined with `--stack-connection`. Saved inventory is a snapshot, not a reservation. Kubernetes enforces exclusive GPU requests during scheduling.

To test Flash-Next, select `--model qwen3.8-flash-next` instead of the NVFP4 27B recipe and add `--capabilities "$WORK/capabilities.json"`. Copy [capabilities.example.json](recipes/capabilities.example.json) into that private file and replace its documentation addresses and node names. Set `localNvme=true` only after checking the node's ephemeral storage location, and omit fabric fields on nodes without the required link. The precision pair above uses ordinary model cache storage and single-node serving.

Optional workload settings:

- `--context-length` and `--concurrency` set the defaults for selected models.
- `--requirements /path/to/requirements.json` sets per-model overrides, for example `{"qwen3.8-flash-next":{"contextLength":32768,"concurrency":9}}`. This excludes the one-node profile.
- `--no-nvme-offload` requires in-memory serving. With FP8 27B and Flash-Next selected, the included profiles need three eligible GPU nodes.
- `--preference latency` prefers the distributed Flash-Next profile when eligible capacity exists.

### Deploy and verify

Start with one release from the plan. For example, `qwen3-8-27b` is the Helm release for `qwen3.8-27b`; NVFP4 27B uses `qwen3-8-27b-nvfp4` and Flash-Next uses `qwen3-8-flash-next`. Every release has a separate values file, cache claims, Service and InferenceEndpoint.

```bash
export RELEASE=qwen3-8-27b
export VALUES="$WORK/render/$RELEASE-values.json"

python3 recipes.py check-plan --plan "$WORK/plan.json" --release "$RELEASE"

helm install "$RELEASE" ./charts/sglang \
  --kube-context "$CONTEXT" --namespace "$NAMESPACE" \
  --values "$VALUES" --set phase=qualify \
  --wait --wait-for-jobs --timeout 10m
```

`check-plan` revalidates the saved stack connection when present, then rechecks node identities and schedulable capacity immediately before a fresh install. Stop if it fails and regenerate the plan. It is not an update or adoption command.

Read all qualification Job logs. Require `qualification_pass` on every rank and correct GPU/NCCL identity. A failed Job stops this phase. Keep its logs and resolve the failure before replacing that release's failed qualification Jobs or starting a new trial.

```bash
kubectl --context "$CONTEXT" -n "$NAMESPACE" logs \
  -l "app.kubernetes.io/instance=$RELEASE" --all-containers --prefix

helm upgrade "$RELEASE" ./charts/sglang \
  --kube-context "$CONTEXT" --namespace "$NAMESPACE" \
  --values "$VALUES" --set phase=download \
  --wait --wait-for-jobs --timeout 4h

helm upgrade "$RELEASE" ./charts/sglang \
  --kube-context "$CONTEXT" --namespace "$NAMESPACE" \
  --values "$VALUES" --set phase=serve \
  --wait --timeout 2h

for condition in Ready TransportReady Registered; do
  kubectl --context "$CONTEXT" -n "$NAMESPACE" wait \
    "inferenceendpoint/$RELEASE" --for="condition=$condition" --timeout=10m || break
done
```

The download phase populates the pinned snapshot on every rank. Serving requires that local snapshot and its successful download marker. Rank zero waits for rank one's startup marker before entering the distributed runtime. The Service selects rank zero. The operator creates the Pylon transport automatically.

Repeat these phases for the other planned release. Concurrent installation is safe only while the plan's selected nodes remain distinct and available.

Then verify both models through the same gateway. Use a reachable HTTPS gateway URL including `/v1`, the existing CA and a caller key file. This sends real chat and streaming requests and an invalid-key probe.

```bash
python3 verify.py \
  --url https://your-gateway:8080/v1 \
  --ca /path/to/ca.crt --key-file /path/to/api-key \
  --model qwen3.8-27b --model qwen3.8-27b-nvfp4 \
  --output "$WORK/gateway-results.json"
```

Accept the first live trial only after:

1. Both models appear healthy in discovery and registry, and chat/streaming returns the requested model ID and token usage.
2. Both runtimes stay within memory limits without swap, OOM or unexpected restarts. Two-node runs exercise both GPUs and the selected network link.
3. An explicitly scheduled interruption of one model leaves the other serving. Registry health reflects failure and recovery.
4. Restarting with retained caches works. The single-node Flash-Next PLE file is disposable and recreated on local NVMe each boot, avoiding the upstream populated-file rewrite issue.

Record results against the exact catalog/image/model pins. Update validation status only after those checks pass. Tool calling, vision, Station hardware and workloads beyond the recorded checks need separate validation.

### Lifecycle

A model release owns only its runtime, Service, endpoint, scripts and cache claims. Cache claims are retained on uninstall. Remove a model with `helm uninstall "$RELEASE" --kube-context "$CONTEXT" -n "$NAMESPACE" --wait`; keep the shared operator and gateway installed so endpoint cleanup can finish. Deleting retained model data is a separate explicit action.

For a reversible stop, keep the serving phase and set `suspended=true`. This scales every GPU rank to zero while retaining the release, cache claims, Service and endpoint. Registration becomes unavailable until the backend is Ready again. Wait for its GPU pods to terminate before planning another recipe on those nodes.

```bash
helm upgrade "$RELEASE" ./charts/sglang --kube-context "$CONTEXT" -n "$NAMESPACE" \
  --reuse-values --set suspended=true --timeout 10m
kubectl --context "$CONTEXT" -n "$NAMESPACE" wait --for=delete pod \
  -l "app.kubernetes.io/instance=$RELEASE" --timeout=10m
```

The suspend command deliberately omits Helm's readiness wait because an unavailable endpoint is the intended result. Helm 4's default watcher would wait for that endpoint to become Ready again. The explicit pod wait confirms that GPU allocations are released.

Resume the same release on its retained placement after the GPUs are free:

```bash
helm upgrade "$RELEASE" ./charts/sglang --kube-context "$CONTEXT" -n "$NAMESPACE" \
  --reuse-values --set suspended=false --wait --timeout 120m
```

Recheck registry health and inference after recovery. Suspension applies to the selected recipe while the shared stack and other recipes continue serving.

Changing a distributed topology requires stopping both ranks before restarting the group. Do not use a rolling update to move one rank to another node while the other remains active. Re-plan and requalify the group before serving it again. Retained local cache claims remain bound to their original nodes; a new placement needs separate caches or an explicit data migration. The planner does not migrate stored weights. Do not rerun preparation phases against a serving release as an update shortcut.

The runtime checks host `MemAvailable` before loading. Its watchdog stops its own process group if host available memory falls below 4 GiB or its cgroup swaps. A persistent pod-local failure marker prevents an automatic memory-failure restart loop; inspect the logs and capacity before replacing that pod.

### Local checks

```bash
python3 -m pip install -r tests/requirements.txt
python3 -m unittest discover -s tests -v
```

Offline tests cover placement, workload envelopes, competing allocations, runtime arguments, every profile/phase render, deployment orchestration and endpoint schema compatibility.
