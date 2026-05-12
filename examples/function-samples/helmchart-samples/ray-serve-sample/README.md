# Ray Serve Sample

This sample demonstrates how to run a [Ray Serve](https://docs.ray.io/en/latest/serve/index.html) application as an NVCF Helm function. It deploys a single Ray head pod that starts Ray Serve and exposes an inference endpoint that NVCF routes to.

Tony Tzeng (NVCF product, NVIDIA) confirmed in the open-source launch thread: "In theory you should be able to run Ray Serve under KubeRay as an NVCF Function." This sample validates that path using a self-contained Helm chart, with no KubeRay operator dependency required.

## What this sample shows

- Ray Serve running inside a single Kubernetes pod (Ray head node)
- An `entrypoint` Service on port 8000 that NVCF uses for invocation routing
- GPU resource requests wired through `nvidia.com/gpu` extended resources
- A ConfigMap-mounted Python app so the serve logic is easy to swap out without rebuilding an image
- Health and readiness probes on `/health` that NVCF's WorkerService gRPC checks before delivering requests

## Prerequisites

- A Kubernetes cluster with `nvidia.com/gpu` extended resources (real or fake via [fake-gpu-operator](https://github.com/run-ai/fake-gpu-operator))
- `helm` >= 3.12
- For NVCF deployment: a self-managed NVCF control plane (see [self-hosted-local-development](../../../self-hosted-local-development/))

## Deploying locally (plain Kubernetes)

For CPU-only testing, disable GPU requests:

```bash
helm install ray-serve-sample ./ray-serve \
  --set gpu.count=0 \
  --set image.tag=2.40.0-py310
```

Verify the pod is running and Ray Serve is ready:

```bash
kubectl get pods -l app.kubernetes.io/name=ray-serve-sample
kubectl logs -l app.kubernetes.io/name=ray-serve-sample --follow
```

Test the inference endpoint directly:

```bash
kubectl port-forward svc/entrypoint 8000:8000 &
curl -s -X POST http://localhost:8000/infer \
  -H 'Content-Type: application/json' \
  -d '{"prompt": "Hello, Ray Serve on NVCF"}'
```

## Deploying on self-managed NVCF

Package and push the chart to an OCI registry your cluster can reach:

```bash
helm package ray-serve
helm push ray-serve-0.1.tgz oci://<your-registry>/<namespace>
```

Register registry credentials with `nvcf-cli`:

```bash
nvcf-cli registry add \
  --hostname <your-registry> \
  --username <user> \
  --password <pass> \
  --artifact-type HELM \
  --artifact-type CONTAINER
```

Create and deploy the function:

```bash
nvcf-cli function create \
  --name ray-serve-sample \
  --helm-chart <your-registry>/<namespace>/ray-serve:0.1 \
  --helm-chart-service entrypoint \
  --inference-url /infer \
  --inference-port 8000

nvcf-cli function deploy create \
  --function-id <function-id> \
  --version-id <version-id> \
  --gpu NVIDIA-H100-80GB-HBM3 \
  --instance-type gpu.h100.80gb \
  --min-instances 0 \
  --max-instances 1
```

Setting `--min-instances 0` enables scale-to-zero: NVCF buffers the first request in NATS JetStream while the Ray pod cold-starts, then delivers it once Ray Serve is ready.

## Extending this sample for real models

Replace the `InferenceDeployment` body in the ConfigMap (`templates/configmap.yaml`) with your model loading and inference logic. For example, to serve a Hugging Face model:

```python
def __init__(self):
    from transformers import pipeline
    self.model = pipeline("text-generation", model="meta-llama/Llama-3.2-1B", device=0)

@app.post("/infer")
async def infer(self, request: Request) -> JSONResponse:
    body = await request.json()
    result = self.model(body.get("prompt", ""), max_new_tokens=256)
    return JSONResponse({"generated_text": result[0]["generated_text"]})
```

For multi-GPU or multi-node Ray clusters, see the [KubeRay documentation](https://docs.ray.io/en/latest/cluster/kubernetes/index.html) and the [multi-node-helm-function-test](../multi-node-helm-function-test/) sample.

## Files

| File | Purpose |
|------|---------|
| `ray-serve/Chart.yaml` | Helm chart metadata |
| `ray-serve/values.yaml` | Configurable defaults (image, GPU count, resources) |
| `ray-serve/templates/configmap.yaml` | Ray Serve application code (swap this for your model) |
| `ray-serve/templates/deployment.yaml` | Ray head pod with serve startup sequence |
| `ray-serve/templates/service.yaml` | `entrypoint` Service that NVCF routes invocations to |
