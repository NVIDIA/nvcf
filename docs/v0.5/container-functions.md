# Container-Based Function Creation

Container-based functions require building and pushing a Cloud Functions compatible [Docker](https://docker.com) container image to your container registry.

## Resources

- Example containers can be found [in the examples repository](https://github.com/NVIDIA/nv-cloud-function-helpers/tree/main/examples).

- The repository also contains [helper functions](https://github.com/NVIDIA/nv-cloud-function-helpers/blob/main/helper_library/nv_cloud_function_helpers/nvcf_container/helpers.py) that are useful when authoring your container, including:

  - Helpers that parse Cloud Functions-specific parameters on invocation
  - Helpers that can be used to instrument your container with Cloud Functions compatible logs

- It's always a **best practice to emit logs** from your inference container. Cloud Functions supports third-party logging and metrics emission from your container.

<Warning>
Please note that container functions **should not run as root user**, running as root is not formally supported on any Cloud Functions backend.

</Warning>

## Container Endpoints

Any server can be implemented within the container, as long as it implements the following:

- For HTTP-based functions, a health check endpoint that returns a 200 HTTP Status Code on success.
- For gRPC-based functions, a standard gRPC health check. See [these docs for more info](https://github.com/grpc/grpc-proto/blob/master/grpc/health/v1/health.proto) also [gRPC Health Checking](https://grpc.io/docs/guides/health-checking/).
- An inference endpoint (this endpoint will be called during function invocation)

These endpoints are expected to be served on the same port, defined as the `inferencePort`.

<Warning>
Cloud Functions reserves the following ports on your container for internal monitoring and metrics:

- Port `8080`
- Port `8010`

Cloud Functions also expects the following directories in the container to remain read-only for caching purposes:

- `/config/` directory
- Nested directories created inside `/config/`

</Warning>

## Composing a FastAPI Container

It's possible to use any container with Cloud Functions as long as it implements a server with the above endpoints. The below is an example of a FastAPI-based container compatible with Cloud Functions. Clone the [FastAPI echo example](https://github.com/NVIDIA/nv-cloud-function-helpers/tree/main/examples/function_samples/fastapi_echo_sample).

### Create the "requirements.txt" File

```text
fastapi==0.110.0
uvicorn==0.29.0
```

### Implement the Server

```python
import os
import time
import uvicorn
from pydantic import BaseModel
from fastapi import FastAPI, status
from fastapi.responses import StreamingResponse

app = FastAPI()

class HealthCheck(BaseModel):
    status: str = "OK"

# Implement the health check endpoint
@app.get("/health", tags=["healthcheck"], summary="Perform a Health Check", response_description="Return HTTP Status Code 200 (OK)", status_code=status.HTTP_200_OK, response_model=HealthCheck)
def get_health() -> HealthCheck:
    return HealthCheck(status="OK")

class Echo(BaseModel):
    message: str
    delay: float = 0.000001
    repeats: int = 1
    stream: bool = False

# Implement the inference endpoint
@app.post("/echo")
async def echo(echo: Echo):
    if echo.stream:
        def stream_text():
            for _ in range(echo.repeats):
                time.sleep(echo.delay)
                yield f"data: {echo.message}\n\n"
        return StreamingResponse(stream_text(), media_type="text/event-stream")
    else:
        time.sleep(echo.delay)
        return echo.message*echo.repeats

# Serve the endpoints on a port
if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=8000, workers=int(os.getenv('WORKER_COUNT', 500)))
```

Note in the example above, the function's configuration during creation will be:

- Inference Protocol: HTTP
- Inference Endpoint: `/echo`
- Health Endpoint: `/health`
- Inference Port (also used for health check): `8000`

### Create the Dockerfile

```dockerfile
FROM python:3.10.13-bookworm

ENV WORKER_COUNT=10

WORKDIR /app

COPY requirements.txt ./

RUN python -m pip install --no-cache-dir -U pip && \
    python -m pip install --no-cache-dir -r requirements.txt

COPY http_echo_server.py /app/

CMD uvicorn http_echo_server:app --host=0.0.0.0 --workers=$WORKER_COUNT
```

### Build the Container & Create the Function

See the [Create the Function] section below for the remaining steps.

## Composing a PyTriton Container

NVIDIA's [PyTriton](https://triton-inference-server.github.io/pytriton/) is a Python native solution of Triton inference server. A minimum version of 0.3.0 is required.

### Create the "requirements.txt" File

- This file should list the Python dependencies required for your model.
- Add nvidia-pytriton to your `requirements.txt` file.

Here is an example of a `requirements.txt` file:

```text
--extra-index-url https://pypi.ngc.nvidia.com
opencv-python-headless
pycocotools
matplotlib
torch==2.1.0
nvidia-pytriton==0.3.0
numpy
```

### Create the "run.py" File

1. Your `run.py` file (or similar Python file) needs to define a PyTriton model.
2. This involves importing your model dependencies, creating a PyTritonServer class with an `__init__` function, an `_infer_fn` function and a `run` function that serves the inference_function, defining the model name, the inputs and the outputs along with optional configuration.

Here is an example of a `run.py` file:

```python
import numpy as np
from pytriton.model_config import ModelConfig, Tensor
from pytriton.triton import Triton, TritonConfig
import time
....
class PyTritonServer:
    """triton server for timed_sleeper"""

    def __init__(self):
        # basically need to accept image, mask(PIL Images), prompt, negative_prompt(str), seed(int)
        self.model_name = "timed_sleeper"

    def _infer_fn(self, requests):
        responses = []
        for req in requests:
            req_data = req.data
            sleep_duration = numpy_array_to_variable(req_data.get("sleep_duration"))
            # deal with header dict keys being lowerscale
            request_parameters_dict = uppercase_keys(req.parameters)
            time.sleep(sleep_duration)
            responses.append({"sleep_duration": np.array([sleep_duration])})

        return responses

    def run(self):
        """run triton server"""
        with Triton(
            config=TritonConfig(
                http_header_forward_pattern="NVCF-*",  # this is required
                http_port=8000,
                grpc_port=8001,
                metrics_port=8002,
            )
        ) as triton:
            triton.bind(
                model_name="timed_sleeper",
                infer_func=self._infer_fn,
                inputs=[
                    Tensor(name="sleep_duration", dtype=np.uint32, shape=(1,)),
                ],
                outputs=[Tensor(name="sleep_duration", dtype=np.uint32, shape=(1,))],
                config=ModelConfig(batching=False),
            )
            triton.serve()
if __name__ == "__main__":
    server = PyTritonServer()
    server.run()
```

### Create the "Dockerfile"

1. Create a file named `Dockerfile` in your model directory.
2. It's **strongly recommended to use NVIDIA-optimized containers like CUDA, Pytorch or TensorRT as your base container**. They can be downloaded from the [NGC Catalog](https://catalog.ngc.nvidia.com/).
3. Make sure to install your Python requirements in your `Dockerfile`.
4. Copy in your model source code, and model weights.

Here is an example of a `Dockerfile`:

```dockerfile
FROM nvcr.io/nvidia/cuda:12.1.1-devel-ubuntu22.04
RUN apt-get update && apt-get install -y \
    git \
    python3 \
    python3-pip \
    python-is-python3 \
    libsm6 \
    libxext6 \
    libxrender-dev \
    curl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /workspace/

# Install requirements file
COPY requirements.txt requirements.txt
RUN pip install --no-cache-dir --upgrade pip
RUN pip install --no-cache-dir -r requirements.txt
ENV DEBIAN_FRONTEND=noninteractive

# Copy model source code and weights
COPY model_weights /models
COPY model_source .
COPY run.py .

# Set run command to start PyTriton to serve the model
CMD python3 run.py
```

### Build the Docker Image

1. Open a terminal or command prompt.
2. Navigate to the `my_model` directory.
3. Run the following command to build the docker image:

```bash
docker build -t my_model_image .
```

Replace `my_model_image` with the desired name for your docker image.

### Push the Docker Image

Tag and push the docker image to your container registry.

```bash
docker tag my_model_image:latest ${REGISTRY}/${REPOSITORY}/my_model_image:latest
docker push ${REGISTRY}/${REPOSITORY}/my_model_image:latest
```

### Create the Function

Create the function via the NVCF API. In this example, we defined the inference port as `8000` and are using the default inference and health endpoint paths.

```bash
 curl -s -X POST "http://${GATEWAY_ADDR}/v2/nvcf/functions" \
 -H "Host: api.${GATEWAY_ADDR}" \
 -H 'Content-Type: application/json' \
 -H "Authorization: Bearer $NVCF_TOKEN" \
 -d '{
     "name": "my-model-function",
     "inferenceUrl": "/v2/models/my_model_image/infer",
     "inferencePort": 8000,
     "containerImage": "'${REGISTRY}'/'${REPOSITORY}'/my_model_image:latest",
     "health": {
                 "protocol": "HTTP",
                 "uri": "/v2/health/ready",
                 "port": 8000,
                 "timeout": "PT10S",
                 "expectedStatusCode": 200
             }
 }'
```

### Additional Examples

See more examples of containers that are Cloud Functions compatible [in the function samples directory](https://github.com/NVIDIA/nv-cloud-function-helpers/tree/main/examples/function_samples/).

## Creating gRPC-based Functions

Cloud Functions supports function invocation via gRPC. During function creation, specify that the function is a gRPC function by setting the `inferenceUrl` field to `/grpc`.

### Prerequisites

- The function container must implement a gRPC port, endpoint and health check. The health check is expected to be served by the gRPC inference port, there is no need to define a separate health endpoint path.

  - See [gRPC health checking](https://grpc.io/docs/guides/health-checking/).
  - See an [example container](https://github.com/NVIDIA/nv-cloud-function-helpers/blob/main/examples/function_samples/grpc_echo_sample/grpc_echo_server.py) with a gRPC server that is Cloud Functions compatible.

### gRPC Function Creation via API

When creating the gRPC function, set the `inferenceUrl` field to `/grpc`:

```bash
 curl -s -X POST "http://${GATEWAY_ADDR}/v2/nvcf/functions" \
 -H "Host: api.${GATEWAY_ADDR}" \
 -H 'Content-Type: application/json' \
 -H "Authorization: Bearer $NVCF_TOKEN" \
 -d '{
     "name": "my-grpc-function",
     "inferenceUrl": "/grpc",
     "inferencePort": 8001,
     "containerImage": "'${REGISTRY}'/'${REPOSITORY}'/grpc_echo_sample:latest"
 }'
```

### gRPC Function Invocation

gRPC function invocation uses the same `Authorization: Bearer $NVCF_TOKEN` header as HTTP invocation, passed as gRPC metadata. See the [gRPC invocation examples](https://github.com/NVIDIA/nv-cloud-function-helpers/tree/main/examples) for details on how to authenticate and invoke your gRPC function.
