# ModelExpress

This guide covers ModelExpress on a self-managed NVCF compute plane: when it
helps, how to install and configure it, how to confirm it is working, and how it
behaves when it cannot.

ModelExpress distributes model weights between workers. Normally every Dynamo
worker downloads the model itself, so starting ten workers means ten downloads of
the same weights. With ModelExpress, the first worker to hold the weights serves
them to its peers over RDMA, and later workers read from a peer instead of from
object storage or a model hub.

ModelExpress is optional and disabled by default. It is not part of the NVCF
compute-plane stack, so nothing about it changes an existing installation. You
install it yourself, per cluster, when you want it.

A runnable example is in
[the ModelExpress Dynamo sample](https://github.com/NVIDIA/nvcf/tree/main/examples/function-samples/helmchart-samples/modelexpress-dynamo-sample).

## When peer-to-peer distribution helps

ModelExpress addresses one specific cost: repeated download of the same weights
during scale-out. It helps most when all of the following hold.

- Several workers start at once, or scale up while one worker already holds the
  model. A single worker has no peer to read from, so the first load is never
  faster.
- The model is large enough that loading dominates start-up time. Weight
  transfer is what ModelExpress optimizes, so a model that loads in seconds
  leaves little to win.
- The path to the model hub or bucket is genuinely slower than a peer. Verify
  this rather than assuming it. Inside a cloud region a CDN-backed hub can be
  considerably faster than a peer transfer, so the cases that favour peers are
  heavy rate limiting, cross-region or on-premises egress, or a private store
  with no CDN in front of it.
- Nodes have RDMA-capable networking. Without it, peers still transfer, but over
  a slower transport that narrows the difference.
- The fan-out is narrow. Workers that start together all read from the same peer,
  and one peer serves a fixed total bandwidth however many read from it, so each
  additional simultaneous reader gets a proportionally smaller share.

It helps little or not at all in the opposite conditions: single-worker
deployments, small models, weights already on a fast local volume or a warm node
cache, or a cluster with no RDMA fabric. It also does not reduce the first
worker's cold start, only the cold starts of the workers that follow it.

### Measure before relying on it

Whether ModelExpress helps depends on your model size, your hub or bucket, and
your fabric, and the answer is not obvious in either direction. It also changes
with worker count: on NVCF's own test cluster, a small model with a CDN-backed hub
came out about the same either way at a narrow fan-out, and clearly faster
*without* ModelExpress at a wide one. Expect a crossover rather than a verdict
that holds at every scale, and measure at the fan-out you actually run.

Two properties determine the outcome and are worth measuring directly.

- Peer serving throughput. A source serves a roughly fixed total bandwidth
  regardless of how many workers read from it, and that figure, not the fabric's
  line rate, is what a scale-out divides between receivers. It can be well below
  the fabric's benchmarked speed.
- Hub throughput from inside your network. Compare it against the figure
  above. A CDN-backed public hub reached from the same cloud region is often
  faster than a peer transfer; a rate-limited, cross-region, on-premises, or
  private store frequently is not.

Three practical consequences follow. Seed the cache before a wide scale-out where
you can, so more than one peer is able to serve; the difference between one source
and several is large, and it is under your control. Size expectations by
concurrent readers per source rather than by total worker count. And re-check the
comparison as you scale, because a configuration that pays for itself at a few
workers can stop paying at many, while the hub's cost per worker stays flat.

Compare the same topology with ModelExpress on and off, holding everything else
constant, as described in [Scale-out behavior](#scale-out-behavior). Measure
wall-clock time from container start to Ready rather than a log line reporting
model load time: those lines do not always cover the same stages in both
configurations, so comparing them can be misleading.

## Supported versions

The worker image is the constraint. It bundles a ModelExpress client, and both the
server image and the CRDs should match that client rather than the chart version.

| Component | Version | Source |
| --- | --- | --- |
| Worker image | `nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.2.1` | function chart values |
| ModelExpress client | 0.4.0 | bundled in the worker image |
| ModelExpress server image | `nvcr.io/nvidia/ai-dynamo/modelexpress-server:0.4.0` | server values |
| ModelExpress CRDs | 0.4.0 | `ai-dynamo/modelexpress` tag `v0.4.0` |
| ModelExpress chart | 0.5.1 | `https://helm.ngc.nvidia.com/nvidia/ai-dynamo` |
| vLLM | 0.20.1 | bundled in the worker image |
| NIXL | 0.10.1 | bundled in the worker image |

The chart version is the odd one out, so read that table carefully. You install
chart 0.5.1, but the ModelExpress that actually runs is 0.4.0. The chart is only
packaging: this guide overrides the server image tag it deploys, and the CRDs are
applied separately from the 0.4.0 tag. Chart 0.5.1 is used because it is the
current published chart, not because 0.5.1 is the version in play anywhere else.
Read "0.5.1" as the chart and nothing more.

Two further traps.

The chart's own default is a third version. Chart 0.5.1 defaults its server image
tag to `0.3.0`, which matches neither the chart nor the client in the worker image
above. An install that does not set the tag explicitly runs 0.3.0. Always set it.

The client version is a property of the worker image, not of the chart you
installed. Check it rather than assuming:

```bash
docker run --rm --entrypoint bash \
  nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.2.1 \
  -c "pip list | grep -iE 'modelexpress|^vllm|nixl'"
```

If you change the worker image, re-check the client version and re-pin the
server to match. Client and server versions that differ have no published
compatibility statement.

## Worker image contents

A worker image must provide three things for ModelExpress to function:

- The `modelexpress` Python package, which supplies the vLLM model loader.
- NIXL, which performs the transfer. `vllm-runtime:1.2.1` carries NIXL 0.10.1.
- A vLLM build whose loader registry accepts a plugin loader.

vLLM 0.23 and later discover the ModelExpress loader on their own. Earlier
versions require the plugin to be named through `VLLM_PLUGINS`.
`vllm-runtime:1.2.1` ships vLLM 0.20.1, so it needs `VLLM_PLUGINS=modelexpress`.

## Prerequisites

- An NVCF self-hosted cluster with the Dynamo, Grove, and KAI Scheduler add-ons.
  See [Self-Managed Clusters](./self-managed.md).
- Two or more GPU nodes. One worker has no peer.
- On AWS EFA, enough GPU nodes to supply one EFA unit per worker. An EFA interface
  is not shareable between Pods, so the bound is the total
  `vpc.amazonaws.com/efa` the nodes advertise, not GPUs per node. On instances
  exposing a single interface, such as the EFA-capable G-family, that means one
  worker per node; larger instances advertise several units and fit more. See
  [Provider-specific networking](#provider-specific-networking).
- Enough node disk for the worker image. `vllm-runtime:1.2.1` is about 13 GB
  compressed and larger unpacked, and the frontend pulls the same image. A node
  sized for ordinary workloads can cross the kubelet ephemeral-storage eviction
  threshold while pulling it, which evicts running pods and cancels in-flight
  image extraction with `context canceled`. Budget well beyond the image size,
  since the platform and server images share the same disk.
- `kubectl` with cluster-admin, to install the CRDs.
- A storage class for the server cache volume. The default access mode is
  ReadWriteOnce, which limits the server to a single replica.
- Credentials for the model source, on the server. The server performs the
  upstream download; workers read from the server or from peers.
- RDMA-capable networking between GPU nodes, if you want the accelerated path.
  See [Provider-specific networking](#provider-specific-networking).

## NIXL and RDMA requirements

NIXL performs the actual transfer and selects a backend through
`MX_NIXL_BACKEND`. The client accepts exactly two values.

| Backend | Fabrics | Notes |
| --- | --- | --- |
| `UCX` | InfiniBand, RoCE | The provider-neutral default when the variable is unset. |
| `LIBFABRIC` | AWS EFA | Requires an EFA runtime image and `FI_PROVIDER=efa`. |

Both peers must use the same backend. A mismatch prevents the transfer.

The stock `vllm-runtime:1.2.1` image does not ship libfabric, so NIXL's
`LIBFABRIC` plugin cannot load there:

```bash
ldd /opt/nvidia/nvda_nixl/lib64/plugins/libplugin_LIBFABRIC.so | grep fabric
# Example output:
# libfabric.so.1 => not found
```

On AWS, use `vllm-runtime:1.2.1-efa-amd64`. It supplies AWS libfabric under
`/opt/amazon/efa`; with `MX_NIXL_BACKEND=LIBFABRIC` and `FI_PROVIDER=efa`, NIXL
loads the provider and transfers weights over EFA. A validated worker logs
`Backend LIBFABRIC was instantiated` and `NIXL agent ...
(backend=LIBFABRIC)`.

### Locked-memory limit

RDMA registers pinned memory, and `RLIMIT_MEMLOCK` caps how much a process may
pin. Containers inherit that limit from the node's container runtime, where 8 MiB
is a common default. That is too small for UCX to register its buffers, so the
worker fails engine initialization:

```text
ibv_reg_mr(address=..., length=20971520, access=0x1) failed: Cannot allocate
memory : Please set max locked memory (ulimit -l) to 'unlimited'
(current: 8192 kbytes)
```

Raise the limit on the runtime on every RDMA node, then restart it:

```bash
sudo mkdir -p /etc/systemd/system/containerd.service.d
sudo tee /etc/systemd/system/containerd.service.d/10-memlock.conf <<'EOF'
[Service]
LimitMEMLOCK=infinity
EOF
sudo systemctl daemon-reload && sudo systemctl restart containerd
```

Prefer setting this in node bootstrap so replacement nodes inherit it. Restarting
containerd does not stop running containers, but it briefly interrupts the CRI
socket, so the node may report `NotReady` for a few seconds.

There is no workload-level alternative. Kubernetes exposes no ulimit field, and
granting `CAP_IPC_LOCK` does not help a container that runs as a non-root UID,
because such a process has an empty effective capability set. `vllm-runtime:1.2.1`
runs as UID 1000, so the capability would be permitted but never active.

The important failure mode is silent. If the backend does not match the fabric,
ModelExpress can still complete the transfer over a slower path, and the
deployment looks healthy. Nothing errors. You only notice because the cold-start
improvement you expected does not appear. This is why
[Verification](#verification) checks the transport rather than checking whether
the workload came up.

## Installation

### Step 1. Install the CRDs

The chart does not include its CRDs. Apply them as cluster-admin, from the tag
that matches the server image rather than the chart. The schemas differ
between releases: 0.5.1 adds fields such as `sourceType` and `artifactSource`
that a 0.4.0 server never writes. Since the server is pinned to 0.4.0 to match
the client in the worker image, use the 0.4.0 CRDs:

```bash
kubectl apply -f https://raw.githubusercontent.com/ai-dynamo/modelexpress/v0.4.0/examples/crds.yaml
```

If you re-pin the server image, re-apply the CRDs from the matching tag.

This creates two namespaced custom resources in the `modelexpress.nvidia.com`
group:

```bash
kubectl get crd | grep modelexpress.nvidia.com
# modelcacheentries.modelexpress.nvidia.com
# modelmetadatas.modelexpress.nvidia.com
```

`ModelMetadata` tracks the peer-to-peer metadata for a model. `ModelCacheEntry`
is the model registry. The server owns both; you do not create them by hand.

### Step 2. Create the namespace and secrets

```bash
kubectl create namespace modelexpress
```

The server image is public, so no registry credential is needed for a default
install. Create one only if you pull from a private mirror, and set
`imagePullSecrets` to match:

```bash
kubectl create secret docker-registry nvcr-secret \
  --namespace modelexpress \
  --docker-server=nvcr.io \
  --docker-username='$oauthtoken' \
  --docker-password="$NGC_API_KEY"
```

For gated or private models, give the server the credential it needs:

```bash
kubectl create secret generic hf-token-secret \
  --namespace modelexpress \
  --from-literal=HF_TOKEN="$HF_TOKEN"
```

The workers need it too, in their own namespace. A worker that finds no peer
source falls back to downloading the model itself, and for the first worker of a
model that is the normal path rather than a failure. Without the credential there,
that fallback fails on a gated model. See
[Fallback behavior](#fallback-behavior) for the full list of cases, and
[Secrets](#secrets) for wiring it into the worker without rendering it into the
manifest.

### Step 3. Install the server

Install the upstream chart with explicit values. Several upstream defaults do
not work as shipped, so a default install is not a valid starting point:

```bash
helm upgrade --install modelexpress modelexpress \
  --repo https://helm.ngc.nvidia.com/nvidia/ai-dynamo \
  --version 0.5.1 \
  --namespace modelexpress \
  -f modelexpress-server-values.yaml
```

The values file in the sample documents each override. The defaults that must be
changed are:

| Default | Problem | Setting |
| --- | --- | --- |
| `MX_METADATA_BACKEND` unset | Required. The server does not start without it, so a default install crash-loops. | `kubernetes` or `redis` |
| `image.tag: "0.3.0"` | Matches neither chart 0.5.1 nor the client in the worker image. | the client version, `0.4.0` |
| `serviceAccount.rbac.enabled: false` | The server cannot reach its own CRDs, so the `kubernetes` backend fails. | `true` |
| `runAsNonRoot: true` with no `runAsUser`, in both `podSecurityContext` and `securityContext` | Cannot start the chart's own image, which runs as root. Fails with `container has runAsNonRoot and image will run as root`. | an explicit non-root UID, plus `fsGroup` so the cache volume is writable |
| `MODEL_EXPRESS_CACHE_DIRECTORY: /root` | Unwritable once the server runs as a non-root UID. | a path the UID owns, matching `persistence.mountPath` |
| `imagePullSecrets: nvcr-secret` | Assumes a secret of that name already exists in the namespace, so a default install fails where it does not. | `[]`, since the server image is public. Set one only for a private mirror. |
| `persistence.size: 10Gi` | Sized for one small model. | large enough for every model you serve |

`serviceAccount.automount: false` looks like a sixth entry but is inert. The
chart renders it through Helm's `default` function, which treats `false` as
empty, so the token is mounted regardless. The `kubernetes` backend needs the
token anyway.

### Step 4. Choose a metadata backend

`MX_METADATA_BACKEND` on the server selects where metadata lives.

| Value | Requires | Use when |
| --- | --- | --- |
| `kubernetes` | The CRDs from step 1 and `serviceAccount.rbac.enabled=true` | Default choice. No datastore to operate. |
| `redis` | A Redis you deploy and run. The chart bundles none. | You already operate Redis, or you need its throughput. |

Note that the same variable name means something different on the worker. On the
server it selects the metadata store. On the client, an unset value and the
values `server`, `redis`, `kubernetes`, and `crd` all mean "use the central
ModelExpress server", and only `k8s-service` changes behavior, selecting a
decentralized mode with no central server. Setting `kubernetes` on a worker is
therefore not an error, but it does not do what the name suggests. Leave it unset
on workers that use a central server.

## RBAC

With the `kubernetes` backend the server needs access to its own custom
resources. The chart creates a namespaced Role and RoleBinding when
`serviceAccount.rbac.enabled=true`. It grants the full verb set on
`modelmetadatas`, `modelcacheentries`, their status subresources, and
`configmaps`, all within the release namespace.

No ClusterRole is needed and none is created. If you see a request for
cluster-scoped permissions, something other than this chart is asking for it.

The chart's service account mounts an API token. The `serviceAccount.automount`
value appears to default to `false`, but the rendered manifest sets
`automountServiceAccountToken: true` regardless, because the template's default
expression treats `false` as unset. Verify the rendered output rather than the
values file:

```bash
helm template modelexpress modelexpress \
  --repo https://helm.ngc.nvidia.com/nvidia/ai-dynamo --version 0.5.1 \
  | grep automountServiceAccountToken
```

## Secrets

Model credentials belong on the server, which performs the upstream download.
Workers reading from the server or a peer do not need their own token.

Supply them through `extraEnv` with a `secretKeyRef` rather than a literal, so
the value is not rendered into the manifest:

```yaml
extraEnv:
  - name: HF_TOKEN
    valueFrom:
      secretKeyRef:
        name: hf-token-secret
        key: HF_TOKEN
        optional: true
```

The registry pull secret is separate. If you configured one for a private mirror,
it must exist in the release namespace before install. A default install pulls
public images and needs none.

## Configuration

Worker-side configuration is environment on the Dynamo worker plus one vLLM
argument.

| Setting | Value | Why |
| --- | --- | --- |
| `--load-format` | `modelexpress` | Required. vLLM only uses the ModelExpress loader when the load format names it. |
| `MX_SERVER_ADDRESS` | `host:port` of the server | Where to find the server. |
| `MODEL_EXPRESS_URL` | same as above | Older client name. Client 0.4.0 reads both, so setting both is safe. |
| `VLLM_PLUGINS` | `modelexpress` | Required for vLLM below 0.23. |
| `MX_NIXL_BACKEND` | `UCX` or `LIBFABRIC` | Use `LIBFABRIC` for AWS EFA and `UCX` for InfiniBand/RoCE. |

The load format is the setting most often missed. Environment variables alone
configure a loader that vLLM never calls, so the deployment starts, serves
correct results, and quietly never uses ModelExpress.

### How these reach the worker

Dynamo workers are described by a `DynamoGraphDeployment`, and every variable in
the table above is delivered through its `envs` field. Set them on one service
under `spec.services.<name>.envs`, or across every service under `spec.envs`,
where a service-level entry wins if both set the same name. The
[Dynamo API reference](https://github.com/ai-dynamo/dynamo/blob/v1.2.1/docs/kubernetes/api-reference.md#dynamographdeployment)
documents the full schema.

```yaml
apiVersion: nvidia.com/v1alpha1
kind: DynamoGraphDeployment
metadata:
  name: my-function
spec:
  services:
    VllmDecodeWorker:
      componentType: worker
      envs:
        - name: MX_SERVER_ADDRESS
          value: modelexpress.modelexpress.svc.cluster.local:8001
        - name: VLLM_PLUGINS
          value: modelexpress
        - name: MX_NIXL_BACKEND
          value: LIBFABRIC
      extraPodSpec:
        mainContainer:
          args:
            - --load-format
            - modelexpress
```

The operator may already be setting one of these for you. It injects
`MODEL_EXPRESS_URL` into every component container when the platform is deployed
with `infrastructure.modelExpressURL` configured, so on such a cluster a worker
can carry a server address with no `envs` entry at all. That by itself does not
turn ModelExpress on, because vLLM still needs `--load-format modelexpress`. What
it does mean is that the address a worker uses is not always the one in your
manifest. Values in `envs` take precedence, since the operator merges its own
variables first and lets the container's environment override them, so an explicit
`MX_SERVER_ADDRESS` is authoritative. When a worker connects somewhere
unexpected, read the resolved environment rather than the manifest:

```bash
kubectl exec <worker-pod> -- env | grep -iE 'model_express|^mx_'
```

## Provider-specific networking

The chart defaults are provider-neutral. The provider override selects the
runtime image, NIXL backend, environment, and device resource.

### AWS EFA

EFA needs the AWS runtime image, LIBFABRIC backend, provider selection, and
device request:

```yaml
modelExpress:
  nixlBackend: LIBFABRIC
  extraEnv:
    - name: FI_PROVIDER
      value: efa
    - name: MX_ARTIFACT_READY_URL
      value: http://127.0.0.1:9090/health
    - name: MX_ARTIFACT_TRANSFER
      value: "1"
    - name: UCX_RNDV_SCHEME
      value: get_zcopy
    - name: UCX_RNDV_THRESH
      value: "0"
    - name: VLLM_RPC_TIMEOUT
      value: "7200000"

vllmDecodeWorker:
  image: nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.2.1-efa-amd64
  resources:
    limits:
      gpu: "1"
      custom:
        vpc.amazonaws.com/efa: "1"
```

Declare it under `limits`. EFA is an extended resource, and Kubernetes rejects a
Pod that requests one without a matching limit, with
`Limit must be set for non overcommitable resources`. Under `requests` the
resulting Pod is invalid and never created: the PodClique reports
`ERR_CREATE_POD` and the workers never appear, which reads like a scheduling
failure rather than a bad spec.

EFA also has to exist on the node before any of this matters, and it cannot be
added later. An EFA interface is attachable only at instance launch, so a running
node without one has to be replaced, not reconfigured. Peers must also share a
subnet, and the instance type itself has to support EFA. Within a family the
smaller sizes often do not.

The security group attached to every EFA must allow all protocols both inbound
and outbound to itself. An outbound `0.0.0.0/0` rule is not equivalent for EFA
OS-bypass traffic because that traffic is non-IP. This failure mode passes a TCP
control while EFA transmit counters increase and receive counters remain zero.

One worker per node. EFA-capable G-family instances expose a single EFA
interface, and the device plugin advertises one `vpc.amazonaws.com/efa` unit per
interface, so only one Pod per node can hold the fabric. A second worker
requesting it on the same node stays `Pending` with `Insufficient
vpc.amazonaws.com/efa`. Size the cluster by worker count, not by GPUs per node:
a four-GPU instance still runs one EFA worker, so four workers need four nodes.

Confirm the node advertises the resource:

```bash
kubectl get nodes -o json | jq '.items[].status.allocatable | keys' | grep -i efa
```

Before testing ModelExpress, run a TCP control and then `ucx_perftest` over EFA
`srd`. Confirm EFA hardware `recv_bytes` increases on both peers; UCX merely
reporting that it selected `srd` is not sufficient.

If EFA is absent from the node, transfers still succeed over another path. This is
the most common reason an EFA cluster shows no improvement.

### InfiniBand and RoCE

Both use `UCX`, which is already the default, so no ModelExpress setting changes.
What differs is the device resource your RDMA device plugin advertises, commonly
along these lines:

```yaml
vllmDecodeWorker:
  resources:
    limits:
      custom:
        rdma/hca: "1"
```

The resource name varies by plugin, so check what your nodes actually advertise
with `kubectl get nodes -o json | jq '.items[].status.allocatable | keys'`. It
goes under `limits` for the same reason EFA does: an extended resource must be
declared there, and a requests-only spec is rejected at admission.

On hosts with several RDMA NICs, `MX_RDMA_NIC_PIN` and
`MX_RDMA_NIC_PIN_MIN_RATE_GBPS` select one. Leave them unset until a measurement
justifies changing them.

<Note>
InfiniBand and RoCE are not validated by NVCF. The settings above are derived
from upstream client behavior, not from a tested deployment. AWS EFA is the
tested fabric. Verify InfiniBand and RoCE on your own hardware before relying on
them.

Both use the `UCX` backend, and there is a known defect on that path. With
ModelExpress 0.4.0 in `vllm-runtime:1.2.1`, the source worker segfaulted while
serving a transfer, inside `nixlUcxSharedThread::run()`. The source exits with
`EngineDeadError` and receivers report `NIXL_ERR_REMOTE_DISCONNECT`. It
reproduced over plain TCP, so it is not specific to any fabric and may appear on
InfiniBand and RoCE as well. `LIBFABRIC` on EFA does not hit it. Prove the path
with an actual peer-to-peer transfer before relying on it, because a worker that
reaches `Ready` does not demonstrate one: a failed transfer falls back to
downloading and still serves correct results.
</Note>

## Rollout

Roll out in the order the dependencies require, and verify at each step rather
than at the end.

1. Install the CRDs and confirm both are present.
2. Install the server and confirm it reaches ready. A crash-loop here is almost
   always a missing `MX_METADATA_BACKEND`.
3. Deploy one worker with ModelExpress enabled and invoke it once. This proves
   configuration without involving peer transfer.
4. Scale to two or more workers and verify the transport, as below.

Enabling ModelExpress does not require redeploying the compute-plane stack. It is
a per-function setting plus a cluster-wide server.

## Scale-out behavior

Worker replica count is what exercises ModelExpress. The first worker loads from
the source; later workers read from a peer.

Measure against the same topology with ModelExpress off. Nothing else should
differ between the two runs:

```bash
helm template mx-dyn modelexpress-dynamo --set modelExpress.enabled=false
```

Then compare worker time-to-ready as the replica count grows, for example at 2,
4, and 10 workers, repeating each step. A single run does not show whether a
result is consistent, and cold-start timings vary enough that one measurement is
not evidence.

On EFA, each of those replica counts needs that many advertised
`vpc.amazonaws.com/efa` units, which on single-interface instances means that
many GPU nodes. Scaling replicas beyond the available units leaves the surplus
workers `Pending` and silently changes what the run measures.

Hold these constant across compared runs, or the comparison means nothing:

- Model and revision
- Worker image digest
- Node instance type and GPU count per worker
- Node cache state. A warm page cache on one run and a cold one on the other
  produces a difference unrelated to ModelExpress.
- Container image cache state on every node, for the reason below.
- Tensor and pipeline parallel shape

Expect no improvement on the first worker. The benefit appears in the workers
that follow it.

### Worker start times change the result

Which peer a worker reads from depends on which peers have finished publishing
when it asks, so start times decide the shape of the transfer:

- Staggered starts produce a chain. Each worker finds the previously finished
  worker and reads from it, so several peers serve in parallel and each transfer
  gets the fabric largely to itself.
- Simultaneous starts produce a star. Every worker asks before any of the
  others has published, all of them select the one seeded peer, and they divide
  that peer's serving bandwidth.

The same four-worker deployment measured both ways differed by a factor of
roughly three, purely from this effect. The staggered result came from an
unintended source: nodes that still had to pull the multi-gigabyte worker image
started minutes later than nodes with it cached.

A real scale-out, meaning an autoscaler adding replicas where the image is
already cached, is the simultaneous case, so treat the star as representative and pull
the worker image to every node before measuring. Otherwise a first run on cold
nodes and a second on warm ones will disagree, and neither figure describes
production.

## Verification

A successful invocation does not prove ModelExpress is in use, because a worker
that cannot use it falls back and serves correct results anyway. Verify three
things separately.

That the loader ran:

```bash
kubectl logs -l nvidia.com/dynamo-component-type=worker --tail=200 \
  | grep -iE 'modelexpress|mx '
```

That the transport is the one you intended. On EFA, require the worker to log
`Backend LIBFABRIC was instantiated`, `FI_PROVIDER=efa` in its environment, and
the EFA hardware counters to show payload bytes received by the scale-out peer.
A transfer over a fallback path is not a validated accelerated configuration.

That peers actually served each other. The server records metadata per model, so
check that later workers resolved a peer source rather than the upstream:

```bash
kubectl get modelmetadatas -n modelexpress
kubectl logs -n modelexpress deploy/modelexpress | grep -iE 'peer|source|transfer'
```

`MX_TRANSFER_LOG_DIR` on the worker writes transfer detail to a directory, which
is useful when the logs above are not conclusive.

## Fallback behavior

ModelExpress is designed to degrade rather than fail. When no compatible peer
source is available, a worker falls back to loading the model itself, and
inference is unaffected.

Fallback is expected in normal operation, not only in failure:

- The first worker for a model has no peer to read from.
- The server is unreachable or not yet ready.
- No peer holds the requested model and revision.
- The peer's transport does not match the worker's.

Because the worker performs its own download on this path, a gated model needs
the Hugging Face credential in the worker namespace as well as on the server.
Without it, fallback fails rather than degrading, which defeats the point of
having a fallback at all.

`MX_SOURCE_QUERY_TIMEOUT` bounds how long a worker waits for a source before
falling back, and `MX_TRANSFER_TIMEOUT` bounds the transfer itself. Both trade
start-up latency against the chance of using a peer.

Because fallback is silent by design, treat a healthy deployment as insufficient
evidence and rely on [Verification](#verification).

## Troubleshooting

Server crash-loops immediately on install. `MX_METADATA_BACKEND` is unset.
Upstream ships it commented out and the server requires it.

Server starts but cannot read or write metadata. RBAC is disabled. Set
`serviceAccount.rbac.enabled=true`, and confirm the CRDs from step 1 exist.

Server image is not the version you expected. Chart 0.5.1 defaults the tag to
`0.3.0`. Set `image.tag` explicitly.

Workers come up and serve, but nothing indicates ModelExpress ran. Almost always
a missing `--load-format modelexpress`. The environment variables are configuring
a loader vLLM is not calling.

Workers use ModelExpress but there is no cold-start improvement. On EFA, confirm
the `-efa-amd64` image, `LIBFABRIC` backend, `FI_PROVIDER=efa`, a node EFA
interface, and self-referencing all-protocol security-group rules in both
directions. Then check that a peer actually held the model, that the compared
runs had matching cache state, and that the model is large enough for transfer
time to matter.

Setting `MX_NIXL_BACKEND=LIBFABRIC` fails with `libfabric.so.1` missing. The
stock `vllm-runtime:1.2.1` image is in use. On AWS EFA, switch the worker to
`vllm-runtime:1.2.1-efa-amd64` and set `FI_PROVIDER=efa`.

Transfers fail between specific pairs of workers. Both peers must use the same
NIXL backend. Check for a mixed configuration during a rolling change.

Pull failures in the `modelexpress` namespace. The chart defaults
`imagePullSecrets` to `nvcr-secret` and fails where no such secret exists. The
values in this guide set it to `[]`, since the server image is public. If you
restored the default for a private mirror, create the secret to match.

Server volume fills up. `persistence.size` defaults to 10Gi, which holds one
small model. Size it for every model you serve.

Cannot scale the server past one replica. The default access mode is
ReadWriteOnce. A second replica requires a ReadWriteMany storage class.

## Cleanup

Remove the function first, then the cluster-wide pieces:

```bash
helm uninstall modelexpress --namespace modelexpress
kubectl delete -f https://raw.githubusercontent.com/ai-dynamo/modelexpress/v0.4.0/examples/crds.yaml
kubectl delete namespace modelexpress
```

Deleting the CRDs deletes every `ModelMetadata` and `ModelCacheEntry` in the
cluster. Do not do it while another function still uses ModelExpress.

The secrets are namespaced, so deleting the namespace removes them. Workers with
ModelExpress still configured fall back to native loading once the server is
gone, so removing the server does not break a running function, though it does
remove the benefit.
