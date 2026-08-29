# ModelExpress with Dynamo on a self-managed compute plane

This sample runs a Dynamo vLLM function whose workers load model weights through
[ModelExpress](https://github.com/ai-dynamo/modelexpress) instead of each worker
downloading the model itself. When several workers start at once, the first one
to hold the weights serves them to its peers over RDMA, so scale-out cold starts
do not repeat the same download.

ModelExpress is optional. It is not part of the NVCF compute-plane stack, and
nothing here changes an existing installation. See
[the ModelExpress guide](../../../../docs/user/cluster-management/modelexpress.md)
for the full explanation, verification steps, and troubleshooting.

## How this sample is split

ModelExpress needs one cluster-wide install and one per-function chart:

- `modelexpress-server-values.yaml` configures the upstream ModelExpress chart.
  A cluster administrator installs this once. It is not part of the function.
- `modelexpress-dynamo/` is the NVCF function chart. It contains only a
  `DynamoGraphDeployment` whose workers point at the server above.
- `override.yaml.sample` holds the provider-specific fabric settings. The chart
  defaults are provider-neutral.

ModelExpress is wired into the workers entirely through the
`DynamoGraphDeployment` `envs` field, at `spec.services.VllmDecodeWorker.envs`,
together with `--load-format modelexpress` on the container args. Nothing else in
the function is ModelExpress-aware, which is why `modelExpress.enabled=false`
yields the same topology on the native loader. The
[Dynamo API reference](https://github.com/ai-dynamo/dynamo/blob/v1.2.1/docs/kubernetes/api-reference.md#dynamographdeployment)
documents the field. Note that the Dynamo operator also injects
`MODEL_EXPRESS_URL` on clusters deployed with `infrastructure.modelExpressURL`
configured; values in `envs` override it, so the address set here wins.

The server, its CRDs, and its RBAC are installed by an administrator rather than
shipped inside the function chart, because a CRD is a cluster-scoped object that
a function chart should not own, and the server is shared by every function that
uses it. The custom resources themselves are namespaced.

## Prerequisites

- An NVCF self-hosted cluster with the Dynamo, Grove, and KAI Scheduler add-ons.
  See [Self-Managed Clusters](../../../../docs/user/cluster-management/self-managed.md),
  and for local testing the
  [local Dynamo Operator guide](../../../../tools/ncp-local-cluster/docs/dynamo-operator.md).
- Two or more GPU nodes. One worker cannot demonstrate peer-to-peer transfer.
- On AWS EFA, enough nodes to supply one EFA unit per worker. An EFA interface is
  not shareable, so the bound is the advertised `vpc.amazonaws.com/efa` rather
  than GPUs per node. The EFA-capable G-family used here exposes one interface, so
  a four-GPU instance still runs one EFA worker; larger instances advertise more.
- RDMA-capable networking between those nodes. Without it ModelExpress still
  works, but over a slower transport, and the cold-start benefit shrinks.
- A node container runtime that allows a large locked-memory limit. RDMA pins
  memory, and containers inherit `RLIMIT_MEMLOCK` from the runtime, which
  commonly defaults to 8 MiB. That is too small for UCX to register its buffers,
  so the worker fails engine init with
  `ibv_reg_mr(...) failed: Cannot allocate memory`. Check and fix it on each GPU
  node:

  ```bash
  systemctl show containerd -p LimitMEMLOCK --value   # 8388608 is too small

  sudo mkdir -p /etc/systemd/system/containerd.service.d
  sudo tee /etc/systemd/system/containerd.service.d/10-memlock.conf <<'EOF'
  [Service]
  LimitMEMLOCK=infinity
  EOF
  sudo systemctl daemon-reload && sudo systemctl restart containerd
  ```

  Kubernetes has no ulimit field, so the chart cannot set this. Adding
  `CAP_IPC_LOCK` is the usual workload-level answer and that capability is meant
  to bypass `RLIMIT_MEMLOCK`, but it did not lift the limit for this image in our
  testing, and whether it works at all depends on the runtime and cluster policy.
  Either way, confirm from inside a worker with
  `grep 'Max locked memory' /proc/self/limits`.
- Enough node disk for the worker image. `vllm-runtime:1.2.1` is ~13 GB
  compressed, larger unpacked, and the frontend pulls it too. On an undersized
  node the pull crosses the kubelet ephemeral-storage eviction threshold, which
  evicts pods and cancels the extraction with `context canceled`.
- `kubectl` cluster-admin, to install the CRDs.
- A Hugging Face [token](https://huggingface.co/docs/hub/en/security-tokens),
  only if you switch to a gated model. The default,
  [Qwen/Qwen3-0.6B](https://huggingface.co/Qwen/Qwen3-0.6B), is ungated and needs
  none. For a gated model, both the server and the workers need it. The server
  performs the upstream download, and a worker that finds no peer source falls
  back to downloading for itself, which is the normal path for the first worker
  of a model. Set `hfToken` and the chart wires it into the workers.

## Versions

The worker image determines everything else. Both `vllm-runtime:1.2.1` and its
`1.2.1-efa-amd64` variant contain ModelExpress client 0.4.0, vLLM 0.20.1, and
NIXL 0.10.1, so the server is pinned to 0.4.0 to match the client rather than to
the chart version. The EFA variant additionally supplies AWS libfabric.

| Component | Version | Where it is set |
| --- | --- | --- |
| Worker image | `nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.2.1` | `modelexpress-dynamo/values.yaml` |
| AWS EFA worker image | `nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.2.1-efa-amd64` | `override.yaml.sample` |
| ModelExpress client | 0.4.0 | bundled in the worker image |
| ModelExpress server | `nvcr.io/nvidia/ai-dynamo/modelexpress-server:0.4.0` | `modelexpress-server-values.yaml` |
| ModelExpress CRDs | 0.4.0 | applied in step 1 below |
| ModelExpress chart | 0.5.1 | install command below |
| vLLM | 0.20.1 | bundled in the worker image |
| NIXL | 0.10.1 | bundled in the worker image |

Only one line in that table says 0.5.1, and it is the chart. You install chart
0.5.1, but the ModelExpress that runs is 0.4.0: the chart is packaging, and the
values file in this directory overrides the server image tag it would otherwise
deploy. The CRDs are applied separately from the 0.4.0 tag for the same reason.
Left unset, chart 0.5.1 would deploy server 0.3.0, which matches nothing here.

Confirm the client version yourself before changing any of these:

```bash
docker run --rm --entrypoint bash \
    nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.2.1 \
    -c "pip list | grep -iE 'modelexpress|^vllm|nixl'"
```

## Step 1: install the CRDs (administrator, once per cluster)

The chart does not ship its CRDs. Apply them separately, from the tag that matches
the server image rather than the chart, because the schemas differ between
releases. v0.5.1 adds fields such as `sourceType` and `artifactSource` that a
0.4.0 server never writes:

```bash
kubectl apply -f https://raw.githubusercontent.com/ai-dynamo/modelexpress/v0.4.0/examples/crds.yaml
kubectl get crd | grep modelexpress.nvidia.com
# Example output:
# modelcacheentries.modelexpress.nvidia.com
# modelmetadatas.modelexpress.nvidia.com
```

If you re-pin the server image, re-apply the CRDs from the matching tag.

## Step 2: install the ModelExpress server (administrator, once per cluster)

Install the upstream chart with the values file in this directory:

```bash
kubectl create namespace modelexpress
helm upgrade --install modelexpress modelexpress \
    --repo https://helm.ngc.nvidia.com/nvidia/ai-dynamo \
    --version 0.5.1 \
    --namespace modelexpress \
    -f modelexpress-server-values.yaml
```

No registry secret is needed: the server image is publicly pullable. Add one only
if you mirror the image privately.

The default model, `Qwen/Qwen3-0.6B`, is ungated and needs no token either. For a
gated model, add the token before installing. The values file already wires it as
an optional secret reference, so its absence is not an error:

```bash
kubectl create secret generic hf-token-secret \
    --namespace modelexpress \
    --from-literal=HF_TOKEN="$HF_TOKEN"
```

Do not install the chart with its own defaults. Several of them do not work as
shipped, and `modelexpress-server-values.yaml` documents each override inline.
Two will stop the server outright: `MX_METADATA_BACKEND` is required but left
unset, and the chart's `runAsNonRoot: true` with no `runAsUser` cannot start the
chart's own image, which runs as root. A default install fails with
`container has runAsNonRoot and image will run as root`.

Confirm the server is serving before continuing:

```bash
kubectl get pods -n modelexpress
kubectl logs -n modelexpress deploy/modelexpress | tail -20
```

## Step 3: deploy the function

Package the chart and register pull credentials:

```bash
helm package modelexpress-dynamo
helm push nvcf-modelexpress-dynamo-0.1.0.tgz oci://<your-registry>/<namespace>/charts
nvcf-cli registry-credential add \
    --hostname <your-registry> \
    --username <user> \
    --password <pass> \
    --artifact-type HELM \
    --artifact-type CONTAINER
```

Create the function. Set `helmChartServiceName` to the
`DynamoGraphDeployment` name plus `-frontend`; this chart names it
`modelexpress-dynamo`, so the service is `modelexpress-dynamo-frontend`.

```bash
cat <<EOF > function-create.json
{
  "name": "modelexpress-dynamo-function",
  "inferenceUrl": "/v1/chat/completions",
  "inferencePort": 8000,
  "helmChartServiceName": "modelexpress-dynamo-frontend",
  "helmChart": "oci://<your-registry>/<namespace>/charts/nvcf-modelexpress-dynamo-0.1.0.tgz",
  "healthProtocol": "HTTP",
  "healthUri": "/health",
  "healthPort": 8000,
  "healthTimeout": "PT10S",
  "healthExpectedStatusCode": 200
}
EOF
nvcf-cli function create --input-file ./function-create.json
```

Save the function and version IDs, then deploy:

```bash
cat <<EOF > function-deploy.json
{
  "functionId": "<saved-function-id>",
  "versionId": "<saved-function-version-id>",
  "deploymentSpecifications": [
    {
      "gpu": "<gpu>",
      "instanceType": "<instance-type>",
      "backend": "nvcf-default",
      "minInstances": 1,
      "maxInstances": 1
    }
  ]
}
EOF
nvcf-cli function deploy create --input-file ./function-deploy.json
```

Invoke it:

```bash
nvcf-cli function invoke \
    --function-id <saved-function-id> --version-id <saved-function-version-id> \
    --request-body '{
      "model": "Qwen/Qwen3-0.6B",
      "messages": [{"role": "user", "content": "What is the capital of France?"}],
      "stream": false,
      "max_tokens": 30
    }'
```

## Step 4: confirm ModelExpress is actually being used

A worker that cannot reach ModelExpress falls back to its native loader and
still serves traffic, so a successful invocation does not prove anything. Check
the worker logs for the ModelExpress loader:

```bash
kubectl logs -l nvidia.com/dynamo-component-type=worker --tail=200 \
    | grep -iE 'modelexpress|mx |nixl'
```

Two specific things to look for:

- The loader is `modelexpress`. If `--load-format` does not name it, vLLM uses
  its own loader and ModelExpress never runs, even with every environment
  variable set correctly.
- The intended backend loaded. InfiniBand and RoCE use the `UCX` default. AWS
  EFA uses `LIBFABRIC` with `vllm-runtime:1.2.1-efa-amd64` and
  `FI_PROVIDER=efa`; require `Backend LIBFABRIC was instantiated` and
  `NIXL agent ... (backend=LIBFABRIC)` in the worker log.
- A scale-out worker logged `Receiving ... tensors from source` and `Transfer
  complete`, without `Loading weights from disk`. Start one seed worker and
  scale from one to two; starting both simultaneously can make both miss a
  Ready source and correctly fall back to disk.

## Step 5: observe scale-out

Cold-start behavior is the point of ModelExpress, so measure it against the same
topology with ModelExpress switched off. Nothing else in the chart changes
between the two runs:

```bash
helm template mx-dyn modelexpress-dynamo --set modelExpress.enabled=false
```

Compare worker time-to-ready at increasing worker counts, and repeat each step,
because a single run does not show whether the result is consistent:

```bash
helm upgrade ... --set vllmDecodeWorker.replicas=2
helm upgrade ... --set vllmDecodeWorker.replicas=4
helm upgrade ... --set vllmDecodeWorker.replicas=10
```

On EFA, each step needs that many GPU nodes. Surplus replicas stay `Pending` with
`Insufficient vpc.amazonaws.com/efa`, which quietly changes what is being
measured, so scale the node pool alongside the replica count.

Keep the model, image, node type, and cache state identical across compared
runs. A warm page cache on one run and a cold one on the other produces a
difference that has nothing to do with ModelExpress.

## Provider-specific networking

The chart defaults are provider-neutral. Copy `override.yaml.sample` to
`override.yaml` and apply the block matching your fabric:

```bash
helm template mx-dyn modelexpress-dynamo -f override.yaml
```

AWS EFA is the validated path: use the `-efa-amd64` image, the `LIBFABRIC`
backend, `FI_PROVIDER=efa`, and the EFA extended-resource limit in
`override.yaml.sample`. The EFA security group must allow all protocols both
inbound and outbound to itself; an outbound `0.0.0.0/0` rule is not equivalent
for non-IP OS-bypass traffic. Validate with a passing TCP control, an SRD test,
and increasing EFA receive counters on both peers. InfiniBand and RoCE use the
default `UCX` backend and remain unvalidated by NVCF.

## Cleanup

```bash
nvcf-cli function deploy remove --function-id <id> --version-id <version-id>
nvcf-cli function delete --function-id <id> --version-id <version-id>
```

To remove the cluster-wide pieces as well:

```bash
helm uninstall modelexpress --namespace modelexpress
kubectl delete -f https://raw.githubusercontent.com/ai-dynamo/modelexpress/v0.4.0/examples/crds.yaml
kubectl delete namespace modelexpress
```

Deleting the CRDs deletes every `ModelMetadata` and `ModelCacheEntry` in the
cluster. Do not do it while another function is still using ModelExpress.
