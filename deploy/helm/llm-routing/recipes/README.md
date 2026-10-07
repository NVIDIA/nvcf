# Independent models on DGX Spark

Choose models and workload requirements. The planner chooses supported profiles and available Sparks, then renders an independent Helm release for each model. The shared gateway and Pylon Operator stay installed when a model is added or removed.

The Qwen3.8-27B FP8 and NVIDIA NVFP4 profiles passed live Spark smoke tests on October 6, 2026, with context length configured to 8,192 and concurrency 1. After installing a shared stack with an empty registry, both independent precision releases passed discovery, short-prompt chat, streaming and authentication through the same gateway address and credential.

Suspending NVFP4 returned HTTP 503 while FP8 continued serving chat and streaming. The shared infrastructure and FP8 serving pods remained unchanged. NVFP4 recovered using its retained cache, and both models passed verification again. These checks did not exercise the full configured context, benchmark performance or evaluate output quality. Flash-Next profiles remain unverified on Spark.

Startup and readiness probes allow five seconds because SGLang health checks can take more than one second.

## Models and placement

| Model | Precision | Available Spark profiles |
| --- | --- | --- |
| Qwen3.8-27B | FP8 or NVIDIA NVFP4 | Independent single-GPU precision recipes, no speculation, float32 SSM state |
| Qwen3.8-Flash-Next | NVFP4 | One GPU with NVMe file offload, or two GPUs with tensor parallelism |

These are supported candidate topologies, not required node counts supplied by the user. The default planner selects the fewest Sparks that satisfy every requested model's workload and capacity constraints. `--preference latency` prefers in-memory profiles over NVMe offload when enough nodes are available. This preference is a placement policy, not a measured latency guarantee.

Each profile declares memory, storage, CPU, context and concurrency requirements. The planner accounts for current pod requests, including init containers and sidecars, rejects occupied/shared GPUs, and places all selected models together without overlap. It does not evict workloads or re-shard running models. Unknown or unsupported capacity fails with reasons instead of inventing a topology.

Each Qwen3.8-27B precision has its own served model ID, release, endpoint and cache. Use `qwen3.8-27b` for FP8 and `qwen3.8-27b-nvfp4` for NVIDIA NVFP4. The NVFP4 checkpoint contains FP8 attention and GDN projections with NVFP4 MLPs and language-model head. The runtime reads that mixed quantization from the pinned checkpoint without a forced quantization override. Both precision recipes reserve the same conservative Spark memory envelope until separate hardware measurements justify different limits.

The initial Qwen3.8-27B envelope is at most 9,216 total context tokens and one concurrent request, matching the cookbook's tested request envelope. Flash-Next profiles allow up to 262,144 context tokens, with concurrency up to 8 for NVMe offload and 24 for two-node serving. Defaults are 8,192 total context tokens and concurrency 1. These are limits for candidate testing, not measured performance claims.

Source recipes:

- [Qwen3.8-27B](https://lmsysorg.mintlify.app/cookbook/autoregressive/Qwen/Qwen3.8-27B)
- [NVIDIA Qwen3.8-27B NVFP4 checkpoint](https://huggingface.co/nvidia/Qwen3.8-27B-NVFP4)
- [Qwen3.8-Flash-Next](https://lmsysorg.mintlify.app/cookbook/autoregressive/Qwen/Qwen3.8-Flash-Next)

The H200 BF16 Flash-Next configuration does not describe Spark placement. The Spark profiles use the RadixArk NVFP4 checkpoint. Images are pinned to public Linux ARM64 manifest digests and checkpoints to commit revisions in [catalog.json](catalog.json). Review [model and runtime terms](NOTICE) before downloading weights.

## Shared infrastructure

For a new installation, follow the [shared infrastructure flow](../README.md) and verify its empty registry before adding models. `stack.py` exports a private `connection.json` that binds recipes to the installed stack identity and credentials. You can also use an existing gateway/operator installation, including installations made directly with the [LLM Gateway Stack](../../llm-gateway-stack/README.md#install) and [Pylon Operator](../../pylon-operator/README.md) charts. Pass `--kube-context "$CONTEXT"` to every Helm command and `--context "$CONTEXT"` to every kubectl command in those guides.

For the first test, deploy these recipes in the namespace already watched by the operator. A Spark installation's operator may watch only its original namespace. Additional namespaces must be configured by the stack administrator before registering models. Reuse the existing gateway address, CA and caller key. Do not run GLM's `init`, `stack` or uninstall sequence to add a Qwen model.

Prerequisites:

- Python 3.11+, kubectl and Helm on the workstation.
- Ready Linux ARM64 GB10 nodes with exclusive `nvidia.com/gpu` allocation, GPU product labels, cgroup v2 and an installed NVIDIA RuntimeClass.
- A `WaitForFirstConsumer` StorageClass for per-node retained model caches. Check physical disk capacity before installation; Kubernetes allocatable storage does not establish PVC capacity.
- For NVMe offload, the node's Kubernetes ephemeral storage must reside on local NVMe. The planner requires this explicit capability because Kubernetes does not discover it reliably.
- For two-node tensor parallelism, both nodes must share a 200GbE fabric. Supply its actual IPv4 addresses and interface names. This chart initially uses NCCL TCP over that fabric with host networking, not RDMA. Live collective qualification is required before model download. Runtime ports are generated per release and must be free on both nodes.
- The pinned images must be pullable or preloaded on selected nodes. Allow model download access during the download phase. Optional Hugging Face credentials are referenced by `hfTokenSecret` (key `token`), never embedded in a plan.

## Plan and render

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

To test Flash-Next, select `--model qwen3.8-flash-next` instead of the NVFP4 27B recipe and add `--capabilities "$WORK/capabilities.json"`. Copy [capabilities.example.json](capabilities.example.json) into that private file and replace its documentation addresses and node names. Set `localNvme=true` only after checking the node's ephemeral storage location, and omit fabric fields on nodes without the required link. The precision pair above needs no NVMe-offload or fabric capability declarations.

Optional workload settings:

- `--context-length` and `--concurrency` set the defaults for selected models.
- `--requirements /path/to/requirements.json` sets per-model overrides, for example `{"qwen3.8-flash-next":{"contextLength":32768,"concurrency":9}}`. This excludes the one-node profile.
- `--no-nvme-offload` requires in-memory serving. With FP8 27B and Flash-Next selected, this needs three idle Sparks.
- `--preference latency` prefers the distributed Flash-Next profile when eligible capacity exists.

## Test on Spark

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

The download phase populates the pinned snapshot on every rank. Serving uses that local snapshot and refuses to run without a successful download marker. Rank zero waits for rank one's startup marker before entering the distributed runtime. The Service selects rank zero only; the operator creates the Pylon transport automatically.

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

Record results against the exact catalog/image/model pins. Update validation status only after those checks pass. The catalog does not claim tool calling, vision, Station hardware or arbitrary context/concurrency beyond the declared profiles has been tested.

## Lifecycle

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

Recheck registry health and inference after recovery. Suspending one recipe does not change another recipe or shared credentials.

Changing a distributed topology requires stopping both ranks before restarting the group. Do not use a rolling update to move one rank to another node while the other remains active. Re-plan and requalify the group before serving it again. Retained local cache claims remain bound to their original nodes; a new placement needs separate caches or an explicit data migration. The planner does not migrate stored weights. Do not rerun preparation phases against a serving release as an update shortcut.

The runtime checks host `MemAvailable` before loading. Its watchdog stops its own process group if host available memory falls below 4 GiB or its cgroup swaps. A persistent pod-local failure marker prevents an automatic memory-failure restart loop; inspect the logs and capacity before replacing that pod.

## Local checks

```bash
python3 -m pip install -r tests/requirements.txt
python3 -m unittest discover -s tests -v
```

Tests cover placement, workload envelopes, competing allocations, runtime arguments, every profile/phase render, and endpoint schema compatibility. They do not deploy workloads or download model weights.
