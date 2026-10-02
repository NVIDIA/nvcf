# LLM routing stack on DGX Spark

Deploy the LLM Gateway Stack (LLM API Gateway and request router) and Pylon Operator on an existing ARM64 DGX Spark Kubernetes cluster. The stack serves full GLM-5.3 `UD-IQ2_M` on exactly two GPUs and validates inference through authenticated gateway requests.

## Overview

The `spark.py` installer coordinates the combined gateway/router chart, the Pylon Operator chart and the GLM backend chart. The backend chart builds llama.cpp, downloads and verifies the GGUF model files, runs GLM across the two GPUs, and creates an `InferenceEndpoint` for Pylon to register.

[source.lock.json](spark/source.lock.json) pins the application source and gateway/router/operator charts, including the endpoint canary timing settings. `prepare` fetches that revision into your external work directory. Edit gateway/router code in that checkout when [iterating the deployment](#update-only-gateway-or-router).

## Prerequisites

- Hardware: two dedicated DGX Spark GB10 model nodes with one GPU each, plus a separate ARM64 control node for gateway/router/operator. Each model node needs more than 113 GiB host `MemAvailable` before loading.
- Kubernetes and storage: an existing ARM64 Kubernetes cluster containing these nodes, with pod networking, `cluster.local` DNS, NetworkPolicy enforcement and node-compatible `ReadWriteOnce` persistent storage. Reserve 400 GiB on the leader and 160 GiB on the worker.
- GPU enablement: model nodes with a working NVIDIA driver, NVIDIA Container Toolkit configured for the container runtime, and a device plugin advertising `nvidia.com/gpu`. Use an installed `RuntimeClass` matching `runtimeClass` in the config (`nvidia` in the example).
- Access: an authorized kubeconfig with an explicit context, cluster read access, and permission to create the namespace and install the Helm resources, including persistent volume claims (PVCs), the Pylon custom resource definition (CRD) and role-based access control (RBAC) resources.
- Workstation: Python 3.11+, Git, Helm 3.14+ or Helm 4, and kubectl. Provide access to GitHub, model downloads and container images. The [image build guide](spark/BUILDING.md) covers build tools and distribution credentials.

## Installation

Follow these steps for the first installation of the stack and GLM model.

### Configure and prepare

1. Start at the root of a repository checkout containing `deploy/helm/llm-routing/spark`. Use one Bash session and a separate external work directory for each installation.

   ```bash
   SPARK_RECIPE="$(pwd)/deploy/helm/llm-routing/spark"
   SPARK_WORK="$HOME/llm-spark-work"
   mkdir -m 700 "$SPARK_WORK"
   cp "$SPARK_RECIPE/config.example.json" "$SPARK_WORK/config.json"
   ```

2. Edit `config.json` with the actual deployment values.

   - Cluster: explicit context, unused namespace, release prefix and cluster ID.
   - Nodes: distinct `nodes.leader` and `nodes.worker`, with a separate `nodes.control` by default. To share the leader, qualify that placement with the same pre-load memory gate.
   - Runtime and storage: the installed runtime class and compatible storage class.
   - Images: registry prefix, fresh tag and distribution method. Keep `images.pullSecrets` empty for this Pylon version.

3. Define the runner, prepare the source, render the manifests and inventory the cluster.

   ```bash
   spark() { python3 "$SPARK_RECIPE/spark.py" --config "$SPARK_WORK/config.json" --work-dir "$SPARK_WORK" "$@"; }
   spark prepare
   spark render
   spark inventory
   ```

Check the results before continuing:

- `prepare` fetches the immutable source commit from `source.lock.json` and builds local Helm dependencies.
- Optional `--source-dir /absolute/path` selects an already prepared checkout at that revision. Existing source edits are preserved.
- `render` creates offline manifests and temporary Transport Layer Security (TLS) keys under the private work directory.
- `inventory` records node identities, GPU allocations, workloads, `RuntimeClass` and `StorageClass`. It establishes cluster identity for later phases and requires available model GPUs.
- CUDA correctness and memory qualification follow during preflight. Resolve namespace, CRD ownership or operator watch-scope conflicts before proceeding.

### Build and distribute the application images

Follow the [image build guide](spark/BUILDING.md) to build `gateway`, `router`, `pylon` and `operator` from the prepared source and distribute them to the nodes. It includes the build requirements and single-component rebuilds for later updates. Complete image distribution before continuing.

The NVIDIA CUDA environment image is separate from these four application images and is pinned by digest in [backend.defaults.json](spark/backend.defaults.json). GLM runs the compiled llama.cpp server inside that environment. Make the image available on both model nodes before preflight. If mirroring it, set `runtimeImage` to a verified equivalent digest.

### Deploy in order

The reference GPU environment uses NVIDIA driver `580.178.04` and CUDA 13. Run preflight qualification after changing these versions.

Run each command individually. Wait for its acceptance gate to pass before continuing.

```bash
spark preflight
spark stack
spark build-runtime
spark qualify
spark download
spark load
spark verify-direct
spark register
spark verify-gateway
```

1. `preflight`: Run bounded CUDA matrix comparisons in GPU-allocated Jobs on both nodes and record actual host memory. Both nodes must pass.
2. `stack`: Install operator/CRD/RBAC, the generated cluster credential, and gateway/router with static caller-key hashes and verified caller/tunnel TLS.
3. `build-runtime`: Build the pinned llama.cpp CUDA runtime with remote procedure call (RPC) support on the leader. Record the build archive checksum for later integrity checks.
4. `qualify`: Run upstream buffer-isolation and two-GPU matrix tests, then a dependent graph across the real RPC pair. Investigate a failed Job before proceeding.
5. `download`: Review the [external artifact terms](spark/NOTICE), including the custom GLM-5.3 model license. Download the six pinned GLM shards into the leader PVC and verify every size and SHA256.
6. `load`: Check memory headroom, replace the qualification servers with the two-node model layout and wait for direct health.
7. `verify-direct`: Check arithmetic, sorting, final answers, incremental server-sent events (SSE), usage and `[DONE]` through a loopback port-forward to the GLM Service.
8. `register`: Add `InferenceEndpoint/glm53-iq2` with the scoped canary settings. Require Ready, TransportReady and Registered.
9. `verify-gateway`: Repeat GLM requests through verified HTTPS and Pylon. Check missing/invalid keys, model discovery and healthy GLM registration.

Helm manages persistent resources, and every API operation uses the configured context. Once the runner records a loaded model, it blocks backend preflight, build, qualification, download and load phases to preserve that model. Use [the recovery workflow](#recovery-and-limits) for restart testing.

Failed Jobs and PVCs remain available for inspection. Resolve the cause before retrying. After an unsuccessful `qualify` phase, run `spark qualify --retry`. The runner saves the previous Job and pod status and available logs under `$SPARK_WORK/evidence`, then uses Helm to start qualification and chain Jobs with new attempt names. Failed Jobs from earlier Helm revisions may remain for inspection. It refuses to retry while an existing qualification or chain Job is active. Only successful pods belonging to the new Jobs can satisfy the acceptance gates.

Pinned runtime:

- Model revision: `346b3591c7f28d1a23716f97a065ecf12ec14771`, with 238,577,585,701 bytes across six GGUF shards.
- llama.cpp revision: `f872b591121761ac7b2af18283bd99bdc092a63a`. Each build records its own runtime archive checksum.
- Placement and capacity: two model nodes, equal layer split, context 2048, one request slot and conservative batch sizes.
- Integrity and health: checkpoint hashes, host memory guard and RPC probes are retained by the recipe.

## Authentication and TLS

The gateway uses static API keys for inference. Model and registry reads are public. Chat, responses and embeddings require a valid caller key. The selected backend determines which inference APIs it supports.

The operator chart generates a separate cluster token for Pylon to authenticate with the router. The runner handles it privately and passes its SHA256 to the router chart. It creates the caller key at `$SPARK_WORK/api-key` with mode 0600. To reuse an authorized key, set `apiKeyFile` in the external config. Keep the raw key because the gateway stores its hash.

The stack generates a certificate authority (CA) and listener/tunnel certificates. The runner saves the client CA at `$SPARK_WORK/ca.crt`. Client HTTPS and Pylon QUIC verify their CAs. Gateway/router HTTP, registration gRPC and Pylon/backend HTTP use plaintext inside the cluster. Keep that traffic on the trusted cluster network.

To use existing certificates:

1. Set `tls.selfSigned.enabled=false` in the external configuration.
2. Before `stack`, create Secrets `llm-gateway-stack-gateway-tls` and `llm-gateway-stack-router-tls` in the namespace with valid `tls.crt` and `tls.key` fields.
3. Create the configured CA ConfigMap with a `ca.crt` field. Certificates must cover the configured service names and client address.

## Verification

### Inspect the deployment

Read the context and namespace from your configuration, then inspect the deployment:

```bash
SPARK_CONTEXT=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["context"])' "$SPARK_WORK/config.json")
SPARK_NAMESPACE=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["namespace"])' "$SPARK_WORK/config.json")
kubectl --context "$SPARK_CONTEXT" get nodes -o wide
kubectl --context "$SPARK_CONTEXT" -n "$SPARK_NAMESPACE" get deployments
kubectl --context "$SPARK_CONTEXT" -n "$SPARK_NAMESPACE" get pods -o wide
kubectl --context "$SPARK_CONTEXT" -n "$SPARK_NAMESPACE" get services,inferenceendpoints
kubectl --context "$SPARK_CONTEXT" -n "$SPARK_NAMESPACE" describe inferenceendpoint glm53-iq2
```

The pod list shows which node runs each component. Check that deployments have their expected ready replicas and that the model endpoint is Ready, TransportReady and Registered.

`glm53-iq2` is the model InferenceEndpoint, and `pylon-glm53-iq2` is its operator-created transport Deployment. GLM leader/worker Deployments and PVCs use your configured release prefix.

### Send a chat or streaming request

1. Open the gateway port-forward and keep this workstation terminal running.

   ```bash
   kubectl --context "$SPARK_CONTEXT" -n "$SPARK_NAMESPACE" \
     port-forward svc/llm-api-gateway 18443:8080 --address 127.0.0.1
   ```

2. In another terminal, set the same path variables and send a chat or streaming request. Substitute the approved key path if you configured `apiKeyFile`.

   ```bash
   python3 "$SPARK_RECIPE/client.py" --ca-file "$SPARK_WORK/ca.crt" \
     --api-key-file "$SPARK_WORK/api-key" 'What is 17 multiplied by 19? Give one short sentence.'
   python3 "$SPARK_RECIPE/client.py" --ca-file "$SPARK_WORK/ca.crt" \
     --api-key-file "$SPARK_WORK/api-key" --stream 'Explain what a GPU does in two sentences.'
   ```

### Run the automated gateway checks

Stop any manual gateway port-forward on local port 18443, then run:

```bash
spark verify-gateway
```

The runner manages its own port-forward and checks GLM answers, streaming, missing/invalid API keys, public model discovery and registration under the configured cluster ID. It saves the report at `$SPARK_WORK/evidence/gateway.json`.

## Maintenance

### Update only gateway or router

To reuse a running stack from a new local work directory, [attach to it first](#update-an-existing-installation).

1. Keep the GLM release and its PVCs running. Use the same source lock as the installed stack, then edit the selected service under `$SPARK_WORK/source`.
2. Build and distribute that component with a fresh tag using the guide under [Build and distribute the application images](#build-and-distribute-the-application-images). Keep its `COMPONENT` and `NEW_TAG` variables in the same Bash session.
3. Update the selected image and verify gateway requests.

   ```bash
   spark update --component "$COMPONENT" --tag "$NEW_TAG"
   spark verify-gateway
   ```

4. Save the printed result path, for example `$SPARK_WORK/evidence/update-YYYYMMDDTHHMMSSZ.json`. Use it if rollback is needed.

The update uses the prepared stack chart with `--reuse-values` and changes the selected image tag. It records old/new tags and compares pod identities to verify that other workloads stay unchanged. GLM keeps running with its existing weights.

A gateway update briefly interrupts its requests. A router update reconnects Pylon transports. To publish source edits, follow [the pinned-stack validation steps](#updating-the-pinned-stack). Review and release chart-template changes separately.

To roll back the image update:

1. Replace the example filename below with the saved update record. The previous image must remain available in the node cache or registry.
2. Restore the recorded tag and verify requests.

   ```bash
   spark rollback --result "$SPARK_WORK/evidence/update-YYYYMMDDTHHMMSSZ.json"
   spark verify-gateway
   ```

Rollback verifies context, namespace, release, source identity and that the live tag still matches the recorded new tag. It uses the exact prepared chart to restore the previous image tag. If another update has occurred, use that update's record instead.

### Update an existing installation

Use this workflow to connect a new local work directory to a running stack and GLM model. It preserves the loaded model while you update gateway/router.

1. Create a separate external work directory and obtain an approved key file and cluster access settings from your team.
2. Set the path variables and define the `spark` helper from [Configure and prepare](#configure-and-prepare). Use the attachment commands in step 4 for this workflow.
3. Merge the following example fields into a full private configuration using the actual release names and image repositories.

   ```json
   {
     "releases": {"stack": "existing-gateway", "operator": "existing-operator", "glm": "existing-glm"},
     "images": {
       "prefix": "registry.example.com/team",
       "tag": "new-iteration-tag",
       "pullPolicy": "Never",
       "pullSecrets": [],
       "repositories": {
         "gateway": "registry.example.com/team/existing-gateway-image",
         "router": "registry.example.com/team/existing-router-image"
       }
     },
     "apiKeyFile": "/approved/local/path/api-key"
   }
   ```

   - Set the actual context, namespace, cluster ID, nodes, CA ConfigMap and registry/import settings.
   - Use the current resource names. Obtain deployment-specific values and credential paths outside GitHub.

4. Prepare the source, attach to the existing deployment and verify gateway requests.

   ```bash
   spark prepare
   spark attach-existing
   spark verify-gateway
   ```

5. Follow [Update only gateway or router](#update-only-gateway-or-router) with these explicit release names.

`attach-existing` uses read-only API calls to verify node identities, Helm ownership, cluster ID, gateway/router repositories and the GLM Service reference. It saves local state and a public CA copy while preserving existing workloads.

This attachment supports request checks and image iteration when the installed source marker matches `source.lock.json`. A stack installed without that marker, or with a different source identity, requires a coordinated stack installation before single-component updates. Fresh-install, backend-registration and recovery phases are disabled for attachments.

### Updating the pinned stack

[spark/source.lock.json](spark/source.lock.json) selects a tested immutable source revision. Upstream integration or history changes leave this deployment pinned to that revision.

After upstream history changes, run `spark prepare` in a fresh external work directory to check that the pinned revision remains fetchable. Branch retirement can leave it available, while squash or rebase integration may leave it outside maintained upstream history. Use the fetch result to decide whether the pin needs attention.

If the pin becomes unavailable, or you choose to adopt newer or merged code:

1. Commit runtime, API and chart fixes in their owning source directories, including generated files and regression tests.
2. Select a reachable commit containing those fixes and update the repository and full revision in `spark/source.lock.json`.
3. In a fresh external work directory, rerun `spark prepare`, `spark render` and the [local regression checks](#local-validation).
4. Build and deploy gateway and router together when their API contract changes. Both must use the same pinned source and compatible chart configuration.
5. Repeat the relevant deployment and integration checks in this README before claiming support for the new revision.

## Recovery and limits

1. Schedule a time when a model interruption is acceptable.
2. Run the explicit recovery check.

   ```bash
   spark recover --confirm-model-interruption
   ```

3. Save and review the results from the target cluster.

Helm interrupts the owned RPC worker. The check records a failed request or unavailable direct path, restores the worker in a `finally` block, then runs direct and gateway checks.

Observed on the tested two-GPU setup: about 26 minutes for a cold load and 11 minutes for the recovery check with cached weights. Measure these times in your environment.

Runtime guards and capacity:

- The memory supervisor stops the owned runtime below 1 GiB host `MemAvailable` or on model-process/cgroup swap. Keep the guard enabled when evaluating context size or other workloads.
- Host swap is recorded separately. Unrelated CPU memory may be paged while model allocations stay resident.
- CPU and GPU share physical memory. Use host memory measurements alongside process resident set size (RSS) and `memory.current`, which only partially reflect GPU allocations.
- The worker uses a checksum-verified NVMe loading cache, with model weights resident during inference.
- The RPC probe inspects the owned process and listening socket, preserving the single-client server's connection backlog for real traffic.
- GLM uses endpoint canary timeout 180 seconds and interval 60 seconds, with generation and inference checks. Other endpoints retain their defaults.
- Runtime settings are aggressive two-bit quantization, context 2048, one request slot and TCP/RPC. Qualify broader operating requirements before relying on them.

Keep the runtime source and generated evidence for troubleshooting. Both model PVCs remain after uninstall. Use your reviewed cleanup workflow for owned releases, preserving model caches and unrelated workloads.

## Local validation

1. Prepare the pinned source and set the `spark` helper as described under [Configure and prepare](#configure-and-prepare).
2. From the repository root, run the runner/client tests, runtime chart tests and offline render checks.

   ```bash
   python3 -m unittest discover -s deploy/helm/llm-routing/spark/tests -v
   python3 -m unittest discover -s deploy/helm/llm-routing/spark/charts/gguf-backend/tests -v
   spark render
   git diff --check
   ```
