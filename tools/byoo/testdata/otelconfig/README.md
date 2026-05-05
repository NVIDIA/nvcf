# Generating Testing Otel Configs

This is a simple golang tool to generate YAML configuration files for the OpenTelemetry Collector given a JSON input file, backend, workload_type and compute_type.

## Usage
* The main tool is located at `testdata/create/main.go` and can be run with the following arguments:
    ```bash
    go run ./testdata/otelconfig/create/main.go <input_file> <output_dir> <backend> <workload_type> <compute_type>
    ```
* `<input_file>`: Path to the JSON input file containing telemetry specifications
* `<output_dir>`: Directory where the generated configuration will be saved
* `<backend>`: Backend type, either "vm" (for GFN) or "k8s" (for non-GFN)
* `<workload_type>`: Workload type, either "container" or "helm"
* `<compute_type>`: Compute type, either "function" or "task"
* The tool will generate a configuration file at: `<output_dir>/byoo-otel-collector/config.<compute_type>_<backend>_<workload_type>.yaml`

## Examples
* To generate a configuration for a function running in a VM with container workload:
    ```bash
    go run ./testdata/otelconfig/create/main.go testdata/otelconfig/input1.json ./output vm container function
    ```
    * This creates `./output/byoo-otel-collector/config.function_vm_container.yaml`.
* To generate a configuration for a task running in Kubernetes with helm workload:
    ```bash
    go run ./testdata/otelconfig/create/main.go testdata/otelconfig/input2.json ./output k8s helm task
    ```
    * This creates `./output/byoo-otel-collector/config.task_k8s_helm.yaml`.

## Input JSON
* The input JSON files contain the telemetry specifications.
* `input1.json` is an example with logs set to splunk, metrics set to grafana cloud and traces set to lightstep.
    ```json
    {
        "telemetries": {
            "logsTelemetry": {
                "telemetryId": "fd28fc13-b44c-4e21-9132-1e1883f9dec5",
                "protocol": "HTTP",
                "provider": "SPLUNK",
                "endpoint": "endpoint",
                "name": "splunk-prd",
                "types": ["LOGS"],
                "createdAt": "2025-02-04T17:41:34.013Z"
            },
            "metricsTelemetry": {
                "telemetryId": "fd28fc13-b44c-4e21-9132-1e1883f9dec5",
                "protocol": "HTTP",
                "provider": "GRAFANA_CLOUD",
                "endpoint": "endpoint",
                "name": "Grafana_prd",
                "types": ["METRICS"],
                "createdAt": "2025-02-04T17:41:34.013Z"
            },
            "tracesTelemetry": {
                "telemetryId": "fd28fc13-b44c-4e21-9132-1e1883f9dec5",
                "protocol": "HTTP",
                "provider": "SERVICENOW",
                "endpoint": "endpoint:8323",
                "name": "nv-lightstep-stg",
                "types": ["TRACES"],
                "createdAt": "2025-02-04T17:41:34.013Z"
            }
        }
    }
    ```
* `validator.json` is used specifically in the CI pipeline for validating the byoo metrics and labels. It only contains metrics telemetry and is pointed to production Grafana Cloud endpoint.