# ESS Metrics

| Metric name                                | Metric type | Source                                | Description                            | Unit (where applicable) | Interesting Labels                                                  | Required Filters (where applicable) |
| ------------------------------------------ | ----------- | ------------------------------------- | -------------------------------------- | ----------------------- | ------------------------------------------------------------------- | ----------------------------------- |
| ess_templates_rendered_total               | Counter     | ess-api:9464/metrics                  | Total number of ESS templates rendered |                         | function_id, function_version_id, status                            |                                     |
| http_client_request_duration_seconds_count | Gauge       | nvcf-invocation-service:41337/metrics | HTTP Request Duration                  | seconds                 | http_request_method, http_response_status_code, http_route, service | server_address="ess.ngc.nvidia.com" |
