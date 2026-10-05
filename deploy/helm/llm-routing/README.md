# LLM routing stack on DGX Spark

Deploy the LLM Gateway Stack (LLM API Gateway and request router) and Pylon Operator on an existing ARM64 DGX Spark Kubernetes cluster. The stack serves full GLM-5.3 `UD-IQ2_M` on exactly two GPUs and validates inference through authenticated gateway requests.

## Overview

The `spark.py` installer coordinates the combined gateway/router chart, the Pylon Operator chart and the GLM backend chart. The backend chart builds llama.cpp, downloads and verifies the GGUF model files, runs GLM across the two GPUs, and creates an `InferenceEndpoint` for Pylon to register.

Application images and charts use your checkout, including local edits.

## Prerequisites

- Hardware: two dedicated DGX Spark GB10 model nodes with one GPU each, plus a separate ARM64 control node for gateway/router/operator. Each model node needs more than 113 GiB host `MemAvailable` before loading.
- Kubernetes and storage: an existing ARM64 Kubernetes cluster containing these nodes, with pod networking, `cluster.local` DNS, NetworkPolicy enforcement and node-compatible `ReadWriteOnce` persistent storage. Reserve 400 GiB on the leader and 160 GiB on the worker.
- GPU enablement: model nodes with a working NVIDIA driver, NVIDIA Container Toolkit configured for the container runtime, and a device plugin advertising `nvidia.com/gpu`. Use an installed `RuntimeClass` matching `runtimeClass` in the config (`nvidia` in the example).
- Access: kubeconfig for the target cluster. For a kubeconfig with multiple contexts, pass `--context <name>` to the commands below. Initial installation requires creating namespaces, custom resource definitions (CRDs), role-based access control (RBAC) resources and namespaced Helm resources.
- Workstation: Python 3.11+, Git, Helm 3.14+ or Helm 4, and kubectl. Provide access to GitHub, model downloads and container images. The [image build guide](spark/BUILDING.md) covers build tools and distribution credentials.

## Installation

Follow these steps for the first installation of the stack and GLM model.

### Configure

Use your existing kubeconfig.

1. From the repository root, open the recipe directory and create the configuration.

   ```bash
   cd deploy/helm/llm-routing/spark
   python3 spark.py init
   ```

   Review the selected nodes and generated configuration. `init` discovers idle GPUs, storage and image preload settings for K3s. The configuration is saved at the printed path in a private work directory.

2. Render the manifests and inventory the cluster.

   ```bash
   python3 spark.py render
   python3 spark.py inventory
   ```

`render` lints the Helm charts and generates manifests. `inventory` checks node readiness, GPU availability and cluster prerequisites. Continue after both commands pass.

### Build and distribute the application images

Build and distribute `gateway`, `router`, `pylon` and `operator` using the [image build guide](spark/BUILDING.md).

### Deploy in order

Run each command in order and continue after it succeeds.

```bash
python3 spark.py preflight
python3 spark.py stack
python3 spark.py build-runtime
python3 spark.py qualify
python3 spark.py download
python3 spark.py load
python3 spark.py verify-direct
python3 spark.py register
python3 spark.py verify-gateway
```

1. `preflight`: Check GPU calculations and available memory on both model nodes. The reference GPU environment uses NVIDIA driver `580.178.04` and CUDA 13. Rerun `preflight` and `qualify` after changing these versions.
2. `stack`: Install the gateway, router and Pylon Operator. This generates the default caller API key and self-signed certificates. To supply your own, complete [Optional configuration](#optional-configuration) before running `stack`.
3. `build-runtime`: Build llama.cpp with CUDA and remote procedure call (RPC) support.
4. `qualify`: Test calculations and data transfer across both GPUs. (After fixing a failed qualification Job, run `python3 spark.py qualify --retry`.)
5. `download`: Download the six GLM files and verify their sizes and SHA256 checksums. See the [model and runtime licenses](spark/NOTICE).
6. `load`: Load GLM across both GPUs and wait for the model server.
7. `verify-direct`: Test model answers and streaming directly.
8. `register`: Register GLM with Pylon and wait for readiness.
9. `verify-gateway`: Test GLM answers, streaming, authentication, discovery and registration through the gateway. For failures, see [Gateway check troubleshooting](#gateway-check-failures).

Pinned runtime:

- Model revision: `346b3591c7f28d1a23716f97a065ecf12ec14771`, with 238,577,585,701 bytes across six GGUF shards.
- llama.cpp revision: `f872b591121761ac7b2af18283bd99bdc092a63a`.
- Capacity: two model nodes, equal layer split, context 2048 and one request slot.

## Verification

Run these commands from `deploy/helm/llm-routing/spark` after installation or [attachment to an existing stack](#update-an-existing-installation).

### Inspect the deployment

```bash
context="$(python3 spark.py context)" &&
  kubectl --context "$context" get nodes -o wide &&
  kubectl --context "$context" get deployments,pods,services,inferenceendpoints --all-namespaces -o wide
```

Check the node placement, ready replicas and model endpoint status.

### Send a chat or streaming request

```bash
python3 spark.py chat 'What is 17 multiplied by 19? Give one short sentence.'
python3 spark.py chat 'Explain what a GPU does in two sentences.' --stream
```

## Maintenance

### Update only gateway or router

If this workstation has not used the running installation before, [attach to it first](#update-an-existing-installation).

1. Edit the service in your checkout: `src/invocation-plane-services/llm-api-gateway` for gateway or `src/libraries/rust/stargate` for router.
2. [Build and distribute that component](spark/BUILDING.md#rebuild-gateway-or-router) with a fresh tag. Run the next commands in the same terminal.
3. Update the selected image and verify gateway requests.

   ```bash
   python3 spark.py update --component "$COMPONENT" --tag "$NEW_TAG"
   python3 spark.py verify-gateway
   ```

4. Save the printed update record path for rollback.

GLM stays loaded. Gateway updates briefly interrupt requests. Router updates reconnect Pylon transports. Image-only updates require unchanged routing charts. For changes to the required source revision, follow [Update the source baseline](#update-the-source-baseline).

To roll back the image update:

1. Replace the example filename below with the saved update record. The previous image must remain available in the node cache or registry.
2. Restore the recorded tag and verify requests.

   ```bash
   python3 spark.py rollback --result /path/to/saved-update.json
   python3 spark.py verify-gateway
   ```

Use the record from the latest update when rolling back.

### Update an existing installation

Use your existing kubeconfig.

1. From the repository root, open the recipe directory.

   ```bash
   cd deploy/helm/llm-routing/spark
   ```

2. Discover the installation and verify gateway requests.

   ```bash
   python3 spark.py attach-existing
   python3 spark.py verify-gateway
   ```

Add `--namespace <namespace>` to attachment when the cluster has multiple installations or your access is limited to one namespace.

Continue with [Update only gateway or router](#update-only-gateway-or-router).

### Uninstall

Run the entire block, including parentheses, from `deploy/helm/llm-routing/spark` in the same configured terminal used for installation. The context lookup uses the recipe's normal selection. If you passed `--context`, `--config` or `--work-dir` during installation, pass the same options before `context` in the lookup below.

The namespace and release names below are the default K3s recipe values. If you changed `namespace`, `releasePrefix` or `releases` in your saved configuration, replace these names to match. The model chain release is the GLM release name plus `-chain`, and the image-import release is the release prefix plus `-images`.

The block skips absent releases, including the optional image importer, and stops on other failures. It keeps the operator running until endpoint cleanup finishes.

```bash
(
  set -eu
  context="$(python3 spark.py context)"
  : "${context:?Context lookup returned an empty value}"
  namespace=llm-spark-poc
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
python3 spark.py init
python3 spark.py render
python3 spark.py inventory
```

`init` checks that the demo is uninstalled, reuses the saved placement, image references and credentials, and archives stale progress under `before-reinit-*` in the work directory. Continue with [Deploy in order](#deploy-in-order), starting at `preflight`.

### Update the source baseline

[spark/source.lock.json](spark/source.lock.json) records the baseline required by the recipe. Update it when the recipe requires newer runtime, API or chart changes.

1. Commit runtime, API and chart fixes in their owning source directories, including generated files and regression tests.
2. Update the repository and full baseline revision in `spark/source.lock.json`. The revision must exist in the selected checkout and be an ancestor of its `HEAD`.
3. Render the manifests, then run the [local regression checks](#local-validation).

   ```bash
   python3 spark.py render
   ```

4. Build and deploy gateway and router together when their API contract changes. Use the same checkout and compatible chart configuration for both.
5. Rerun gateway verification, recovery and image update/rollback checks.

Use `--source-dir /path/to/existing/checkout` on commands to select another checkout.

### Recovery and limits

This optional resilience check restarts the RPC worker, interrupts model service and verifies inference after recovery.

1. Schedule a time when a model interruption is acceptable.
2. Run the explicit recovery check.

   ```bash
   python3 spark.py recover --confirm-model-interruption
   ```

3. Save and review the results from the target cluster.

The tested setup took about 26 minutes for a cold load and 11 minutes for recovery with cached weights.

Memory and runtime limits:

- CPU and GPU share memory. Monitor host `MemAvailable` when changing context size or adding workloads.
- The runtime stops below 1 GiB available memory or when the model process/container swaps.
- GLM uses two-bit quantization, context 2048, one request slot and TCP/RPC.
- GLM canary timing is 180 seconds for the timeout and 60 seconds for the interval.

Both model persistent volume claims (PVCs) remain after uninstall.

## Optional configuration

### Alternative container runtimes and external configuration

For a non-K3s cluster or custom container runtime, prepare an external copy of [config.example.json](spark/config.example.json) before installation. Set the context, node placement, storage, runtime and image settings for your cluster. Use that file instead of `init`, and pass `--config /path/to/config.json` to each recipe command, starting with `render` and `inventory`.

### Runtime image mirror

To use a mirror of the pinned CUDA image, set `runtimeImage` in the saved configuration before running `preflight`.

### API keys

The gateway requires an API key for inference. Model and registry reads are public. By default, `stack` saves the caller key as `api-key` in the private work directory, and the recipe client uses it automatically.

To supply your own key, set `apiKeyFile` in the saved configuration to a file containing the key before running `stack`.

### TLS certificates

Gateway clients use HTTPS and Pylon connects to the router over verified QUIC. Gateway/router HTTP, registration gRPC and Pylon/backend HTTP use plaintext inside the cluster.

To use existing certificates, complete these steps before running `stack`:

1. Set `tls.selfSigned.enabled=false` in the external configuration.
2. Create Secrets `llm-gateway-stack-gateway-tls` and `llm-gateway-stack-router-tls` in the namespace with valid `tls.crt` and `tls.key` fields.
3. Create the configured CA ConfigMap with a `ca.crt` field. Certificates must cover the configured service names and client address.

## Troubleshooting

If a command fails, follow the next check and diagnostic log path printed by the CLI. Detailed tool output is saved in private `evidence/*.log` files inside the work directory. Use `python3 spark.py paths` to locate that directory.

### Gateway check failures

If `verify-gateway` fails, inspect its results in `evidence/gateway.json` under the local work directory. The check uses local port 18443. Stop a previous port-forward if it occupies that port.

If the command reports incomplete key cleanup, run `python3 spark.py cleanup-key`.

After resolving the problem, rerun the check from the recipe directory:

```bash
python3 spark.py verify-gateway
```

## Local validation

From the recipe directory, run the runner/client tests, runtime chart tests and offline render checks.

```bash
python3 -m unittest discover -s tests -v
python3 -m unittest discover -s charts/gguf-backend/tests -v
python3 spark.py render
git diff --check
```
