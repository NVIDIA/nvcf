# KAI Scheduler Integration Guide

[KAI Scheduler](https://github.com/kai-scheduler/KAI-Scheduler) is an open
source Kubernetes scheduler for AI workloads. NVCF integrates with KAI to
bin-pack GPU workloads onto eligible nodes to help improve cluster utilization.

KAI coexists with the default Kubernetes scheduler. When the `KAIScheduler`
feature gate is enabled, the NVIDIA Cluster Agent (NVCA) assigns NVCF workload
Pods to KAI. Other Pods can continue using the default scheduler.

## Operational responsibilities

The platform operator owns the KAI installation and lifecycle. NVCA manages
its integration with KAI, not the KAI service itself.

| Component | Responsibility |
| --- | --- |
| Platform operator | Install and configure KAI, enable it for NVCA, and manage monitoring, availability, and upgrades. |
| NVCA | Create NVCF workloads and assign their scheduler and queue. |
| KAI Scheduler | Select eligible nodes and schedule the workload Pods. |

The compute plane stack can automate KAI installation and configuration.
This does not transfer lifecycle ownership to NVCA.

## Install KAI Scheduler

<Note>
Use a tested [KAI Scheduler release](https://github.com/kai-scheduler/KAI-Scheduler/releases)
that is compatible with your NVCF compute plane stack.
</Note>

### Use the compute plane stack

Set the following in your `nvcf-compute-plane` Helmfile environment:

```yaml
addons:
  kaiScheduler:
    enabled: true
```

The add-on installs KAI as release and namespace `kai-scheduler`, configures
its default queues, and enables NVCA's `KAIScheduler` feature gate unless
explicitly disabled with `-KAIScheduler`. Apply the environment through your
compute plane installation workflow. Skip the manual installation below.

### Use an existing or separately managed installation

If KAI is managed outside the compute plane stack, leave the installation
add-on disabled and configure KAI with the values below. Do not install a
second KAI release.

NVCA expects a parent queue named `default-parent-queue` and a child queue
named `default-queue`. Other queues may also exist.

<Warning>
Set unlimited (`-1`) quotas and limits on the queues used for NVCF workloads.
NVCA relies on this configuration for cluster capacity and usage tracking.
</Warning>

If NVCF and non-NVCF workloads share a cluster with limited KAI queues,
enable [Shared Cluster mode](./configuration.md#cluster-features) so NVCA
excludes non-NVCF nodes from capacity tracking and scheduling. Nodes running
NVCF workloads must be labeled `nvca.nvcf.nvidia.io/schedule=true`.

Create `values.yaml` with the required scheduler and queue settings:

<Accordion title="values.yaml">

```yaml title="values.yaml"
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

For a new installation, replace `<kai-version>` with the `version` of the
`kai-scheduler` release in
`deploy/stacks/nvcf-compute-plane/helmfile.d/01-dependencies.yaml.gotmpl`.
Read this file from your selected compute plane release tag, not from `main`.

```bash
helm install kai-scheduler \
  oci://ghcr.io/kai-scheduler/kai-scheduler/kai-scheduler \
  --namespace kai-scheduler --create-namespace \
  --version <kai-version> -f values.yaml
```

For an existing KAI release, apply the same values with `helm upgrade`:

```bash
helm upgrade kai-scheduler \
  oci://ghcr.io/kai-scheduler/kai-scheduler/kai-scheduler \
  --namespace kai-scheduler \
  --version <kai-version> -f values.yaml
```

After KAI is ready, add `KAIScheduler` to the existing NVCA feature-gate list.
For standalone NVCA Helm values, use `selfManaged.featureGateValues`.
For compute plane environment values, use
`global.nvcaOperator.selfManaged.featureGateValues`.
Preserve the other feature gates when updating the list. See
[Managing Feature Flags](./configuration.md#managing-feature-flags).

Installing KAI alone does not enable NVCA to use it. Both the KAI installation
and the NVCA feature gate are required.

## Verify the integration

Check the KAI components and default queues in the compute cluster:

```bash
kubectl get pods -n kai-scheduler
kubectl get queues.scheduling.run.ai default-parent-queue default-queue -o yaml
```

After deploying a function, inspect its workload Pods:

```bash
kubectl get pods -n <workload-namespace> \
  -o custom-columns='NAME:.metadata.name,SCHEDULER:.spec.schedulerName,NODE:.spec.nodeName' \
  -L kai.scheduler/queue
```

New NVCF workload Pods should use `kai-scheduler` and `default-queue`.
If a Pod remains `Pending`, check its events, KAI component health, queue
configuration, and available resources on eligible nodes.

## Maintain KAI Scheduler

- Monitor KAI component readiness and pending workload Pods.
- Validate KAI upgrades with your NVCA and compute plane versions before
  applying them to production. Follow the selected KAI release's upgrade guidance.
- After an upgrade, verify the queues and deploy a test function to confirm
  scheduling, readiness, and invocation.

<Note>
Pods assigned to KAI do not automatically fall back to the default scheduler.
If KAI is unavailable, new or replacement workload Pods can remain pending.
The platform operator is responsible for restoring KAI availability.
</Note>

## Schedule multi-Pod workloads

KAI can hold a multi-Pod workload until all required members fit. Grove and
Dynamo build on this behavior for multi-role inference services. See
[Gang Scheduling](./gang-scheduling.md) for add-on configuration, workload
examples, supported resource types, and troubleshooting.

On NVLink-optimized clusters, KAI can also place the complete gang in one GPU
clique. See
[Topology-Aware Scheduling](./topology-aware-scheduling.md) for GPU DRA
prerequisites, topology configuration, Grove bindings, and function examples.
