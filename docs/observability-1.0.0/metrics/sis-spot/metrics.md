# SIS/SPOT Metrics

| Metric name                                | Metric type | Source                             | Description                      | Unit (where applicable) | Interesting Labels                                                  | Required Filters (where applicable)                               |
| ------------------------------------------ | ----------- | ---------------------------------- | -------------------------------- | ----------------------- | ------------------------------------------------------------------- | ----------------------------------------------------------------- |
| http_client_request_duration_seconds_count | Counter     | spot-instance-service:9464/metrics | http_response codes with timings |                         | http_request_method, http_response_status_code, http_route, service | server_address="spot.gdn.nvidia.com", namespace="gdn-spot-api-fp" |
