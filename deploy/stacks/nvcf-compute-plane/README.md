# nvcf-compute-plane-stack

Helmfile stack for the NVCF compute plane. Installs the NVCA operator and optional
ML-framework operators (Grove, Dynamo) onto GPU clusters registered with an NVCF control plane.

## Prerequisites

- An NVCF control plane deployed via `nvcf-self-managed-stack`
- `helmfile` v1.1.x (v1.2.0+ breaks ordering; see version note below)
- `helm` v3.x
- `helm-diff` plugin
- `nvcf-cli` (for cluster registration)
- A kubeconfig pointing at the target GPU cluster

## Quickstart

```sh
# 1. Register the cluster with ICMS (idempotent)
make register-cluster \
  CLUSTER_NAME=gpu-east \
  NCA_ID=nvcf-default \
  CLUSTER_REGION=us-west-1 \
  ICMS_URL=https://sis.your-nvcf.example.com

# 2. Deploy the compute plane
make install \
  CLUSTER_NAME=gpu-east \
  HELMFILE_ENV=<env>
```

Repeat steps 1-2 for each GPU cluster (see [this example](#multi-cluster-example)).
The `CLUSTER_NAME` variable scopes all state to
that cluster so multiple clusters can be managed from a single checkout.
`register-cluster` writes `registration/<cluster>-register-values.yaml`.
`install`, `apply`, and `template` copy that file into `out/` before running
Helmfile.

`HELMFILE_ENV` maps to `environments/<env>.yaml`. Source checkouts include a
`local` environment for development. Release archives ship `base.yaml`; create
an environment file for your registry and service endpoints, then pass its name
without the `.yaml` suffix.

## Observability

The stack defaults `observability.profile` to `compute`. The `compute` and
`all` profiles enable the NVCA collector and `BYOObservability` feature gate.
The `control` and `disabled` profiles leave both off.

One value selects the normal behavior:

```yaml
observability:
  profile: compute
```

Explicit NVCA values override the profile defaults:

```yaml
global:
  nvcaOperator:
    selfManaged:
      otelCollector:
        enabled: false
      featureGateValues:
        - "-BYOObservability"
```

## Chart and Image Sources

The stack pins the NVCA operator chart in
`helmfile.d/02-nvca.yaml.gotmpl`. The chart supplies the default NVCA,
NVCA operator, image credential helper, and shared storage image tags.

Use `global.helm.sources` for chart repository location and `global.image` for
container image repository location. The stack rewrites repositories through
those global values, while chart defaults supply the tested image tags.
Only set `global.nvcaOperator.selfManaged.imageCredHelper.imageTag` when
pinning a tested replacement helper image.

## Helmfile Version Note

Helmfile v1.2.0+ changed `helmfile.d/` processing to parallel mode which breaks
implicit ordering. Use helmfile v1.1.x:

```sh
# Pinned binaries are auto-downloaded by the dev Makefile:
make install CLUSTER_NAME=...   # downloads helmfile v1.1.9 + helm v3.15.4 on first run
```

## Optional Add-ons

KAI Scheduler, Grove (service orchestration), Dynamo (inference framework
scheduling), and optional MNNVL topology-aware scheduling are disabled by
default. Grove and Dynamo require KAI Scheduler (release and namespace
`kai-scheduler`). Topology-aware scheduling also requires KAI Scheduler. Enable
per-environment in `environments/<env>.yaml`:

```yaml
addons:
  topologyAwareScheduling:
    enabled: true
  kaiScheduler:
    enabled: true
  groveOperator:
    enabled: true
  dynamoOperator:
    enabled: true
```

Override KAI component resources under `addons.kaiScheduler.<component>.resources`
(for example `addons.kaiScheduler.scheduler.resources.requests.memory`). Defaults
are set in `helmfile.d/01-dependencies.yaml.gotmpl`.

`addons.topologyAwareScheduling` installs cluster-scoped KAI `Topology`
resources from `topologyAwareScheduling.topologies` when KAI is enabled.
When Grove is also enabled, the same toggle sets Grove
`topologyAwareScheduling.enabled=true` and installs one `ClusterTopologyBinding` per KAI Topology.
The default topology keys/node labels are `nvidia.com/gpu.clique` and `kubernetes.io/hostname`, in order.
See [KAI-Scheculer docs](https://github.com/kai-scheduler/KAI-Scheduler/blob/main/docs/topology/README.md),
[Grove Operator docs](https://github.com/ai-dynamo/grove/blob/main/docs/user-guide/topology-aware-scheduling.md),
or [Dynamo Operator docs](https://docs.nvidia.com/dynamo/dev/knowledge-base/kubernetes/multinode/topology-aware-scheduling)
for topology-aware scheduling configuration for Helm chart functions using respective component(s).

Enabling `addons.kaiScheduler.enabled` or `addons.dynamoOperator.enabled` also
adds the matching NVCA feature gates (`KAIScheduler`, `DynamoOperatorSupport`).
Enabling KAI, Grove, or Dynamo also adds the matching CRDs to the NVCA
validation policy, so functions may deploy `PodGroup`, `PodCliqueSet`, and
Dynamo graph objects without restating the whole policy.

For a standalone KAI install outside this stack, follow the
[KAI Scheduler guide](https://docs.nvidia.com/cloud-functions/current/latest/cluster-management/kai-scheduler.html).

## Multi-Cluster Example

Each cluster is registered and installed independently. Pass `KUBECONFIG_FILE`
to every target so that registration reads the JWKS/issuer from the correct
cluster. Omitting it during `register-cluster` causes both clusters to register
with the ambient context's JWKS, which leads to PSAT auth failures at runtime.

```sh
make register-cluster \
  CLUSTER_NAME=gpu-east \
  CLUSTER_REGION=us-east-1 \
  KUBECONFIG_FILE=~/.kube/gpu-east.yaml

make register-cluster \
  CLUSTER_NAME=gpu-west \
  CLUSTER_REGION=us-west-2 \
  KUBECONFIG_FILE=~/.kube/gpu-west.yaml

make install \
  CLUSTER_NAME=gpu-east \
  HELMFILE_ENV=<env> \
  KUBECONFIG_FILE=~/.kube/gpu-east.yaml

make install \
  CLUSTER_NAME=gpu-west \
  HELMFILE_ENV=<env> \
  KUBECONFIG_FILE=~/.kube/gpu-west.yaml
```
