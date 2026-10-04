# LLM routing stack on DGX Spark

Deploy the LLM Gateway Stack (LLM API Gateway and request router) and Pylon Operator on an existing ARM64 DGX Spark Kubernetes cluster. The stack serves full GLM-5.3 `UD-IQ2_M` on exactly two GPUs and validates inference through authenticated gateway requests.

## Overview

The `spark.py` installer coordinates the combined gateway/router chart, the Pylon Operator chart and the GLM backend chart. The backend chart builds llama.cpp, downloads and verifies the GGUF model files, runs GLM across the two GPUs, and creates an `InferenceEndpoint` for Pylon to register.

[source.lock.json](spark/source.lock.json) pins the application source and charts. `prepare` downloads that revision and prints the directory to edit for [gateway or router updates](#update-only-gateway-or-router).

## Prerequisites

- Hardware: two dedicated DGX Spark GB10 model nodes with one GPU each, plus a separate ARM64 control node for gateway/router/operator. Each model node needs more than 113 GiB host `MemAvailable` before loading.
- Kubernetes and storage: an existing ARM64 Kubernetes cluster containing these nodes, with pod networking, `cluster.local` DNS, NetworkPolicy enforcement and node-compatible `ReadWriteOnce` persistent storage. Reserve 400 GiB on the leader and 160 GiB on the worker.
- GPU enablement: model nodes with a working NVIDIA driver, NVIDIA Container Toolkit configured for the container runtime, and a device plugin advertising `nvidia.com/gpu`. Use an installed `RuntimeClass` matching `runtimeClass` in the config (`nvidia` in the example).
- Access: kubeconfig for the target cluster. For a kubeconfig with multiple contexts, pass `--context <name>` to the commands below. Initial installation requires creating namespaces, custom resource definitions (CRDs), role-based access control (RBAC) resources and namespaced Helm resources.
- Workstation: Python 3.11+, Git, Helm 3.14+ or Helm 4, and kubectl. Provide access to GitHub, model downloads and container images. The [image build guide](spark/BUILDING.md) covers build tools and distribution credentials.

## Installation

Follow these steps for the first installation of the stack and GLM model.

### Configure and prepare

Use your existing kubeconfig.

1. From the repository root, open the recipe directory and create the configuration.

   ```bash
   cd deploy/helm/llm-routing/spark
   python3 spark.py init
   ```

   Review the selected nodes and generated configuration. `init` configures idle GPUs, storage and image preload for K3s. For another container runtime, edit a copy of [config.example.json](spark/config.example.json) and pass `--config /path/to/config.json` instead of running `init`.

2. Prepare the source, render the manifests and inventory the cluster.

   ```bash
   python3 spark.py prepare
   python3 spark.py render
   python3 spark.py inventory
   ```

Check the rendered manifests and cluster inventory before deploying.

### Build and distribute the application images

Build and distribute `gateway`, `router`, `pylon` and `operator` using the [image build guide](spark/BUILDING.md).

Set `runtimeImage` in the configuration if using a mirror of the pinned CUDA image.

### Deploy in order

The reference GPU environment uses NVIDIA driver `580.178.04` and CUDA 13. Run preflight qualification after changing these versions.

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

1. `preflight`: Check GPU calculations and available memory on both model nodes.
2. `stack`: Install the gateway, router and Pylon Operator.
3. `build-runtime`: Build llama.cpp with CUDA and remote procedure call (RPC) support.
4. `qualify`: Test calculations and data transfer across both GPUs.
5. `download`: Download the six GLM files and verify their sizes and SHA256 checksums. See the [model and runtime licenses](spark/NOTICE).
6. `load`: Load GLM across both GPUs and wait for the model server.
7. `verify-direct`: Test model answers and streaming directly.
8. `register`: Register GLM with Pylon and wait for readiness.
9. `verify-gateway`: Test GLM through the gateway, including authentication and discovery.

Use [the recovery workflow](#recovery-and-limits) to test restarting a loaded model.

After fixing a failed qualification Job, run `python3 spark.py qualify --retry`.

Pinned runtime:

- Model revision: `346b3591c7f28d1a23716f97a065ecf12ec14771`, with 238,577,585,701 bytes across six GGUF shards.
- llama.cpp revision: `f872b591121761ac7b2af18283bd99bdc092a63a`.
- Capacity: two model nodes, equal layer split, context 2048 and one request slot.

## Authentication and TLS

The gateway requires an API key for inference. Model and registry reads are public.

Gateway clients use HTTPS and Pylon connects to the router over verified QUIC. Gateway/router HTTP, registration gRPC and Pylon/backend HTTP use plaintext inside the cluster.

To use existing certificates:

1. Set `tls.selfSigned.enabled=false` in the external configuration.
2. Before `stack`, create Secrets `llm-gateway-stack-gateway-tls` and `llm-gateway-stack-router-tls` in the namespace with valid `tls.crt` and `tls.key` fields.
3. Create the configured CA ConfigMap with a `ca.crt` field. Certificates must cover the configured service names and client address.

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

### Run the automated gateway checks

The check uses local port 18443. Stop a previous port-forward if it occupies that port.

```bash
python3 spark.py verify-gateway
```

Checks GLM answers, streaming, authentication, discovery and registration. Results are saved in `evidence/gateway.json` under the local work directory.

If the command reports incomplete key cleanup, run `python3 spark.py cleanup-key`.

## Maintenance

### Update only gateway or router

If this workstation has not used the running installation before, [attach to it first](#update-an-existing-installation).

1. Run `python3 spark.py prepare`, then edit the selected service in the source directory it prints.
2. [Build and distribute that component](spark/BUILDING.md#rebuild-gateway-or-router) with a fresh tag. Run the next commands in the same terminal.
3. Update the selected image and verify gateway requests.

   ```bash
   python3 spark.py update --component "$COMPONENT" --tag "$NEW_TAG"
   python3 spark.py verify-gateway
   ```

4. Save the printed update record path for rollback.

GLM stays loaded. Gateway updates briefly interrupt requests; router updates reconnect Pylon transports. For changes to the shared source revision, follow [Updating the pinned stack](#updating-the-pinned-stack).

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

### Updating the pinned stack

[spark/source.lock.json](spark/source.lock.json) selects the source revision used for builds.

After upstream history changes, check that the pinned revision remains fetchable using your configuration and a fresh work directory:

```bash
python3 spark.py --config /path/to/config.json --work-dir /path/to/fresh-work prepare
```

Use the same configuration and work directory options for the checks below.

To adopt a new source revision:

1. Commit runtime, API and chart fixes in their owning source directories, including generated files and regression tests.
2. Select a reachable commit containing those fixes and update the repository and full revision in `spark/source.lock.json`.
3. Repeat `prepare` and `render` with the explicit configuration and fresh work directory options above, then run the [local regression checks](#local-validation).
4. Build and deploy gateway and router together when their API contract changes. Both must use the same pinned source and compatible chart configuration.
5. Deploy the new revision and rerun gateway verification, recovery and image update/rollback checks.

## Recovery and limits

1. Schedule a time when a model interruption is acceptable.
2. Run the explicit recovery check.

   ```bash
   python3 spark.py recover --confirm-model-interruption
   ```

3. Save and review the results from the target cluster.

The recovery check restarts the RPC worker and verifies inference afterward.

The tested setup took about 26 minutes for a cold load and 11 minutes for recovery with cached weights.

Memory and runtime limits:

- CPU and GPU share memory. Monitor host `MemAvailable` when changing context size or adding workloads.
- The runtime stops below 1 GiB available memory or when the model process/container swaps.
- GLM uses two-bit quantization, context 2048, one request slot and TCP/RPC.
- GLM canary timing is 180 seconds for the timeout and 60 seconds for the interval.

Both model persistent volume claims (PVCs) remain after uninstall.

## Local validation

1. Prepare the pinned source as described under [Configure and prepare](#configure-and-prepare).
2. From the recipe directory, run the runner/client tests, runtime chart tests and offline render checks.

   ```bash
   python3 -m unittest discover -s tests -v
   python3 -m unittest discover -s charts/gguf-backend/tests -v
   python3 spark.py render
   git diff --check
   ```
