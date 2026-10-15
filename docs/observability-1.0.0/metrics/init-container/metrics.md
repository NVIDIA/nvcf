# Init Container Metrics

| Metric name                                 | Metric type | Source                                     | Description                                  | Unit (where applicable) | Interesting Labels                               | Required Filters (where applicable) |
| ------------------------------------------- | ----------- | ------------------------------------------ | -------------------------------------------- | ----------------------- | ------------------------------------------------ | ----------------------------------- |
| kube_pod_container_status_restarts_total    | Counter     | prometheus-kube-state-metrics:8080/metrics | Total number of restarts for init containers |                         | function_id, function_version_id, namespace, pod | container="init"                    |
| kube_pod_container_status_terminated_reason | Gauge       | prometheus-kube-state-metrics:8080/metrics | Reason an init container terminated          |                         | namespace, pod, reason                           | container="init"                    |
| kube_pod_container_status_waiting_reason    | Gauge       | prometheus-kube-state-metrics:8080/metrics | Reason an init container is waiting to start |                         | namespace, pod, reason                           | container="init"                    |
