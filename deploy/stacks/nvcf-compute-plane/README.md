# nvcf-compute-plane-stack

Helmfile stack for the NVCF compute plane. Installs the NVCA operator and optional
ML-framework operators (Grove, Dynamo) onto GPU clusters registered with an NVCF control plane.

## Prerequisites

- An NVCF control plane deployed via `nvcf-self-managed-stack`
- `helmfile` v1.1.x (v1.2.0+ breaks ordering; see version note below)
- `helm` v3.x
- `helm-diff` plugin
- `yq` v4
- `nvcf-cli` (for cluster registration)
- A kubeconfig pointing at the target GPU cluster

## Quickstart

```sh
# 1. Export the control-plane profile from the self-managed control-plane stack.
nvcf-cli self-hosted \
  --control-plane-stack ../self-managed \
  control-plane profile export

# 2. Initialize the CLI config used for registration.
nvcf-cli --config <path-to-config> init

# 3. Register the cluster with the generated profile.
make register-cluster \
  CLUSTER_NAME=gpu-east \
  CLUSTER_REGION=us-west-1 \
  NVCF_CLI_CONFIG=<path-to-config>

# 4. Deploy the compute plane.
make install \
  CLUSTER_NAME=gpu-east \
  HELMFILE_ENV=<env>
```

Repeat steps 1 and 3 for each GPU cluster (see [this example](#multi-cluster-example)).
The `CLUSTER_NAME` variable scopes all state to
that cluster so multiple clusters can be managed from a single checkout.
`register-cluster` writes `registration/<cluster>-register-values.yaml`.
`install`, `apply`, and `template` copy that file into `out/` before running
Helmfile.

By default, `register-cluster` reads
`../self-managed/out/control-plane-profile.yaml`. Set
`CONTROL_PLANE_PROFILE=/path/to/control-plane-profile.yaml` when the control
and compute stacks are not sibling directories. The profile is the canonical
handoff for account identity, service endpoints, Host overrides, and transport
trust. Set `NVCF_CLI_CONFIG` when registration must use a non-default CLI
config. The Makefile forwards the path to `nvcf-cli` but does not run `init`.

`HELMFILE_ENV` maps to `environments/<env>.yaml`. Create that environment file
with the artifact registry and any deployment-specific overrides before
installing. Local-development guides provide canonical fixtures rather than an
implicit `local.yaml`. Release archives ship `base.yaml`; pass the environment
name without the `.yaml` suffix.

## Observability

The stack defaults `observability.profile` to `compute`. The `compute` and
`all` profiles enable the `BYOObservability` feature gate. The optional NVCA
collector stays disabled for every profile until the operator opts in. This
prevents a self-hosted install from depending on an image that was not mirrored
into its registry.

One value selects the normal behavior:

```yaml
observability:
  profile: compute
```

Enable the collector after publishing it under `global.image`. Its repository
defaults to `${global.image.registry}/${global.image.repository}/nvcf-otel-collector`:

```yaml
global:
  nvcaOperator:
    selfManaged:
      otelCollector:
        enabled: true
```

If the collector is published outside that global image path, set its explicit
repository override while opting in:

```yaml
global:
  nvcaOperator:
    selfManaged:
      otelCollector:
        enabled: true
        imageRepository: nvcr.io/example/collectors/nvcf-otel-collector
```

## Chart and Image Sources

The stack pins the NVCA operator chart in
`helmfile.d/02-nvca.yaml.gotmpl`. Compute-plane base values supply the tested
NVCA, NVCA operator, and OTel collector image tags. The chart supplies the
image credential helper and shared storage image tags.

Use `global.helm.sources` for chart repository location and `global.image` for
container image repository location. The stack rewrites repositories through
those global values. Chart defaults provide fallback versions for values the
compute-plane stack does not override.

Only set `global.nvcaOperator.selfManaged.imageCredHelper.imageTag` when
pinning a tested replacement helper image.

For pre-release validation, `global.nvcaOperator.chartPath` can point to a
local NVCA operator chart. Leave it empty for the pinned released chart.

## Multiple Isolated Control Planes

Set the same control-plane identity used by the selected self-managed control
plane. The identity must be a lowercase DNS label no longer than 20 characters:

```yaml
global:
  controlPlane:
    id: plane-a
```

A named compute-plane install creates the Helm release and operator namespace
`plane-a-nvca-operator`. The operator then owns only the derived namespaces
`plane-a-nvca-system`, `plane-a-nvcf-backend`, and
`plane-a-nvca-modelcache-init`. Empty identity keeps the legacy names.

Install cluster-wide prerequisites once, independently from either control
plane. This includes the shared `NVCFBackend` CRD; named worker releases never
own or remove that definition. Named `install`, `apply`, and `destroy` commands
automatically read the identity from the selected environment and select only
the per-instance NVCA release. `CONTROL_PLANE_ID` remains available as an
explicit override:

```sh
make install-shared-prerequisites HELMFILE_ENV=<env>

make install \
  CLUSTER_NAME=gpu-plane-a \
  HELMFILE_ENV=plane-a

make install \
  CLUSTER_NAME=gpu-plane-b \
  HELMFILE_ENV=plane-b
```

The command-line identity must match `global.controlPlane.id` when both are
set. Named `install` and `apply` create the operator namespace with its
`nvcf.nvidia.com/control-plane-id` ownership label, or verify that an existing
namespace has the expected label before Helm writes resources. Named `destroy`
checks that label before uninstall and again before deleting the namespace.

Re-run `install-shared-prerequisites` before a worker upgrade that changes the
CRD. Removing one instance leaves shared prerequisites and the other instance
untouched. `make destroy-shared-prerequisites` refuses to run while any labeled
named compute-plane namespaces remain; remove every named compute plane first.

## Helmfile Version Note

Helmfile v1.2.0+ changed `helmfile.d/` processing to parallel mode which breaks
implicit ordering. Use helmfile v1.1.x:

```sh
# Pinned binaries are auto-downloaded by the dev Makefile:
make install CLUSTER_NAME=...   # downloads helmfile v1.1.9 + helm v3.15.4 on first run
```

## Optional Add-ons

The stack can install KAI Scheduler, Grove, and Dynamo. KAI provides resource
allocation and gang placement, Grove orchestrates related Pod groups, and
Dynamo describes inference services that Grove manages. GPU clique
topology-aware scheduling connects those workloads to the cluster's NVLink
topology.

All add-ons are disabled by default. Grove requires KAI Scheduler, and Dynamo
requires Grove. Topology-aware scheduling also requires KAI Scheduler. Enable
the complete stack in `environments/<env>.yaml`:

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
`topologyAwareScheduling.enabled=true` and installs one
`ClusterTopologyBinding` per KAI `Topology`. The default topology labels are
`nvidia.com/gpu.clique` and `kubernetes.io/hostname`, in that order.

Enabling `addons.kaiScheduler.enabled` or `addons.dynamoOperator.enabled` also
adds the matching NVCA feature gate. Enabling KAI, Grove, or Dynamo permits
their workload resource types in the NVCA validation policy.

See [Gang Scheduling](../../../docs/user/cluster-management/gang-scheduling.md)
for atomic workload placement and
[Topology-Aware Scheduling](../../../docs/user/cluster-management/topology-aware-scheduling.md)
for GPU clique placement. See
[KAI Scheduler](../../../docs/user/cluster-management/kai-scheduler.md) for
queue configuration and standalone installation.

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
