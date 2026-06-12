# Examples

Sample containers, configurations, and guides for self-hosted NVIDIA Cloud Functions (NVCF). All samples target the self-hosted control plane and are driven via `nvcf-cli`.

Local development tooling for self-hosted NVCF lives in [`tools/ncp-local-cluster/`](../tools/ncp-local-cluster/), not under `examples/`. Run `make build-and-deploy-cluster` there to bring up the canonical k3d topology.

## Function Samples

Functions are long-running services that respond to HTTP or gRPC invocations.

| Sample | Description |
|--------|-------------|
| [FastAPI Echo](function-samples/fastapi-echo-sample/) | Minimal FastAPI function that echoes back the request payload. |
| [FastAPI Streaming](function-samples/fastapi-streaming-sample/) | FastAPI function demonstrating server-sent event (SSE) streaming responses. |
| [FastAPI Multi-Endpoint](function-samples/fastapi-multi-endpoint-sample/) | FastAPI function exposing multiple endpoints with query parameter support. |
| [gRPC Echo](function-samples/grpc-echo-sample/) | Echo function served via Triton Inference Server with an included Gradio client. |
| [Secrets](function-samples/secrets-sample/) | Shows how to read and use NVCF-managed secrets inside a container. |
| [vLLM OTLP Exporter](function-samples/vllm-otlp-exporter-sample/) | vLLM inference with OpenTelemetry (OTLP) metric exporting for BYO Observability. |
| [Inference Helm Chart](function-samples/helmchart-samples/inference-test-sample/) | Helm chart that deploys the FastAPI Echo sample on a Kubernetes cluster. |
| [Multi-Node Helm Function](function-samples/helmchart-samples/multi-node-helm-function-test/) | Multi-node Helm chart for running NCCL and GPU bandwidth tests via NVCF. |
| [Load Tester Supreme](function-samples/load-tester-supreme/) | HTTP and gRPC echo servers designed for load and throughput testing. |

## Task Samples

Tasks are one-shot workloads that run to completion and surface progress and results through the NVCT API.

| Sample | Description |
|--------|-------------|
| [Task Simple](task-samples/task-simple-sample/) | Minimal task that writes progress updates until completion. |
| [Task BYOO](task-samples/task-byoo-sample/) | Task instrumented with OpenTelemetry for BYO Observability. |
| [Task Helm Chart](task-samples/task-helmchart-sample/) | Helm chart that deploys the simple task container as a Kubernetes Job. |
| [Task Helm Chart BYOO](task-samples/task-helmchart-byoo-sample/) | Helm chart that deploys the BYOO task container as a Kubernetes Job. |
| [Multi-Node Helm Task](task-samples/multi-node-helm-task-test/) | Multi-node NCCL and GPU bandwidth test packaged as a Helm task. |

## Load Tests

k6 load testing scripts for NVCF function and NVCT task endpoints are in the [load-tests/](load-tests/) directory.

## Building for Multiple Compute Architectures

If both `amd64` and `arm64` support is required, Docker can build multi-platform images:

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t my_image_name .
```

See the Docker [multi-platform build documentation](https://docs.docker.com/build/building/multi-platform/#cross-compilation) for more details.

## Publishing container images

Push each built container to an OCI registry that your self-hosted NVCF cluster can access (Harbor, ECR, GCR, `nvcr.io`, a private Docker registry, etc.), then register pull credentials with `nvcf-cli`:

```bash
docker tag my_image <your-registry>/<namespace>/my_image:<tag>
docker push <your-registry>/<namespace>/my_image:<tag>

nvcf-cli registry add \
  --hostname <your-registry> \
  --username <user> \
  --password <pass> \
  --artifact-type CONTAINER
```

Helm charts follow the same pattern with `--artifact-type HELM`. See each sample's README for the full `nvcf-cli function create` / `function deploy create` flow.
