# Task Sample with Bring Your Own Observability (BYOO)

NVCT task that emits OpenTelemetry traces, metrics, and logs through the BYO Observability path. Use it to validate that your `otlp` collector receives task telemetry from a self-hosted NVCF cluster.

## Build the sample container

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t task-byoo-sample .
```

Push the image to an OCI registry your self-hosted NVCF cluster can access and register pull credentials with `nvcf-cli registry add`. See [examples/README.md](../../README.md#publishing-container-images) for the full flow.

## Run the sample locally

```bash
docker run --rm \
  -v ${PWD}:/tmp/output \
  -e NVCT_RESULTS_DIR=/tmp/output \
  -e OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://host.docker.internal:4317 \
  -e OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=grpc \
  task-byoo-sample
```

Without `OTEL_EXPORTER_OTLP_*_ENDPOINT` set, signals are emitted to the console.

## Launch on self-hosted NVCF

The tasks API checks that the task image exists in its registry before it accepts a task, so push the image to a registry as described above. Loading the image only into the cluster nodes (for example with `kind load` or `ctr images import`) is not enough.

Resolve the cluster gateway and generate a task API key via `nvcf-cli`. Launching a task needs a key generated for the task service, which carries the `launch_task` scope; the default function key does not.

```bash
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
export NVCF_NVCT_API_KEY=$(nvcf-cli api-key generate --for task --description "task-byoo-sample" --json | jq -r '.keys[] | select(.service=="task") | .apiKey')
export ORG_ID=<your-org-id>
```

Register a telemetry endpoint for each signal first. [byoo-telemetry-sample](../../function-samples/byoo-telemetry-sample/README.md) includes a reference sink and the `POST /v2/nvcf/telemetries` registration; reuse the returned `telemetryId` values here. On self-managed clusters NVCA removes NetworkPolicies from workload namespaces, so an in-cluster sink needs no extra policy.

Submit the task through the NVCT API. The `telemetries` object selects the registered endpoints, and NVCF populates the `OTEL_EXPORTER_OTLP_*` environment variables for the task container. Set `gpuSpecification` to a GPU, instance type, and backend that your cluster advertises; `nvcf-cli cluster list` shows them.

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/v2/orgs/${ORG_ID}/nvct/tasks" \
  --header "Host: api.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${NVCF_NVCT_API_KEY}" \
  --header "Content-Type: application/json" \
  --data '{
    "name": "task-byoo-sample",
    "containerImage": "<your-registry>/<namespace>/task-byoo-sample:<tag>",
    "gpuSpecification": {
      "gpu": "<gpu-name>",
      "instanceType": "<instance-type>",
      "backend": "<backend>"
    },
    "telemetries": {
      "logsTelemetryId": "<logs-telemetry-id>",
      "metricsTelemetryId": "<metrics-telemetry-id>",
      "tracesTelemetryId": "<traces-telemetry-id>"
    },
    "maxRuntimeDuration": "PT1H",
    "maxQueuedDuration": "PT2H",
    "terminationGracePeriodDuration": "PT15M",
    "resultHandlingStrategy": "NONE"
  }'
```

The same request is available as `nvcf-cli task create` with `--logs-telemetry-id`, `--metrics-telemetry-id`, and `--traces-telemetry-id`.

See [byoo-documentation.md](byoo-documentation.md) for the telemetry registration flow and `otlp` collector requirements.

## OpenTelemetry configuration

The sample supports HTTP and gRPC protocols for `otlp` signals (traces, metrics, logs). Select the protocol for each signal with:

```bash
export OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=grpc
export OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http
export OTEL_EXPORTER_OTLP_METRICS_PROTOCOL=grpc
```

`gRPC` is used by default when the protocol is not set.

When a signal's `OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT` variable is unset, that signal is written to the console. NVCF populates the following variables at runtime when BYOO is configured:

```text
OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT
OTEL_EXPORTER_OTLP_<SIGNAL>_PROTOCOL
```

`<SIGNAL>` is one of `TRACES`, `LOGS`, or `METRICS`.
