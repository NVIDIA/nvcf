# LLM routing stack on DGX Spark

Deploy the LLM Gateway Stack (LLM API Gateway and request router) and Pylon Operator on an existing ARM64 DGX Spark Kubernetes cluster. This proof of concept (POC) serves full GLM-5.3 `UD-IQ2_M` on exactly two GPUs and validates inference through authenticated gateway requests.

## Overview

- The stack uses the revision in [source.lock.json](spark/source.lock.json) and the bundled [operator/chart patch](spark/patches/stack-fixes.patch).
- `prepare` fetches that public source into your external work directory. All application images and stack/operator charts come from that checkout.
- Edit gateway/router code in the prepared checkout when [iterating the deployment](#update-only-gateway-or-router).

Validation coverage:

- The two-node runtime, direct/gateway chat, streaming, authentication and recovery configuration have been exercised on Sparks.
- The runner has offline render and regression coverage. Its existing-installation attachment and portable client passed against the running GLM deployment, including auth failures and retained routes.
- Fresh-cluster end-to-end validation of this published recipe remains pending.

## Prerequisites

### Cluster and access

- A reachable Kubernetes cluster with ARM64 DGX Spark GB10 nodes, working pod networking, `cluster.local` DNS and NetworkPolicy enforcement.
- An authorized kubeconfig and an explicit Kubernetes context for the target cluster.
- Exactly two distinct model nodes, each advertising one `nvidia.com/gpu`, with the NVIDIA runtime class configured and the GPU available exclusively for GLM.
- Driver `580.178.04` and CUDA 13 were tested. Qualify other versions before deployment.
- Workload, Secret and ConfigMap access in the target namespace, plus permission to install the Pylon custom resource definition (CRD) and its role-based access control (RBAC) resources.
- For a new deployment, use a new namespace. Resolve any existing Pylon CRD ownership and operator watch-scope overlap with the owner before proceeding.

### Memory and storage

- Each model node needs about 121.69 GiB of OS-visible shared CPU/GPU memory. The pre-load gate requires more than 113 GiB actual host `MemAvailable` per node.
- Measure host memory directly. Kubelet capacity and CUDA free-memory reports describe different views of the same shared memory.
- Use a separate ARM64 control node for gateway/router/operator. Sharing the leader requires the same memory gate and a separate placement qualification.
- Use node-compatible `ReadWriteOnce` storage. A shared provider must support the required topology and performance.
- Reserve 400 GiB on the leader for six weight shards, source, build and runtime artifacts, and 160 GiB on the worker for its loading cache.
- Both persistent volume claims (PVCs) are retained on uninstall. Preserve model caches during cleanup.

### Workstation and artifacts

- Install Python 3.11+, Git, Helm 3.14+ or Helm 4, kubectl, Docker with Buildx, and ARM64 build capability on the workstation. Install k9s for interactive inspection.
- Provide network access to GitHub source, public model downloads, build dependencies and container images. Authenticate to registries through your approved mechanism.
- For the pinned Pylon version, keep `images.pullSecrets` empty. Configure registry access on nodes or pre-import application images on every node where Pylon can schedule.
- Review the [external artifact terms](spark/NOTICE), including the custom GLM-5.3 model license, before downloading. Store weights, credentials and private deployment evidence outside Git.
- The CUDA/build environment is pinned by digest in [backend.defaults.json](spark/backend.defaults.json). GLM runs through the compiled llama.cpp server inside that environment.
- If mirroring the environment image, set `runtimeImage` to a verified equivalent digest and make it available on both model nodes before preflight.

## Installation

Choose the workflow for your cluster:

- New deployment: follow the preparation, image distribution and deployment steps below in order.
- Running deployment: use [the existing-installation workflow](#iterate-an-existing-installation-with-different-release-names). Define the path variables and `spark` helper below, then use `attach-existing` in place of `inventory`.

### Configure and prepare

1. Check out the POC branch and create an external work directory. Run from the repository root in one Bash session. Use a separate work directory for each installation.

   ```bash
   git clone --branch feat/standalone-llm-routing https://github.com/NVIDIA/nvcf.git
   cd nvcf
   SPARK_RECIPE="$(pwd)/deploy/helm/llm-routing/spark"
   SPARK_WORK="$HOME/llm-spark-work"
   mkdir -m 700 "$SPARK_WORK"
   cp "$SPARK_RECIPE/config.example.json" "$SPARK_WORK/config.json"
   ```

2. Edit `config.json` with the actual deployment values.

   - Cluster: explicit context, unused namespace, release prefix and cluster ID.
   - Nodes: distinct `nodes.leader` and `nodes.worker`. `nodes.control` may equal `nodes.leader` after qualifying that placement.
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

- `prepare` fetches public NVCF commit `198e867c3a8ff1dac0a0063f77ed1ea4c1761984`, verifies the patch checksum, applies the patch once and builds local Helm dependencies.
- Optional `--source-dir /absolute/path` selects an already prepared checkout at that revision. Existing source edits are preserved.
- `render` creates offline manifests and temporary Transport Layer Security (TLS) keys under the private work directory.
- `inventory` records node identities, GPU allocations, workloads, `RuntimeClass` and `StorageClass`. It establishes cluster identity for later phases and requires available model GPUs.
- CUDA correctness and memory qualification follow during preflight. Resolve namespace or CRD ownership conflicts before proceeding.

### Build and distribute the application images

The build produces four `linux/arm64` images: `gateway`, `router`, `pylon` and `operator`.

- Router and Pylon use Cargo profile `integration` for functional validation. Performance claims require separate qualification.
- The operator image includes the tested endpoint canary support.

#### Use a registry

1. Select a registry repository you are authorized to write. Configure `IfNotPresent` and a fresh tag for each source revision.
2. Build and push the four application images under your configured prefix and tag.

   ```bash
   spark build-images
   spark push-images
   ```

#### Preload images on the nodes

1. Run `spark build-images` and set `images.pullPolicy` to `Never`. Export the four local images, replacing the example prefix and tag with your configuration.

   ```bash
   IMAGE_PREFIX=registry.example.com/team/llm-poc
   IMAGE_TAG=dev-1
   docker save -o "$SPARK_WORK/arm64-images.tar" \
     "$IMAGE_PREFIX/gateway:$IMAGE_TAG" "$IMAGE_PREFIX/router:$IMAGE_TAG" \
     "$IMAGE_PREFIX/pylon:$IMAGE_TAG" "$IMAGE_PREFIX/operator:$IMAGE_TAG"
   ```

2. Copy the archive through your authorized transfer path to `containerd.archiveDirectory` on `containerd.archiveNode`.

   - The example `/var/tmp/llm-poc-images` is a node directory. The serving Job's configured user ID (UID) needs read access to it and the archive.
   - Set `containerd.nodeNames` to every ARM64 node where Pylon can schedule.
   - For runtimes other than K3s, configure the actual containerd socket path and a compatible `ctr` client in the image-loader chart.

3. Provision the large CUDA environment image separately through the registry or your normal node image-provisioning path. Use this helper for the four application images.
4. Import the archive and inspect the retained import Jobs.

   ```bash
   spark import-images --archive "$SPARK_WORK/arm64-images.tar" --allow-containerd-import
   ```

Importer behavior:

- Opt-in Jobs access the selected nodes' containerd sockets with runtime administration privileges.
- The server exposes the dedicated archive directory and exits after acknowledgements.
- The importer verifies the archive checksum and imports into the `k8s.io` namespace, preserving containerd configuration.
- Keep the application archive below 1 GiB for the default importer. Increase its chart storage limit for larger archives.

### Deploy in order

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
5. `download`: Download the six pinned GLM shards into the leader PVC and verify every size and SHA256.
6. `load`: Check memory headroom, replace the qualification servers with the two-node model layout and wait for direct health.
7. `verify-direct`: Check arithmetic, sorting, final answers, incremental server-sent events (SSE), usage and `[DONE]` through a loopback port-forward to the GLM Service.
8. `register`: Add `InferenceEndpoint/glm53-iq2` with the scoped canary settings. Require Ready, TransportReady and Registered.
9. `verify-gateway`: Repeat requests through verified HTTPS and Pylon. Check missing/invalid keys, disallowed embeddings and each requested retained model.

Runner behavior:

- Helm manages persistent resources. Every API operation uses the configured context.
- Once the runner records a loaded model, use [the recovery workflow](#recovery-and-limits) for restart testing. Preparation phases are gated at that point.
- A failed phase saves logs. Inspect its Jobs and resolve the cause before retrying. Failed Jobs and PVCs remain available for inspection.

Pinned runtime:

- Model revision: `346b3591c7f28d1a23716f97a065ecf12ec14771`, with 238,577,585,701 bytes across six GGUF shards.
- llama.cpp revision: `f872b591121761ac7b2af18283bd99bdc092a63a`. Each build records its own runtime archive checksum.
- Placement and capacity: two model nodes, equal layer split, context 2048, one request slot and conservative batch sizes.
- Integrity and health: checkpoint hashes, host memory guard and RPC probes are retained by the recipe.

## Authentication and TLS

Default credentials:

- The operator chart generates a cluster token. The runner handles it privately and passes its SHA256 to the router chart.
- The runner creates the caller key at `$SPARK_WORK/api-key` with mode 0600. To reuse an authorized key, set `apiKeyFile` in the external config.
- Keep the raw key file. The gateway stores its hash.

Default TLS and request policy:

- The stack generates a certificate authority (CA) and listener/tunnel certificates. The runner saves the client CA at `$SPARK_WORK/ca.crt`.
- Client HTTPS and Pylon QUIC verify their CAs.
- Gateway/router HTTP, registration gRPC and Pylon/backend HTTP use plaintext inside the cluster. Keep that traffic on the trusted cluster network.
- `/v1/models` and `/v1/registry` are public under the default policy. Chat requires the caller key, and the policy rejects embeddings.

To use existing certificates:

1. Set `tls.selfSigned.enabled=false` in the external configuration.
2. Before `stack`, create Secrets `llm-gateway-stack-gateway-tls` and `llm-gateway-stack-router-tls` in the namespace with valid `tls.crt` and `tls.key` fields.
3. Create the configured CA ConfigMap with a `ca.crt` field. Certificates must cover the configured service names and client address.

## Verification

### Inspect the deployment

1. Use the workstation with your authorized kubeconfig. If API access requires SSH forwarding, obtain an isolated kubeconfig and approved forwarding path from the cluster owner.
2. Read context and namespace from the config, then inspect the cluster and releases.

   ```bash
   SPARK_CONTEXT=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["context"])' "$SPARK_WORK/config.json")
   SPARK_NAMESPACE=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["namespace"])' "$SPARK_WORK/config.json")
   kubectl --context "$SPARK_CONTEXT" get nodes -o wide
   kubectl --context "$SPARK_CONTEXT" -n "$SPARK_NAMESPACE" get pods,inferenceendpoints
   helm --kube-context "$SPARK_CONTEXT" -n "$SPARK_NAMESPACE" list
   k9s --context "$SPARK_CONTEXT" -n "$SPARK_NAMESPACE" --readonly
   ```

Access and resource names:

- Each noninteractive remote command must supply its kubeconfig and context explicitly. Shell exports apply only to that shell session.
- Keep Kubernetes API forwarding separate from the gateway request port-forward below.
- `glm53-iq2` is the model InferenceEndpoint. `pylon-glm53-iq2` is the separate transport Deployment created by the operator.
- GLM leader/worker Deployments and PVCs use your configured release prefix.

### Custom chat, streaming and portable verification

1. Open the gateway port-forward and keep this workstation terminal running.

   ```bash
   kubectl --context "$SPARK_CONTEXT" -n "$SPARK_NAMESPACE" \
     port-forward svc/llm-api-gateway 18443:8080 --address 127.0.0.1
   ```

2. In another terminal, set the same path variables and run chat, streaming, verification and authentication checks. Substitute the approved key path if you configured `apiKeyFile`.

   ```bash
   python3 "$SPARK_RECIPE/client.py" --ca-file "$SPARK_WORK/ca.crt" \
     --api-key-file "$SPARK_WORK/api-key" 'What is 17 multiplied by 19? Give one short sentence.'
   python3 "$SPARK_RECIPE/client.py" --ca-file "$SPARK_WORK/ca.crt" \
     --api-key-file "$SPARK_WORK/api-key" --stream 'Explain what a GPU does in two sentences.'
   python3 "$SPARK_RECIPE/client.py" --ca-file "$SPARK_WORK/ca.crt" \
     --api-key-file "$SPARK_WORK/api-key" --mode verify
   python3 "$SPARK_RECIPE/client.py" --ca-file "$SPARK_WORK/ca.crt" \
     --api-key-file "$SPARK_WORK/api-key" --mode auth
   ```

Verification options:

- `verify` checks regular and streamed GLM requests, negative authentication cases and each requested retained model.
- For an existing Qwen route, add `--retained-model Qwen/Qwen3-0.6B`. That separate deployment requires its own GPU beyond GLM's two.
- `--output /path/report.json` saves timing and content evidence with keys excluded.

For direct backend comparison:

- Run `spark verify-direct` to let the runner open and close its own port-forward.
- Alternatively, forward leader Service `<releasePrefix>-glm:8000` to local 18000 and run `client.py --url http://127.0.0.1:18000 --mode verify`.
- Direct HTTP exercises the backend separately from gateway authentication and routing. Omit caller keys on this plaintext endpoint.
- Bind port-forwards to loopback and give each active manual or runner check its own local port.

## Maintenance

### Update only gateway or router

1. Keep the GLM release and its PVCs running. Edit the selected service under `$SPARK_WORK/source`.
2. Choose one component and a fresh tag, then build, distribute, update and verify it. The following commands use a registry. For preloaded images, replace the push with the import steps below before running `update`.

   ```bash
   COMPONENT=gateway # Or router.
   NEW_TAG=dev-$(date -u +%Y%m%d%H%M%S)
   spark build-images --component "$COMPONENT" --tag "$NEW_TAG"
   spark push-images --component "$COMPONENT" --tag "$NEW_TAG"
   spark update --component "$COMPONENT" --tag "$NEW_TAG"
   spark verify-gateway
   ```

3. Save the printed result path, for example `$SPARK_WORK/evidence/update-YYYYMMDDTHHMMSSZ.json`. Use it if rollback is needed.

For preloaded images:

1. Export the selected fresh image and copy the archive to the configured node directory.
2. Before `update`, run `spark import-images --archive /local/path/arm64-images.tar --component "$COMPONENT" --tag "$NEW_TAG" --allow-containerd-import`.
3. Continue with `spark update` and `spark verify-gateway` from the commands above. A gateway/router import targets the configured control node.

Update behavior:

- The update uses the prepared stack chart with `--reuse-values` and changes the selected image tag.
- It records old/new tags and compares pod identities to verify that other workloads stay unchanged. GLM keeps running with its existing weights.
- A gateway update briefly interrupts its requests. A router update reconnects Pylon transports.
- To publish source edits, update the public source pin or reviewed patch and follow [the pinned-stack validation steps](#updating-the-pinned-stack).
- Review and release chart-template changes separately.

To roll back the image update:

1. Replace the example filename below with the saved update record. The previous image must remain available in the node cache or registry.
2. Restore the recorded tag and verify requests.

   ```bash
   spark rollback --result "$SPARK_WORK/evidence/update-YYYYMMDDTHHMMSSZ.json"
   spark verify-gateway
   ```

- Rollback verifies context, namespace, release and that the live tag still matches the recorded new tag.
- It uses the exact prepared chart to restore the previous image tag. If another update has occurred, use that update's record instead.

### Iterate an existing installation with different release names

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
     "apiKeyFile": "/approved/local/path/api-key",
     "retainedModels": ["Qwen/Qwen3-0.6B"]
   }
   ```

   - Set the actual context, namespace, cluster ID, nodes, CA ConfigMap and registry/import settings.
   - Use the current resource names. Obtain deployment-specific values and credential paths outside GitHub.
   - Include only existing routes in `retainedModels`. Add `test-model` if that route already exists and must be preserved.

4. Prepare the source, attach to the existing deployment and verify gateway requests.

   ```bash
   spark prepare
   spark attach-existing
   spark verify-gateway
   ```

5. Follow the image build/distribution, `update` and `rollback` steps above with these explicit release names.

Attachment behavior:

- `attach-existing` uses read-only API calls to verify node identities, Helm ownership, cluster ID, gateway/router repositories and the GLM Service reference.
- It saves local state and a public CA copy while preserving existing workloads.
- This attachment supports image iteration and request checks. Fresh-install, backend-registration and recovery phases are disabled.

### Updating the pinned stack

Pin behavior:

- [spark/source.lock.json](spark/source.lock.json) selects a tested immutable commit. Upstream `feat/new-llm-stack` changes, merges or deletion leave this deployment pinned to that revision.
- After an upstream merge or branch retirement, run `spark prepare` in a fresh external work directory to verify that the pinned commit remains fetchable.
- Branch deletion can leave a commit fetchable. A squash or rebase merge may leave the old commit outside `main` ancestry. Use the fetch result to decide whether the pin needs attention.

If the pin becomes unavailable, or you choose to adopt newer or merged code:

1. Select a reachable revision from maintained upstream history. Update `spark/source.lock.json` and the source revision cited in this README.
2. Review which bundled fixes landed upstream. Regenerate `spark/patches/stack-fixes.patch` for remaining changes and update `patchSha256` in the lock file.
3. The runner requires and verifies a patch. If every fix is upstream, add and test support for an unpatched source before removing it.
4. In a fresh external work directory, rerun `spark prepare`, `spark render` and the [local regression checks](#local-validation).
5. Repeat the relevant deployment and integration checks in this README before claiming support for the new revision.

## Recovery and limits

1. Schedule a time when a model interruption is acceptable.
2. Run the explicit recovery check.

   ```bash
   spark recover --confirm-model-interruption
   ```

3. Save and review the results from the target cluster.

Recovery behavior:

- Helm interrupts the owned RPC worker. The check records a failed request or unavailable direct path, restores the worker in a `finally` block, then runs direct and gateway checks.
- Observed on the tested two-node setup: about 25 minutes for a cold load and 10 minutes for recovery with cached weights. Measure these times in your environment.

Runtime guards and capacity:

- The memory supervisor stops the owned runtime below 1 GiB host `MemAvailable` or on model-process/cgroup swap. Keep the guard enabled when evaluating context size or other workloads.
- Host swap is recorded separately. Unrelated CPU memory may be paged while model allocations stay resident.
- CPU and GPU share physical memory. Use host memory measurements alongside process resident set size (RSS) and `memory.current`, which only partially reflect GPU allocations.
- The worker uses a checksum-verified NVMe loading cache, with model weights resident during inference.
- The RPC probe inspects the owned process and listening socket, preserving the single-client server's connection backlog for real traffic.
- GLM uses endpoint canary timeout 180 seconds and interval 60 seconds, with generation and inference checks. Other endpoints retain their defaults.
- Runtime settings are aggressive two-bit quantization, context 2048, one request slot and TCP/RPC. Qualify broader operating requirements before relying on them.

Keep the runtime source and generated evidence for troubleshooting. Use your reviewed cleanup workflow for owned releases, preserving model caches and unrelated workloads.

## Local validation

1. Prepare the pinned source and set the `spark` helper as described under [Configure and prepare](#configure-and-prepare).
2. From the repository root, run the runner/client tests, runtime chart tests and offline render checks.

   ```bash
   python3 -m unittest discover -s deploy/helm/llm-routing/spark/tests -v
   python3 -m unittest discover -s deploy/helm/llm-routing/spark/charts/gguf-backend/tests -v
   spark render
   git diff --check
   ```

Test configuration:

- Set `testFixture: true` in a private test configuration to exercise the pinned sample and retained-route checks in an automated integration environment.
- Treat those fixture results as routing-test evidence. Validate model behavior with the GLM request checks above.
