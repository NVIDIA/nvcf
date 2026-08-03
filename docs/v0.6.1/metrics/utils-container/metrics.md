# Utils Container Metrics

| Metric name                                 | Metric type | Source                                      | Description                    | Unit (where applicable) | Interesting Labels                               | Required Filters (where applicable) |
| ------------------------------------------- | ----------- | ------------------------------------------- | ------------------------------ | ----------------------- | ------------------------------------------------ | ----------------------------------- |
| kube_pod_container_status_restarts_total    | Counter     | prometheus-kube-state-metrics:8080/metrics  | Pod restart count              |                         | function_id, function_version_id, namespace, pod | container="utils"                   |
| kube_pod_container_status_terminated_reason | Gauge       | prometheus-kube-state-metrics:8080/metrics  | Reason pod was terminated      |                         | namespace, pod, reason                           | container="utils"                   |
| kube_pod_container_status_waiting_reason    | Gauge       | prometheus-kube-state-metrics:8080/metrics  | Reason pod is waiting to start |                         | namespace, pod                                   | container="utils"                   |
| nvcf_worker_service_response_total          | Gauge       | prometheus-agent-metrics-utils:8010/metrics |                                |                         |                                                  |                                     |
