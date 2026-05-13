# Ray Serve Sample

This sample demonstrates how to run a [Ray Serve](https://docs.ray.io/en/latest/serve/index.html) application as an NVCF Helm function. It deploys a single Ray head pod that starts Ray Serve and exposes an inference endpoint that NVCF routes to. No KubeRay operator is required.

## What this sample shows

- Ray Serve running inside a single Kubernetes pod (Ray head node)
- An `entrypoint` Service on port 8000 that NVCF uses for invocation routing
- GPU resource requests wired through `nvidia.com/gpu` extended resources
- A ConfigMap-mounted Python app so the serve logic is easy to swap out without rebuilding an image
- Kubernetes liveness and readiness probes on `/health` so NVCF knows when the pod is ready to accept requests

## Prerequisites

- A Kubernetes cluster with `nvidia.com/gpu` extended resources (real or fake via [fake-gpu-operator](https://github.com/run-ai/fake-gpu-operator)) when `gpu.count > 0`
- `helm` >= 3.12
- For NVCF deployment: a self-managed NVCF control plane (see [self-hosted-local-development](../../../self-hosted-local-development/))

> **Apple Silicon note:** The default image tag (`2.40.0-py310-gpu`) is AMD64-only. For local testing on macOS ARM64 (k3d, kind), use the `-aarch64` variant: `--set image.tag=2.40.0-py310-aarch64`.

## Deploying locally (plain Kubernetes)

For CPU-only testing on AMD64, disable GPU requests:

```bash
helm install ray-serve-sample ./ray-serve \
  --set gpu.count=0 \
  --set image.tag=2.40.0-py310
```

On Apple Silicon (ARM64), use the aarch64 tag:

```bash
helm install ray-serve-sample ./ray-serve \
  --set gpu.count=0 \
  --set image.tag=2.40.0-py310-aarch64 \
  --set resources.requests.memory=2Gi \
  --set resources.limits.memory=4Gi
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
# {"echo": {"prompt": "Hello, Ray Serve on NVCF"}}

curl -s http://localhost:8000/health
# {"status": "ok"}

kill %1
```

## Deploying on self-managed NVCF

Package and push the chart to an OCI registry your cluster can reach:

```bash
helm package ray-serve
helm push ray-serve-0.1.0.tgz oci://<your-registry>/<namespace>
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
  --helm-chart <your-registry>/<namespace>/ray-serve:0.1.0 \
  --helm-chart-service entrypoint \
  --inference-url /infer \
  --inference-port 8000 \
  --health-uri /health \
  --health-port 8000

nvcf-cli function deploy create \
  --function-id <function-id> \
  --version-id <version-id> \
  --gpu H100 \
  --instance-type NCP.GPU.H100_1x \
  --min-instances 1 \
  --max-instances 1
```

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
