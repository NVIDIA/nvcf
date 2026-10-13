# KAI Scheduler Integration Guide

[KAI Scheduler](https://github.com/kai-scheduler/KAI-Scheduler) is an open
source Kubernetes scheduler for AI workloads. NVCF integrates with KAI to
place GPU workload Pods. With the configuration in this guide, KAI bin-packs
those Pods onto eligible nodes. This keeps whole nodes free for larger
workloads and improves cluster utilization. Helm functions can also request
gang scheduling and topology-aware placement in their charts. See
[Gang Scheduling](./gang-scheduling.md) and
[Topology-Aware Scheduling](./topology-aware-scheduling.md).

KAI coexists with the default Kubernetes scheduler. When the `KAIScheduler`
feature gate is enabled, the NVIDIA Cluster Agent (NVCA) assigns NVCF workload
Pods to KAI. Other Pods can continue using the default scheduler. When the
feature gate is disabled, all Pods use the default scheduler, even if KAI is
installed.

## Operational responsibilities

The platform operator owns the KAI installation and lifecycle. NVCA manages
its integration with KAI, not the KAI service itself.

| Component | Responsibility |
| --- | --- |
| Platform operator | Install and configure KAI, enable it for NVCF, and manage monitoring, availability, and upgrades. |
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

The add-on manages the Helm release `kai-scheduler` in the `kai-scheduler`
namespace. If a release with that name exists, enabling the add-on upgrades
it to the version pinned by the compute plane stack and applies the stack's
values. A KAI installation under another release name or namespace is not
adopted. Remove it before enabling the add-on, or keep the add-on disabled
and follow the next section.

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

Installing KAI alone does not enable NVCF to use it. Both the KAI installation
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
If a Pod remains `Pending`, see [Troubleshoot the integration](#troubleshoot-the-integration).

## Troubleshoot the integration

When the `KAIScheduler` feature gate is enabled, NVCA checks the KAI queue
configuration and reports the result as the `kai-scheduler-queues` health
component. A failed check marks the agent unhealthy.

Check the agent status:

```bash
kubectl get nvcfbackends.nvcf.nvidia.io -A
```

If `HEALTH` is not `healthy`, read the component detail from the NVCA
health endpoint:

```bash
kubectl get --raw /api/v1/namespaces/nvca-system/services/nvca:8000/proxy/healthz \
  | jq '.Components["kai-scheduler-queues"]'
```

| Error contains | Cause | Fix |
| --- | --- | --- |
| `KAI Scheduler is not installed` | The feature gate is on but the KAI CRDs are missing. | Install KAI, or remove `KAIScheduler` from the feature gates. |
| `either one or both were not found` | `default-parent-queue` or `default-queue` does not exist. | Apply the queue values from this guide. |
| `Queue hierarchy misconfigured` | `default-queue` is not a child of `default-parent-queue`. | Correct `parentName` and `childName` in the KAI values. |
| `resource violation for queue` | A queue quota, limit, or `overQuotaWeight` differs from `-1`, `-1`, `1`. | Restore the unlimited values for CPU, GPU, and memory. |

If the component is healthy but workload Pods stay `Pending`, the queue
configuration is correct. Confirm that the Pod uses `kai-scheduler` and that
the KAI components are running, then check the `Unschedulable` event on the
Pod and the KAI scheduler logs:

```bash
kubectl get pods -n kai-scheduler
kubectl -n <workload-namespace> describe pod <pod-name>
kubectl -n kai-scheduler logs deploy/kai-scheduler-default --tail=100
```

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
Dynamo build on this behavior for multi-role inference services.

A Helm function requests this in its chart. NVCA passes the chart's KAI,
Grove, and Dynamo resources through when the matching add-ons are enabled.
It does not create `PodGroup` resources or set `minMember` on a workload's
behalf. See [Gang Scheduling](./gang-scheduling.md) for add-on setup,
workload examples, and troubleshooting.

On NVLink-optimized clusters, KAI can also place the complete gang in one GPU
clique. See
[Topology-Aware Scheduling](./topology-aware-scheduling.md) for GPU DRA
prerequisites, topology configuration, Grove bindings, and function examples.
