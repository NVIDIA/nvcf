[Key Features](#key-features) | [Quick Start](#quick-start) | [Development](#development) | [Documentation](#documentation) | [Requirements](#requirements)

# BYO Observability OpenTelemetry Collector

## Build with Bazel

100% Bazel build and image publish. The legacy `docker-build-push`
jobs from the `cds-components/docker-build-push` template are
disabled in `.gitlab-ci.yml`; the Bazel lanes below are the single
source of truth for upstream CI image build and publish.

```shell
# Build the wrapper Go binary.
bazel build //cmd/byoo-otel-collector:byoo-otel-collector

# Build the collector binary (genrule-wrapped `go build` against the
# checked-in otelcol/ module; see Why below).
bazel build //otelcol:otelcol-contrib-bin

# Build the two multi-arch OCI images.
bazel build //:byoo-otel-collector-image_index
bazel build //:nvcf-otel-collector-image

# Run unit tests with auto-retry on timing-sensitive failures.
bazel test //cmd/byoo-otel-collector/... //internal/... --flaky_test_attempts=3

# Release publishing is split: source CI stages these image indexes and
# emits release-manifest.json; nvcf-internal owns NVCR promotion.
```

### Regenerating the collector source tree

`otelcol/` is the output of the OpenTelemetry Collector Builder
(`ocb`) applied to `otel-collector-build.yaml`. It is checked in to
give reviewers a real diff on OpenTelemetry version bumps -- the
same generated-and-committed pattern the upstream OpenTelemetry
ecosystem uses. The `dist.module` field in `otel-collector-build.yaml`
pins the generated module's identity so the diff stays clean across
regenerations.

```shell
# For a coordinated OpenTelemetry Collector version bump across all
# `gomod:` entries, use the dedicated upgrade helper which edits the
# YAML and then regenerates otelcol/ in one pass.
./scripts/update-collector-version.sh

# For ad-hoc edits (adding or removing components without changing
# the OpenTelemetry release line), edit otel-collector-build.yaml
# directly and then re-run the regenerator.
./scripts/regenerate-otelcol.sh

# Drift detector. CI runs this on every MR to fail when
# otel-collector-build.yaml has been edited without regenerating
# otelcol/.
./tools/ci/check-otelcol-generated
```

### Why the collector binary uses a genrule rather than go_binary

`//otelcol:otelcol-contrib-bin` is a `genrule` that shells out to
`go build` against the checked-in `otelcol/` module (see
`otelcol/BUILD.bazel` for the full rationale). The collector's
transitive dep graph (>250 Go modules) includes packages
(`github.com/DataDog/datadog-agent/pkg/opentelemetry-mapping-go/otlp/attributes`
and several internal contrib helpers) whose Gazelle-generated
`BUILD.bazel` files reference `@rules_go` from a resolution context
where it is not visible. The fix -- per-module
`go_deps.gazelle_override` declarations in `MODULE.bazel` -- is
undefined-timeline work without a clean upstream precedent in the
OpenTelemetry Bazel community. The genrule trades Bazel's per-package
dep tracking for forward progress: the binary lives inside Bazel's
output graph and flows through to `oci_image` + `oci_push` cleanly.

Cache contract: Bazel rebuilds the genrule when any input
(`otelcol/**/*.go`, `go.mod`, `go.sum`) changes. The genrule uses
`local = True` + `tags = ["no-sandbox"]` so it can resolve `go` from
`$PATH` and write to the standard Go module cache. The wrapper
binary, in contrast, is a regular `go_binary` and benefits from full
Bazel hermeticity + nvcfbarn remote-cache reuse.

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
- 19091: `/metrics` endpoint for optional metric subset user metrics

### nvcf-otel-collector Image

The `nvcf-otel-collector` image contains **only** the custom `otelcol` binary without the BYOO functionalities. This is used as a sidecar container in NVCA pods to collect and forward Kubernetes events for observability.

Exposed Ports:
- 13133: Health check endpoint
- 8888: Metrics endpoint

## Key Features

### 📊 Telemetry Processing

The configuration produced by otelconfig-generator guarantees that only `otlp` telemetry and selected platform metrics are received, processed and exported by the collector using the generated configuration.

### 🧩 Oversized Log Chunking

Some telemetry backends reject a single log entry when its body and attributes are larger than the backend's per-entry size limit. The BYOO collector can insert a custom `logchunk/byoo` processor into the logs pipeline to split oversized UTF-8 string log bodies plus string/bytes attributes into correlated chunks before export. Non-string attribute values are counted toward the payload limit and emitted once without changing their type.

Chunking is disabled by default. Configure it with:

- `BYOO_LOG_CHUNKING_ENABLED`: enables the `logchunk/byoo` processor. When enabled without overrides, the collector uses `262144` bytes for `max_payload_bytes` and `false` for dry-run.
- `BYOO_LOG_CHUNK_MAX_PAYLOAD_BYTES`: maximum combined log body and attribute payload size in bytes before chunking. `0` uses the enabled-mode default when `BYOO_LOG_CHUNKING_ENABLED=true`. Explicit enabled values must be at least `4` bytes so chunks can preserve UTF-8 rune boundaries.
- `BYOO_LOG_CHUNK_MAX_BODY_BYTES`: deprecated alias for `BYOO_LOG_CHUNK_MAX_PAYLOAD_BYTES`. When both are set, `BYOO_LOG_CHUNK_MAX_PAYLOAD_BYTES` wins.
- `BYOO_LOG_CHUNK_DRY_RUN`: records oversized-log metrics and warnings without mutating log payloads. Dry-run metric datapoints use `mode=dry_run`.
- `BYOO_DEBUG_MODE`: enables collector debug logging and adds the `debug` exporter to every generated pipeline.
- `BYOO_OTEL_COLLECTOR_CONFIG_B64`: optional base64-encoded JSON for advanced collector rendering overrides, such as exporterhelper timeout, retry, sending queue, sending queue batch, memory limiter, batch, and log batch settings.
- `BYOO_METRIC_SUBSET_ENABLED`: enables an additional OTLP-only metrics pipeline that exposes filtered user metrics through a Prometheus exporter on port `19091`. Disabled by default.
- `BYOO_METRIC_SUBSET_FILTER_CONFIG`: optional YAML filter processor config for the metric subset pipeline. If unset, the default drops every metric except `BpsInstrument`, `FpsInstrument`, `RtdInstrument`, and `StageOpenDuration`, and drops datapoints/resources explicitly labeled `metric_subset_enabled=false`.
- `BYOO_WORKLOAD_METRICS_DROP_LABELS`: comma-separated resource attribute names removed from the generated workload `metrics` pipeline. If unset, defaults to `metric_subset_enabled` only when the metric subset pipeline is enabled.

When chunking is enabled, each emitted chunk preserves the original log metadata and adds these attributes so chunks can be grouped in the backend:

- `log.chunk.id`
- `log.chunk.index`
- `log.chunk.count`
- `log.chunk.offset_bytes`
- `log.chunk.original_size_bytes`
- `log.chunk.final`

The processor emits `otelcol_processor_logchunk_*` metrics for oversized records, original bytes, emitted chunks, output bytes, and errors. The metric `mode` attribute distinguishes active chunking (`mode=chunk`) from dry run (`mode=dry_run`).

Advanced collector config can enable exporterhelper byte batching with `sending_queue.batch.sizer=bytes` and configurable `min_size=max_size` where applicable. That exporter-side split limits serialized request size, but it cannot split a single oversized log record; the log chunk processor handles the per-record body limit.

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
docker build --build-arg OTEL_BUILDER_VERSION=v0.157.0 \
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
go install go.opentelemetry.io/collector/cmd/builder@v0.157.0

# Build collector
builder --config=./otel-collector-build.yaml
```

The output binary will be generated under the `./output` folder.

### Docker Images

#### BYOO Otel Collector Container

The BYOO otel collector container can be built directly without a GitLab access token.

```bash
docker build --build-arg OTEL_BUILDER_VERSION=v0.157.0 \
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
