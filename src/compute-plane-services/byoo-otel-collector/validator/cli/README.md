# BYOO Metrics Validator CLI

> Limitations
> - Only supports the metrics from Grafana Cloud
> - Only supports the platform metrics (does not include the OTLP metrics collected from users' applications)

### Introduction

The BYOO Metrics Validator CLI is a tool designed to validate metrics collected by the byoo-otel-collector. It helps ensure that your metrics are being properly collected and reported according to expected specifications.

This tool allows you to validate metrics for different wrapper types (function or task), workload types (container or helm), and cloud providers (GFN or non-GFN). It queries metrics from a Grafana Cloud Prometheus instance within a specified time range and verifies that the metrics meet the required criteria.

Key features:

- Validates metrics for different wrapper and workload combinations
- Configurable time ranges for metric validation
- Customizable allow list via configuration files
- Rules
  - Metrics
    - [ERROR] Required job is not present
    - [ERROR] No metrics are found for the job
    - [ERROR] "up" metric is 0
    - [ERROR] Metric is not in the allow list
    - [WARNING] Metric is not present
  - Attributes
    - [ERROR] Attribute is not in the allow list
    - [ERROR] Required metadata attributes is missing

### Run the validator locally

Prerequisite

- Be sure that your Python environment is >= 3.11.
- Install uv: https://docs.astral.sh/uv/getting-started/installation/#standalone-installer

```bash
# sync the required packages
uv sync

# Set the Grafana Cloud credentials before running the validator
export GRAFANA_CLOUD_PROMETHEUS_URL="xxx"
export GRAFANA_CLOUD_PROMETHEUS_USERNAME="xxx"
export GRAFANA_CLOUD_PROMETHEUS_PASSWORD="xxx"
```

Check the CLI

```bash
> uv run -m src.validator --help
usage: validator.py [-h] [--config CONFIG] --wrapper-type {function,task} --workload-type {container,helm} --cloud-provider {gfn,non-gfn} [--start START] [--end END]
                    [--log-level {debug,info,warning,error,critical}] [--extra-promql-filters EXTRA_PROMQL_FILTERS] [--golden]
                    id

Validator for metrics collected by byoo-otel-collector

positional arguments:
  id                    ID to validate

options:
  -h, --help            show this help message and exit
  --config CONFIG       Path to the configuration file
  --wrapper-type {function,task}
                        Wrapper type (function or task)
  --workload-type {container,helm}
                        Workload type (container or helm)
  --cloud-provider {gfn,non-gfn}
                        Cloud provider (gfn or non_gfn)
  --start START         Start time in RFC3339 format (default: 12 hours ago)
  --end END             End time in RFC3339 format (default: now)
  --log-level {debug,info,warning,error,critical}
                        Log level
  --extra-promql-filters EXTRA_PROMQL_FILTERS
                        Additional PromQL filters (e.g. 'job="my-job",env="prod"')
  --golden              Check if the metrics are golden (validating against golden metrics)
```

Validate the metrics

```bash
> uv run -m src.validator --cloud-provider=non-gfn --wrapper-type=function --workload-type=container aadb8822-7992-4d63-a771-76bdb7d2f402 
INFO     ###########################################################                                                    
INFO      Wrapper Type        : WrapperType.FUNCTION                                                                    
INFO      Workload Type       : WorkloadType.CONTAINER                                                                  
INFO      Cloud Provider      : CloudProvider.NON_GFN                                                                   
INFO      ID                  : aadb8822-7992-4d63-a771-76bdb7d2f402                                                    
INFO      Start               : 2025-06-01T11:10:12Z                                                                    
INFO      End                 : 2025-06-02T11:10:12Z                                                                    
INFO      Extra PromQL Filters:                                                                                         
INFO     ###########################################################                                                    
                                                                                                                        
                                                                                                                        
INFO     Found 5 jobs in the metrics data: ['nvidia-dcgm-exporter', 'byoo-test', 'kubernetes-cadvisor',                 
         'kube-state-metrics', 'opentelemetry-collector']                                                               
INFO     All required jobs are present                                                                                  
INFO     No unexpected jobs found                                                                                       
                                                                                                                        
                                                                                                                        
INFO     === Start validating metrics for job 'nvidia-dcgm-exporter' ===                                                
INFO     Found 2 metrics for this job.                                                                                  
INFO     === Finished metrics validation for job ===                                                                    
                                                                                                                        
                                                                                                                        
INFO     === Start validating metrics for job 'byoo-test' ===                                                           
INFO     Found 40 metrics for this job.                                                                                 
INFO     === Finished metrics validation for job ===                                                                    
                                                                                                                        
                                                                                                                        
INFO     === Start validating metrics for job 'kubernetes-cadvisor' ===                                                 
INFO     Found 27 metrics for this job.                                                                                 
WARNING  Metrics not found: {'container_cpu_cfs_throttled_seconds_total', 'container_fs_writes_total',                  
         'container_fs_limit_bytes', 'container_fs_reads_bytes_total', 'container_fs_reads_total',                      
         'container_fs_usage_bytes', 'container_fs_writes_bytes_total', 'container_cpu_cfs_throttled_periods_total'}    
INFO     === Finished metrics validation for job ===                                                                    
                                                                                                                        
                                                                                                                        
INFO     === Start validating metrics for job 'kube-state-metrics' ===                                                  
INFO     Found 9 metrics for this job.                                                                                  
WARNING  Metrics not found: {'kube_pod_container_status_terminated_reason',                                             
         'kube_pod_container_status_last_terminated_reason', 'kube_pod_container_status_last_terminated_exitcode',      
         'kube_pod_container_status_waiting_reason'}                                                                    
INFO     === Finished metrics validation for job ===                                                                    
                                                                                                                        
                                                                                                                        
INFO     === Start validating metrics for job 'opentelemetry-collector' ===                                             
INFO     Found 15 metrics for this job.                                                                                 
WARNING  Metrics not found: {'otelcol_receiver_refused_log_records_total',                                              
         'otelcol_receiver_accepted_log_records_total', 'otelcol_exporter_send_failed_log_records_total',               
         'otelcol_exporter_sent_log_records_total'}                                                                     
INFO     === Finished metrics validation for job ===                                                                    
                                                                                                                        
                                                                                                                        
INFO     ###########################################################                                                    
INFO      Validation Result:                                                                                            
INFO       - nvidia-dcgm-exporter          : Valid                                                                      
INFO       - byoo-test                     : Valid                                                                      
INFO       - kubernetes-cadvisor           : Valid with warnings                                                        
INFO       - kube-state-metrics            : Valid with warnings                                                        
INFO       - opentelemetry-collector       : Valid with warnings                                                        
INFO     ###########################################################
```

Vaidate the metrics against to the golden metrics
- Check the golden metrics in ../golden for reference
``` bash
> uv run -m src.validator --cloud-provider=non-gfn --wrapper-type=function --workload-type=container --golden aadb8822-7992-4d63-a771-76bdb7d2f402
INFO     ###########################################################                                                    
INFO      Wrapper Type        : WrapperType.FUNCTION                                                                    
INFO      Workload Type       : WorkloadType.CONTAINER                                                                  
INFO      Cloud Provider      : CloudProvider.NON_GFN                                                                   
INFO      ID                  : aadb8822-7992-4d63-a771-76bdb7d2f402                                                    
INFO      Start               : 2025-06-01T11:10:48Z                                                                    
INFO      End                 : 2025-06-02T11:10:48Z                                                                    
INFO      Extra PromQL Filters:                                                                                         
INFO     ###########################################################                                                    
                                                                                                                   
INFO     Validating against golden metrics...                                                                           
INFO     Number of golden metrics: 41                                                                                   
INFO     Number of actual metrics: 41                                                                                   
INFO     Printing diff with colorized output:                                                                           
--- 
+++ 
@@ -501,74 +501,69 @@
         "job": "byoo-test",
         "nca_id": "<SKIP_VALIDATION>",
         "service_name": "byoo-test"
       },
       "values": []
     },
     {
       "metric": {
         "__name__": "kube_pod_container_info",
         "cloud_provider": "<SKIP_VALIDATION>",
         "cloud_region": "<SKIP_VALIDATION>",
         "container": "inference",
         "function_id": "<SKIP_VALIDATION>",
         "function_version_id": "<SKIP_VALIDATION>",
         "host_id": "<SKIP_VALIDATION>",
-        "image": "<SKIP_VALIDATION>",
         "instance_id": "<SKIP_VALIDATION>",
         "job": "kube-state-metrics",
         "nca_id": "<SKIP_VALIDATION>",
         "pod": "0-sr-<SKIP_VALIDATION>",
         "service_name": "kube-state-metrics"
       },
       "values": []
     },
     {
       "metric": {
         "__name__": "kube_pod_container_resource_limits",
         "cloud_provider": "<SKIP_VALIDATION>",
         "cloud_region": "<SKIP_VALIDATION>",
         "container": "inference",
         "function_id": "<SKIP_VALIDATION>",
         "function_version_id": "<SKIP_VALIDATION>",
         "host_id": "<SKIP_VALIDATION>",
         "instance_id": "<SKIP_VALIDATION>",
         "job": "kube-state-metrics",
         "nca_id": "<SKIP_VALIDATION>",
         "pod": "0-sr-<SKIP_VALIDATION>",
-        "resource": "nvidia_com_gpu",
-        "service_name": "kube-state-metrics",
-        "unit": "integer"
+        "service_name": "kube-state-metrics"
       },
       "values": []
     },
     {
       "metric": {
         "__name__": "kube_pod_container_resource_requests",
         "cloud_provider": "<SKIP_VALIDATION>",
         "cloud_region": "<SKIP_VALIDATION>",
         "container": "inference",
         "function_id": "<SKIP_VALIDATION>",
         "function_version_id": "<SKIP_VALIDATION>",
         "host_id": "<SKIP_VALIDATION>",
         "instance_id": "<SKIP_VALIDATION>",
         "job": "kube-state-metrics",
         "nca_id": "<SKIP_VALIDATION>",
         "pod": "0-sr-<SKIP_VALIDATION>",
-        "resource": "nvidia_com_gpu",
-        "service_name": "kube-state-metrics",
-        "unit": "integer"
+        "service_name": "kube-state-metrics"
       },
       "values": []
     },
     {
       "metric": {
         "__name__": "kube_pod_container_status_ready",
         "cloud_provider": "<SKIP_VALIDATION>",
         "cloud_region": "<SKIP_VALIDATION>",
         "container": "inference",
         "function_id": "<SKIP_VALIDATION>",
         "function_version_id": "<SKIP_VALIDATION>",
         "host_id": "<SKIP_VALIDATION>",
         "instance_id": "<SKIP_VALIDATION>",
         "job": "kube-state-metrics",
         "nca_id": "<SKIP_VALIDATION>",
ERROR    Differences found between the metrics and golden metric.                                                       
INFO     ###########################################################                                                    
INFO      Validation Result:                                                                                            
INFO       - golden_metrics_compare_result : Invalid                                                                    
INFO     ###########################################################
```

Use customized configuration

```bash
> uv run -m src.validator --config=<file_path> --cloud-provider=non-gfn --wrapper-type=function --workload-type=helm ef5356e3-afe4-47c3-9ad9-7b4402f84456
```

### Run the validator through Docker

Prerequisite

```bash

docker login <registry>
export VALIDATOR_IMAGE="<registry>/byoo_metrics_validator:0.0.4"

# Set the Grafana Cloud credentials before running the validator
export GRAFANA_CLOUD_PROMETHEUS_URL="xxx"
export GRAFANA_CLOUD_PROMETHEUS_USERNAME="xxx"
export GRAFANA_CLOUD_PROMETHEUS_PASSWORD="xxx"
```

Validate the metrics

```
docker run \
  -t \
  -e GRAFANA_CLOUD_PROMETHEUS_URL="$GRAFANA_CLOUD_PROMETHEUS_URL" \
  -e GRAFANA_CLOUD_PROMETHEUS_USERNAME="$GRAFANA_CLOUD_PROMETHEUS_USERNAME" \
  -e GRAFANA_CLOUD_PROMETHEUS_PASSWORD="$GRAFANA_CLOUD_PROMETHEUS_PASSWORD" \
  "$VALIDATOR_IMAGE" \
  --cloud-provider=non-gfn \
  --wrapper-type=function \
  --workload-type=helm \
  ef5356e3-afe4-47c3-9ad9-7b4402f84456
```

Use the config file locally

```
docker run \
  -t \
  --mount type=bind,src="<file_path>",dst=/app/validator-config.yaml,readonly \
  -e GRAFANA_CLOUD_PROMETHEUS_URL="$GRAFANA_CLOUD_PROMETHEUS_URL" \
  -e GRAFANA_CLOUD_PROMETHEUS_USERNAME="$GRAFANA_CLOUD_PROMETHEUS_USERNAME" \
  -e GRAFANA_CLOUD_PROMETHEUS_PASSWORD="$GRAFANA_CLOUD_PROMETHEUS_PASSWORD" \
  "$VALIDATOR_IMAGE" \
  --cloud-provider=non-gfn \
  --wrapper-type=function \
  --workload-type=helm \
  ef5356e3-afe4-47c3-9ad9-7b4402f84456
```

Currently, only two OS/Arch combinations (LINUX/ARM64 and LINUX/AMD64) of images are provided. If you're using a different environment, you can also build the image on your own:

```bash
> docker build . -t "$VALIDATOR_IMAGE"
```
