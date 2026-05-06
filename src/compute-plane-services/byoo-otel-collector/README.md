[Key Features](#key-features) | [Quick Start](#quick-start) | [Development](#development) | [Documentation](#documentation) | [Requirements](#requirements)

# BYO Observability OpenTelemetry Collector

A containerized Go application that provides a complete observability solution by orchestrating three functional components: it generates OpenTelemetry Collector configurations, extracts and manages secrets from ESS (Encrypted Secret Store), and runs a custom-built OpenTelemetry Collector binary.

The BYOO collector container handles receiving OTLP telemetry (logs, metrics, traces) from applications, collecting platform metrics, processing and exporting telemetry to various backends, with support for both Kubernetes and VM deployments.

## Container Architecture

### byoo-otel-collector Image

The `byoo-otel-collector` image is deployed as a single container image that contains:
- **byoo-otel-collector binary** - The main orchestrator that:
  - Generates OpenTelemetry Collector configuration YAML using the nvcf-otelconfig library ([./internal/otelconfig](./internal/otelconfig))
  - Extracts and parses secrets from ESS (Encrypted Secret Store) into individual files ([./internal/secrets](./internal/secrets))
  - Manages the lifecycle of the OpenTelemetry Collector process
- **otel-collector-contrib binary** - Custom-built OpenTelemetry Collector with healthcheck v2 extension support from upstream [OpenTelemetry Collector Contrib](https://github.com/open-telemetry/opentelemetry-collector-contrib), executed and managed by the byoo-otel-collector binary

Supported Deployment Types:
- **Kubernetes Deployments** → Container and Helm chart workloads
- **VM Deployments** → Container and Helm chart workloads
- **Multiple Backends** → Grafana Cloud, Datadog, Azure Monitor, Splunk, Kratos, and more

Exposed Ports:
- 18888: `/metrics` endpoint for the otel-collector-contrib metrics
- 14357: OTLP gRPC receiver
- 14358: OTLP HTTP receiver
- 13133: `/health?verbose` endpoint to get detailed health status of collector (healthcheck v2 extension)
- 19090: `/metrics` endpoint for the byoo-otel-collector metrics

### nvcf-otel-collector Image

The `nvcf-otel-collector` image contains **only** the custom `otelcol` binary without the BYOO functionalities. This is used as a sidecar container in NVCA pods to collect and forward Kubernetes events for observability.

Exposed Ports:
- 13133: Health check endpoint
- 8888: Metrics endpoint

## Key Features

### 📊 Telemetry Processing

The configuration produced by otelconfig-generator guarantees that only `otlp` telemetry and selected platform metrics are received, processed and exported by the collector using the generated configuration.

### 🔐 Secrets Management

Secrets-extractor handles ESS (Encrypted Secret Store) secrets, flattening them into individual files for easy consumption by the OpenTelemetry Collector.

ESS Secret File Pattern: `<provider>-<endpoint_name>-<credential_type>`

Examples:
- GRAFANA-Grafana_prd-username
- GRAFANA-Grafana_prd-password
- THANOS-kratos-cds-client_cert
- THANOS-kratos-cds-client_key
- SPLUNK-splunk-prd-token
- DATADOG-aws-us-east-key

See [examples](./examples/secrets) for more details.

### 🏷️ Attribute Enrichment

All traces, logs, and metrics have OpenTelemetry attributes added to their metadata. See the [complete attributes list](generator/doc/README.md#opentelemetry-attributes) for detailed information.

Platform Metrics Attributes:

- cadvisor: container, cpu, device, image, job[1], service[2], interface, pod
- kube state metrics:
  - container: container[3], job[1], service[2], pod, reason
  - helm: condition, configmap, container[3], created_by_kind, created_by_name, deployment, host_network, image, job [1], phase, pod, qos_class, reason, replicaset, resource, secret, service, statefulset, status and unit
- DCGM: container, DCGM_FI_DRIVER_VERSION, device, job[1], service[2], modelName, pci_bus_id and pod
- nvcf worker: error_code

Attribute Notes:
- [1] `job` attribute is available in Grafana Cloud
- [2] `service` is used in Datadog instead of attribute `job`
- [3] `container` is not present in Azure Monitor
- [4] `service.name` is used in Azure Monitor instead of attribute `job`

### Configuration and documentation generator (Python)

The `generator/` directory contains a Python script that runs at build or development time (via `make update-config-template` and `make update-examples`). It reads [source-config.yaml](generator/source-config.yaml) (metrics, attributes, backends) and produces: (1) the metrics and attributes documentation in [generator/doc/README.md](generator/doc/README.md), and (2) the Jinja2 config templates in `internal/otelconfig/templates/` that are embedded into the Go binary and used at runtime to render OpenTelemetry Collector configuration. Do not edit the generated files by hand; re-run the generator after changing `source-config.yaml` or the source templates in `internal/otelconfig/source_templates/`.

### ✅ Validation & Testing

Comprehensive validation tools ensure generated configurations are valid and functional.

**Validation Features:**
- YAML syntax validation
- OpenTelemetry Collector binary validation
- End-to-end testing with real collector instances
- Example configuration generation and validation

Use `make validate-otelconfig` to validate generated configurations against the OpenTelemetry Collector binary.

## Quick Start

### Assumptions

- Secrets/tokens path: `/etc/byoo-otel-collector/secrets/<secret_name>`
- Rendered otel-collector config path: `/etc/byoo-otel-collector/config.yaml`

### Build and Run

```bash
# Build the byoo-otel-collector binary
go build -o bin/byoo-otel-collector ./cmd/byoo-otel-collector

# Build Docker image
docker build --build-arg OTEL_BUILDER_VERSION=v0.147.0 \
  -f ./Dockerfile -t byoo-otel-collector:latest .

# Run the collector
./bin/byoo-otel-collector \
  --byoo-accounts-secrets=/var/secrets/accounts-secrets.json \
  --byoo-secrets-folder=/etc/byoo-otel-collector/secrets/ \
  --otel-config-path=/etc/byoo-otel-collector/config.yaml \
  --telemetries=<base64_encoded_telemetries>
```

See [example](./examples/pod) for Kubernetes deployment examples.

## Development

```bash
# Run all tests
go test ./...

# Run linting
make lint

# Regenerate configuration templates
make update-config-template

# Regenerate examples
make update-examples

# Validate generated configurations
make validate-otelconfig
```

### Custom Otel Collector Binary

Otel Collector core is built from source to enable healthcheck v2 extension support.

- See `otel-collector-build.yaml` for complete component dependencies and build settings.
- Official component registry: https://opentelemetry.io/ecosystem/registry/?language=all&component=all&s=resource

#### Build Steps

```bash
# Install otel collector builder
go install go.opentelemetry.io/collector/cmd/builder@v0.147.0

# Build collector
builder --config=./otel-collector-build.yaml
```

The output binary will be generated under the `./output` folder.

### Docker Images

#### BYOO Otel Collector Container

The BYOO otel collector container can be built directly without a GitLab access token.

```bash
docker build --build-arg OTEL_BUILDER_VERSION=v0.147.0 \
  -t YOUR_REGISTRY/byoo-otel-collector:latest .
```

#### NVCF Otel Collector Container

The `nvcf-otel-collector` image contains only the custom `otelcol` binary without the BYOO functionalities.

```bash
docker build -f Dockerfile.nvcf-otel-collector -t YOUR_REGISTRY/nvcf-otel-collector:latest .
```

### Pre-commit Hooks

This repository uses [pre-commit](https://pre-commit.com) to automatically regenerate examples, config templates, and validate configurations when files under `internal/otelconfig/` or `generator/` directories are modified.

#### Setup

```bash
# Install pre-commit
pip install pre-commit>=4.2.0
pre-commit --version  # Should show pre-commit 4.2.0

# Enable hooks
pre-commit install --hook-type pre-push
```

This creates symlinks `.git/hooks/pre-commit` and `.git/hooks/pre-push` that invoke hooks listed in `.pre-commit-config.yaml` on each commit/push.

#### After Modifying Templates

```bash
# Regenerate configuration templates
make update-config-template

# Regenerate examples
make update-examples
```

## Metrics

See the [complete metrics list](generator/doc/README.md) for detailed information.

Platform Metric Sources:
- cadvisor: Container resource usage metrics
- Kube state metrics: Kubernetes resource state metrics ([complete list](https://github.com/kubernetes/kube-state-metrics/tree/main/docs/metrics))
- GPU/DCGM: GPU telemetry from NVIDIA Data Center GPU Manager ([DCGM exporter](https://docs.nvidia.com/datacenter/dcgm/latest/gpu-telemetry/dcgm-exporter.html))
- NVCF worker: Worker service metrics
- OpenTelemetry Collector: Collector self-monitoring metrics

### NVCF Worker Metrics

- Always available:

  - nvcf_worker_service_request_total
  - nvcf_worker_service_response_total
  - nvcf_worker_service_worker_thread_count_total
  - nvcf_worker_service_worker_thread_busy_seconds_total
  - nvca_instance_type_allocatable
  - nvca_instance_type_capacity

- Only streaming functions:

  - nvcf_worker_service_stream_streaming_app_ready
  - nvcf_worker_service_stream_session_duration_seconds_bucket
  - nvcf_worker_service_stream_session_duration_seconds_count
  - nvcf_worker_service_stream_session_duration_seconds_sum

- Only inference requests:

  - nvcf_worker_service_inference_request_time_seconds_total
  - nvcf_worker_service_inference_uploads_total
  - nvcf_worker_service_inference_bytes_total
  - nvcf_worker_service_inference_failure_total

The metrics `nvcf_worker_service_bytes_total` and `nvcf_worker_service_inference_bytes_total` are only available if bytes are being transmitted as part of function or task. `nvcf_worker_service_inference_failure_total` is only available if inference request failed.

## Documentation

- **[AGENTS.md](AGENTS.md)** - Comprehensive development guide including architecture, testing, code style, and commit conventions
- **[CONTRIBUTING.md](CONTRIBUTING.md)** - Contribution guidelines and development workflow
- **[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)** - Code of conduct for contributors
- **[SECURITY.md](SECURITY.md)** - Security policy and vulnerability reporting
- **[docs/Deployment.md](docs/Deployment.md)** - Deployment guide and version policy
- **[generator/doc/README.md](generator/doc/README.md)** - Detailed metrics and attributes documentation
- **[validator/README.md](validator/README.md)** - End-to-end validation tools documentation

## Requirements

- Go 1.23+ (toolchain: 1.23.4)
- Python 3.x with uv (for template generator)
- OpenTelemetry Collector (for validation)
- Docker (for building container images)