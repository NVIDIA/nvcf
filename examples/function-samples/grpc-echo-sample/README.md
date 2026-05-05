# Echo Sample
This sample is a simple echo function served within a Triton Inference Server instance.

## Build the sample container
```bash
docker buildx build --platform linux/amd64,linux/arm64 -t grpc-echo-sample .
```

Push the image to an OCI registry your self-hosted NVCF cluster can access and register pull credentials with `nvcf-cli registry add`. See [examples/README.md](../../README.md#publishing-container-images) for the full flow.

## Run sample client application
Included with this sample is a client application that uses the function via an HTTP call. Run it using:
```bash
bash run_client_app.sh
```
A `gradio` base user interface will then be available at `localhost:7860`