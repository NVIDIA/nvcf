# KAI Scheduler Integration Guide

[KAI Scheduler](https://github.com/kai-scheduler/KAI-Scheduler) is an open source Kubernetes Native scheduler for AI workloads at large scale.
To use the KAI Scheduler for NVCF Workloads the following configuration should be applied post the installation of the KAI Scheduler in the cluster and the [Optimized AI Workload Scheduling](./configuration) enabled on the
cluster. NVCF Workloads deployed will be automatically BinPacked upon this cluster configuration changes.

**KAI Scheduler Installation**

<Note>
Upgrade to latest [KAI Scheduler release](https://github.com/kai-scheduler/KAI-Scheduler/releases) is recommended to get latest fixes and security patches

</Note>

Create `values.yaml` with queue attributes:

<Accordion title="kai-scheduler-queues.yaml">
```yaml title="kai-scheduler-queues.yaml"
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
