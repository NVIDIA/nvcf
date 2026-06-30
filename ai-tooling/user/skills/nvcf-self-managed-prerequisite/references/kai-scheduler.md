# KAI Scheduler — install detail

Reference for the KAI Scheduler step in `nvcf-self-managed-prerequisite/SKILL.md`.

## What KAI Scheduler is

An open-source Kubernetes-native scheduler for AI workloads at large scale. The NVCA operator's `KAIScheduler` feature gate requires it installed **and** correctly quota'd. Without it, NVCA crash-loops on the feature value and cluster-group registration with SIS never completes.

## Version pin

KAI moves with the NVCF stack. Cross-check the chart version against the NVCF KAI Scheduler docs version table (linked below) before installing; that table lists the version NVCF has validated against the current NVCA / SIS combination. KAI is an upstream chart, not an NVCF image, so it is not in `manifest.yaml`.

At the time of writing, the pinned chart version is `v0.14.0`. See [NVCF KAI Scheduler docs](https://docs.nvidia.com/cloud-functions/current/latest/cluster-management/kai-scheduler.html) for the canonical version table.

## Install

```bash
cat > nvca-values.yaml << 'EOF'
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
EOF

helm install kai-scheduler \
  oci://ghcr.io/kai-scheduler/kai-scheduler/kai-scheduler \
  -n kai-scheduler \
  --create-namespace \
  --version v0.14.0 -f nvca-values.yaml \
  --wait --timeout 5m
```

Verify pods come up:

```bash
kubectl get pods -n kai-scheduler
# Expect 7 pods, all Running (controller + queue + scheduler + admission components).
```

## Failure modes

| Symptom | Cause | Fix |
| ------- | ----- | --- |
| `kai-scheduler` namespace pods stuck `Pending` with `Insufficient cpu` | KAI's controller pods couldn't be scheduled on small system pools | Add a system / general-purpose node pool with at least 2 CPU + 2Gi memory free |
| `helm install kai-scheduler` fails on `oci://ghcr.io` pull | Cluster can't reach ghcr.io (air-gap or proxy) | Mirror the chart to your private registry first and install from there |
| `Queue` CRs not present after install | Helm install succeeded but CRDs didn't apply | Re-run with `--wait --timeout 5m`; if still missing, check `kubectl get crd queues.scheduling.run.ai` |

## Uninstall

```bash
helm uninstall kai-scheduler -n kai-scheduler
kubectl delete namespace kai-scheduler
```

## References

- [NVCF KAI Scheduler docs](https://docs.nvidia.com/cloud-functions/current/latest/cluster-management/kai-scheduler.html)
- [KAI Scheduler upstream](https://github.com/kai-scheduler/kai-scheduler)
