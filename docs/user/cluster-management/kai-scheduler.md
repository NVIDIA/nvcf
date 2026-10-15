# KAI Scheduler Integration Guide

[KAI Scheduler](https://github.com/kai-scheduler/KAI-Scheduler) is an open
source Kubernetes scheduler for AI workloads. NVCF uses it for GPU bin-packing,
queues, gang scheduling, and topology-aware placement.

## Install KAI Scheduler

<Note>
Use a tested [KAI Scheduler release](https://github.com/kai-scheduler/KAI-Scheduler/releases)
that is compatible with the NVCF compute plane stack.
</Note>

Set `addons.kaiScheduler.enabled` in the `nvcf-compute-plane` Helmfile
environment to install KAI Scheduler as release and namespace `kai-scheduler`.
Grove, Dynamo, and topology-aware scheduling require this add-on. Skip the
manual installation below when the add-on is enabled.

Use the manual path when KAI is managed outside the compute plane stack.

NVCA expects a parent queue named `default-parent-queue` and a child queue
named `default-queue`. Other queues may also exist.

<Warning>
Set unlimited (`-1`) quotas and limits on every queue used for NVCF workloads.
This lets NVCA track the complete cluster capacity. If NVCF and non-NVCF
workloads share a cluster with limited KAI queues, enable
[Shared Cluster mode](./configuration.md#cluster-features) so NVCA excludes
non-NVCF nodes from capacity tracking and scheduling.
</Warning>

Create `values.yaml` with the required default queues:

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

## Schedule multi-Pod workloads

KAI can hold a multi-Pod workload until all required members fit. Grove and
Dynamo build on this behavior for multi-role inference services. See
[Gang Scheduling](./gang-scheduling.md) for add-on configuration, workload
examples, supported resource types, and troubleshooting.

On NVLink-optimized clusters, KAI can also place the complete gang in one GPU
clique. See
[Topology-Aware Scheduling](./topology-aware-scheduling.md) for GPU DRA
prerequisites, topology configuration, Grove bindings, and function examples.
