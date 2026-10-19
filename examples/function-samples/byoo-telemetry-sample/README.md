# BYOO Telemetry Sample

CPU container function that emits OpenTelemetry logs, metrics, and traces for every inference request, plus a reference telemetry sink built on the stock OpenTelemetry Collector. Use it to check a Bring Your Own Observability (BYOO) setup end to end on a self-hosted NVCF cluster without a GPU workload or a model server.

The function listens on port 8000:

- `GET /health` returns `{"status":"OK"}`. Health checks do not emit telemetry.
- `POST /echo` takes `{"message":"..."}` and returns the message with the request trace ID. Every call, including rejected ones, exports one span, one log record, and a cumulative snapshot of two metrics: `byoo_sample_requests_total` and `byoo_sample_request_duration_seconds`, labeled by route and status code. A W3C `traceparent` header is honored, and the `NVCF-REQID` header is recorded as `nvcf.request_id`.

The sample sends OTLP/HTTP with JSON payloads and uses only the Go standard library. It reads the per-signal `OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT` and `_PROTOCOL` variables that BYOO injects, and falls back to `OTEL_EXPORTER_OTLP_ENDPOINT` (with `/v1/<signal>` appended) for local runs. A signal with no endpoint, or with protocol `grpc`, is disabled with a warning, so register the telemetry with protocol `HTTP`.

## Layout

- `http-server/`: the function (`main.go`, `otlp.go`) and its tests.
- `Dockerfile`: container build.
- `sink/`: Kustomize manifests for the reference sink. `collector-config.yaml` receives OTLP gRPC on 4317 and OTLP HTTP on 4318, writes a summary per batch with the `debug` exporter, and serves the collector's own Prometheus metrics on 8888.

## Build the sample container

```bash
docker build -t byoo-telemetry-sample .
```

Push the image to an OCI registry your self-hosted NVCF cluster can access and register pull credentials with `nvcf-cli registry add`. See [examples/README.md](../../README.md#publishing-container-images) for the full flow. NVCF checks that the image exists in that registry when you create the function, so an image loaded only into the cluster nodes (for example with `k3d image import`) is rejected with `Artifact '...' presence validation failed`.

## Run the tests

```bash
cd http-server
go test -race ./...
```

Bazel runs the same tests with `bazel test //examples/function-samples/byoo-telemetry-sample/http-server:http-server_test`.

## Run the sample and sink locally

Start the sink and the sample on one Docker network:

```bash
docker network create byoo-local
docker run -d --name byoo-sink --network byoo-local -p 8888:8888 \
  -v "${PWD}/sink/collector-config.yaml:/conf/config.yaml:ro" \
  otel/opentelemetry-collector-contrib:0.129.1 --config=/conf/config.yaml
docker run -d --name byoo-sample --network byoo-local -p 8000:8000 \
  -e OTEL_EXPORTER_OTLP_ENDPOINT=http://byoo-sink:4318 \
  -e OTEL_EXPORTER_OTLP_PROTOCOL=http \
  byoo-telemetry-sample
```

Send a few requests and read the sink counters:

```bash
curl -s localhost:8000/health
for i in 1 2 3 4 5; do
  curl -s -X POST localhost:8000/echo -H 'Content-Type: application/json' -d "{\"message\":\"hello $i\"}"
done
curl -s localhost:8888/metrics | grep -E '^otelcol_receiver_accepted_(log_records|metric_points|spans)_total'
```

After five requests the sink reports five log records, five spans, and ten metric points (one counter point and one histogram point per request):

```text
otelcol_receiver_accepted_log_records_total{...,receiver="otlp",transport="http"} 5
otelcol_receiver_accepted_metric_points_total{...,receiver="otlp",transport="http"} 10
otelcol_receiver_accepted_spans_total{...,receiver="otlp",transport="http"} 5
```

Clean up with `docker rm -f byoo-sample byoo-sink && docker network rm byoo-local`.

## Run on self-hosted NVCF

On a BYOO deployment, NVCF starts a BYOO collector next to the function and points the function's `OTEL_EXPORTER_OTLP_*` variables at it. That collector forwards each signal to the registered telemetry endpoint and adds an `Authorization: Bearer <token>` header built from the telemetry secret. The reference sink accepts any token.

### 1. Deploy the sink

```bash
kubectl apply -k sink/
kubectl -n byoo-sink rollout status deployment/byoo-otel-sink
```

The sink's in-cluster OTLP HTTP endpoint is `http://byoo-otel-sink.byoo-sink.svc.cluster.local:4318`.

NVCA removes NetworkPolicies from workload namespaces on self-managed clusters, so the function can reach an in-cluster sink without an extra NetworkPolicy.

### 2. Get credentials

`nvcf-cli init` mints the admin token that carries the `manage_telemetries` scope used to register telemetry endpoints, and saves it to `~/.nvcf-cli.state`. If you run `nvcf-cli` with `--config <name>.yaml`, read `~/.nvcf-cli.<name>.state` instead. The function API key is used to invoke the function.

```bash
nvcf-cli init
export NVCF_TOKEN=$(jq -r .token ~/.nvcf-cli.state)
export NVCF_API_KEY=$(nvcf-cli api-key generate --for function --description "byoo-telemetry-sample" --json | jq -r '.keys[] | select(.service=="function") | .apiKey')
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
```

### 3. Register the sink

Register one telemetry endpoint for all three signals with provider `OTEL_COLLECTOR` and protocol `HTTP`. The secret value becomes the bearer token the BYOO collector sends to the sink.

```bash
export TELEMETRY_ID=$(curl -s --request POST \
  --url "http://${GATEWAY_ADDR}/v2/nvcf/telemetries" \
  --header "Host: api.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${NVCF_TOKEN}" \
  --header "Content-Type: application/json" \
  --data '{
    "endpoint": "http://byoo-otel-sink.byoo-sink.svc.cluster.local:4318",
    "protocol": "HTTP",
    "provider": "OTEL_COLLECTOR",
    "types": ["LOGS", "METRICS", "TRACES"],
    "secret": {
      "name": "byoo-sink-token",
      "value": "<any-token>"
    }
  }' | jq -r .telemetry.telemetryId)
echo "${TELEMETRY_ID}"
```

### 4. Create and deploy the function

```bash
nvcf-cli function create \
  --name byoo-telemetry-sample \
  --image <your-registry>/<namespace>/byoo-telemetry-sample:<tag> \
  --inference-url /echo \
  --inference-port 8000 \
  --health-uri /health \
  --health-port 8000 \
  --logs-telemetry-id "${TELEMETRY_ID}" \
  --metrics-telemetry-id "${TELEMETRY_ID}" \
  --traces-telemetry-id "${TELEMETRY_ID}"

nvcf-cli function deploy create \
  --function-id <function-id> \
  --version-id <version-id> \
  --gpu <gpu-name> \
  --instance-type <instance-type> \
  --backend <backend>
```

`function create` prints the function ID and version ID. The function itself needs no GPU, but a deployment still names a GPU, instance type, and backend that your cluster advertises; `nvcf-cli cluster list` shows them. `function deploy create` waits up to `--timeout` seconds (default 900) for the deployment to become `ACTIVE`. Check it later with `nvcf-cli function deploy get --function-id <function-id> --version-id <version-id>`.

### 5. Invoke the function

```bash
for i in 1 2 3 4 5; do
  curl -s --request POST \
    --url "http://${GATEWAY_ADDR}/echo" \
    --header "Host: <function-id>.invocation.${GATEWAY_ADDR}" \
    --header "Authorization: Bearer ${NVCF_API_KEY}" \
    --header "Content-Type: application/json" \
    --data "{\"message\":\"hello $i\"}"
done
```

### 6. Verify the sink received all three signals

```bash
kubectl -n byoo-sink port-forward svc/byoo-otel-sink 8888:8888 &
curl -s localhost:8888/metrics | grep -E '^otelcol_receiver_accepted_(log_records|metric_points|spans)_total'
kubectl -n byoo-sink logs deployment/byoo-otel-sink | tail
```

Non-zero `otelcol_receiver_accepted_log_records_total`, `otelcol_receiver_accepted_metric_points_total`, and `otelcol_receiver_accepted_spans_total` show that logs, metrics, and traces reached the sink. The BYOO collector batches before it exports, so allow up to a minute after the requests. The sink log shows one `Logs`, `Metrics`, or `Traces` line per received batch.

To launch a BYOO task against the same sink, reuse `TELEMETRY_ID` in the `telemetries` object described in [task-byoo-sample](../../task-samples/task-byoo-sample/README.md).

### Clean up

```bash
nvcf-cli function deploy remove --function-id <function-id> --version-id <version-id>
kubectl delete -k sink/
```
