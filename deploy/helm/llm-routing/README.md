# NVCF Lite on DGX Spark

Deploy the LLM Gateway Stack (LLM API Gateway and request router) and Pylon Operator on an existing ARM64 DGX Spark Kubernetes cluster. This proof of concept (POC) uses exactly two GPUs to serve full GLM-5.3 `UD-IQ2_M` and validates inference through authenticated gateway requests. It installs no Kubernetes cluster, GPU driver, GPU Operator, device plugin, or host network configuration.

## Overview

This is the supported standalone POC entry point. It replaces the earlier CPU-only fixture deployment. The compatible gateway/router/operator implementation is pinned in [source.lock.json](spark/source.lock.json), with the reviewed [operator/chart patch](spark/patches/stack-fixes.patch). `prepare` fetches that public source into your external work directory. All app images and stack/operator charts come from the same source. Change gateway/router code in that prepared checkout when iterating this recipe.

The two-node runtime, direct/gateway chat, streaming, auth and recovery configuration has been exercised on Sparks. The generalized runner has offline render and regression coverage. Its read-only existing-installation attachment and portable client also passed against the running GLM deployment, including auth failures and retained routes. A deployment from this published recipe onto a fresh cluster has not yet been rehearsed. Do not treat render success as deployment success.

## Prerequisites

- An existing reachable Kubernetes cluster with ARM64 DGX Spark GB10 nodes, working pod networking, `cluster.local` DNS, NetworkPolicy enforcement, and an explicit kube context. No local or remote default context is assumed.
- Exactly two distinct model nodes, each advertising one `nvidia.com/gpu`, with the NVIDIA runtime class already configured and no other GPU process. The tested environment used driver `580.178.04` and CUDA 13. Other versions require qualification.
- About 121.69 GiB of OS-visible shared CPU/GPU memory per model node. The pre-load gate requires more than 113 GiB actual host `MemAvailable` on each node. Kubelet memory capacity and CUDA free-memory reports do not replace this measurement. A separate ARM64 control node is recommended for gateway/router/operator. You may use the leader as control node only if the same memory gate still passes. That two-machine placement has not been rehearsed.
- A storage class with node-compatible `ReadWriteOnce` storage. Reserve 400 GiB on the leader for six weight shards, source, build and runtime artifacts, and 160 GiB on the worker for its loading cache. The two PVCs remain on uninstall. A shared storage provider is acceptable only if its topology and performance support this placement.
- Namespace workload/Secret/ConfigMap access, plus permission to install the Pylon CRD and required RBAC. Use a new namespace. The runner rejects a Pylon CRD owned by another release. Coordinate existing operator watch scopes rather than adopting shared resources.
- Python 3.11+, Git, Helm 3.14+ or Helm 4, kubectl, Docker with Buildx, and an ARM64 build capability on the workstation. These are not installed on the cluster by the recipe.
- Network access for GitHub source, public model downloads, build dependencies and container images. Authenticate to required registries using your existing approved mechanism. Pylon does not propagate per-pod image-pull secrets in the pinned source. Use registry access configured on nodes or pre-import application images on every node where Pylon can schedule.
- Review the [external artifact terms](spark/NOTICE), including the custom GLM-5.3 model license, before downloading. No weights, credentials or private deployment evidence belong in Git.

The default CUDA/build environment is pinned by digest in [backend.defaults.json](spark/backend.defaults.json). Despite its image name, the GLM process is the compiled llama.cpp server, not vLLM. If the image must be mirrored, set `runtimeImage` to a verified equivalent digest and make it available on both nodes before preflight.

## Installation

For a new deployment, follow the steps below in order. For a running deployment, use [the existing-installation workflow](#iterate-an-existing-installation-with-different-release-names) after defining the path variables and `spark` helper below. Use `attach-existing` in place of `inventory` and skip the fresh deployment phases.

### Configure and prepare

Check out the POC branch, then run from the repository root in one Bash session. Keep the configuration and all generated files outside the checkout:

```bash
git clone --branch feat/standalone-llm-routing https://github.com/NVIDIA/nvcf.git
cd nvcf
SPARK_RECIPE="$(pwd)/deploy/helm/llm-routing/spark"
SPARK_WORK="$HOME/llm-spark-work"
mkdir -m 700 "$SPARK_WORK"
cp "$SPARK_RECIPE/config.example.json" "$SPARK_WORK/config.json"
```

Edit `config.json` for your explicit context, unused namespace, release prefix, cluster ID, node names, runtime/storage classes, image prefix/tag, and distribution method. Generic names in the example are placeholders, not a preconfigured cluster. `nodes.control` may equal `nodes.leader`. `nodes.leader` and `nodes.worker` must differ. Keep `images.pullSecrets` empty with this pinned Pylon version.

```bash
spark() { python3 "$SPARK_RECIPE/spark.py" --config "$SPARK_WORK/config.json" --work-dir "$SPARK_WORK" "$@"; }
spark prepare
spark render
spark inventory
```

`prepare` fetches public NVCF commit `198e867c3a8ff1dac0a0063f77ed1ea4c1761984`, verifies the patch checksum, applies the patch once, and builds local Helm dependencies. An optional `--source-dir /absolute/path` selects an already prepared checkout at that revision. Existing edits are not reset. Use a separate work directory for each installation. `render` creates private offline manifests, including temporary generated TLS keys, under the work directory and does not contact Kubernetes.

`inventory` records node identities, GPU allocations, workloads, RuntimeClass and StorageClass. It establishes cluster identity for later phases and refuses occupied model GPUs. It does not establish CUDA correctness or memory fit. If a namespace or CRD is already owned elsewhere, stop and resolve ownership instead of deleting it.

### Build and distribute the application images

```bash
spark build-images
spark push-images
```

The commands build and push `gateway`, `router`, `pylon` and `operator` under your configured registry prefix and tag. All builds target `linux/arm64`. The router and Pylon use Cargo profile `integration`, intended for functional checks rather than performance claims. The operator build includes the tested endpoint canary support. `push-images` publishes to the configured registry, so use a repository you are authorized to write. For normal registry pulls, use `IfNotPresent` and a fresh tag for every source revision.

If your environment requires preloading instead, skip `push-images`, set `images.pullPolicy` to `Never`, and export the four locally built images. Replace the prefix and tag below with your configuration:

```bash
IMAGE_PREFIX=registry.example.com/team/llm-poc
IMAGE_TAG=dev-1
docker save -o "$SPARK_WORK/arm64-images.tar" \
  "$IMAGE_PREFIX/gateway:$IMAGE_TAG" "$IMAGE_PREFIX/router:$IMAGE_TAG" \
  "$IMAGE_PREFIX/pylon:$IMAGE_TAG" "$IMAGE_PREFIX/operator:$IMAGE_TAG"
```

Copy that archive through your authorized transfer path into `containerd.archiveDirectory` on `containerd.archiveNode`. The example `/var/tmp/llm-poc-images` is a node path, not a workstation path. The serving Job's configured UID must be able to read the directory and archive. Set `containerd.nodeNames` to include every ARM64 node where Pylon can schedule. Configure the actual containerd socket path and a compatible `ctr` client in the image-loader chart if you are not using K3s. Import only the four small application images through this helper. Make the large CUDA environment image available through the registry or your normal node image-provisioning path separately.

```bash
spark import-images --archive "$SPARK_WORK/arm64-images.tar" --allow-containerd-import
```

Import uses explicit opt-in Jobs with access to the selected nodes' containerd sockets, which grants runtime administration. The server exposes only the dedicated archive directory and exits after acknowledgements. It checks the archive checksum and imports into the `k8s.io` namespace. The default importer needs less than 1 GiB per-node temporary archive storage. Increase its chart limit deliberately if your application archive exceeds that size. It does not modify the containerd configuration. Completed import Jobs are retained for inspection.

### Deploy in order

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

| Phase | Work and acceptance gate |
| --- | --- |
| `preflight` | GPU-allocated Jobs run a bounded CUDA matrix comparison on each selected node and record actual host memory. Both must pass. |
| `stack` | Install operator/CRD/RBAC, generated cluster credential, gateway/router with static caller-key hashes and verified caller/tunnel TLS. |
| `build-runtime` | Build the pinned llama.cpp CUDA/RPC runtime on the leader. Capture the actual build archive checksum for later integrity checks. |
| `qualify` | Run upstream buffer-isolation and two-GPU matrix tests, then a dependent graph across the real RPC pair. A failed Job stops progression. |
| `download` | Download the six pinned shards into the leader PVC and verify every size and SHA256. No alternate model is selected. |
| `load` | Require actual memory headroom, switch from small qualification servers to the two-node model layout, and wait for direct health. This phase does not register GLM. |
| `verify-direct` | Check arithmetic, sorting, final answer, incremental SSE, usage and `[DONE]` against the GLM Service through a loopback port-forward. |
| `register` | Add `InferenceEndpoint/glm53-iq2` with the scoped canary settings and require Ready, TransportReady and Registered. |
| `verify-gateway` | Repeat real requests through verified HTTPS and Pylon. Check missing/invalid keys, disallowed embeddings and each explicitly requested existing retained model. |

All persistent resources are managed through Helm. Every API operation passes the configured context. Preparation phases refuse to run after the runner records a loaded model. Do not use an earlier phase as a restart command. A failed phase saves logs but is not a successful checkpoint. Inspect failed Jobs before deciding how to retry them. The runner never deletes arbitrary failed Jobs or PVCs to make a retry succeed.

The model has 238,577,585,701 bytes in six GGUF shards at revision `346b3591c7f28d1a23716f97a065ecf12ec14771`. llama.cpp is pinned at `f872b591121761ac7b2af18283bd99bdc092a63a`. Each new build records its own runtime archive checksum. The known checkpoint hashes, runtime source, two model nodes, context 2048, single slot, equal layer split, conservative batch sizes, memory guard and RPC probes are preserved by the recipe.

## Authentication and TLS

By default the operator chart generates a cluster token. The runner reads it without logging it and passes only its SHA256 to the router chart. It creates a caller key at `$SPARK_WORK/api-key` with mode 0600. Set `apiKeyFile` in your external config to use an existing authorized key instead. The raw key is not recoverable from the gateway's hash configuration.

The stack generates its own CA and listener/tunnel certificates. The runner saves only the client CA as `$SPARK_WORK/ca.crt`. To use existing TLS, set `tls.selfSigned.enabled=false` and provision the required `llm-gateway-stack-gateway-tls` and `llm-gateway-stack-router-tls` Secrets and the configured CA ConfigMap in the namespace before `stack`. They must contain valid `tls.crt`/`tls.key`, the ConfigMap must contain `ca.crt`, and certificates must cover the configured service names and client address. Keep all private material outside Git.

Client HTTPS and Pylon QUIC verify their CAs. Internal gateway/router HTTP, registration gRPC, and Pylon/backend HTTP are plaintext inside the cluster. No insecure TLS bypass is enabled. `/v1/models` and `/v1/registry` are public under the chart's default policy, chat requires the API key, and embeddings are excluded from the allowed paths.

## Verification

### Inspect the deployment

Read context and namespace from your config, then inspect from the workstation that has the authorized kubeconfig:

```bash
SPARK_CONTEXT=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["context"])' "$SPARK_WORK/config.json")
SPARK_NAMESPACE=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["namespace"])' "$SPARK_WORK/config.json")
kubectl --context "$SPARK_CONTEXT" get nodes -o wide
kubectl --context "$SPARK_CONTEXT" -n "$SPARK_NAMESPACE" get pods,inferenceendpoints
helm --kube-context "$SPARK_CONTEXT" -n "$SPARK_NAMESPACE" list
k9s --context "$SPARK_CONTEXT" -n "$SPARK_NAMESPACE" --readonly
```

These use your existing authorized kubeconfig. If your cluster API needs an SSH tunnel, obtain an isolated kubeconfig and an approved API forwarding path from the cluster owner. An interactive export on a remote shell does not affect later noninteractive SSH commands. Any such remote command must supply its kubeconfig and context explicitly. Kubernetes API access is separate from the gateway request port-forward below.

The model InferenceEndpoint is `glm53-iq2`. The operator creates the separate Deployment `pylon-glm53-iq2`. The GLM leader/worker Deployments and PVCs use your configured release prefix.

### Custom chat, streaming and portable verification

Keep this workstation terminal open:

```bash
kubectl --context "$SPARK_CONTEXT" -n "$SPARK_NAMESPACE" \
  port-forward svc/llm-api-gateway 18443:8080 --address 127.0.0.1
```

In another terminal with the same path variables:

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

Use the explicit approved key path if you configured `apiKeyFile`. `verify` performs both regular and streamed GLM requests, negative auth checks, and each requested retained model. If your cluster has an existing Qwen route, check it with an additional `--retained-model Qwen/Qwen3-0.6B`. Qwen is not deployed by default and would require its own GPU beyond GLM's two. `--output /path/report.json` saves timing/content evidence without keys.

For direct comparison, use `spark verify-direct`, which owns and closes its port-forward. Or manually forward the leader Service, `<releasePrefix>-glm:8000`, to local 18000 and run `client.py --url http://127.0.0.1:18000 --mode verify`. Direct HTTP bypasses gateway auth and routing. Do not pass a caller key to a plaintext direct endpoint. Keep port-forwards on loopback. Avoid running a manual forward and a runner check on the same local port.

## Maintenance

### Update only gateway or router

Keep the GLM release and its PVCs running. Edit the corresponding service under `$SPARK_WORK/source`, then choose one component and a fresh image tag:

```bash
COMPONENT=gateway # Or router.
NEW_TAG=dev-$(date -u +%Y%m%d%H%M%S)
spark build-images --component "$COMPONENT" --tag "$NEW_TAG"
spark push-images --component "$COMPONENT" --tag "$NEW_TAG"
spark update --component "$COMPONENT" --tag "$NEW_TAG"
spark verify-gateway
```

For preloaded images, export the selected fresh image instead of pushing. Copy the archive to the configured node directory, then run `spark import-images --archive /local/path/arm64-images.tar --component "$COMPONENT" --tag "$NEW_TAG" --allow-containerd-import` before `update`. A gateway/router import targets only the configured control node. Source edits belong to the prepared owning checkout. Publishing those edits requires an updated public source pin or reviewed patch in this recipe, followed by the same validation. The update uses the prepared stack chart, `--reuse-values`, and changes only the selected image tag. It records the old/new tags and compares running pod identities, requiring other workloads to remain unchanged. It does not rebuild or reload GLM. Updating gateway interrupts its requests briefly. Updating router reconnects Pylon transports.

The result path is printed, for example `$SPARK_WORK/evidence/update-YYYYMMDDTHHMMSSZ.json`. To roll back exactly that image change:

```bash
spark rollback --result "$SPARK_WORK/evidence/update-YYYYMMDDTHHMMSSZ.json"
spark verify-gateway
```

Replace the example filename with the printed record. Rollback verifies context, namespace, release and that the live tag still equals the recorded new tag. It restores only the recorded previous image tag using the exact prepared chart, and refuses to overwrite a subsequent image update. The previous image must still be available in the node cache or registry. Chart-template changes require their own review and release operation.

### Iterate an existing installation with different release names

Use a separate external work directory and an approved key file. Add explicit release names and image repositories to its private configuration. These are generic examples, not an instruction to rename live resources:

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

Merge these fields into a full private configuration with the actual context, namespace, cluster ID, selected nodes, CA ConfigMap and registry/import settings. Add `test-model` to `retainedModels` only if that test route already exists and must be preserved. The generic example configuration alone is not an overlay for another person's cluster.

```bash
spark prepare
spark attach-existing
spark verify-gateway
```

`attach-existing` makes read-only API calls to verify node identities, Helm ownership, cluster ID, gateway/router repositories and the existing GLM Service reference. It saves only local state and a public CA copy. It does not create, adopt, rename or restart any workload. Fresh-install, backend-registration and recovery phases are disabled for this attachment. The image build/distribution, `update`, `rollback` and request checks above then work with the explicit existing release names. No private scratchpad scripts are required. Obtain actual access paths and credential files from your authorized team configuration outside GitHub.

### Updating the pinned stack

This recipe pins a tested immutable commit in [spark/source.lock.json](spark/source.lock.json) and does not track `feat/new-llm-stack`. Updates, merges or deletion of that branch do not themselves change this deployment or require an immediate upgrade.

After an upstream merge or branch retirement, verify that the pinned commit remains fetchable by running `spark prepare` in a fresh external work directory. A deleted branch can still have a fetchable commit, while a squash or rebase merge may leave the old commit outside `main` ancestry.

If the pin becomes unavailable, or when choosing to adopt newer or merged code:

1. Select a reachable revision from the maintained upstream history. Update `spark/source.lock.json` and the source revision cited in this README.
2. Review which bundled fixes landed upstream. Regenerate `spark/patches/stack-fixes.patch` for the remaining changes and update `patchSha256` in the lock file. The current runner requires and verifies a patch file. If every fix is upstream, add and test support for an unpatched source before removing the patch.
3. Use a fresh external work directory to rerun `spark prepare`, `spark render`, and the [local regression checks](#local-validation). Repeat the relevant deployment and integration checks in this README before claiming support for the new revision.

## Recovery and limits

The explicit recovery check interrupts only the owned RPC worker through Helm, records a failed request or unavailable direct path, restores the worker in a `finally` block, then runs direct and gateway checks:

```bash
spark recover --confirm-model-interruption
```

Run this only when a model interruption is acceptable. Save results from the fresh cluster. The earlier two-node run loaded cold in about 25 minutes and recovered with cached weights in about 10 minutes. These observations are not an SLA. Gateway/router image updates do not require model loading.

The memory supervisor stops the owned runtime below 1 GiB host MemAvailable or on model-process/cgroup swap. It records host swap separately because unrelated CPU memory can be paged while model allocations stay resident. CPU and GPU share physical memory. GPU allocations are not fully reflected by process RSS or cgroup memory.current. Do not disable the host memory guard to fit a larger context or additional workload.

The worker uses a checksum-verified NVMe loading cache. It does not page model weights during inference. The RPC probe inspects the owned process and listening socket instead of filling the single-client server's connection backlog. GLM uses endpoint canary timeout 180 seconds and interval 60 seconds, retaining generation and inference checks. Other endpoints keep their existing defaults.

Aggressive two-bit quantization, context 2048, one request slot, and TCP/RPC are deliberate limits. Broad quality evaluation, concurrent load, production readiness, offline deployment and a fresh-cluster rehearsal remain separate acceptance work. Keep the runtime source and generated evidence for troubleshooting. Remove only releases you own through your normal reviewed cleanup workflow, preserving model caches and unrelated workloads.

## Local validation

```bash
python3 -m unittest discover -s deploy/helm/llm-routing/spark/tests -v
python3 -m unittest discover -s deploy/helm/llm-routing/spark/charts/gguf-backend/tests -v
spark render
git diff --check
```

Mock responses exist only in test fixtures. The normal `stack` phase does not deploy a CPU mock. An explicit `testFixture: true` in a private test configuration can exercise the pinned sample and retained-route checks in an automated integration environment. It is not a model substitute or a separate supported demo.
