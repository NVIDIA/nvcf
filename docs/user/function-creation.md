# Function Creation

This page describes how to create functions within Cloud Functions.

Functions can be created in one of two ways:

1. Custom Container
   - Enables any container-based workload as long as the container exposes an inference endpoint and a health check.
   - Option to leverage any server, ex. [PyTriton](https://triton-inference-server.github.io/pytriton/), [FastAPI](https://fastapi.tiangolo.com/), [Triton](https://developer.nvidia.com/triton-inference-server).
   - See [Container-Based Function Creation](./container-functions.md).

2. Helm Chart
   - Enables orchestration across multiple containers. For complex use cases where a single container isn't flexible enough.
   - Requires one "mini-service" container defined as the inference entry point for the function.
   - Does not support partial response reporting, gRPC or HTTP streaming-based invocation.
   - See [Helm-Based Function Creation](./helm-functions.md).

Additionally, Cloud Functions supports [Low Latency Streaming (LLS) functions](./streaming-functions.md) for video, audio, and data streaming via WebRTC.

For LLM functions, see [LLM Gateway](./llm-gateway.md#function-configuration) for OpenAI-compatible model route configuration.

## Invocation Types

- [Generic HTTP function invocation](./generic-http-function-invocation.md):
  Invoke HTTP functions through the standard invocation route.
- [gRPC function invocation](./grpc-function-invocation.md): Invoke gRPC
  functions through the Gateway TCP listener.
- [LLM invocation](./llm-gateway.md#endpoint-behavior): Invoke
  OpenAI-compatible LLM functions through `llm.invocation.<domain>`.
- [LLS/WebRTC client connection](./streaming-functions.md#connecting-to-a-streaming-function-with-a-client):
  Connect browser or proxy clients to streaming functions.

## Best Practices

### Container Versioning

- Ensure that any resources that you tag for deployment into production environments are not simply using "latest" and are following a standard version control convention.
  - During autoscaling, a function scaling any additional instances will pull the same specified container image and version. If version is set to "latest", and the "latest" container image is updated between instance scaling, this can lead to undefined behavior.

- Function versions created are immutable, this means that the container image and version cannot be updated for a function without creating a new version of the function.

### Security

- Do not run containers as root user: Running containers as root is not supported in Cloud Functions. Always specify a non-root user in your Dockerfile using the `USER` instruction.
- Use Kubernetes Secrets: For sensitive information like API keys, credentials, or tokens, use Kubernetes Secrets instead of environment variables. This provides better security and follows Kubernetes best practices for secret management.

#### Available Container Variables

The following is a reference of available variables via the headers of the invocation message (auto-populated by Cloud Functions), accessible within the container.

For examples of how to extract and use some of these variables, see [NVCF Container Helper Functions](https://github.com/NVIDIA/nvcf/tree/main/examples).

| Name                         | Description                                             |
| ---------------------------- | ------------------------------------------------------- |
| NVCF-REQID                   | Request ID for this request.                            |
| NVCF-SUB                     | Message subject.                                        |
| NVCF-NCAID                   | Function's organization's NCA ID.                       |
| NVCF-FUNCTION-NAME           | Function name.                                          |
| NVCF-FUNCTION-ID             | Function ID.                                            |
| NVCF-FUNCTION-VERSION-ID     | Function version ID.                                    |
| NVCF-LARGE-OUTPUT-DIR        | Large output directory path.                            |
| NVCF-MAX-RESPONSE-SIZE-BYTES | Max response size in bytes for the function.            |
| NVCF-NSPECTID                | NVIDIA reserved variable.                               |
| NVCF-BACKEND                 | Backend or "Cluster Group" the function is deployed on. |
| NVCF-INSTANCETYPE            | Instance type the function is deployed on.              |
| NVCF-REGION                  | Region or zone the function is deployed in.             |
| NVCF-ENV                     | Spot environment if deployed on spot instances.         |

#### Environment Variables

The following environment variables are automatically injected into your function containers when they are deployed and can be accessed using standard environment variable access methods in your application code:

| Name                     | Description                                             |
| ------------------------ | ------------------------------------------------------- |
| NVCF_BACKEND             | Backend or "Cluster Group" the function is deployed on. |
| NVCF_ENV                 | Spot environment if deployed on spot instances.         |
| NVCF_FUNCTION_ID         | Function ID.                                            |
| NVCF_FUNCTION_NAME       | Function name.                                          |
| NVCF_FUNCTION_VERSION_ID | Function version ID.                                    |
| NVCF_INSTANCETYPE        | Instance type the function is deployed on.              |
| NVCF_NCA_ID              | Function's organization's NCA ID.                       |
| NVCF_REGION              | Region or zone the function is deployed in.             |

<Note>
All environment variables with the `NVCF_*` prefix are reserved and should not be overridden in your application code or function configuration.

</Note>
