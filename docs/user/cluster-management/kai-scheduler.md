# KAI Scheduler Integration Guide

[KAI Scheduler](https://github.com/kai-scheduler/KAI-Scheduler) is an open source Kubernetes Native scheduler for AI workloads at large scale.
To use the KAI Scheduler for NVCF Workloads the following configuration should be applied post the installation of the KAI Scheduler in the cluster and the [Optimized AI Workload Scheduling](./configuration.md) enabled on the
cluster. NVCF Workloads deployed will be automatically BinPacked upon this cluster configuration changes.

**KAI Scheduler Installation**

<Note>
    Upgrade to latest [KAI Scheduler release](https://github.com/kai-scheduler/KAI-Scheduler/releases) is recommended to get latest fixes and security patches

</Note>

When you enable `addons.kaiScheduler.enabled` in the `nvcf-compute-plane` Helmfile stack, the stack installs KAI Scheduler for you (release and namespace `kai-scheduler`). Enable that flag whenever you enable Grove or Dynamo. Skip the manual install below in that case.

Use the manual install when you need KAI without the compute-plane add-on, for example when enabling only the NVCA `KAIScheduler` feature gate.

NVCA's KAI scheduler integration expects default queues to exist with names `default-parent-queue` (parent) and `default-queue` (child);
other queues may exist in the cluster.

<Warning>
One caveat is that NVCA expects all queues used to create NVCF workloads to have unlimited (`-1`) quotas and limits
to ensure full cluster capacity utilization and accurate usage tracking. If the cluster is partitioned to serve both NVCF and non-NVCF workloads
and KAI scheduler queue quotas/limits are limited to reflect this, then [Shared Cluster mode](./configuration.md#cluster-features) must be enabled so non-NVCF workload nodes
are accurately excluded from tracking and scheduling by NVCA.

</Warning>

Create `values.yaml` with [default queue](https://raw.githubusercontent.com/NVIDIA/KAI-Scheduler/refs/heads/main/docs/quickstart/default-queues.yaml) attributes:

<Accordion title="kai-scheduler-queues.yaml">
```yaml title="kai-scheduler-queues.yaml"
scheduler:
  placementStrategy: binpack
  plugins:
    nodeplacement:
      arguments:
        gpu: binpack
        cpu: spread
  actions:
    preempt:
      enabled: false
    consolidation:
      enabled: false

defaultQueue:
  createDefaultQueue: true
  parentName: default-parent-queue
  childName: default-queue
  parentResources:
    cpu:
      quota: -1
      limit: -1
      overQuotaWeight: 1
    gpu:
      quota: -1
      limit: -1
      overQuotaWeight: 1
    memory:
      quota: -1
      limit: -1
      overQuotaWeight: 1
  childResources:
    cpu:
      quota: -1
      limit: -1
      overQuotaWeight: 1
    gpu:
      quota: -1
      limit: -1
      overQuotaWeight: 1
    memory:
      quota: -1
      limit: -1
      overQuotaWeight: 1
```
</Accordion>

```bash
helm install kai-scheduler oci://ghcr.io/kai-scheduler/kai-scheduler/kai-scheduler -f values.yaml -n kai-scheduler --create-namespace --version v0.14.0
```

## NVLink Clique Gang Scheduling

On [NVLink-optimized clusters](./configuration.md#nvlink-optimized-clusters), a multi-node
function must land entirely inside one NVLink clique to get the inter-node bandwidth it was
sized for. Without a topology constraint, KAI Scheduler binds pods one at a time and
bin-packs the first pod into the most saturated clique. If that clique cannot hold the rest
of the replicas, the remaining pods stay `Pending` and the deployment never becomes ready.
Redeploying may succeed only because placement happens to land somewhere else.

A cluster `Topology` fixes this. It tells KAI Scheduler which node labels describe the
clique hierarchy, so it can hold placement until every replica of a workload fits in a
single clique.

### Install the Topology

The compute plane stack ships this as an optional add-on. Enable it in
`environments/<env>.yaml` and run `make apply`:

```yaml
addons:
  kaiScheduler:
    enabled: true
    clusterTopologies:
      enabled: true
      topologies:
        - name: nvcf-mnnvl-topology
          levels:
            - nodeLabel: nvidia.com/gpu.clique
            - nodeLabel: kubernetes.io/hostname
```

The add-on requires the `kai-scheduler` release (enable `addons.kaiScheduler.enabled`)
and KAI Scheduler v0.12.0 or later, which is when the native `kai.scheduler/v1alpha1`
Topology CRD was introduced. For each entry under `topologies` it creates a
cluster-scoped resource such as:

```yaml
apiVersion: kai.scheduler/v1alpha1
kind: Topology
metadata:
  name: nvcf-mnnvl-topology
spec:
  levels:
  - nodeLabel: nvidia.com/gpu.clique
  - nodeLabel: kubernetes.io/hostname
```

The `nvidia.com/gpu.clique` label is applied by the NVIDIA GPU DRA driver, which is already
a prerequisite on NVLink-optimized clusters.

Verify the resource exists:

```bash
kubectl get topologies.kai.scheduler nvcf-mnnvl-topology
```

### Opt a function in

Creating the Topology changes nothing on its own. Each workload opts in through annotations
on the StatefulSet, described in [Helm Functions](../helm-functions.md#multi-node-gang-scheduling).

### Gang-scheduling object types

Enabling the `KAIScheduler` feature gate adds the KAI `PodGroup` type to the cluster
validation policy, and enabling the Grove add-on adds the `PodCliqueSet`, `PodClique`,
`PodCliqueScalingGroup`, and `PodGang` types. The Cluster Agent needs these to render,
admit, and clean up the objects the schedulers create for a function, and to grant itself
the matching RBAC. Charts that ship their own gang-scheduling objects are validated against
the same list.
