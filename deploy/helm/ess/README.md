# NVCF ESS API Helm Chart

This repository contains the Helm chart for deploying the NVCF ESS (Encrypted Secrets Service) API on Kubernetes.

## Overview

The chart packages the ESS API deployment along with its Vault Agent sidecar configuration for fetching service credentials from a Vault or OpenBao backend.

The default chart values do not set the required image registry and repository. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
ess:
  image:
    registry: <your-registry>
    repository: <your-org>/ess-api
    tag: <appVersion>
```

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`
- A reachable Cassandra cluster
- A reachable Vault or OpenBao instance with a JWT authentication path configured for this service

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install ess-api ess-api \
  --namespace ess-api \
  --create-namespace \
  --values ess-api/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade ess-api ess-api \
  --namespace ess-api \
  --values ess-api/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall ess-api --namespace ess-api
```

## Configuration

The default chart configuration lives in `ess-api/values.yaml`.

Important settings to review before deployment:

- `ess.image.*` for the ESS API container image
- `ess.imagePullSecrets` for private registry access
- `ess.replicaCount`, resource requests, and limits for your environment
- `ess.configuration.*` for the Spring profile (`springProfile`) and additional Java options (`additionalJavaOpts`)

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

## Autoscaling (HPA)

The chart can provision a [HorizontalPodAutoscaler](https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/) (`autoscaling/v2`) for the ESS API deployment. It is rendered by `ess-api/templates/hpa.yaml` and is disabled by default.

### Configuration

Set the values under `ess.autoscaling`:

```yaml
ess:
  autoscaling:
    enabled: true
    minReplicas: 1
    maxReplicas: 12
    targetCPUUtilizationPercentage: 70      # scale to keep avg CPU at this % of the CPU request
```

| Value | Description |
| --- | --- |
| `enabled` | Create the HPA when `true`. When `false`, no HPA is rendered and the deployment stays at `ess.replicaCount`. |
| `minReplicas` | Lower bound on replica count. |
| `maxReplicas` | Upper bound on replica count. |
| `targetCPUUtilizationPercentage` | Target average CPU utilization, as a percentage of the pod's CPU request. Optional. |
| `targetMemoryUtilizationPercentage` | Target average memory utilization, as a percentage of the pod's memory request. Optional. |

Notes:

- At least one metric is required. If `enabled: true` but neither `targetCPUUtilizationPercentage` nor `targetMemoryUtilizationPercentage` is set, the template fails the render with a clear error rather than producing an HPA that never scales.
- Utilization targets are evaluated against the pod requests (`ess.resources.requests`), so meaningful requests must be set for autoscaling to behave predictably.
- HPA targets the Deployment rendered by ess-api/templates/deployment.yaml
- Autoscaling requires the Kubernetes [metrics-server](https://github.com/kubernetes-sigs/metrics-server) to be installed in the cluster.

### Scaling behavior

The chart does not set a custom `behavior` block, so Kubernetes defaults apply. The timings below are typical Kubernetes defaults, not guaranteed; actual response varies with the controller sync period, metrics collection interval, and pod readiness delays:

- Scale-up is fast: the controller typically reacts within one sync interval (~15s by default) with no stabilization delay.
- Scale-down is conservative: it typically requires sustained low utilization across the default 5-minute stabilization window before reducing replicas.
- When both CPU and memory metrics are configured, the controller computes a desired replica count per metric and uses the higher one. Consequently it will only scale down when *both* metrics are below their targets.

### Caveat: memory is a poor autoscaling signal

Prefer CPU-based autoscaling for ESS. Memory utilization (`targetMemoryUtilizationPercentage`) is an unreliable scaling trigger for this workload, for several reasons:

- Memory is not elastic. Unlike CPU, high memory usage usually does not mean the pod is overloaded, and adding replicas does not reduce the memory already held by existing pods. Scaling out on memory often adds capacity that does not relieve the condition that triggered it.
- The JVM retains heap. ESS runs on the JVM, which holds onto heap it has allocated rather than returning it to the OS. Container memory utilization therefore tends to climb to a plateau and stay there even after load subsides.
- It blocks scale-down. Because scale-down requires *all* configured metrics to be below target, a memory metric stuck near its plateau keeps the memory-derived replica recommendation high and prevents scale-down even when CPU is idle, leaving replicas pinned at an elevated count long after traffic stops.
- It can cause runaway scale-up or flapping. Startup heap allocation, caches, or GC timing can push memory past the target and trigger scale-ups that never bring per-pod memory back down, sometimes driving the deployment straight to `maxReplicas`.

If you must autoscale on memory, set `-Xmx` and the container memory request/limit so that steady-state idle memory sits comfortably below `targetMemoryUtilizationPercentage`, and validate scale-down behavior under realistic load before relying on it. Otherwise, configure CPU only and leave `targetMemoryUtilizationPercentage` unset.

### Verifying

The HPA name is derived from the chart's fullname and depends on the release name and any name overrides, so look it up first, then describe it by name:

```bash
kubectl get hpa -n <namespace>
kubectl describe hpa <hpa-name> -n <namespace>   # use the NAME shown above; current vs target metrics + scaling events
kubectl get hpa -n <namespace> -w                # watch scaling decisions live
```

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
