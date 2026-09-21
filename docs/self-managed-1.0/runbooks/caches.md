# Caches Runbook

## UCC & DDCS

Caching is a critical component of the rendering pipeline. However, being that these
services are caches, data loss is tolerated by the rendering pipeline. Offline pods
or missing data will result in reduced simulation performance, i.e. increased load times.

Each DDCS replica/pod is a partition of the data set. Partial data loss in DDCS
will trigger re-computation for some or all of the data set.

UCC replicas serve as a pull-through cache for USD assets and are populated on-demand
during scene load time. Each replica will contain a distinct
copy of the source content. Data loss of a pod means traffic serviced by that peer
may see reduced performance as the cache is rebuilt from requests to the source.

### Cache Co-locality

For maximum performance it is recommended that caches are deployed as close to the
pods they service as possible. We recommend placing one cache per availability zone
to reduce network transit times between the caches and the render nodes. It is also
recommended to keep all render nodes that are participating in a single simulation
in the same availability zone to further reduce network latency.

## Kubernetes Pod Health Monitoring with Prometheus

Monitoring Kubernetes with Prometheus is essential to detect signs of unhealthy
pods before they impact your application. Below are just a few Prometheus metrics
that can be monitored for issues.

| Metric | Signal |
| --- | --- |
| `kube_pod_status_ready` | Shows if a pod is ready or not to accept traffic. Pods in non-ready states for extended periods of time (e.g. more than 5min) indicate an unresolved problem. |
| `kube_pod_container_status_restarts_total` | Counts total restarts per container. High or increasing values indicate crashes or instability. Cache pods are long running processes and do not restart unless they encounter fatal errors. |
| `kube_pod_container_status_waiting_reason` | Waiting reasons such as `CrashLoopBackOff`, indicate persistent failures and restarts in the cache applications. |
| `kube_pod_status_phase` | When pod status is not `Running` for an extended period, this indicates a persistent failure state. |
| `container_cpu_usage_seconds_total` | Although more difficult to monitor, cache pods that consume large amounts of CPU cycles without traffic may indicate compaction issues. |

## Removing Unhealthy Pods and PVCs

Cache pods maintain a persistent data set. When cache services are in an unhealthy
state, (due to crashes, restarts etc.) it is recommended to delete the pod(s) and
storage to attempt to return the service to normal operation.

1. Delete the PVCs for the Pod *before* restarting it:

   `kubectl delete pvc <pvc-name> -n <namespace>`

2. Delete the Pod:

   `kubectl delete pod <pod-name> -n <namespace>`

Monitor the offending pod(s) and ensure that Kubernetes correctly re-creates and
attaches new PVCs to recreated Cache pod(s).
