# Advanced deployment and configuration

## Helm configuration

The [main guide](README.md) uses local chart archives and an explicit values file. Build these archives with [package-charts.sh](dev-artifacts/package-charts.sh). A chart source directory can also be installed after its local dependencies are built. Helm and kubectl use your current kubeconfig context. Use their `--kube-context` and `--context` flags for an explicit override.

The supported installation is the `llm-stack` release in namespace `llm-stack`. It contains the gateway, router, Pylon operator and InferenceEndpoint CRD. Independent model releases use that stack in the same namespace. Use [shared-stack/values.local.example.yaml](charts/shared-stack/values.local.example.yaml) for image references and placement, with the operator watching `llm-stack`. Images may come from a registry or a node cache. [Local image preparation](dev-artifacts/README.md) is a temporary development convenience.

`operator.installCRDs` inherits the operator chart's boolean default, `true`. The CRD stays in the chart templates so upgrades of the owning release update its schema. Its `helm.sh/resource-policy: keep` annotation retains it and existing InferenceEndpoints on uninstall. Set `operator.installCRDs=false` only when a compatible CRD is managed externally. The chart checks compatibility but does not change that CRD's schema or ownership. Do not delete the CRD to resolve a Helm ownership conflict.

The shared chart generates the caller key in `llm-shared-caller-key` and the transport credential in `llm-shared-cluster-token`. Existing installations retain these Secrets and their CA during upgrades and reinstalls. For an existing caller credential, set `callerKey.existingSecret` to a Secret containing `api-key`. For an existing transport credential, set `clusterCredential.create: false` and `operator.credential.existingSecret` to a Secret containing `cluster-token`. Both Secrets must be in the release namespace. Keep generated credentials and site values in private storage.

TLS customization uses `gatewayStack.tls` and the child chart listener TLS values. See [shared-stack/values.yaml](charts/shared-stack/values.yaml) and the [gateway chart](../llm-gateway-stack/llm-gateway-stack/values.yaml). Model values reference the same CA ConfigMap through `sharedCAConfigMap`.

To configure additional callers, create a Secret containing `api-key` for each caller in the release namespace, then list them in shared values:

```yaml
extraCallerKeys:
  - id: demo-ui
    secretName: demo-ui-api-key
```

Caller IDs and trimmed keys must be distinct, including the primary caller. Upgrades regenerate authentication hashes from these configured Secrets. Removing an entry revokes it after the gateway reloads its authentication file; unlisted live entries are not preserved.

### Shared Helm installation and verification

Use the context selected in the [installation guide](README.md#1-install-shared-infrastructure). The shared chart runs a verification Job after installation and upgrade. It uses the installed CA to check gateway TLS and the `/v1/models` response, then checks that an unknown model returns HTTP 401 with an invalid key and HTTP 404 with the valid key. An empty registry is valid on first installation. Existing models remain valid on upgrades. A failed check makes the Helm command fail:

```bash
helm upgrade --install llm-stack dev-artifacts/charts/llm-shared-stack-0.1.0.tgz \
  --namespace llm-stack --create-namespace \
  --values dev-artifacts/values.yaml --wait --timeout 10m
```

The Job retries temporary connection failures, gateway unavailability and HTTP 401 for the configured caller key for up to 180 seconds. This allows time for Secret projection and authentication reload after key rotation. TLS failures and other unexpected authentication responses fail immediately. Successful verification Jobs are removed. Failed Jobs and their logs remain available until the next install or upgrade.

The verification image must be pullable or preloaded on eligible nodes. See [verification image distribution](dev-artifacts/README.md#verification-image) for its default and offline settings.

### Shared CLI connection options

`llm.py` uses the current kubeconfig context and the `llm-stack` installation. To select another cluster, put `--context` before the command:

```bash
python3 llm.py --context my-cluster models
python3 llm.py --context my-cluster chat \
  --model qwen3.8-27b 'Say hello in one short sentence.'
```

The `recipes` command reads the local catalog and works without cluster access. The `models` and `chat` commands retrieve the installed CA and caller credential for each call. Their local connection and temporary files last only for that command. Chat uses the model's normal generation defaults; pass `chat --max-tokens N` to limit output. Reaching that limit returns the partial answer. The separate verifier retains deterministic prompts and strict completion checks. The `plan` command reads Kubernetes and Helm inventory and never opens a gateway connection. Its install command defaults to the recipe source chart. Pass `plan --chart-source PATH_OR_REFERENCE` to select a local archive or published Helm chart. Use `--ca-configmap NAME` and `--api-key-file FILE` to select an existing CA and caller key. Image preparation remains in the [image build guide](dev-artifacts/README.md).

### Model capacity check

Run from `deploy/helm/llm-routing`. Check one model against cluster allocations before copying its printed Helm command:

```bash
python3 llm.py --namespace llm-stack plan \
  --model qwen3.8-27b --runtime-class nvidia --storage-class local-path --verbose
```

The default view shows requirements, a GPU allocation table and a model cache table across namespaces. It discovers recipe-owned claims by their Helm ownership annotations and recipe cache names, plus claims referenced by GPU pods. Retained recipe claims remain visible after uninstall. Arbitrarily named external claims without a GPU pod reference are not identified as model storage.

The cache table reports allocated disk blocks from `du -sk` in existing running containers with a full-volume mount. Partial mounts are skipped. Measurements have a ten-second timeout per attempt. `Not mounted`, `Not provisioned` and `Unavailable` are distinct from measured zero usage. The planner does not use SSH, create pods or mount retained volumes. Storage reads require permission to list persistent volume claims and volumes; measurements also require container exec permission and `du` in the image. Missing permissions do not discard the GPU table. Claimed capacity is not actual disk usage or free physical disk.

Use `--verbose` for deployment checks, per-pod reservations, scheduler messages and installation commands. A pending GPU workload blocks a new placement until resolved. An existing model endpoint or Helm release in the selected namespace suppresses a fresh installation command, including a suspended release. For a stopped model, follow [Stop and resume](README.md#stop-and-resume) using its existing release. For an uninstalled release with retained claims, the planner restricts placement to their original nodes and checks ownership, Bound capacity and filesystem access. Unknown or conflicting cache placement, or an incompatible claim, blocks a fresh command. These checks do not verify cache contents. Reuse the original release, chart and cache placement. Printed commands use `helm install`, so they cannot upgrade another deployment.

Options:

- `--profile NAME` restricts the check to one catalog hardware profile.
- `--context-length TOKENS --concurrency COUNT` checks a requested workload against the recipe's supported envelope. SGLang commands include these overrides. GGUF profiles use fixed recipe tuning and reject workload overrides.
- `--capabilities FILE` supplies verified per-node NVMe, device-memory or fabric facts. Start from [capabilities.example.json](recipes/capabilities.example.json), replace example addresses and names, and save it outside the checkout.
- `--runtime-class NAME --storage-class NAME --shared-ca-configmap NAME` sets the installation's site values. The check requires the RuntimeClass and a StorageClass with `WaitForFirstConsumer` and unrestricted topology. It does not verify the CA or gateway connection.
- `--release NAME` changes the name of a new release. It does not adopt or replace an existing deployment.
- `--verbose` shows detailed diagnostics and an install command when all deployment checks pass.
- `--json` prints the complete report, including storage measurements and errors, as JSON for other tools. Exit status is `0` when a new placement fits and `2` when blocked, unsupported or already deployed.

The report checks GPU, CPU and memory reservations, hardware compatibility, node readiness, resource pressure and exclusive GPU requirements. NVMe offload also checks reserved ephemeral storage and the supplied local-NVMe fact. Distributed profiles require compatible fabric facts. Kubernetes still schedules the pods; this read-only snapshot is not a reservation. It does not measure physical free disk, live host memory, image availability or runtime health. Resolve storage shortages separately using the [cache cleanup procedure](#remove-downloaded-model-files).

For Flash-Next, the printed Helm command includes the selected profile and verified node capabilities. Its [automatic startup](#helm-flash-next-recipes) runs preparation and serving in Kubernetes. For GLM, the scheduling check does not close the documented automatic-startup validation gap. Plan and install each independent recipe through this same Helm workflow.

### Gateway access for SDKs and curl

For an SDK or raw HTTP client, keep a gateway connection open and provide its CA and caller key. Install curl for the examples below.

Forward the gateway in one terminal:

```bash
kubectl -n llm-stack port-forward svc/llm-api-gateway 18443:8080
```

In another terminal, use the selected context and create a directory for the exported CA. Retrieve the gateway CA and caller key, then list models:

```bash
LLM_WORK="$(mktemp -d)"
umask 077

kubectl -n llm-stack get configmap llm-gateway-stack-ca \
  -o 'jsonpath={.data.ca\.crt}' > "$LLM_WORK/gateway-ca.crt"
API_KEY=$(kubectl -n llm-stack get secret llm-shared-caller-key \
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

Change `model` to `qwen3.8-27b-nvfp4` to call the other precision. Add `"stream":true` for streaming. When finished, stop the port-forward, unset `API_KEY` and remove the temporary CA file.

### Connect a coding agent

Coding agents such as [Pi](https://github.com/badlogic/pi-mono) and [Codex](https://github.com/openai/codex) can use the model through the gateway. They need a large context: their first request is 1,500 to 6,500 tokens before any file contents. For GLM, the GB300 profile uses `tuning.discrete` in [recipe.json](recipes/glm-5.3/recipe.json), including 65,536 tokens per slot. The GB10 default of 2048 tokens is too small. See [Recipe resources and tuning](#recipe-resources-and-tuning) before changing these settings.

Tested in October 2026 with Pi 1.0.4 and Codex 0.161.0 against `glm-5.3` on one GB300 with two 65,536-token slots. Each agent found and fixed a one-line bug by running a test, editing the file and running the test again, using tool calls through the gateway. Pi took 7 seconds and Codex 33 seconds. Automatic Helm profile validation is recorded separately in the [catalog](recipes/index.json).

#### Open the gateway

Use the shared `llm-stack` installation. Set `LLM_CONTEXT` to your kubeconfig context and keep a port-forward running in its own terminal:

```bash
LLM_CONTEXT=my-cluster
kubectl --context "$LLM_CONTEXT" -n llm-stack port-forward svc/llm-api-gateway 18443:8080 --address 127.0.0.1
```

In the terminal where you run the agent, select the same context and retrieve the existing gateway CA and caller key:

```bash
LLM_CONTEXT=my-cluster
LLM_CA_CONFIGMAP=llm-gateway-stack-ca
LLM_CALLER_SECRET=llm-shared-caller-key
umask 077
LLM_WORK="$(mktemp -d)"
kubectl --context "$LLM_CONTEXT" -n llm-stack get configmap "$LLM_CA_CONFIGMAP" \
  -o 'jsonpath={.data.ca\.crt}' > "$LLM_WORK/gateway-ca.crt"
export GLM_API_KEY="$(kubectl --context "$LLM_CONTEXT" -n llm-stack get secret "$LLM_CALLER_SECRET" \
  -o 'go-template={{index .data "api-key" | base64decode}}')"
```

These are the shared chart defaults. If the installation overrides them, use `operator.trustBundle.configMap` for `LLM_CA_CONFIGMAP` and `callerKey.existingSecret` (or `callerKey.secretName` when empty) for `LLM_CALLER_SECRET`. Read the installed names with `helm --kube-context "$LLM_CONTEXT" -n llm-stack get values llm-stack --all`. An existing caller key file can instead supply `GLM_API_KEY` with `export GLM_API_KEY="$(cat /path/to/api-key)"`.

The default gateway certificate covers `127.0.0.1`, so the agents connect to `https://127.0.0.1:18443/v1` and trust `$LLM_WORK/gateway-ca.crt`. When finished, stop the port-forward, unset `GLM_API_KEY` and remove the temporary CA file.

#### Pi

Pi uses the chat completions API. Install it and add the model:

```bash
npm install -g --ignore-scripts @earendil-works/pi-coding-agent
mkdir -p ~/.pi/agent
cat > ~/.pi/agent/models.json <<'EOF'
{"providers": {"llm-routing": {
  "baseUrl": "https://127.0.0.1:18443/v1", "api": "openai-completions", "apiKey": "$GLM_API_KEY",
  "compat": {"supportsStore": false, "supportsDeveloperRole": false, "supportsReasoningEffort": false,
             "supportsUsageInStreaming": true, "supportsStrictMode": false, "maxTokensField": "max_tokens"},
  "models": [{"id": "GLM-5.3-UD-IQ2_M", "name": "GLM-5.3", "reasoning": false,
              "input": ["text"], "contextWindow": 65536, "maxTokens": 8192}]}}}
EOF
echo '{"defaultProvider": "llm-routing", "defaultModel": "GLM-5.3-UD-IQ2_M"}' > ~/.pi/agent/settings.json
NODE_EXTRA_CA_CERTS="$LLM_WORK/gateway-ca.crt" pi
```

- Set `contextWindow` to the selected recipe tuning's `contextPerSlot`. Pi sizes `max_tokens` from it and compacts the conversation before it fills.
- If `~/.pi/agent` already has these files, merge the provider into them instead.

#### Codex

Codex supports only the Responses API, which the gateway passes through to llama.cpp. Do not use `codex --oss`; it targets Ollama or LM Studio and cannot send the API key. Install Codex and add a [configuration profile](https://learn.chatgpt.com/docs/config-file/config-advanced):

```bash
npm install -g @openai/codex
mkdir -p ~/.codex
cat > ~/.codex/glm.config.toml <<'EOF'
model = "GLM-5.3-UD-IQ2_M"
model_provider = "llm-routing"
model_context_window = 65536
model_auto_compact_token_limit = 56000
web_search = "disabled"
include_apps_instructions = false
show_raw_agent_reasoning = true

[model_providers.llm-routing]
name = "LLM routing gateway"
base_url = "https://127.0.0.1:18443/v1"
env_key = "GLM_API_KEY"
wire_api = "responses"
stream_idle_timeout_ms = 600000

[features]
multi_agent = false
goals = false
view_image = false
apps = false

[tools]
experimental_request_user_input = { enabled = false }

[skills]
include_instructions = false
EOF
CODEX_CA_CERTIFICATE="$LLM_WORK/gateway-ca.crt" codex --profile glm
```

- Set `model_context_window` to the selected recipe tuning's `contextPerSlot`. Without it, Codex assumes a much larger context and requests fail when the conversation grows.
- The `[features]`, `[tools]` and `[skills]` settings remove tools the model cannot use and keep the first request near 4,500 tokens instead of 6,600.
- The recipe's `defaultMaxTokens` supplies the server's output cap for requests that omit a limit, including reasoning.

#### Tips

- The first request processes the whole prompt, at about 250 tokens per second on GB300. Later requests reuse the cached prefix, so start each agent once before a demo.
- Each slot serves one request at a time. Two agents working at once use both GB300 slots; the Pylon canary waits for the next free slot.
- For scripted runs, close stdin: `pi -p "<task>" < /dev/null` or `codex exec --profile glm "<task>" < /dev/null`. `pi -p` reads piped stdin as part of the prompt and waits until it closes.

### Helm cache reuse and recovery

Automatic SGLang startup checks its cache before downloading. Reinstalling the same release in the same namespace on the original node reuses its retained claim without extra flags. Completed files are verified locally, including complete Hugging Face snapshots without a recipe completion marker. Missing files are downloaded into that cache. A cache marked complete but failing validation stops startup instead of silently replacing its contents.

To use a differently named claim in the same namespace, set `cache.existingClaim`. To require a complete offline cache and prohibit model downloads, also set `reuseCaches: true`:


```yaml
reuseCaches: true
cache:
  existingClaim: retained-qwen-cache
```

For a two-node SGLang profile, use `cache.existingClaims` in rank order instead of `cache.existingClaim`:

```yaml
reuseCaches: true
cache:
  existingClaims:
    - retained-flash-rank-0
    - retained-flash-rank-1
```

Keep `nodes[0]` and `nodes[1]` on the respective claims' original nodes. Each rank needs its own claim. For GLM's artifact and RPC claims, use the [GLM cache settings](#helm-glm-recipe).

Select the node holding each PVC. The chart mounts existing claims and preserves their Helm ownership. `reuseCaches: true` validates the pinned files locally and fails when required files are missing or corrupt. New downloads use atomic completion markers, so interrupted preparation retries from the retained cache.

Inspect startup and readiness:

```bash
kubectl -n llm-stack get pods,inferenceendpoints
kubectl -n llm-stack logs deployment/qwen-fp8-serve-0 -c placement-check
kubectl -n llm-stack logs deployment/qwen-fp8-serve-0 -c sglang
```

Correct the values and rerun the original Helm command. Set `suspended: true` in the model values to release its GPUs while retaining cache claims. Set it back to `false` and rerun Helm to resume. Automatic recipes withdraw their endpoint while suspended and restore it on resume. The shared release and other models continue serving. `helm uninstall` removes model-owned workloads and endpoint resources while retaining model PVCs.

### Remove downloaded model files

Complete uninstall combines [removing the model release](README.md#uninstall-keep-downloads) with manual storage cleanup. Helm alone retains the model PVCs. Stop preserves both the release and its downloads.

1. Before uninstalling, record the model's cache claims and bound persistent volumes (PVs). Check the installed values for existing-claim overrides. The example below applies to the FP8 release installed from the main README with its own new cache:

   ```bash
   helm get values qwen-fp8 --namespace llm-stack
   LLM_MODEL_CACHE=qwen-fp8-cache-0
   kubectl -n llm-stack get pvc "$LLM_MODEL_CACHE" -o yaml
   LLM_MODEL_VOLUME="$(kubectl -n llm-stack get pvc "$LLM_MODEL_CACHE" -o jsonpath='{.spec.volumeName}')"
   kubectl get pv "$LLM_MODEL_VOLUME" -o yaml
   ```

   Verify that the claim's `meta.helm.sh/release-name` and `meta.helm.sh/release-namespace` annotations match this model release and namespace. Check the PV's `persistentVolumeReclaimPolicy`. Exclude externally supplied claims and any storage shared with another model. Names alone do not establish ownership. Qwen uses one cache claim per rank. GLM uses separate artifact and RPC-cache claims.

2. Uninstall the model release and wait for its pods to terminate. Check that no other workload uses the cache before deleting it. Keep the shared infrastructure and namespace installed.

3. Delete only the confirmed, dedicated claim by its exact name. This is irreversible when the provisioner deletes its backing data:

   ```bash
   kubectl -n llm-stack delete pvc "$LLM_MODEL_CACHE"
   ```

   Repeat only for other claims confirmed to belong exclusively to the removed model. Do not delete claims selected only by a name prefix or delete the namespace to reclaim one model's storage.

4. Verify reclamation in the storage system. With `Delete`, the provisioner is responsible for deleting the volume and its data. With `Retain`, deleting the claim leaves the PV and data behind, so complete removal needs the storage administrator's cleanup procedure. Removing a claim alone does not prove disk space was freed.

### Helm GLM recipe

The committed [GLM values](recipes/values/glm-5.3.yaml) select the automatic two-node GB10 profile. Replace `gpu-node-1` and `gpu-node-2` below with distinct available nodes. Override `runtimeClassName` and `storageClassName` if your cluster uses different names.

Install it with the shared stack already running:

```bash
helm upgrade --install glm dev-artifacts/charts/pylon-gguf-backend-0.2.0.tgz \
  --namespace llm-stack \
  --values recipes/values/glm-5.3.yaml \
  --set 'nodes[0]=gpu-node-1' --set 'nodes[1]=gpu-node-2' \
  --wait --timeout 120m &&
kubectl --namespace llm-stack wait \
  --for=condition=Registered inferenceendpoint/glm --timeout=5m &&
echo 'Model installed and registered.'
```

Kubernetes checks placement, builds the pinned llama.cpp runtime, starts the worker RPC server and qualifies both GPUs. It then verifies or downloads the pinned GGUF cache and starts the model server. The served model ID is `GLM-5.3-UD-IQ2_M`. Use that ID for [gateway verification](#verification). The common catalog lists its per-node resource and storage requirements. The current cached two-node Helm trial stopped at the host-memory guard during loading. Automatic serving validation remains open.

By default (`reuseCaches: false`), a changed image, llama.cpp revision or CUDA architecture rebuilds compiled runtime state while preserving model files and cached source archives. Unchanged runtimes are verified and reused.

To require offline reuse without rebuilding, set `reuseCaches: true`, `artifacts.existingClaim` and `rpc.cache.existingClaim` to the retained claims. Supply their original nodes in leader/worker order. The artifact claim must contain the pinned build and source files used for GPU qualification, as well as the runtime bundle and complete model files. Missing qualification state stops startup without rebuilding or deleting the retained files. Use a complete retained artifact cache or a new runtime cache; do not bypass the qualification check. Runtime identity and complete model checksums are validated before serving.

### Helm Flash-Next recipes

Flash-Next uses recipe `qwen3.8-flash-next` with two NVFP4 profiles. Both use automatic startup: one Helm install qualifies the hardware, verifies or downloads the pinned checkpoint, and starts serving. Each profile's live validation is recorded separately in the catalog.

| Profile | Placement | Additional requirements |
| --- | --- | --- |
| `spark-nvfp4-nvme` | One GB10 node | Local NVMe-backed pod ephemeral storage, 56 GiB offload allocation and a separate 180 GiB cache |
| `spark-nvfp4-tp2` | Two GB10 nodes | A shared fabric of at least 200 Gbps, with each node's fabric IPv4 address and interface |

List recipe and profile names before selecting a placement:

```bash
python3 llm.py recipes
```

Copy [capabilities.example.json](recipes/capabilities.example.json) to a private path and record verified facts for your nodes. Set `localNvme` only after checking the storage backing `/var/lib/kubelet`. For two-node serving, verify both interfaces, addresses, link speeds and mutual reachability. The planner does not measure these facts. Select available GPUs with enough host memory and disk space.

Print an install command for the one-node profile:

```bash
python3 llm.py plan --model qwen3.8-flash-next \
  --profile spark-nvfp4-nvme \
  --capabilities /path/to/capabilities.json --verbose
```

For the two-node profile, use:

```bash
python3 llm.py plan --model qwen3.8-flash-next \
  --profile spark-nvfp4-tp2 \
  --capabilities /path/to/capabilities.json --verbose
```

Run the printed Helm command when all placement checks pass. It contains the recipe, profile, selected nodes and node capabilities. It uses `helm install` to avoid changing an existing release. Preparation and serving run automatically, with one local model cache per node. Both nodes use the same pinned checkpoint. The two-node startup waits until both caches are ready before GPU qualification and serving.

For a direct one-node install, replace `NODE_FROM_PLANNER` in both places below. The entire block installs the model and waits for gateway registration:

```bash
helm upgrade --install qwen-flash dev-artifacts/charts/pylon-sglang-recipe-0.2.0.tgz \
  --namespace llm-stack \
  --set recipe=qwen3.8-flash-next --set profileName=spark-nvfp4-nvme \
  --set 'nodes[0]=NODE_FROM_PLANNER' \
  --set-json 'nodeCapabilities={"NODE_FROM_PLANNER":{"localNvme":true}}' \
  --wait --timeout 120m &&
kubectl --namespace llm-stack wait \
  --for=condition=Registered inferenceendpoint/qwen-flash --timeout=5m &&
echo 'Model installed and registered.'
```

For two-node serving, replace the Helm command in this block with the command printed by the two-node plan. Keep the registration wait, using your chosen release name if you set `--release`. Then [discover and call the model](README.md#3-discover-and-call-models) with model ID `qwen3.8-flash-next`.

For direct Helm configuration, the [one-node values](recipes/values/qwen3.8-flash-next-nvme.yaml) and [two-node values](recipes/values/qwen3.8-flash-next-tp2.yaml) are examples. Replace their nodes and capability facts with your verified settings. Model pins and runtime flags come from the chart. Retain the release name and node placement when reusing its caches.

## Independent model lifecycle

All supported recipes use the same Helm lifecycle. Preparation, startup and endpoint registration belong to the model chart. Keep the shared `llm-stack` release installed while managing models. That release name is reserved and rejected by model charts. The examples above use these independent releases:

| Model | Release | Chart archive | Served model ID |
| --- | --- | --- | --- |
| Qwen FP8 | `qwen-fp8` | `pylon-sglang-recipe-0.2.0.tgz` | `qwen3.8-27b` |
| Qwen NVFP4 | `qwen-nvfp4` | `pylon-sglang-recipe-0.2.0.tgz` | `qwen3.8-27b-nvfp4` |
| Flash-Next, either profile | `qwen-flash` | `pylon-sglang-recipe-0.2.0.tgz` | `qwen3.8-flash-next` |
| GLM | `glm` | `pylon-gguf-backend-0.2.0.tgz` | `GLM-5.3-UD-IQ2_M` |

For the commands below, select the Kubernetes context, existing release and its chart. Use the chart version that installed it for stop and resume. For example:

```bash
CONTEXT=your-kubernetes-context
MODEL_RELEASE=glm
MODEL_CHART=dev-artifacts/charts/pylon-gguf-backend-0.2.0.tgz
MODEL_ID=GLM-5.3-UD-IQ2_M
```

### Upgrade a model

Save its values privately and review changes to the same recipe, profile and node placement before upgrading a serving release. Use the target chart version in `MODEL_CHART`. GPU placement and retained claim names must stay consistent. Changing model pins or hardware needs fresh qualification.

```bash
helm --kube-context "$CONTEXT" get values "$MODEL_RELEASE" --namespace llm-stack -o yaml > /path/to/private/model-values.yaml
helm --kube-context "$CONTEXT" upgrade "$MODEL_RELEASE" "$MODEL_CHART" --namespace llm-stack \
  --values /path/to/private/model-values.yaml --wait --timeout 120m &&
kubectl --context "$CONTEXT" --namespace llm-stack wait \
  --for=condition=Registered "inferenceendpoint/$MODEL_RELEASE" --timeout=5m
```

After installation, upgrade or resume, verify the selected model separately:

```bash
python3 llm.py --context "$CONTEXT" models
python3 recipes/verify.py --context "$CONTEXT" --model "$MODEL_ID" \
  --output /path/to/private/model-results.json
```

### Stop and resume any recipe

Stop the model and wait for all its pods to terminate before reusing its GPUs:

```bash
helm --kube-context "$CONTEXT" upgrade "$MODEL_RELEASE" "$MODEL_CHART" --namespace llm-stack \
  --reuse-values --set suspended=true --timeout 10m &&
kubectl --context "$CONTEXT" --namespace llm-stack wait --for=delete pod \
  -l "app.kubernetes.io/instance=$MODEL_RELEASE" --timeout=10m
```

The stop command omits readiness waiting because the model is intentionally unavailable. The release and cache claims remain. When its original GPUs are free, resume and wait for registration:

```bash
helm --kube-context "$CONTEXT" upgrade "$MODEL_RELEASE" "$MODEL_CHART" --namespace llm-stack \
  --reuse-values --set suspended=false --wait --timeout 120m &&
kubectl --context "$CONTEXT" --namespace llm-stack wait \
  --for=condition=Registered "inferenceendpoint/$MODEL_RELEASE" --timeout=5m
```

### Remove and reinstall a recipe

```bash
helm --kube-context "$CONTEXT" uninstall "$MODEL_RELEASE" --namespace llm-stack --wait --timeout 10m
```

This removes the model workloads and endpoint, while retaining its caches. Reinstall with the same release name, recipe, profile and original cache nodes using its installation example. See [retained-cache settings](#helm-cache-reuse-and-recovery) for external claim names and offline reuse. Permanently deleting downloads is a separate [storage cleanup](#remove-downloaded-model-files).

For startup failures, inspect the model pods and logs, correct its values, and rerun its Helm command. Saved Helm values, Kubernetes workloads and retained caches describe the deployment; no separate Python phase checkpoint is required.

## Shared-stack upgrades

Use `helm upgrade --install llm-stack` in namespace `llm-stack` for installation and upgrades. Keep its service names, cluster ID, credential Secret names and CA name stable. Keep site values outside the repository. Update image references in that file after the images are available to all eligible nodes.

Save the installed values and release revision privately before upgrading. Review the new chart defaults and merge your existing settings into the new site values:

```bash
umask 077
helm get values llm-stack --namespace llm-stack -o yaml > /path/to/private/previous-values.yaml
helm history llm-stack --namespace llm-stack
helm upgrade --install llm-stack dev-artifacts/charts/llm-shared-stack-0.1.0.tgz \
  --namespace llm-stack --values /path/to/private/site-values.yaml \
  --wait --timeout 10m
```

The shared chart preserves generated caller and cluster credentials and reuses the existing CA. On each upgrade, it regenerates gateway caller-key hashes and router cluster hashes from the selected credential Secrets, including explicitly changed credentials. Missing or empty source credentials and ownership conflicts on chart-managed Secrets cause an error. Listener certificates are reused when their identity and validity still match the configured names. A planned TLS rotation requires a separate rollout and client trust update.

The upgrade also updates the CRD schema when `operator.installCRDs=true`. Review schema compatibility with every existing InferenceEndpoint before upgrading. With `false`, the external CRD owner must apply a compatible schema first. Do not switch ownership with an ordinary upgrade or delete the CRD to bypass Helm's ownership check.

The post-upgrade Job checks gateway access, discovery and authentication. Check model health and inference separately with `llm.py models` and `llm.py chat`. A successful Helm hook does not establish model readiness or validate a distributed runtime.

Before rolling back, inspect the saved values and the manifests for the target revision. A Helm rollback restores that revision's templates and may also revert its templated CRD schema. Only use a revision compatible with current InferenceEndpoint objects, credentials and runtime contracts.

## Shared-stack uninstall

Remove model releases first, keeping the shared operator running until their InferenceEndpoints and transport pods finish cleanup. Use the [model lifecycle commands](README.md#4-stop-or-uninstall-a-model) and the [cache cleanup procedure](#remove-downloaded-model-files). Check the installed models before uninstalling the shared release:

```bash
kubectl --namespace llm-stack get inferenceendpoints
helm list --namespace llm-stack
```

After confirming that no model still depends on the shared stack, uninstall its one release:

```bash
helm uninstall llm-stack --namespace llm-stack --wait --timeout 10m
```

This removes the gateway, router, operator and their non-retained configuration. The CRD, generated caller and cluster-token Secrets, and self-signed CA Secret carry a keep policy. Externally managed credentials and TLS resources remain under their external owner. Model PVCs retained by their own releases remain untouched.

The generated CA ConfigMap, listener TLS Secrets and generated hashed credential configuration are not retained by the shared chart. Back them up privately before uninstall if exact restoration is required. Reinstall `llm-stack` in namespace `llm-stack` with the same values to reuse retained credentials and the CA. Keep the namespace while any retained state is needed. Namespace deletion can remove Secrets, InferenceEndpoints and caches even when Helm marked them to keep. CRD deletion removes all InferenceEndpoints cluster-wide and is not part of this workflow.

## Verification

`llm.py models` reads the Helm-installed gateway directly. `llm.py chat` uses its CA and caller credential for real inference. Neither command requires a saved connection file. For SDK checks, use the [gateway connection example](#gateway-access-for-sdks-and-curl).

For repeatable chat, streaming and authentication checks against one or more registered models:

```bash
python3 recipes/verify.py --namespace llm-stack \
  --model qwen3.8-27b --model qwen3.8-27b-nvfp4 \
  --output /path/to/private/gateway-results.json
```

The verifier opens a temporary connection through the installed shared stack. `--context`, `--ca-configmap` and `--api-key-file` override connection settings. It writes results to the requested path and cleans up its connection files when finished.

Recipe qualification has separate stages: hardware compatibility and placement, preparation, runtime readiness, registration, and gateway inference. Offline tests establish rendering and local behavior. Accept a hardware trial only after its exact model pins and profile pass discovery, chat, streaming, authentication, retained-cache restart and isolation from another running model. Record the tested context and concurrency separately from the profile's candidate limits.

The current Qwen FP8 and NVFP4 evidence covers short prompts at configured context 8,192 and concurrency 1. The one-node Flash-Next NVMe profile also passed startup and short-prompt gateway checks at context 8,192 and concurrency 1. Flash-Next tensor-parallel serving remains unverified. GLM's latest automatic Helm trial stopped at its host-memory guard. These records do not establish maximum context, throughput or output quality. Gateway checks passed against the existing shared Helm release. Fresh-cluster installation remains unverified.

## Monitoring

Monitoring is an optional Helm release with its own retained metrics volume. It reads the installed stack and model metrics. It does not install routing infrastructure. See [monitoring configuration](recipes/MONITORING.md) for values, dashboard access and teardown.

## Recipe resources and tuning

The [catalog](recipes/index.json) records each profile's hardware, pinned image and checkpoint, placement, cache sizes, memory reservations and candidate workload limits. `python3 llm.py recipes` lists these profiles, and `plan --verbose` checks the requested model against current reservations.

SGLang accepts `contextLength` and `concurrency` in Helm values within the selected profile's limits. The GGUF automatic chart derives tuning from the recipe's `tuning.unified` or `tuning.discrete` settings and exposes its effective arguments in the rendered workload. Changing tuning or model pins requires new qualification. Inspect host memory as well as GPU memory on unified-memory hardware.

A distributed topology change requires stopping every rank before restarting the group. Retained local caches remain bound to their original nodes. A new placement needs separate caches or an explicit data migration. The planner does not move downloaded weights.

Memory guards stop a recipe's runtime when its required memory floor is crossed or its container swaps. Inspect the pod logs and available host memory before replacing a failed pod. Keep these guards active during qualification and serving.

## Add a recipe

Run the steps below manually. Install Helm, kubectl and Python 3.11 or newer. Start at the repository root and replace the example names with your recipe and cluster:

```bash
cd deploy/helm/llm-routing
CONTEXT=your-kubernetes-context
RECIPE_ID=your-model
PROFILE_ID=your-profile
MODEL_RELEASE=your-model-release
SERVED_MODEL_ID=your-served-model-id
BACKEND=sglang
BACKEND_TEST=test_helm_sglang.py
CHART_SOURCE="recipes/charts/$BACKEND"
VALUES_EXAMPLE="recipes/values/$RECIPE_ID-$PROFILE_ID.yaml"
umask 077
PRIVATE_DIR="$(mktemp -d)"
MODEL_VALUES="$PRIVATE_DIR/model-values.yaml"
python3 -m venv "$PRIVATE_DIR/venv" &&
. "$PRIVATE_DIR/venv/bin/activate" &&
python3 -m pip install -r recipes/tests/requirements.txt
```

For GGUF, set `BACKEND=gguf-backend` and `BACKEND_TEST=test_helm_gguf.py` in the setup block. Take `MODEL_RELEASE` and `SERVED_MODEL_ID` from the recipe metadata. For an existing recipe, set `VALUES_EXAMPLE` to its existing file under `recipes/values/`.

1. Add the maintained metadata. For SGLang, edit [recipes/catalog.json](recipes/catalog.json). For GGUF, create `recipes/<recipe-id>/recipe.json`, `model.lock.json`, `profiles.json` and `NOTICE`. Verify checkpoint/runtime pins, hardware fit, license, workload limits and chart support. Keep validation pending until tested. Remove a promoted model's entry from `recipes/planned.json`. For a new GGUF family, update the exporter's license and guide mappings, which currently target GLM.

2. Synchronize the chart copy. For SGLang, copy only the selected catalog entry into the chart's model map:

   ```bash
   python3 - "$RECIPE_ID" <<'PY'
   import json
   import sys
   from pathlib import Path
   recipe_id = sys.argv[1]
   models = json.loads(Path('recipes/catalog.json').read_text())['models']
   model = next(model for model in models if model['id'] == recipe_id)
   path = Path('recipes/charts/sglang/files/profiles.json')
   bundled = json.loads(path.read_text())
   bundled[recipe_id] = model
   path.write_text(json.dumps(bundled, indent=2) + '\n')
   PY
   ```

   For GGUF, use this copy command instead:

   ```bash
   mkdir -p "$CHART_SOURCE/files/recipes/$RECIPE_ID" &&
   cp "recipes/$RECIPE_ID/recipe.json" "recipes/$RECIPE_ID/model.lock.json" \
     "recipes/$RECIPE_ID/profiles.json" "recipes/$RECIPE_ID/NOTICE" \
     "$CHART_SOURCE/files/recipes/$RECIPE_ID/"
   ```

3. Add a values example for each profile, then edit its recipe, profile and node placeholders. Set `profileName` to your `PROFILE_ID`. Add the field if the template omits it. Keep real node names and cache settings in the private copy:

   ```bash
   if [ ! -e "$VALUES_EXAMPLE" ]; then
     cp "$CHART_SOURCE/values.example.yaml" "$VALUES_EXAMPLE"
   fi
   ```

   After editing the example, copy it and fill in the actual node placement, capabilities and retained claims:

   ```bash
   cp "$VALUES_EXAMPLE" "$MODEL_VALUES"
   ```

   When updating an existing release, use its saved values as the private starting point instead, then apply the intended recipe changes:

   ```bash
   helm --kube-context "$CONTEXT" get values "$MODEL_RELEASE" \
     --namespace llm-stack --output yaml > "$MODEL_VALUES"
   ```

   Extend the recipe's tests, including any explicit supported-ID inventories. Then regenerate the index, test, lint and render. Stop if a check fails:

   ```bash
   python3 recipes/export_catalog.py &&
   python3 recipes/export_catalog.py --check &&
   PYTHONPATH=recipes python3 -m unittest discover -s recipes/tests \
     -p test_catalog_export.py -v &&
   PYTHONPATH=recipes python3 -m unittest discover -s recipes/tests \
     -p test_committed_values.py -v &&
   PYTHONPATH=recipes python3 -m unittest discover -s recipes/tests \
     -p "$BACKEND_TEST" -v &&
   helm lint --strict "$CHART_SOURCE" --values "$MODEL_VALUES" &&
   helm template "$MODEL_RELEASE" "$CHART_SOURCE" --namespace llm-stack \
     --values "$MODEL_VALUES" > "$PRIVATE_DIR/model-rendered.yaml"
   ```

   Run additional startup/cache tests when those implementations change.

4. Package into a fresh directory, verify checksums, and select the archive listed for your recipe and profile:

   ```bash
   PACKAGE_DIR="$(mktemp -d)"
   bash dev-artifacts/package-charts.sh --output-dir "$PACKAGE_DIR" &&
   python3 - "$PACKAGE_DIR" <<'PY' &&
   import hashlib
   import sys
   from pathlib import Path
   root = Path(sys.argv[1])
   for line in (root / 'SHA256SUMS').read_text().splitlines():
       expected, name = line.split(None, 1)
       assert hashlib.sha256((root / name).read_bytes()).hexdigest() == expected, name
   print('Package checksums passed')
   PY
   MODEL_CHART="$PACKAGE_DIR/$(python3 - "$RECIPE_ID" "$PROFILE_ID" <<'PY'
   import json
   import sys
   from pathlib import Path
   recipes = json.loads(Path('recipes/index.json').read_text())['recipes']
   recipe = next(item for item in recipes if item['id'] == sys.argv[1])
   profile = next(item for item in recipe['profiles'] if item['id'] == sys.argv[2])
   print(profile['deployment']['chart']['archive'])
   PY
   )" &&
   test -f "$MODEL_CHART"
   ```

5. Reuse the shared stack, or [install it once](README.md#1-install-shared-infrastructure). Inspect the plan before deploying. Exit code 2 means an existing release, blocked placement or an unsupported recipe. For an expected existing release, retain its cache nodes and saved values. Resolve other blockers before deploying:

   ```bash
   python3 llm.py --context "$CONTEXT" plan --model "$RECIPE_ID" \
     --profile "$PROFILE_ID" --release "$MODEL_RELEASE" --verbose
   ```

   Supply `--capabilities /path/to/private/capabilities.json` for profiles needing verified NVMe or fabric facts. Once placement and values are correct, deploy and verify. Registration can precede gateway discovery. The loop below waits for discovery and a healthy registry before testing inference. Each verification run needs a fresh output path:

   ```bash
   VERIFY_DIR="$(mktemp -d)"
   helm --kube-context "$CONTEXT" upgrade --install "$MODEL_RELEASE" "$MODEL_CHART" \
     --namespace llm-stack --values "$MODEL_VALUES" --wait --timeout 120m &&
   kubectl --context "$CONTEXT" --namespace llm-stack wait \
     --for=condition=Registered "inferenceendpoint/$MODEL_RELEASE" --timeout=5m &&
   python3 - "$CONTEXT" "$SERVED_MODEL_ID" <<'PY' &&
   import sys
   import time
   import llm
   deadline = time.monotonic() + 300
   with llm.gateway(sys.argv[1], 'llm-stack') as client:
       while True:
           try:
               client.discovery(sys.argv[2])
               break
           except RuntimeError as error:
               if time.monotonic() >= deadline:
                   raise SystemExit(f'Model discovery did not become ready: {error}')
               time.sleep(2)
   PY
   python3 llm.py --context "$CONTEXT" models &&
   python3 recipes/verify.py --context "$CONTEXT" --model "$SERVED_MODEL_ID" \
     --output "$VERIFY_DIR/results.json"
   ```

   Confirm the deployed revision and pod configuration match the intended chart and pins. Check logs, memory and restarts. Exercise the intended context/concurrency and [cache-preserving lifecycle](#independent-model-lifecycle). If a check fails, adjust maintained metadata, repeat steps 2-4, then upgrade and retest. Preserve caches and other models.

6. Record the tested profile, artifact pins, date, workload and passed checks in the existing validation fields. Update the README validation summary. Repeat synchronization, export, checks and packaging above, using a fresh `PACKAGE_DIR`. Compare the final rendered pod templates with the tested deployment. Metadata can change the configuration checksum and trigger a rollout, so repeat affected live checks with the final archive when that happens. Then refresh the distribution from that verified package:

   ```bash
   cp -R "$PACKAGE_DIR/." dev-artifacts/charts/ &&
   python3 recipes/export_catalog.py --check &&
   git diff --check
   ```

   Review the generated diff before committing. Keep private values and evidence outside Git. Leave untested profiles pending.

## Local validation

Run from `deploy/helm/llm-routing`. These checks do not deploy to a cluster:

```bash
python3 -m pip install -r recipes/tests/requirements.txt -r recipes/tests/requirements-monitoring.txt
python3 -m unittest discover -s tests -v
python3 -m unittest discover -s recipes/tests -v
python3 -m unittest discover -s recipes/charts/gguf-backend/tests -v
python3 recipes/export_catalog.py --check
helm lint --strict dev-artifacts/charts/llm-shared-stack-0.1.0.tgz \
  --namespace llm-stack --values dev-artifacts/values.yaml
helm lint --strict recipes/charts/sglang --values recipes/charts/sglang/values.example.yaml
helm lint --strict recipes/charts/gguf-backend --values recipes/charts/gguf-backend/values.example.yaml
git diff --check
```

Use private temporary output for offline rendered manifests because they can contain generated credentials:

```bash
umask 077
RENDER_DIR="$(mktemp -d)"
helm template llm-stack dev-artifacts/charts/llm-shared-stack-0.1.0.tgz \
  --namespace llm-stack --values dev-artifacts/values.yaml > "$RENDER_DIR/shared.yaml"
helm template qwen-fp8 recipes/charts/sglang --namespace llm-stack \
  --set recipe=qwen3.8-27b --set 'nodes[0]=offline-gpu-node' > "$RENDER_DIR/model.yaml"
```

Offline rendering cannot inspect live ownership or prove credential reuse. Use [package-charts.sh](dev-artifacts/package-charts.sh) to build archives in a fresh output directory, then check catalog paths, bundled source equality and `SHA256SUMS`. Package generation and developer image builds do not install or upgrade a release.
