# gRPC Function Invocation

gRPC invocation executes requests against Cloud Functions functions that expose a
gRPC service. gRPC functions use the gRPC proxy instead of the HTTP invocation
route.

In self-hosted deployments, the gRPC route is exposed on the Gateway TCP
listener. See [Gateway Routing](./gateway-routing.md) for listener and DNS
configuration.

## Invocation Path

![gRPC invocation path](images/nvcf-grpc-invocation-path.svg)

```bash
export GRPC_GATEWAY_ADDR=<grpc-gateway-address>
export FUNCTION_ID=<function-id>
export FUNCTION_VERSION_ID=<function-version-id>
export API_KEY=<api-key>
```

### Multi-Cluster View

In a global deployment, DNS selects a regional public gRPC endpoint. Each region
keeps its own gRPC Proxy, NVCF API and NATS stateful request path, worker CONNECT
registration, and customer gRPC service placement. The cross-cluster line shows
NATS chatter for regional stateful request-path coordination when configured.

![gRPC multi-cluster invocation path](images/nvcf-grpc-multicluster-invocation.svg)

## Metadata

Set these gRPC metadata values when invoking a function:

| Metadata key | Required | Description |
| --- | --- | --- |
| `authorization` | Yes | API key, formatted as `Bearer <api-key>`. You can also use gRPC call credentials. |
| `function-id` | Yes | Function ID to invoke. |
| `function-version-id` | No | Function version ID to target. |

The data sent to your gRPC function is defined by the Protobuf messages your
function implements. gRPC functions do not have an input request size limit.

gRPC connections stay alive for 30 seconds when idle. Close the gRPC client
connection after your client is finished so function workers are not held longer
than needed.

## Python Example

This example uses a plaintext local or test gateway on port `10081`. For a
production TLS endpoint, use `grpc.secure_channel("grpc.<domain>:443",
grpc.ssl_channel_credentials())`.

```python
import os
import grpc

import grpc_service_pb2_grpc


def call_grpc(model_infer_request) -> None:
    channel = grpc.insecure_channel(f"{os.environ['GRPC_GATEWAY_ADDR']}:10081")
    grpc_client = grpc_service_pb2_grpc.GRPCInferenceServiceStub(channel)

    metadata = [
        ("function-id", os.environ["FUNCTION_ID"]),
        ("function-version-id", os.environ["FUNCTION_VERSION_ID"]),
        ("authorization", f"Bearer {os.environ['API_KEY']}"),
    ]

    infer = grpc_client.ModelInfer(model_infer_request, metadata=metadata)
    _ = infer

    channel.close()
```

<Note>
The official gRPC term for authorization handling is
[call credentials](https://grpc.io/docs/guides/auth/#credential-types). The
example above sets the `authorization` metadata directly for clarity.

</Note>

## Connection Reuse and Streaming

The gRPC proxy pins sessions to the TCP connection to support unmodified gRPC
clients that ignore cookie headers. This matters when an intermediary proxy for
streaming, such as Kit streaming or Low Latency Streaming (LLS), uses HTTP/2 and
reuses connections.

![Single-client flow](images/grpc-single-client.png)

![Reconnect flow](images/grpc-reconnect-flow.png)

<Warning>
Do not pre-allocate streaming sessions with `POST` plus `X-NVCF-ABSORB` when a
shared HTTP/2 client can reuse one TCP connection across multiple users or
flows. Two separate requests sent over the same connection can receive the same
request ID from the proxy, which can bind different users or flows to the same
Kit pod.

Use on-demand binding through the WebSocket instead: establish the WebSocket,
obtain the request ID from the proxy, and use that ID for subsequent requests.

</Warning>

For requirements and a sample intermediary proxy implementation, see
[Intermediary Proxy](./streaming-functions.md#intermediary-proxy).
