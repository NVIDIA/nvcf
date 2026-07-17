# KAI Scheduler Integration Guide

[KAI Scheduler](https://github.com/kai-scheduler/KAI-Scheduler) is an open source Kubernetes Native scheduler for AI workloads at large scale.
To use the KAI Scheduler for NVCF Workloads the following configuration should be applied post the installation of the KAI Scheduler in the cluster and the [Optimized AI Workload Scheduling](./configuration.md#managing-feature-flags) enabled on the
cluster. NVCF Workloads deployed will be automatically BinPacked upon this cluster configuration changes.

**KAI Scheduler Installation**

<Note>
Upgrade to latest [KAI Scheduler release](https://github.com/kai-scheduler/KAI-Scheduler/releases) is recommended to get latest fixes and security patches
</Note>

Create `values.yaml` with queue attributes ([download template](samples/kai-scheduler-queues.yaml)):

<details>
<summary>kai-scheduler-queues.yaml</summary>

```yaml
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

</details>

```bash

  helm install kai-scheduler oci://ghcr.io/kai-scheduler/kai-scheduler/kai-scheduler -f values.yaml -n kai-scheduler --create-namespace --version v0.14.0
```

