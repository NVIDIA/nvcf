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

Resolve the cluster gateway and generate an invocation API key via `nvcf-cli`:

```bash
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
export NVCF_API_KEY=$(nvcf-cli api-key generate --description "task-byoo-sample" --json | jq -r .apiKey)
export ORG_ID=<your-org-id>
```

Submit the task through the NVCT API. NVCF populates the `OTEL_EXPORTER_OTLP_*` environment variables for deployments that register a BYOO telemetry endpoint.

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/v2/orgs/${ORG_ID}/nvct/tasks" \
  --header "Host: api.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${NVCF_API_KEY}" \
  --header "Content-Type: application/json" \
  --data '{
    "name": "task-byoo-sample",
    "containerImage": "<your-registry>/<namespace>/task-byoo-sample:<tag>",
    "gpuSpecification": {
      "gpu": "T10",
      "instanceType": "g6.full",
      "backend": "GFN"
    },
    "maxRuntimeDuration": "PT1H",
    "maxQueuedDuration": "PT2H",
    "terminationGracePeriodDuration": "PT15M",
    "resultHandlingStrategy": "NONE"
  }'
```

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

```
OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT
OTEL_EXPORTER_OTLP_<SIGNAL>_PROTOCOL
```

`<SIGNAL>` is one of `TRACES`, `LOGS`, or `METRICS`.
