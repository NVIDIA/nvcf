# KAI Scheduler Integration Guide

[KAI Scheduler](https://github.com/kai-scheduler/KAI-Scheduler) is an open source Kubernetes Native scheduler for AI workloads at large scale.
To use the KAI Scheduler for NVCF Workloads the following configuration should be applied post the installation of the KAI Scheduler in the cluster and the [Optimized AI Workload Scheduling](./configuration.md) enabled on the
cluster. NVCF Workloads deployed will be automatically BinPacked upon this cluster configuration changes.

**KAI Scheduler Installation**

<Note>
Upgrade to latest [KAI Scheduler release](https://github.com/kai-scheduler/KAI-Scheduler/releases) is recommended to get latest fixes and security patches

</Note>

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
helm install kai-scheduler oci://ghcr.io/kai-scheduler/kai-scheduler/kai-scheduler -f values.yaml -n kai-scheduler --create-namespace --version v0.12.6
```
